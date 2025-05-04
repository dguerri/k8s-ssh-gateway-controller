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
	"sigs.k8s.io/controller-runtime/pkg/predicate"
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
		WithEventFilter(predicate.GenerationChangedPredicate{}). // only trigger on spec changes
		Complete(r)
}

func (r *TCPRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Debug("starting reconciliation")

	var k8sRoute gatewayv1alpha2.TCPRoute
	if err := r.Get(ctx, req.NamespacedName, &k8sRoute); err != nil {
		slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Debug("unable to retrieve TCPRoute", "error", err)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	routeDetails, err := extractTCPRouteDetails(&k8sRoute)
	if err != nil {
		slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Error("failed to extract TCPRoute details", "error", err)
		return ctrl.Result{}, err
	}

	if k8sRoute.DeletionTimestamp.IsZero() {
		// Add or Update.

		// Add a finalizer so we can scorrectly clean up the route when it's deleted
		if !containsString(k8sRoute.Finalizers, tcpRouteFinalizer) {
			k8sRoute.Finalizers = append(k8sRoute.Finalizers, tcpRouteFinalizer)
			if err := r.Update(ctx, &k8sRoute); err != nil {
				slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Error("failed to add finalizer", "error", err)
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
				slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Debug("gateway not ready, requeuing TCPRoute")
				return ctrl.Result{Requeue: true, RequeueAfter: 1 * time.Second}, nil
			}
			slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Error("failed to set route", "error", err)
			return ctrl.Result{}, err
		}
		slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Debug("route set successfully")
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
					slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Error("failed to remove route", "error", err)
					return ctrl.Result{}, err
				}
				// Gateway or route were deleted, no need to requeue
			}
			// Gateway or route were deleted, no need to requeue

			k8sRoute.Finalizers = removeString(k8sRoute.Finalizers, tcpRouteFinalizer)
			if err := r.Update(ctx, &k8sRoute); err != nil {
				slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Error("failed to remove finalizer", "error", err)
				return ctrl.Result{}, err
			}
			slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Debug("route removed successfully")
		}
	}

	return ctrl.Result{}, nil
}

// extractTCPRouteDetails extracts common route details from the resource.
func extractTCPRouteDetails(k8sRoute *gatewayv1alpha2.TCPRoute) (*routeDetails, error) {
	if len(k8sRoute.Spec.ParentRefs) < 1 {
		return nil, fmt.Errorf("TCPRoute %s/%s must have at least one ParentRef", k8sRoute.Namespace, k8sRoute.Name)
	}
	if len(k8sRoute.Spec.ParentRefs) > 1 {
		slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
			Debug("TCPRoute has more than one ParentRef, only the first will be used")
	}
	parent := k8sRoute.Spec.ParentRefs[0]

	if len(k8sRoute.Spec.Rules) < 1 {
		return nil, fmt.Errorf("TCPRoute %s/%s must have at least one Rule", k8sRoute.Namespace, k8sRoute.Name)
	}
	if len(k8sRoute.Spec.Rules) > 1 {
		slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
			Debug("TCPRoute has more than one Rule, only the first will be used")
	}
	rule := k8sRoute.Spec.Rules[0]

	if len(rule.BackendRefs) < 1 {
		return nil, fmt.Errorf("TCPRoute %s/%s must have at least one BackendRef", k8sRoute.Namespace, k8sRoute.Name)
	}
	if len(rule.BackendRefs) > 1 {
		slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
			Debug("TCPRoute Rule has more than one BackendRef, only the first will be used")
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
