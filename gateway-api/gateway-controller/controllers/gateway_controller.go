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

	sshmgr "github.com/dguerri/ssh-gateway-api-controller/ssh"
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
}

type gateway struct {
	listeners   map[string]*Listener
	listenersMu sync.RWMutex
}

// SSHTunnelManagerInterface defines the interface for SSH tunnel management operations
type SSHTunnelManagerInterface interface {
	GetAssignedAddresses(hostname string, port int) []string
	StartForwarding(config sshmgr.ForwardingConfig) error
	StopForwarding(config *sshmgr.ForwardingConfig) error
	Stop()
	Connect() error
	IsConnected() bool
}

type GatewayReconciler struct {
	manager SSHTunnelManagerInterface

	client.Client
	Scheme *runtime.Scheme

	gateways   map[string]*gateway
	gatewaysMu sync.Mutex
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
// For wildcard (0.0.0.0, localhost, or no address): accepts any assigned address.
// Returns false if no addresses assigned or hostname mismatch.
func (r *GatewayReconciler) isForwardingValid(l *Listener) bool {
	addrs := r.manager.GetAssignedAddresses(l.Hostname, l.Port)
	if len(addrs) == 0 {
		return false // No forwarding exists in SSH manager
	}

	// For wildcard hostnames (0.0.0.0, localhost), accept any assigned address
	if l.Hostname == "0.0.0.0" || l.Hostname == "localhost" {
		return true
	}

	// For specific hostnames, verify hostname matches
	for _, addr := range addrs {
		if strings.Contains(addr, l.Hostname) {
			return true // Found matching hostname in URIs
		}
	}
	return false // Hostname mismatch (wrong subdomain assigned)
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
		err := r.manager.StopForwarding(&sshmgr.ForwardingConfig{
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
	if !r.manager.IsConnected() {
		slog.With("function", "setupRouteForwarding").Warn("SSH manager is not connected, cannot start forwarding",
			"gateway", gwKey,
			"route", fmt.Sprintf("%s/%s", routeNamespace, routeName),
			"listener", listenerName)
		return &ErrGatewayNotReady{msg: "SSH client not connected"}
	}

	// Start forwarding
	err := r.manager.StartForwarding(sshmgr.ForwardingConfig{
		RemoteHost:   l.Hostname,
		RemotePort:   l.Port,
		InternalHost: route.Host,
		InternalPort: route.Port,
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
		var availableGWs []string
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
		err := r.manager.StopForwarding(&sshmgr.ForwardingConfig{
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
	manager, err := createSSHManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create SSH manager for gateway controller: %w", err)
	}
	r.manager = manager

	// Try initial connection
	if err := r.manager.Connect(); err != nil {
		// Log error but continue - will retry in Reconcile loop
		slog.With("function", "SetupWithManager").Error("failed to establish initial SSH connection", "error", err)
	}

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

	// Check and maintain SSH connection
	if !r.manager.IsConnected() {
		slog.With("function", "Reconcile").Info("SSH manager disconnected, attempting to reconnect")
		if err := r.manager.Connect(); err != nil {
			slog.With("function", "Reconcile").Error("failed to connect SSH manager", "error", err)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		slog.With("function", "Reconcile").Info("SSH connection established/restored")
		// Route controllers will detect reconnection and restore forwardings via periodic reconciliation
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

	// Not deleting — if we don't yet own it, check GatewayClass to decide adoption
	if !isManagedByUs {
		var gc gatewayv1.GatewayClass
		if err := r.Get(ctx, client.ObjectKey{Name: string(k8sGw.Spec.GatewayClassName)}, &gc); err != nil {
			return ctrl.Result{}, err
		}

		controllerName := getGatewayControllerName()
		if string(gc.Spec.ControllerName) != controllerName {
			slog.With("function", "Reconcile").Debug("skipping Gateway: does not match controllerName", "gatewayClassName", k8sGw.Spec.GatewayClassName)
			return ctrl.Result{}, nil
		}
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
	if err := r.updateGatewayStatusIfChanged(ctx, &k8sGw, key); err != nil {
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
		// Get assigned addresses for this listener from the SSH tunnel manager
		addrs := r.manager.GetAssignedAddresses(listener.Hostname, listener.Port)

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

// updateGatewayStatus fetches the Gateway resource and updates its status with current addresses.
// This should be called after route attachment/removal to reflect address changes.
func (r *GatewayReconciler) updateGatewayStatus(ctx context.Context, gwNamespace, gwName string) error {
	gwKey := getGwKey(gwNamespace, gwName)

	var k8sGw gatewayv1.Gateway
	if err := r.Get(ctx, client.ObjectKey{Namespace: gwNamespace, Name: gwName}, &k8sGw); err != nil {
		slog.With("function", "updateGatewayStatus").Error("failed to fetch Gateway", "gateway", gwKey, "error", err)
		return fmt.Errorf("failed to fetch Gateway %s: %w", gwKey, err)
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

// updateGatewayStatusIfChanged updates the Gateway status in K8s if either conditions or addresses changed.
// This ensures both conditions and addresses are persisted when they change, while avoiding unnecessary API calls.
func (r *GatewayReconciler) updateGatewayStatusIfChanged(ctx context.Context, k8sGw *gatewayv1.Gateway, key string) error {
	// Update Gateway status with Accepted=True and Programmed=True
	conditionsChanged := apiMeta.SetStatusCondition(&k8sGw.Status.Conditions, metav1.Condition{
		Type:               string(gatewayv1.GatewayConditionAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             "Accepted",
		Message:            "Gateway accepted by controller",
		LastTransitionTime: metav1.Now(),
	})
	conditionsChanged = apiMeta.SetStatusCondition(&k8sGw.Status.Conditions, metav1.Condition{
		Type:               string(gatewayv1.GatewayConditionProgrammed),
		Status:             metav1.ConditionTrue,
		Reason:             "Programmed",
		Message:            "Gateway programmed successfully",
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

func (r *GatewayReconciler) handleAddOrUpdateGateway(ctx context.Context, k8sGw *gatewayv1.Gateway) error {
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
			listener := createListener(k8sListener)
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
			listener := createListener(k8sListener)

			if existing, exists := existingListeners[listenerName]; exists {
				if existing.Port != listener.Port || existing.Hostname != listener.Hostname {
					// Listener configuration changed, handle accordingly
					slog.With("function", "handleAddOrUpdateGateway").Debug("listener configuration changed, updating", "listener", listenerName)
					// Stop existing forwarding if no longer needed
					if existing.route != nil {
						if err := r.manager.StopForwarding(&sshmgr.ForwardingConfig{
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
		for _, listener := range gw.listeners {
			if listener.route != nil {
				err := r.manager.StopForwarding(&sshmgr.ForwardingConfig{
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
			delete(gw.listeners, listener.Hostname)
		}
		delete(r.gateways, gwKey)
		slog.With("function", "handleDeleteGateway").Debug("SSHTunnelManager stopped for Gateway", "gateway", gwKey)
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
