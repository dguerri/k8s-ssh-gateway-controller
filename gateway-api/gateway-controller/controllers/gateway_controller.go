package controllers

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	sshmgr "github.com/dguerri/pico-sh-gateway-api-controller/ssh"
)

const gatewayFinalizer = "gateway.networking.k8s.io/finalizer"

type GatewayReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	managers   map[string]*sshmgr.SSHTunnelManager
	managersMu sync.Mutex
}

func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.managers = make(map[string]*sshmgr.SSHTunnelManager)
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1.Gateway{}).
		Complete(r)
}

func (r *GatewayReconciler) GetManager(gwNamespace string, gwName string, listenerName string, listenerPort int, listenerProtocol string) *sshmgr.SSHTunnelManager {
	r.managersMu.Lock()
	defer r.managersMu.Unlock()

	return r.managers[getListenerKey(gwNamespace, gwName, listenerName, listenerPort, listenerProtocol)]
}

func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logAllGateways(ctx, r.Client)

	key := fmt.Sprintf("%s/%s", req.Namespace, req.Name)
	slog.Info("reconciling Gateway", "gateway", key, "request", req)

	var gateway gatewayv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			// Resource deleted before finalizer, nothing to do
			slog.Info("gateway deleted before finalizer, nothing to do", "gateway", key)
			return ctrl.Result{}, nil
		}
		slog.Error("unable to fetch Gateway", "gateway", key, "error", err)
	}

	if gateway.DeletionTimestamp.IsZero() {
		// Handle add or Update
		if !containsString(gateway.Finalizers, gatewayFinalizer) {
			// Add finalizer if not present
			gateway.Finalizers = append(gateway.Finalizers, gatewayFinalizer)
			if err := r.Update(ctx, &gateway); err != nil {
				slog.Error("failed to add finalizer", "gateway", key, "error", err)
				return ctrl.Result{}, err
			}
		}
		err := r.handleAddOrUpdateGateway(ctx, &gateway)
		if err != nil {
			return ctrl.Result{}, err
		}
	} else {
		// Handle deletion
		if containsString(gateway.Finalizers, gatewayFinalizer) {
			r.handleDeleteGateway(ctx, &gateway)
			gateway.Finalizers = removeString(gateway.Finalizers, gatewayFinalizer)
			if err := r.Update(ctx, &gateway); err != nil {
				slog.Error("failed to remove finalizer", "gateway", key, "error", err)
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

func (r *GatewayReconciler) handleAddOrUpdateGateway(_ context.Context, gw *gatewayv1.Gateway) error {
	gatewayKey := fmt.Sprintf("%s/%s", gw.Namespace, gw.Name)
	slog.Info("reconciling SSHManagers for Gateway", "gateway", gatewayKey)

	// Build desired listener keys
	desired := make(map[string]struct{}, len(gw.Spec.Listeners))
	for _, listener := range gw.Spec.Listeners {
		desired[getListenerKey(gw.Namespace, gw.Name, string(listener.Name), int(listener.Port), string(listener.Protocol))] = struct{}{}
	}

	// Remove managers for listeners no longer in spec. This handles updates.
	r.managersMu.Lock()
	for key, mgr := range r.managers {
		if _, keep := desired[key]; !keep {
			mgr.Close()
			delete(r.managers, key)
			slog.Info("SSHManager removed (listener gone)",
				"gateway", gatewayKey,
				"key", key,
			)
		}
	}
	r.managersMu.Unlock()

	// Create managers for any new listeners
	for _, listener := range gw.Spec.Listeners {
		listenerKey := getListenerKey(gw.Namespace, gw.Name, string(listener.Name), int(listener.Port), string(listener.Protocol))
		r.managersMu.Lock()
		if _, exists := r.managers[listenerKey]; !exists {
			if listener.Protocol == "HTTP" && listener.Port != 80 {
				slog.Error("invalid listener port for HTTP listener, pico.sh only supports port 80 and exposes 80 and 443)")
				return ErrInvalidPortForHTTPListener
			}
			mgr := sshmgr.NewSSHTunnelManager("/ssh/id", int(listener.Port))
			r.managers[listenerKey] = mgr
			slog.Info("SSHManager created",
				"gateway", gatewayKey,
				"listener", listener.Name,
				"port", listener.Port,
			)
		}
		r.managersMu.Unlock()
	}
	return nil
}

func (r *GatewayReconciler) handleDeleteGateway(_ context.Context, gw *gatewayv1.Gateway) {
	slog.Info("handling Delete Gateway", "namespace", gw.Namespace, "name", gw.Name)

	r.managersMu.Lock()
	defer r.managersMu.Unlock()

	// Remove SSHManager for each listener on this Gateway
	for _, listener := range gw.Spec.Listeners {
		listenerKey := getListenerKey(gw.Namespace, gw.Name, string(listener.Name), int(listener.Port), string(listener.Protocol))
		if mgr, ok := r.managers[listenerKey]; ok {
			mgr.Close()
			delete(r.managers, listenerKey)
			slog.Info("SSHManager removed for listener", "gateway", fmt.Sprintf("%s/%s", gw.Namespace, gw.Name), "listener", listener.Name)
		} else {
			slog.Warn("SSHManager already gone for listener", "gateway", fmt.Sprintf("%s/%s", gw.Namespace, gw.Name), "listener", listener.Name)
		}
	}
}

func getListenerKey(gwNamespace string, gwName string, listenerName string, listenerPort int, protocol string) string {
	return fmt.Sprintf("%s/%s/%s/%d/%s", gwNamespace, gwName, listenerName, listenerPort, protocol)
}

// containsString checks if a slice contains a string
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// removeString removes a string from a slice
func removeString(slice []string, s string) []string {
	var result []string
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}

// logAllGateways logs all Gateways in the cluster for debugging purposes.
func logAllGateways(ctx context.Context, c client.Client) error {
	logger := slog.With("function", "logAllGateways")
	var gwList gatewayv1.GatewayList

	if err := c.List(ctx, &gwList); err != nil {
		logger.Error("failed to list Gateways", "error", err)
		return fmt.Errorf("listing Gateways: %w", err)
	}

	if len(gwList.Items) == 0 {
		logger.Info("no Gateways found")
		return nil
	}
	logger.Info("found Gateways", "count", len(gwList.Items))

	for _, gw := range gwList.Items {
		key := fmt.Sprintf("%s/%s", gw.Namespace, gw.Name)
		gwLogger := logger.With("gateway", key)
		gwLogger.Info("logging Gateway")

		if len(gw.Annotations) > 0 {
			gwLogger.Info("Annotations", "annotations", gw.Annotations)
		}

		if len(gw.Spec.Listeners) > 0 {
			var listeners []string
			for _, l := range gw.Spec.Listeners {
				listeners = append(listeners, fmt.Sprintf("%s(%s:%d)", l.Name, l.Protocol, l.Port))
			}
			gwLogger.Info("Listeners", "listeners", listeners)
		}
	}
	logger.Info("finished logging Gateways")
	return nil
}
