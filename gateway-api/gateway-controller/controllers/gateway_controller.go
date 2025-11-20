package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	sshmgr "github.com/dguerri/ssh-gateway-api-controller/ssh"
)

const gatewayFinalizer = "gateway.networking.k8s.io/finalizer"

// Get the controller name from environment variable or use a default
func getGatewayControllerName() string {
	if controllerName := os.Getenv("GATEWAY_CONTROLLER_NAME"); controllerName != "" {
		return controllerName
	}
	return "tunnels.ssh.gateway-api-controller"
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

type GatewayReconciler struct {
	manager *sshmgr.SSHTunnelManager

	client.Client
	Scheme *runtime.Scheme

	gateways   map[string]*gateway
	gatewaysMu sync.Mutex
}

// SetRoute sets up forwarding for a specific route on a listener.
func (r *GatewayReconciler) SetRoute(gwNamespace, gwName, listenerName, routeName, routeNamespace, backendHost string, backendPort int) error {
	r.gatewaysMu.Lock()
	defer r.gatewaysMu.Unlock()

	gw := r.gateways[getGwKey(gwNamespace, gwName)]
	if gw == nil {
		return &ErrGatewayNotFound{msg: fmt.Sprintf("gateway not found: %s/%s", gwNamespace, gwName)}
	}

	gw.listenersMu.Lock()
	defer gw.listenersMu.Unlock()

	if l, exist := gw.listeners[listenerName]; exist {
		if l.route != nil {
			// Stop the forwarding.
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
		// Start the forwarding.
		route := Route{
			Name:      routeName,
			Namespace: routeNamespace,
			Host:      backendHost,
			Port:      backendPort,
		}
		err := r.manager.StartForwarding(sshmgr.ForwardingConfig{
			RemoteHost:   l.Hostname,
			RemotePort:   l.Port,
			InternalHost: route.Host,
			InternalPort: route.Port,
		})
		if err != nil {
			var notReadyErr *sshmgr.ErrSSHClientNotReady
			if errors.As(err, &notReadyErr) {
				return &ErrGatewayNotReady{msg: err.Error()}
			}
			return fmt.Errorf("failed to start forwarding: %w", err)
		}
		// Forwarding is set, add the new route
		l.route = &route
		slog.With("function", "SetRoute", "gateway", gwNamespace+"/"+gwName, "listener", listenerName).Debug("route set successfully")
	} else {
		return fmt.Errorf("listener not found: %s", listenerName)
	}

	return nil
}

func (r *GatewayReconciler) RemoveRoute(gwNamespace, gwName, listenerName, routeName, routeNamespace, backendHost string, backendPort int) error {
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

	var k8sGw gatewayv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &k8sGw); err != nil {
		slog.With("function", "Reconcile").Debug("unable to retrieve gateway", "gateway", req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var gc gatewayv1.GatewayClass
	if err := r.Get(ctx, client.ObjectKey{Name: string(k8sGw.Spec.GatewayClassName)}, &gc); err != nil {
		return ctrl.Result{}, err
	}

	controllerName := getGatewayControllerName()
	if string(gc.Spec.ControllerName) != controllerName {
		slog.With("function", "Reconcile").Debug("skipping Gateway: does not match controllerName", "gatewayClassName", k8sGw.Spec.GatewayClassName)
		return ctrl.Result{}, nil
	}

	if k8sGw.DeletionTimestamp.IsZero() {
		// Handle add or Update
		if !containsString(k8sGw.Finalizers, gatewayFinalizer) {
			// Add finalizer if not present
			k8sGw.Finalizers = append(k8sGw.Finalizers, gatewayFinalizer)
			if err := r.Update(ctx, &k8sGw); err != nil {
				slog.With("function", "Reconcile").Error("failed to add finalizer", "gateway", key, "error", err)
				return ctrl.Result{}, err
			}
		}
		err := r.handleAddOrUpdateGateway(ctx, &k8sGw)
		if err != nil {
			return ctrl.Result{}, err
		}
		// Update Gateway status with Accepted=True and Programmed=True
		apiMeta.SetStatusCondition(&k8sGw.Status.Conditions, metav1.Condition{
			Type:               string(gatewayv1.GatewayConditionAccepted),
			Status:             metav1.ConditionTrue,
			Reason:             "Accepted",
			Message:            "Gateway accepted by controller",
			LastTransitionTime: metav1.Now(),
		})
		apiMeta.SetStatusCondition(&k8sGw.Status.Conditions, metav1.Condition{
			Type:               string(gatewayv1.GatewayConditionProgrammed),
			Status:             metav1.ConditionTrue,
			Reason:             "Programmed",
			Message:            "Gateway programmed successfully",
			LastTransitionTime: metav1.Now(),
		})

		// Populate status.addresses with actual assigned addresses from SSH tunnels
		r.updateGatewayAddresses(&k8sGw)

		if err := r.Status().Update(ctx, &k8sGw); err != nil {
			slog.With("function", "Reconcile").Error("failed to update Gateway status", "gateway", key, "error", err)
			return ctrl.Result{}, err
		}
	} else {
		// Handle deletion
		if containsString(k8sGw.Finalizers, gatewayFinalizer) {
			r.handleDeleteGateway(ctx, &k8sGw)
			k8sGw.Finalizers = removeString(k8sGw.Finalizers, gatewayFinalizer)
			if err := r.Update(ctx, &k8sGw); err != nil {
				slog.With("function", "Reconcile").Error("failed to remove finalizer", "gateway", key, "error", err)
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
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
			// Avoid duplicates
			if addressesSeen[addr] {
				continue
			}
			addressesSeen[addr] = true

			// Add to status addresses
			addrType := gatewayv1.HostnameAddressType
			statusAddresses = append(statusAddresses, gatewayv1.GatewayStatusAddress{
				Type:  &addrType,
				Value: addr,
			})
			slog.With("function", "updateGatewayAddresses").Debug("added address to Gateway status",
				"gateway", gwKey, "address", addr)
		}
	}

	k8sGw.Status.Addresses = statusAddresses
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
				}
			}
			updatedListeners[listenerName] = listener
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
