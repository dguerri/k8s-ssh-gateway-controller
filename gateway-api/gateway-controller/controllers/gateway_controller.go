package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	sshmgr "github.com/dguerri/pico-sh-gateway-api-controller/ssh"
)

const gatewayFinalizer = "gateway.networking.k8s.io/finalizer"

type Route struct {
	Name      string
	Namespace string
	Host      string
	Port      int
}

type Listener struct {
	Protocol string
	Hostname string
	Port     int
	route    *Route
}

type gateway struct {
	manager *sshmgr.SSHTunnelManager

	listeners   map[string]*Listener
	listenersMu sync.Mutex
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
			err := gw.manager.StopForwarding(&sshmgr.ForwardingConfig{
				RemoteHost:   l.Hostname,
				RemotePort:   l.Port,
				InternalHost: l.route.Host,
				InternalPort: l.route.Port,
			})
			if err != nil {
				return fmt.Errorf("failed to stop forwarding: %s", err)
			}
		}
		// Start the forwarding.
		route := Route{
			Name:      routeName,
			Namespace: routeNamespace,
			Host:      backendHost,
			Port:      backendPort,
		}
		err := gw.manager.StartForwarding(&sshmgr.ForwardingConfig{
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
		}
		// Forwarding is set, add the new route
		l.route = &route
		slog.With("function", "SetRoute", "gateway", gwNamespace+"/"+gwName, "listener", listenerName).Info("route set successfully")
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
		err := gw.manager.StopForwarding(&sshmgr.ForwardingConfig{
			RemoteHost:   l.Hostname,
			RemotePort:   l.Port,
			InternalHost: l.route.Host,
			InternalPort: l.route.Port,
		})
		if err != nil {
			slog.With("function", "RemoveRoute", "gateway", gwNamespace+"/"+gwName, "listener", listenerName).Error("failed to stop forwarding", "error", err)
			return fmt.Errorf("failed to stop forwarding: %s", err)
		}
		l.route = nil
		slog.With("function", "RemoveRoute", "gateway", gwNamespace+"/"+gwName, "listener", listenerName).Info("route removed successfully")
		return nil
	}

	return &ErrRouteNotFound{fmt.Sprintf("route not found: %s/%s", routeNamespace, routeName)}
}

type GatewayReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	gateways   map[string]*gateway
	gatewaysMu sync.Mutex
}

func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.gateways = make(map[string]*gateway)
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1.Gateway{}).
		Complete(r)
}

func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	key := fmt.Sprintf("%s/%s", req.Namespace, req.Name)
	slog.With("function", "Reconcile").Info("reconciling Gateway", "gateway", key, "request", req)

	var k8sGw gatewayv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &k8sGw); err != nil {
		slog.With("function", "Reconcile").Info("unable to retrieve gateway", "gateway", req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
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
	} else {
		// Handle deletion
		if containsString(k8sGw.Finalizers, gatewayFinalizer) {
			r.handleDeleteGateway(ctx, &k8sGw)
			k8sGw.Finalizers = removeString(k8sGw.Finalizers, gatewayFinalizer)
			if err := r.Update(ctx, &k8sGw); err != nil {
				slog.Error("failed to remove finalizer", "gateway", key, "error", err)
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

func (r *GatewayReconciler) handleAddOrUpdateGateway(ctx context.Context, k8sGw *gatewayv1.Gateway) error {
	r.gatewaysMu.Lock()
	defer r.gatewaysMu.Unlock()

	gwKey := getGwKey(k8sGw.Namespace, k8sGw.Name)
	gw, exists := r.gateways[gwKey]
	if !exists {
		slog.With("function", "handleAddOrUpdateGateway").Info("creating a new gateway", "gatewayKey", gwKey)
		mgr, err := createSSHManager(ctx)
		if err != nil {
			slog.With("function", "handleAddOrUpdateGateway", "gatewayKey", gwKey).Error("failed to create SSH manager", "error", err)
			return fmt.Errorf("failed to create SSH manager for gateway %s: %w", gwKey, err)
		}
		slog.With("function", "handleAddOrUpdateGateway").Info("SSHTunnelManager created for Gateway", "gateway", gwKey)

		gw = &gateway{
			manager:   mgr,
			listeners: make(map[string]*Listener),
		}
		for _, k8sListener := range k8sGw.Spec.Listeners {
			listener := createListener(k8sListener)
			gw.listeners[string(k8sListener.Name)] = listener
		}
		r.gateways[gwKey] = gw
	} else {
		slog.With("function", "handleAddOrUpdateGateway").Info("updating existing gateway", "gatewayKey", gwKey)
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
					slog.With("function", "handleAddOrUpdateGateway").Info("listener configuration changed, updating", "listener", listenerName)
					// Stop existing forwarding if are not longer needed
					if existing.route != nil {
						gw.manager.StopForwarding(&sshmgr.ForwardingConfig{
							RemoteHost:   existing.Hostname,
							RemotePort:   existing.Port,
							InternalHost: existing.route.Host,
							InternalPort: existing.route.Port,
						})
					}
				}
			}
			updatedListeners[listenerName] = listener
		}

		gw.listeners = updatedListeners
	}

	slog.With("function", "handleAddOrUpdateGateway", "gateway", gwKey, "listenerCount", len(k8sGw.Spec.Listeners)).Info("gateway processed successfully")
	return nil
}

func (r *GatewayReconciler) handleDeleteGateway(_ context.Context, k8sGw *gatewayv1.Gateway) {
	slog.With("function", "handleDeleteGateway").Info("handling Delete Gateway", "namespace", k8sGw.Namespace, "name", k8sGw.Name)

	// Clean up single manager for this Gateway
	gwKey := getGwKey(k8sGw.Namespace, k8sGw.Name)
	r.gatewaysMu.Lock()
	defer r.gatewaysMu.Unlock()
	if gw, ok := r.gateways[gwKey]; ok {
		gw.manager.Stop()
		delete(r.gateways, gwKey)
		slog.With("function", "handleDeleteGateway").Info("SSHTunnelManager stopped for Gateway", "gateway", gwKey)
	}
}
