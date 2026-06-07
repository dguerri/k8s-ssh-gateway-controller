package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	sshmgr "github.com/dguerri/k8s-ssh-gateway-controller/ssh"
)

// Get the controller name from environment variable or use a default
func getGatewayControllerName() string {
	if controllerName := os.Getenv("GATEWAY_CONTROLLER_NAME"); controllerName != "" {
		return controllerName
	}
	return "tunnels.ssh.gateway-api-controller"
}

// extractHostnameFromURI extracts just the hostname from a URI.
// For HTTP/HTTPS URIs (e.g., "https://user-dev.tuns.sh"), it returns the hostname.
// For TCP URIs (e.g., "tcp://example.com:8080"), it returns hostname:port.
// Returns the original string if parsing fails.
func extractHostnameFromURI(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil {
		slog.With("function", "extractHostnameFromURI").Warn("failed to parse URI, using as-is", "uri", uri, "error", err)
		return uri
	}

	// For HTTP/HTTPS, return just the hostname
	if parsed.Scheme == "http" || parsed.Scheme == "https" {
		return parsed.Hostname()
	}

	// For TCP, return hostname:port (which is in parsed.Host)
	if parsed.Scheme == "tcp" {
		// Remove tcp:// prefix to get host:port
		return strings.TrimPrefix(uri, "tcp://")
	}

	// For other schemes, return the host part
	if parsed.Host != "" {
		return parsed.Host
	}

	return uri
}

type Route struct {
	Name      string
	Namespace string
	Host      string
	Port      int
}

type Listener struct {
	route    *Route
	Protocol string
	Hostname string
	Port     int
	// SessionKind identifies which SSH session variant handles this listener.
	SessionKind sshmgr.SessionKind
	// Rejected is set when the listener cannot be programmed (e.g. unsupported
	// protocol, unsupported TLS mode, or required session not enabled). Reason
	// holds the condition reason to surface on the Gateway listener status.
	Rejected bool
	Reason   string // one of: SessionNotEnabled, UnsupportedTLSMode, UnsupportedListenerProtocol
}

type gateway struct {
	listeners   map[string]*Listener
	listenersMu sync.RWMutex
}

// SSHSessionPoolInterface is the controller-side view of the pool. Matches
// *sshmgr.SSHSessionPool's exported signature.
type SSHSessionPoolInterface interface {
	ConfigureSessions(ppVersion int, sniEnabled bool) error
	StartForwarding(kind sshmgr.SessionKind, cfg sshmgr.ForwardingConfig) error
	StopForwarding(kind sshmgr.SessionKind, cfg *sshmgr.ForwardingConfig) error
	GetAssignedAddresses(kind sshmgr.SessionKind, hostname string, port int) []string
	IsConnected(kind sshmgr.SessionKind) bool
	Connect()
	Stop()
}

type GatewayReconciler struct {
	pool SSHSessionPoolInterface

	client.Client
	Scheme *runtime.Scheme

	gateways   map[string]*gateway
	gatewaysMu sync.Mutex
}

func requiredSessionKinds(sessions ClassSessionConfig) []sshmgr.SessionKind {
	kinds := []sshmgr.SessionKind{sshmgr.SessionPlain}
	if sessions.ProxyProtocolVersion > 0 {
		kinds = append(kinds, sshmgr.SessionProxyProto)
	}
	if sessions.SNIProxyEnabled {
		kinds = append(kinds, sshmgr.SessionSNIProxy)
	}
	return kinds
}

func (r *GatewayReconciler) reconnectRequiredSessions(sessions ClassSessionConfig) bool {
	required := requiredSessionKinds(sessions)
	disconnected := false
	for _, kind := range required {
		if !r.pool.IsConnected(kind) {
			disconnected = true
			break
		}
	}
	if !disconnected {
		return true
	}

	slog.With("function", "Reconcile").Info("SSH session disconnected, attempting reconnect")
	r.pool.Connect()
	for _, kind := range required {
		if !r.pool.IsConnected(kind) {
			slog.With("function", "Reconcile").Warn("SSH session still disconnected after reconnect",
				"session", kind.String())
			return false
		}
	}
	slog.With("function", "Reconcile").Info("SSH sessions established/restored")
	return true
}

// isRouteAlreadyAttached checks if the route is already correctly attached to the listener
func isRouteAlreadyAttached(l *Listener, routeName, routeNamespace, backendHost string, backendPort int) bool {
	return l.route != nil &&
		l.route.Name == routeName &&
		l.route.Namespace == routeNamespace &&
		l.route.Host == backendHost &&
		l.route.Port == backendPort
}

// isForwardingValid checks if a forwarding exists in SSH manager and matches expectations.
// For hostname-based forwardings: verifies assigned URIs contain the requested hostname.
// For TCP listeners pinning a specific port: verifies the assigned TCP URI's port
// matches l.Port (regardless of hostname).
// For wildcard (0.0.0.0, localhost) without TCP port pinning: accepts any assigned address.
// Returns false if no addresses assigned or a mismatch is detected.
func (r *GatewayReconciler) isForwardingValid(l *Listener) bool {
	addrs := r.pool.GetAssignedAddresses(l.SessionKind, l.Hostname, l.Port)
	if len(addrs) == 0 {
		return false // No forwarding exists in SSH manager
	}

	enforcePort := l.Protocol == "TCP" && l.Port > 0
	hostnameWildcard := l.Hostname == "0.0.0.0" || l.Hostname == "localhost"

	if hostnameWildcard && !enforcePort {
		return true
	}

	return sshmgr.MatchesRequestedHost(addrs, l.Hostname, l.Port, enforcePort)
}

// setupRouteForwarding handles the actual forwarding setup for a route.
// This includes stopping any existing forwarding and starting the new one.
func (r *GatewayReconciler) setupRouteForwarding(l *Listener, gwKey, routeName, routeNamespace, backendHost string, backendPort int, listenerName string) error {
	// Stop existing forwarding if present
	if l.route != nil {
		slog.With("function", "setupRouteForwarding").Debug("stopping existing route forwarding",
			"gateway", gwKey,
			"oldRoute", fmt.Sprintf("%s/%s", l.route.Namespace, l.route.Name),
			"newRoute", fmt.Sprintf("%s/%s", routeNamespace, routeName))
		err := r.pool.StopForwarding(l.SessionKind, &sshmgr.ForwardingConfig{
			RemoteHost:   l.Hostname,
			RemotePort:   l.Port,
			InternalHost: l.route.Host,
			InternalPort: l.route.Port,
		})
		if err != nil {
			return fmt.Errorf("failed to stop forwarding: %w", err)
		}
	}

	// Create new route
	route := Route{
		Name:      routeName,
		Namespace: routeNamespace,
		Host:      backendHost,
		Port:      backendPort,
	}

	// Check SSH connection status
	if !r.pool.IsConnected(l.SessionKind) {
		slog.With("function", "setupRouteForwarding").Warn("SSH manager is not connected, attempting reconnect",
			"gateway", gwKey,
			"route", fmt.Sprintf("%s/%s", routeNamespace, routeName),
			"listener", listenerName)
		r.pool.Connect()
		if !r.pool.IsConnected(l.SessionKind) {
			slog.With("function", "setupRouteForwarding").Warn("SSH manager is not connected, cannot start forwarding",
				"gateway", gwKey,
				"route", fmt.Sprintf("%s/%s", routeNamespace, routeName),
				"listener", listenerName)
			return &ErrGatewayNotReady{msg: "SSH client not connected"}
		}
	}

	// Start forwarding. For TCP listeners pinning a specific port, require the
	// server to honor that port — otherwise we'd silently accept whatever port
	// the SSH server assigns and route traffic to the wrong listener.
	err := r.pool.StartForwarding(l.SessionKind, sshmgr.ForwardingConfig{
		RemoteHost:   l.Hostname,
		RemotePort:   l.Port,
		InternalHost: route.Host,
		InternalPort: route.Port,
		EnforcePort:  l.Protocol == "TCP" && l.Port > 0,
	})
	if err != nil {
		return r.handleForwardingError(err, l, &route, gwKey, routeName, routeNamespace, listenerName)
	}

	// Success - update internal state
	l.route = &route
	slog.With("function", "setupRouteForwarding", "gateway", gwKey, "listener", listenerName).Info("route set successfully",
		"route", fmt.Sprintf("%s/%s", routeNamespace, routeName),
		"backend", fmt.Sprintf("%s:%d", backendHost, backendPort))
	return nil
}

// handleForwardingError processes errors from StartForwarding.
func (r *GatewayReconciler) handleForwardingError(err error, l *Listener, route *Route, gwKey, routeName, routeNamespace, listenerName string) error {
	var notReadyErr *sshmgr.ErrSSHClientNotReady
	var existsErr *sshmgr.ErrSSHForwardingExists

	if errors.As(err, &notReadyErr) {
		slog.With("function", "setupRouteForwarding").Warn("SSH client became not ready during forwarding",
			"gateway", gwKey,
			"route", fmt.Sprintf("%s/%s", routeNamespace, routeName),
			"error", err)
		return &ErrGatewayNotReady{msg: err.Error()}
	}

	if errors.As(err, &existsErr) {
		// Forwarding already exists (controller restart case) - adopt it
		slog.With("function", "setupRouteForwarding").Info("forwarding already exists, adopting it",
			"gateway", gwKey,
			"route", fmt.Sprintf("%s/%s", routeNamespace, routeName),
			"listener", listenerName)
		l.route = route
		return nil
	}

	slog.With("function", "setupRouteForwarding").Error("failed to start forwarding",
		"gateway", gwKey,
		"route", fmt.Sprintf("%s/%s", routeNamespace, routeName),
		"listener", listenerName,
		"error", err)
	return fmt.Errorf("failed to start forwarding: %w", err)
}

// SetRoute sets up forwarding for a specific route on a listener.
func (r *GatewayReconciler) SetRoute(ctx context.Context, gwNamespace, gwName, listenerName, routeName, routeNamespace, backendHost string, backendPort int) error {
	r.gatewaysMu.Lock()
	defer r.gatewaysMu.Unlock()

	gwKey := getGwKey(gwNamespace, gwName)
	gw := r.gateways[gwKey]
	if gw == nil {
		// List available gateway names for debugging
		availableGWs := make([]string, 0, len(r.gateways))
		for k := range r.gateways {
			availableGWs = append(availableGWs, k)
		}
		slog.With("function", "SetRoute").Warn("gateway not found in internal registry",
			"requestedGateway", gwKey,
			"route", fmt.Sprintf("%s/%s", routeNamespace, routeName),
			"availableGateways", availableGWs)
		return &ErrGatewayNotFound{msg: fmt.Sprintf("gateway not found: %s/%s", gwNamespace, gwName)}
	}

	gw.listenersMu.Lock()
	defer gw.listenersMu.Unlock()

	if l, exist := gw.listeners[listenerName]; exist {
		slog.With("function", "SetRoute").Debug("found listener for route",
			"gateway", gwKey,
			"listener", listenerName,
			"route", fmt.Sprintf("%s/%s", routeNamespace, routeName),
			"hasExistingRoute", l.route != nil)

		if l.Rejected {
			return fmt.Errorf("listener %s is not programmed: %s", listenerName, l.Reason)
		}

		// Check if route is already correctly attached (idempotency for periodic reconciliation)
		if isRouteAlreadyAttached(l, routeName, routeNamespace, backendHost, backendPort) {
			// Validate forwarding still exists and is valid
			if !r.isForwardingValid(l) {
				slog.With("function", "SetRoute").Warn("route attached but forwarding invalid, will retry",
					"gateway", gwKey,
					"route", fmt.Sprintf("%s/%s", routeNamespace, routeName))
				l.route = nil // Clear stale state
				// Fall through to retry StartForwarding below
			} else {
				slog.With("function", "SetRoute").Debug("route already correctly attached, nothing to do",
					"gateway", gwKey,
					"route", fmt.Sprintf("%s/%s", routeNamespace, routeName))
				return nil
			}
		}

		// Setup forwarding for the route
		err := r.setupRouteForwarding(l, gwKey, routeName, routeNamespace, backendHost, backendPort, listenerName)
		if err != nil {
			return err
		}

		// Release locks before updating Gateway status to avoid holding locks during K8s API call
		gw.listenersMu.Unlock()
		r.gatewaysMu.Unlock()

		// Update Gateway status with assigned addresses (skip if no Kubernetes client, e.g. in tests)
		if ctx != nil && r.Client != nil {
			if err := r.updateGatewayStatus(ctx, gwNamespace, gwName); err != nil {
				slog.With("function", "SetRoute", "gateway", gwNamespace+"/"+gwName).Warn("failed to update Gateway status", "error", err)
				// Don't return error - forwarding is already set up successfully
			}
		}

		// Reacquire locks before defers execute
		r.gatewaysMu.Lock()
		gw.listenersMu.Lock()
	} else {
		// List available listeners for debugging
		var availableListeners []string
		for name := range gw.listeners {
			availableListeners = append(availableListeners, name)
		}
		slog.With("function", "SetRoute").Warn("listener not found for route",
			"requestedListener", listenerName,
			"gateway", gwKey,
			"route", fmt.Sprintf("%s/%s", routeNamespace, routeName),
			"availableListeners", availableListeners)
		return fmt.Errorf("listener not found: %s", listenerName)
	}

	return nil
}

func (r *GatewayReconciler) RemoveRoute(ctx context.Context, gwNamespace, gwName, listenerName, routeName, routeNamespace, backendHost string, backendPort int) error {
	r.gatewaysMu.Lock()
	defer r.gatewaysMu.Unlock()

	gw := r.gateways[getGwKey(gwNamespace, gwName)]
	if gw == nil {
		return &ErrGatewayNotFound{fmt.Sprintf("gateway not found: %s/%s", gwNamespace, gwName)}
	}

	gw.listenersMu.Lock()
	defer gw.listenersMu.Unlock()

	if l, exist := gw.listeners[listenerName]; exist && l.route != nil && l.route.Name == routeName && l.route.Namespace == routeNamespace {
		err := r.pool.StopForwarding(l.SessionKind, &sshmgr.ForwardingConfig{
			RemoteHost:   l.Hostname,
			RemotePort:   l.Port,
			InternalHost: l.route.Host,
			InternalPort: l.route.Port,
		})
		if err != nil {
			slog.With("function", "RemoveRoute", "gateway", gwNamespace+"/"+gwName, "listener", listenerName).Error("failed to stop forwarding", "error", err)
			return fmt.Errorf("failed to stop forwarding: %w", err)
		}
		l.route = nil
		slog.With("function", "RemoveRoute", "gateway", gwNamespace+"/"+gwName, "listener", listenerName).Debug("route removed successfully")

		// Release locks before updating Gateway status to avoid holding locks during K8s API call
		gw.listenersMu.Unlock()
		r.gatewaysMu.Unlock()

		// Update Gateway status to remove addresses (skip if no Kubernetes client, e.g. in tests)
		if ctx != nil && r.Client != nil {
			if err := r.updateGatewayStatus(ctx, gwNamespace, gwName); err != nil {
				slog.With("function", "RemoveRoute", "gateway", gwNamespace+"/"+gwName).Warn("failed to update Gateway status", "error", err)
				// Don't return error - forwarding is already stopped successfully
			}
		}

		// Reacquire locks before defers execute
		r.gatewaysMu.Lock()
		gw.listenersMu.Lock()

		return nil
	}

	return &ErrRouteNotFound{fmt.Sprintf("route not found: %s/%s", routeNamespace, routeName)}
}

func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.gateways = make(map[string]*gateway)
	ctx := context.Background()
	pool, err := createSSHSessionPool(ctx)
	if err != nil {
		return fmt.Errorf("failed to create SSH session pool: %w", err)
	}
	r.pool = pool

	if err := RegisterGatewayClassController(mgr); err != nil {
		return fmt.Errorf("failed to register GatewayClass controller: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1.Gateway{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}). // only trigger on spec changes
		Complete(r)
}

func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	key := fmt.Sprintf("%s/%s", req.Namespace, req.Name)
	slog.With("function", "Reconcile").Debug("reconciling Gateway", "gateway", key, "request", req)

	// Plain session is always required. Check it before touching Kubernetes so
	// controller startup and transient API misses still exercise the reconnect loop.
	if !r.pool.IsConnected(sshmgr.SessionPlain) {
		slog.With("function", "Reconcile").Info("SSH plain session disconnected, attempting reconnect")
		r.pool.Connect()
		if !r.pool.IsConnected(sshmgr.SessionPlain) {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		slog.With("function", "Reconcile").Info("SSH plain session established/restored")
	}

	var k8sGw gatewayv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &k8sGw); err != nil {
		slog.With("function", "Reconcile").Debug("unable to retrieve gateway", "gateway", req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	isManagedByUs := containsString(k8sGw.Finalizers, getGatewayFinalizer())

	if !k8sGw.DeletionTimestamp.IsZero() {
		if isManagedByUs {
			// Handle deletion
			slog.With("function", "Reconcile", "gateway", req.NamespacedName).Info("processing gateway deletion")
			r.handleDeleteGateway(ctx, &k8sGw)
			k8sGw.Finalizers = removeString(k8sGw.Finalizers, getGatewayFinalizer())
			if err := r.Update(ctx, &k8sGw); err != nil {
				slog.With("function", "Reconcile").Error("failed to remove finalizer", "gateway", key, "error", err)
				return ctrl.Result{}, err
			}
		} else {
			slog.With("function", "Reconcile", "gateway", req.NamespacedName).Debug("gateway being deleted but not managed by us, skipping")
		}
		return ctrl.Result{}, nil
	}

	// Fetch GatewayClass to check ownership and read annotations
	var gc gatewayv1.GatewayClass
	if err := r.Get(ctx, client.ObjectKey{Name: string(k8sGw.Spec.GatewayClassName)}, &gc); err != nil {
		return ctrl.Result{}, err
	}

	if !isManagedByUs {
		controllerName := getGatewayControllerName()
		if string(gc.Spec.ControllerName) != controllerName {
			slog.With("function", "Reconcile").Debug("skipping Gateway: does not match controllerName", "gatewayClassName", k8sGw.Spec.GatewayClassName)
			return ctrl.Result{}, nil
		}
	}

	// Configure sessions based on GatewayClass annotations
	sessions := parseClassSessionConfig(gc.Annotations)
	if err := r.pool.ConfigureSessions(sessions.ProxyProtocolVersion, sessions.SNIProxyEnabled); err != nil {
		slog.With("function", "Reconcile").Error("failed to configure SSH sessions",
			"gatewayClass", gc.Name, "error", err)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if !r.reconnectRequiredSessions(sessions) {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Handle add or update
	slog.With("function", "Reconcile", "gateway", req.NamespacedName).Debug("adding or updating gateway")
	if !containsString(k8sGw.Finalizers, getGatewayFinalizer()) {
		k8sGw.Finalizers = append(k8sGw.Finalizers, getGatewayFinalizer())
		if err := r.Update(ctx, &k8sGw); err != nil {
			slog.With("function", "Reconcile").Error("failed to add finalizer", "gateway", key, "error", err)
			return ctrl.Result{}, err
		}
	}
	if err := r.handleAddOrUpdateGateway(ctx, &k8sGw); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.updateGatewayStatusIfChanged(ctx, &k8sGw, &gc, key); err != nil {
		return ctrl.Result{}, err
	}

	// Periodically requeue to check SSH connection health and update status
	return ctrl.Result{RequeueAfter: gatewayReconcilePeriod}, nil
}

// gatewayAddressesEqual compares two slices of GatewayStatusAddress for equality.
// Returns true if both slices contain the same addresses (order-independent).
func gatewayAddressesEqual(a, b []gatewayv1.GatewayStatusAddress) bool {
	if len(a) != len(b) {
		return false
	}

	// Create maps for O(n) comparison
	aMap := make(map[string]gatewayv1.AddressType)
	for _, addr := range a {
		addrType := gatewayv1.HostnameAddressType
		if addr.Type != nil {
			addrType = *addr.Type
		}
		aMap[addr.Value] = addrType
	}

	for _, addr := range b {
		addrType := gatewayv1.HostnameAddressType
		if addr.Type != nil {
			addrType = *addr.Type
		}
		existingType, exists := aMap[addr.Value]
		if !exists || existingType != addrType {
			return false
		}
	}

	return true
}

// updateGatewayAddresses populates the Gateway status.addresses field with the actual
// assigned addresses from the SSH tunnel manager for each listener.
func (r *GatewayReconciler) updateGatewayAddresses(k8sGw *gatewayv1.Gateway) {
	r.gatewaysMu.Lock()
	defer r.gatewaysMu.Unlock()

	gwKey := getGwKey(k8sGw.Namespace, k8sGw.Name)
	gw, exists := r.gateways[gwKey]
	if !exists {
		return
	}

	gw.listenersMu.RLock()
	defer gw.listenersMu.RUnlock()

	var statusAddresses []gatewayv1.GatewayStatusAddress
	addressesSeen := make(map[string]bool)

	for _, listener := range gw.listeners {
		// Skip rejected listeners (their session kind may not even be enabled)
		if listener.Rejected {
			continue
		}
		// Get assigned addresses for this listener from the SSH session pool
		addrs := r.pool.GetAssignedAddresses(listener.SessionKind, listener.Hostname, listener.Port)

		for _, addr := range addrs {
			// Extract just the hostname from the URI
			hostname := extractHostnameFromURI(addr)

			// Avoid duplicates
			if addressesSeen[hostname] {
				continue
			}
			addressesSeen[hostname] = true

			// Determine address type based on whether it contains a port
			// For TCP URIs like "tcp://nue.tuns.sh:34012", hostname will be "nue.tuns.sh:34012"
			// Gateway API address types:
			// - HostnameAddressType: DNS hostname without port
			// - IPAddressType: IP address (with or without port)
			// - NamedAddressType: implementation-specific format
			var addrType gatewayv1.AddressType
			if strings.Contains(hostname, ":") {
				// Contains port - use NamedAddressType for hostname:port combinations
				addrType = gatewayv1.NamedAddressType
			} else {
				// Just hostname - use HostnameAddressType
				addrType = gatewayv1.HostnameAddressType
			}

			statusAddresses = append(statusAddresses, gatewayv1.GatewayStatusAddress{
				Type:  &addrType,
				Value: hostname,
			})
			slog.With("function", "updateGatewayAddresses").Debug("added address to Gateway status",
				"gateway", gwKey, "uri", addr, "hostname", hostname, "type", addrType)
		}
	}

	k8sGw.Status.Addresses = statusAddresses
}

// listenerProgrammedCondition computes the Programmed condition for a single
// listener entry, given the current ClassSessionConfig in effect.
func (r *GatewayReconciler) listenerProgrammedCondition(l *Listener, sessions ClassSessionConfig) metav1.Condition {
	now := metav1.Now()
	if l.Rejected {
		return metav1.Condition{
			Type:               string(gatewayv1.ListenerConditionProgrammed),
			Status:             metav1.ConditionFalse,
			Reason:             l.Reason,
			Message:            "Listener rejected: " + l.Reason,
			LastTransitionTime: now,
		}
	}
	if l.SessionKind == sshmgr.SessionProxyProto && sessions.ProxyProtocolVersion == 0 {
		return metav1.Condition{
			Type:               string(gatewayv1.ListenerConditionProgrammed),
			Status:             metav1.ConditionFalse,
			Reason:             ReasonSessionNotEnabled,
			Message:            "Listener requires proxy-protocol session but GatewayClass has not enabled it",
			LastTransitionTime: now,
		}
	}
	if l.SessionKind == sshmgr.SessionSNIProxy && !sessions.SNIProxyEnabled {
		return metav1.Condition{
			Type:               string(gatewayv1.ListenerConditionProgrammed),
			Status:             metav1.ConditionFalse,
			Reason:             ReasonSessionNotEnabled,
			Message:            "Listener requires sni-proxy session but GatewayClass has not enabled it",
			LastTransitionTime: now,
		}
	}
	if !r.pool.IsConnected(l.SessionKind) {
		return metav1.Condition{
			Type:               string(gatewayv1.ListenerConditionProgrammed),
			Status:             metav1.ConditionFalse,
			Reason:             string(gatewayv1.ListenerReasonPending),
			Message:            "SSH session not connected",
			LastTransitionTime: now,
		}
	}
	return metav1.Condition{
		Type:               string(gatewayv1.ListenerConditionProgrammed),
		Status:             metav1.ConditionTrue,
		Reason:             string(gatewayv1.ListenerReasonProgrammed),
		Message:            "Listener programmed",
		LastTransitionTime: now,
	}
}

// supportedKindsFor returns the RouteGroupKinds supported by a listener based on its protocol.
func supportedKindsFor(l *Listener) []gatewayv1.RouteGroupKind {
	grp := gatewayv1.Group("gateway.networking.k8s.io")
	switch strings.ToUpper(l.Protocol) {
	case "HTTP":
		return []gatewayv1.RouteGroupKind{{Group: &grp, Kind: gatewayv1.Kind("HTTPRoute")}}
	case "TCP":
		return []gatewayv1.RouteGroupKind{{Group: &grp, Kind: gatewayv1.Kind("TCPRoute")}}
	case "TLS":
		return []gatewayv1.RouteGroupKind{{Group: &grp, Kind: gatewayv1.Kind("TLSRoute")}}
	default:
		return nil
	}
}

// updateGatewayStatus fetches the Gateway resource and updates its status with current addresses.
// This should be called after route attachment/removal to reflect address changes.
func (r *GatewayReconciler) updateGatewayStatus(ctx context.Context, gwNamespace, gwName string) error {
	gwKey := getGwKey(gwNamespace, gwName)

	var k8sGw gatewayv1.Gateway
	if err := r.Get(ctx, client.ObjectKey{Namespace: gwNamespace, Name: gwName}, &k8sGw); err != nil {
		slog.With("function", "updateGatewayStatus").Error("failed to fetch Gateway", "gateway", gwKey, "error", err)
		return fmt.Errorf("failed to fetch Gateway %s: %w", gwKey, err)
	}

	// Fetch GatewayClass to compute listener conditions.
	var gc gatewayv1.GatewayClass
	if err := r.Get(ctx, client.ObjectKey{Name: string(k8sGw.Spec.GatewayClassName)}, &gc); err != nil {
		slog.With("function", "updateGatewayStatus").Warn("failed to fetch GatewayClass, skipping listener conditions", "gateway", gwKey, "error", err)
		// Don't fail the whole status update — addresses are still useful.
	} else {
		r.populateListenerStatuses(&k8sGw, &gc)
	}

	// Update addresses from SSH tunnel manager
	r.updateGatewayAddresses(&k8sGw)

	// Update the Gateway status
	if err := r.Status().Update(ctx, &k8sGw); err != nil {
		slog.With("function", "updateGatewayStatus").Error("failed to update Gateway status", "gateway", gwKey, "error", err)
		return fmt.Errorf("failed to update Gateway status for %s: %w", gwKey, err)
	}

	slog.With("function", "updateGatewayStatus").Debug("updated Gateway status with addresses", "gateway", gwKey)
	return nil
}

// populateListenerStatuses fills k8sGw.Status.Listeners with per-listener Programmed
// conditions and returns whether all listeners are programmed.
func (r *GatewayReconciler) populateListenerStatuses(k8sGw *gatewayv1.Gateway, gc *gatewayv1.GatewayClass) bool {
	sessions := parseClassSessionConfig(gc.Annotations)
	gwKey := getGwKey(k8sGw.Namespace, k8sGw.Name)
	gw, ok := r.gateways[gwKey]
	if !ok {
		return true
	}
	listenerStatuses := make([]gatewayv1.ListenerStatus, 0, len(k8sGw.Spec.Listeners))
	allProgrammed := true
	for _, k8sListener := range k8sGw.Spec.Listeners {
		name := string(k8sListener.Name)
		l, exists := gw.listeners[name]
		if !exists {
			continue
		}
		cond := r.listenerProgrammedCondition(l, sessions)
		if cond.Status != metav1.ConditionTrue {
			allProgrammed = false
		}
		listenerStatuses = append(listenerStatuses, gatewayv1.ListenerStatus{
			Name:           k8sListener.Name,
			Conditions:     []metav1.Condition{cond},
			SupportedKinds: supportedKindsFor(l),
		})
	}
	k8sGw.Status.Listeners = listenerStatuses
	return allProgrammed
}

// updateGatewayStatusIfChanged updates the Gateway status in K8s if either conditions or addresses changed.
// This ensures both conditions and addresses are persisted when they change, while avoiding unnecessary API calls.
func (r *GatewayReconciler) updateGatewayStatusIfChanged(ctx context.Context, k8sGw *gatewayv1.Gateway, gc *gatewayv1.GatewayClass, key string) error {
	// Populate per-listener Programmed conditions and determine aggregate state.
	allProgrammed := r.populateListenerStatuses(k8sGw, gc)

	progStatus := metav1.ConditionTrue
	progReason := "Programmed"
	progMsg := "Gateway programmed successfully"
	if !allProgrammed {
		progStatus = metav1.ConditionFalse
		progReason = "ListenersNotProgrammed"
		progMsg = "At least one listener is not Programmed"
	}

	// Update Gateway status with Accepted=True and Programmed based on listener state.
	conditionsChanged := apiMeta.SetStatusCondition(&k8sGw.Status.Conditions, metav1.Condition{
		Type:               string(gatewayv1.GatewayConditionAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             "Accepted",
		Message:            "Gateway accepted by controller",
		LastTransitionTime: metav1.Now(),
	})
	conditionsChanged = apiMeta.SetStatusCondition(&k8sGw.Status.Conditions, metav1.Condition{
		Type:               string(gatewayv1.GatewayConditionProgrammed),
		Status:             progStatus,
		Reason:             progReason,
		Message:            progMsg,
		LastTransitionTime: metav1.Now(),
	}) || conditionsChanged

	// Populate status.addresses with actual assigned addresses from SSH tunnels
	oldAddresses := k8sGw.Status.Addresses
	r.updateGatewayAddresses(k8sGw)
	addressesChanged := !gatewayAddressesEqual(oldAddresses, k8sGw.Status.Addresses)

	// Only update status if conditions or addresses have changed
	if conditionsChanged || addressesChanged {
		slog.With("function", "updateGatewayStatusIfChanged").Debug("gateway status changed, updating",
			"gateway", key,
			"conditionsChanged", conditionsChanged,
			"addressesChanged", addressesChanged)
		if err := r.Status().Update(ctx, k8sGw); err != nil {
			slog.With("function", "updateGatewayStatusIfChanged").Error("failed to update Gateway status", "gateway", key, "error", err)
			return err
		}
	} else {
		slog.With("function", "updateGatewayStatusIfChanged").Debug("gateway status unchanged, skipping update", "gateway", key)
	}
	return nil
}

func (r *GatewayReconciler) handleAddOrUpdateGateway(_ context.Context, k8sGw *gatewayv1.Gateway) error {
	r.gatewaysMu.Lock()
	defer r.gatewaysMu.Unlock()

	gwKey := getGwKey(k8sGw.Namespace, k8sGw.Name)
	gw, exists := r.gateways[gwKey]
	if !exists {
		slog.With("function", "handleAddOrUpdateGateway").Debug("creating a new gateway", "gatewayKey", gwKey)

		gw = &gateway{
			listeners: make(map[string]*Listener),
		}
		for _, k8sListener := range k8sGw.Spec.Listeners {
			listener := createListener(k8sListener, k8sGw.Annotations)
			gw.listeners[string(k8sListener.Name)] = listener
		}
		r.gateways[gwKey] = gw
	} else {
		slog.With("function", "handleAddOrUpdateGateway").Debug("updating existing gateway", "gatewayKey", gwKey)
		gw.listenersMu.Lock()
		defer gw.listenersMu.Unlock()

		existingListeners := gw.listeners
		updatedListeners := make(map[string]*Listener)

		for _, k8sListener := range k8sGw.Spec.Listeners {
			listenerName := string(k8sListener.Name)
			listener := createListener(k8sListener, k8sGw.Annotations)

			if existing, exists := existingListeners[listenerName]; exists {
				if existing.Port != listener.Port || existing.Hostname != listener.Hostname || existing.SessionKind != listener.SessionKind {
					// Listener configuration changed, handle accordingly
					slog.With("function", "handleAddOrUpdateGateway").Debug("listener configuration changed, updating", "listener", listenerName)
					// Stop existing forwarding on the OLD kind, if needed
					if existing.route != nil {
						if err := r.pool.StopForwarding(existing.SessionKind, &sshmgr.ForwardingConfig{
							RemoteHost:   existing.Hostname,
							RemotePort:   existing.Port,
							InternalHost: existing.route.Host,
							InternalPort: existing.route.Port,
						}); err != nil {
							slog.With("function", "handleAddOrUpdateGateway").Error("failed to stop existing forwarding", "listener", listenerName, "error", err)
						}
					}
					// Configuration changed - use new listener
					updatedListeners[listenerName] = listener
				} else {
					// Configuration unchanged - preserve existing listener (keeps route reference)
					updatedListeners[listenerName] = existing
				}
			} else {
				// New listener - add it
				updatedListeners[listenerName] = listener
			}
		}

		gw.listeners = updatedListeners
	}

	slog.With("function", "handleAddOrUpdateGateway", "gateway", gwKey, "listenerCount", len(k8sGw.Spec.Listeners)).Debug("gateway processed successfully")
	return nil
}

func (r *GatewayReconciler) handleDeleteGateway(_ context.Context, k8sGw *gatewayv1.Gateway) {
	slog.With("function", "handleDeleteGateway").Debug("handling Delete Gateway", "namespace", k8sGw.Namespace, "name", k8sGw.Name)

	// Clean up single manager for this Gateway
	gwKey := getGwKey(k8sGw.Namespace, k8sGw.Name)
	r.gatewaysMu.Lock()
	defer r.gatewaysMu.Unlock()

	if gw, ok := r.gateways[gwKey]; ok {
		gw.listenersMu.Lock()
		defer gw.listenersMu.Unlock()
		// Delete listeners and stop forwardings
		for name, listener := range gw.listeners {
			if listener.route != nil {
				err := r.pool.StopForwarding(listener.SessionKind, &sshmgr.ForwardingConfig{
					RemoteHost:   listener.Hostname,
					RemotePort:   listener.Port,
					InternalHost: listener.route.Host,
					InternalPort: listener.route.Port,
				})
				if err != nil {
					slog.With("function", "handleDeleteGateway").Error("failed to stop forwarding", "error", err)
				} else {
					slog.With("function", "handleDeleteGateway").Debug("stopped forwarding for listener", "listener", listener.Hostname)
				}
			}
			delete(gw.listeners, name)
		}
		delete(r.gateways, gwKey)
		slog.With("function", "handleDeleteGateway").Debug("SSHSessionPool: stopped forwarding for Gateway", "gateway", gwKey)
	}
}

type GatewayClassReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *GatewayClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var gc gatewayv1.GatewayClass
	if err := r.Get(ctx, req.NamespacedName, &gc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	controllerName := getGatewayControllerName()
	if string(gc.Spec.ControllerName) != controllerName {
		return ctrl.Result{}, nil
	}

	condition := metav1.Condition{
		Type:               string(gatewayv1.GatewayClassConditionStatusAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             "Accepted",
		Message:            "Controller has accepted this GatewayClass",
		LastTransitionTime: metav1.Now(),
	}

	gc.Status.Conditions = []metav1.Condition{condition}
	if err := r.Status().Update(ctx, &gc); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update GatewayClass status: %w", err)
	}

	return ctrl.Result{}, nil
}

func RegisterGatewayClassController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1.GatewayClass{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(&GatewayClassReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		})
}

// IsGatewayManaged checks if the Gateway referenced by the route is managed by this controller.
// Returns (false, nil) when Gateway or GatewayClass doesn't exist (not an error condition).
// Returns (false, error) for actual API errors (permissions, network issues, etc.).
// Returns (true, nil) when Gateway exists and is managed by this controller.
func IsGatewayManaged(ctx context.Context, c client.Client, gwNamespace, gwName string) (bool, error) {
	var gw gatewayv1.Gateway
	if err := c.Get(ctx, client.ObjectKey{Namespace: gwNamespace, Name: gwName}, &gw); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Gateway doesn't exist - not managed by this controller
			return false, nil
		}
		// Real API error
		return false, err
	}

	var gc gatewayv1.GatewayClass
	if err := c.Get(ctx, client.ObjectKey{Name: string(gw.Spec.GatewayClassName)}, &gc); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// GatewayClass doesn't exist - not managed by this controller
			return false, nil
		}
		// Real API error
		return false, err
	}

	return string(gc.Spec.ControllerName) == getGatewayControllerName(), nil
}
