package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

const tcpRouteFinalizer = "tcproute.networking.k8s.io/finalizer"

type TCPRouteReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	GatewayReconciler *GatewayReconciler
}

func (r *TCPRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1alpha2.TCPRoute{}).
		Complete(r)
}

func (r *TCPRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.dumpAllTCPRoutes(ctx)

	var k8sRoute gatewayv1alpha2.TCPRoute
	if err := r.Get(ctx, req.NamespacedName, &k8sRoute); err != nil {
		slog.With("function", "Reconcile").Info("unable to retrieve TCPRoute", "tcp route", req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	routeDetails, err := extractTCPRouteDetails(&k8sRoute)
	if err != nil {
		slog.Error("Failed to extract TCPRoute details", "error", err)
		return ctrl.Result{}, err
	}

	if k8sRoute.DeletionTimestamp.IsZero() {
		// Add or Update.

		// Add a finalizer so we can scorrectly clean up the route when it's deleted
		if !containsString(k8sRoute.Finalizers, tcpRouteFinalizer) {
			k8sRoute.Finalizers = append(k8sRoute.Finalizers, tcpRouteFinalizer)
			if err := r.Update(ctx, &k8sRoute); err != nil {
				return ctrl.Result{}, err
			}
		}

		err := r.GatewayReconciler.SetRoute(
			routeDetails.gwNamespace, routeDetails.gwName,
			routeDetails.listenerName,
			routeDetails.routeName, routeDetails.routeNamespace,
			routeDetails.backendHost, routeDetails.backendPort)
		if err != nil {
			var notReadyErr *ErrGatewayNotReady
			var notFoundErr *ErrGatewayNotFound
			if errors.As(err, &notReadyErr) || errors.As(err, &notFoundErr) {
				slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
					Info("gateway not ready, requeuing TCPRoute")
					// Requeue the request after a short delay
				return ctrl.Result{Requeue: true, RequeueAfter: 1 * time.Second}, nil
			}
			return ctrl.Result{}, err
		}
	} else {
		// Handle deletion
		if containsString(k8sRoute.Finalizers, tcpRouteFinalizer) {
			err := r.GatewayReconciler.RemoveRoute(
				routeDetails.gwNamespace, routeDetails.gwName,
				routeDetails.listenerName,
				routeDetails.routeName, routeDetails.routeNamespace,
				routeDetails.backendHost, routeDetails.backendPort)
			if err != nil {
				var gwNotFoundErr *ErrGatewayNotFound
				var routeNotFoundErr *ErrRouteNotFound
				if !errors.As(err, &gwNotFoundErr) && !errors.As(err, &routeNotFoundErr) {
					return ctrl.Result{}, err
				}
				// Gateway or route were deleted, no need to requeue
			}

			k8sRoute.Finalizers = removeString(k8sRoute.Finalizers, tcpRouteFinalizer)
			if err := r.Update(ctx, &k8sRoute); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

// extractHTTPRouteDetails extracts common route details from the resource.
func extractTCPRouteDetails(k8sRoute *gatewayv1alpha2.TCPRoute) (*routeDetails, error) {
	// Check ParentRefs
	if len(k8sRoute.Spec.ParentRefs) < 1 {
		return nil, fmt.Errorf("HTTPRoute %s/%s must have at least one ParentRef", k8sRoute.Namespace, k8sRoute.Name)
	}
	if len(k8sRoute.Spec.ParentRefs) > 1 {
		slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
			Warn("HTTPRoute has more than one ParentRef, only the first will be used")
	}
	parent := k8sRoute.Spec.ParentRefs[0]

	// Check Rules
	if len(k8sRoute.Spec.Rules) < 1 {
		return nil, fmt.Errorf("HTTPRoute %s/%s must have at least one Rule", k8sRoute.Namespace, k8sRoute.Name)
	}
	if len(k8sRoute.Spec.Rules) > 1 {
		slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
			Warn("HTTPRoute has more than one Rule, only the first will be used")
	}
	rule := k8sRoute.Spec.Rules[0]

	// Check BackendRefs
	if len(rule.BackendRefs) < 1 {
		return nil, fmt.Errorf("HTTPRoute %s/%s must have at least one BackendRef", k8sRoute.Namespace, k8sRoute.Name)
	}
	if len(rule.BackendRefs) > 1 {
		slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
			Warn("HTTPRoute Rule has more than one BackendRef, only the first will be used")
	}
	k8Svc := rule.BackendRefs[0]

	return &routeDetails{
		routeName:      string(k8sRoute.Name),
		routeNamespace: string(k8sRoute.Namespace),
		gwName:         string(parent.Name),
		gwNamespace:    string(*parent.Namespace),
		listenerName:   string(*parent.SectionName),
		backendHost:    getSvcHostname(string(k8Svc.Name), string(*k8Svc.Namespace)),
		backendPort:    int(*k8Svc.Port),
	}, nil
}

func (r *TCPRouteReconciler) dumpAllTCPRoutes(ctx context.Context) {
	slog.Info("dumping all TCPRoutes from internal context:")
	r.GatewayReconciler.gatewaysMu.Lock()
	for gwKey, gw := range r.GatewayReconciler.gateways {
		gw.listenersMu.Lock()
		for listenerKey, listener := range gw.listeners {
			if listener.route != nil {
				slog.Info("TCPRoute found in internal context",
					"gateway", gwKey,
					"listener", listenerKey,
					"routeName", listener.route.Name,
					"routeNamespace", listener.route.Namespace,
					"backendHost", listener.route.Host,
					"backendPort", listener.route.Port,
				)
			}
		}
		gw.listenersMu.Unlock()
	}
	r.GatewayReconciler.gatewaysMu.Unlock()

	slog.Info("dumping all TCPRoutes from Kubernetes:")
	var tcpRoutes gatewayv1alpha2.TCPRouteList
	if err := r.List(ctx, &tcpRoutes); err != nil {
		slog.Error("failed to list TCPRoutes from Kubernetes", "error", err)
		return
	}

	for _, route := range tcpRoutes.Items {
		for _, parentRef := range route.Spec.ParentRefs {
			slog.Info("TCPRoute found in Kubernetes",
				"routeName", route.Name,
				"routeNamespace", route.Namespace,
				"parentGateway", fmt.Sprintf("%s/%s", *parentRef.Namespace, parentRef.Name),
				"listener", *parentRef.SectionName,
			)
		}
	}
}
