package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dguerri/pico-sh-gateway-api-controller/ssh"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type HTTPRouteReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	GatewayReconciler *GatewayReconciler
}

func (r *HTTPRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1.HTTPRoute{}).
		Complete(r)
}

func (r *HTTPRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logAllHTTPRoutes(ctx, r.Client)

	routeKey := fmt.Sprintf("%s/%s", req.Namespace, req.Name)
	logger := slog.With("function", "ReconcileHTTPRoute", "route", routeKey)
	logger.Info("reconciling HTTPRoute")

	var route gatewayv1.HTTPRoute
	if err := r.Get(ctx, req.NamespacedName, &route); err != nil {
		logger.Error("unable to fetch HTTPRoute", "error", err)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Warn if more than one parentRef or multiple rules (not supported)
	if len(route.Spec.ParentRefs) != 1 {
		slog.Warn("only single parentRef supported", "route", routeKey, "parentRefsCount", len(route.Spec.ParentRefs))
	}
	if len(route.Spec.Rules) != 1 {
		slog.Warn("only single rule supported", "route", routeKey, "rulesCount", len(route.Spec.Rules))
	}

	if len(route.Spec.ParentRefs) != 1 {
		slog.Error("unsupported number of parentRefs", "route", routeKey, "count", len(route.Spec.ParentRefs))
		return ctrl.Result{}, nil
	}
	if route.Spec.ParentRefs[0].SectionName == nil {
		slog.Error("missing sectionName in parentRef", "route", routeKey)
		return ctrl.Result{}, nil
	}
	if len(route.Spec.Rules) != 1 {
		slog.Error("unsupported number of rules", "route", routeKey, "count", len(route.Spec.Rules))
		return ctrl.Result{}, nil
	}
	if len(route.Spec.Rules[0].BackendRefs) != 1 {
		slog.Error("unsupported number of backendRefs", "route", routeKey, "count", len(route.Spec.Rules[0].BackendRefs))
		return ctrl.Result{}, nil
	}

	if route.DeletionTimestamp.IsZero() {
		if err := r.handleAddOrUpdateHTTPRoute(ctx, &route); err != nil {
			if errors.Is(err, ErrNoGateway) {
				// retry later if no Gateway is available yet
				return ctrl.Result{Requeue: true, RequeueAfter: requeueBackoff}, nil
			}
			return ctrl.Result{Requeue: true}, err
		}
	} else {
		if err := r.handleDeleteHTTPRoute(ctx, &route); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *HTTPRouteReconciler) getSSHManager(ctx context.Context, gwNamespace string, parentName string, listenerName string) (*ssh.SSHTunnelManager, error) {
	// Fetch the Gateway to determine the listener's port
	var gw gatewayv1.Gateway
	if err := r.Get(ctx, types.NamespacedName{Namespace: gwNamespace, Name: parentName}, &gw); err != nil {
		slog.Error("unable to fetch parent Gateway", "gateway", fmt.Sprintf("%s/%s", gwNamespace, parentName), "error", err)
		return nil, ErrNoGateway
	}
	var listenerPort int
	var listenerProtocol string
	for _, l := range gw.Spec.Listeners {
		if string(l.Name) == listenerName {
			listenerPort = int(l.Port)
			listenerProtocol = string(l.Protocol)
			break
		}
	}
	if listenerPort == 0 {
		slog.Error("listener not found on Gateway spec", "gateway", fmt.Sprintf("%s/%s", gwNamespace, parentName), "listener", listenerName)
		return nil, ErrNoListener
	}
	return r.GatewayReconciler.GetManager(gwNamespace, parentName, listenerName, listenerPort, listenerProtocol), nil
}

func (r *HTTPRouteReconciler) handleAddOrUpdateHTTPRoute(ctx context.Context, route *gatewayv1.HTTPRoute) error {
	routeKey := fmt.Sprintf("%s/%s", route.Namespace, route.Name)
	slog.Info("handling Add/Update HTTPRoute", "route", routeKey)

	parent := route.Spec.ParentRefs[0]
	// Determine the namespace of the parent Gateway (default to this route's namespace)
	gwNamespace := route.Namespace
	if parent.Namespace != nil {
		gwNamespace = string(*parent.Namespace)
	}
	listenerName := string(*parent.SectionName)

	mgr, err := r.getSSHManager(ctx, gwNamespace, string(parent.Name), listenerName)
	if err != nil {
		slog.Error("no Gateway found for listener", "gwNamespace", gwNamespace, "parent.Name", parent.Name, "listenerName", listenerName, "error", err)
		return err
	}

	internalHost := string(route.Spec.Rules[0].BackendRefs[0].Name)
	internalSvcNamespace := string(*route.Spec.Rules[0].BackendRefs[0].Namespace)
	internalPort := int(*route.Spec.Rules[0].BackendRefs[0].Port)

	// Remove any sessions not matching this route
	mgr.CleanupSessions(routeKey, internalHost, internalPort)

	remoteHostname := ""
	if val, ok := route.Annotations["pico.sh/remote-hostname"]; ok {
		remoteHostname = val
	}
	slog.Info("using", "remote hostname", remoteHostname)

	_, err = mgr.StartRemotePortForwarding(ctx, routeKey, remoteHostname, fmt.Sprintf("%s.%s.svc.cluster.local", internalHost, internalSvcNamespace), internalPort)
	if err != nil {
		return fmt.Errorf("failed to start remote port forwarding: %w", err)
	}
	slog.Info("SSH session registered for HTTPRoute", "route", routeKey)
	return nil
}

func (r *HTTPRouteReconciler) handleDeleteHTTPRoute(ctx context.Context, route *gatewayv1.HTTPRoute) error {
	routeKey := fmt.Sprintf("%s/%s", route.Namespace, route.Name)
	slog.Info("handling Delete HTTPRoute", "route", routeKey)

	// Remove the session corresponding to this route
	backend := route.Spec.Rules[0].BackendRefs[0]
	internalHost := string(backend.Name)
	internalPort := int(*backend.Port)
	parent := route.Spec.ParentRefs[0]
	gwNamespace := route.Namespace
	if parent.Namespace != nil {
		gwNamespace = string(*parent.Namespace)
	}
	listenerName := string(*parent.SectionName)

	mgr, err := r.getSSHManager(ctx, gwNamespace, string(parent.Name), listenerName)
	if err != nil {
		slog.Error("no Gateway found for listener", "gwNamespace", gwNamespace, "parent.Name", parent.Name, "listenerName", listenerName, "error", err)
		return err
	}
	mgr.RemoveSession(routeKey, internalHost, internalPort)
	return nil
}

// logAllHTTPRoutes logs all HTTPRoutes in the cluster for debugging purposes.
func logAllHTTPRoutes(ctx context.Context, c client.Client) error {
	logger := slog.With("function", "logAllHTTPRoutes")
	var routeList gatewayv1.HTTPRouteList

	if err := c.List(ctx, &routeList); err != nil {
		logger.Error("failed to list HTTPRoutes", "error", err)
		return fmt.Errorf("listing HTTPRoutes: %w", err)
	}

	if len(routeList.Items) == 0 {
		logger.Info("no HTTPRoutes found")
		return nil
	}
	logger.Info("found HTTPRoutes", "count", len(routeList.Items))

	for _, route := range routeList.Items {
		routeLogger := logger.With("namespace", route.Namespace, "name", route.Name)
		routeLogger.Info("logging HTTPRoute")

		// Collect and log parentRefs
		var parents []string
		for _, pr := range route.Spec.ParentRefs {
			ns := route.Namespace
			if pr.Namespace != nil {
				ns = string(*pr.Namespace)
			}
			sec := ""
			if pr.SectionName != nil {
				sec = string(*pr.SectionName)
			}
			parents = append(parents, fmt.Sprintf("%s/%s (section: %s)", ns, pr.Name, sec))
		}
		routeLogger.Info("ParentRefs", "parents", parents)

		// Collect and log backendRefs under rules
		var backends []string
		for _, rule := range route.Spec.Rules {
			for _, br := range rule.BackendRefs {
				backends = append(backends, fmt.Sprintf("%s:%d", br.Name, *br.Port))
			}
		}
		routeLogger.Info("BackendRefs", "backends", backends)
	}

	logger.Info("finished logging HTTPRoutes")
	return nil
}
