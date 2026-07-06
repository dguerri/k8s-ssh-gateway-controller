package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1alpha3 "sigs.k8s.io/gateway-api/apis/v1alpha3"
)

type TLSRouteReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	GatewayReconciler *GatewayReconciler
}

func (r *TLSRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1alpha3.TLSRoute{}).
		Complete(r)
}

func (r *TLSRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Debug("starting reconciliation")

	var k8sRoute gatewayv1alpha3.TLSRoute
	if err := r.Get(ctx, req.NamespacedName, &k8sRoute); err != nil {
		slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Debug("unable to retrieve TLSRoute", "error", err)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	isManagedByUs := containsString(k8sRoute.Finalizers, getTLSRouteFinalizer())

	// Identify the parent Gateway from the route's first ParentRef so we can
	// decide whether this route is ours BEFORE running strict validation. Routes
	// belonging to other controllers legitimately omit the optional sectionName,
	// so validating them fully here would surface spurious reconcile errors.
	if !isManagedByUs {
		gwNamespace, gwName, err := extractParentGatewayRef(k8sRoute.Spec.ParentRefs, k8sRoute.Namespace)
		if err != nil {
			slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Debug("route has no usable parent Gateway ref, skipping", "error", err)
			return ctrl.Result{}, nil
		}
		isManaged, err := IsGatewayManaged(ctx, r.Client, gwNamespace, gwName)
		if err != nil {
			slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Error("failed to check if gateway is managed",
				"gateway", fmt.Sprintf("%s/%s", gwNamespace, gwName),
				"error", err)
			return ctrl.Result{}, err
		}
		if !isManaged {
			slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Debug("gateway not managed by this controller, skipping",
				"gateway", fmt.Sprintf("%s/%s", gwNamespace, gwName))
			return ctrl.Result{}, nil
		}
	}

	// The route is ours (already owned, or it targets a Gateway we manage):
	// validate it fully.
	routeDetails, err := extractTLSRouteDetails(&k8sRoute)
	if err != nil {
		slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Error("failed to extract TLSRoute details", "error", err)
		return ctrl.Result{}, err
	}

	if !k8sRoute.DeletionTimestamp.IsZero() {
		if isManagedByUs {
			return r.handleDeletion(ctx, req, &k8sRoute, routeDetails)
		}
		// Not ours, skip
		slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Debug("route being deleted but not managed by us, skipping")
		return ctrl.Result{}, nil
	}

	return r.handleAddOrUpdate(ctx, req, &k8sRoute, routeDetails)
}

func (r *TLSRouteReconciler) handleAddOrUpdate(ctx context.Context, req ctrl.Request, k8sRoute *gatewayv1alpha3.TLSRoute, routeDetails *routeDetails) (ctrl.Result, error) {
	// Add or Update.
	slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Debug("adding or updating TLSRoute")
	// Add a finalizer so we can correctly clean up the route when it's deleted
	if !containsString(k8sRoute.Finalizers, getTLSRouteFinalizer()) {
		k8sRoute.Finalizers = append(k8sRoute.Finalizers, getTLSRouteFinalizer())
		if err := r.Update(ctx, k8sRoute); err != nil {
			slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Error("failed to add finalizer", "error", err)
			return ctrl.Result{}, err
		}
	}

	err := r.GatewayReconciler.SetRoute(
		ctx,
		routeDetails.gwNamespace, routeDetails.gwName,
		routeDetails.listenerName,
		routeDetails.routeName, routeDetails.routeNamespace,
		routeDetails.backendHost, routeDetails.backendPort)
	if err != nil {
		var notReadyErr *ErrGatewayNotReady
		var notFoundErr *ErrGatewayNotFound
		if errors.As(err, &notReadyErr) || errors.As(err, &notFoundErr) {
			slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Warn("gateway not ready or not found, will retry with backoff",
				"gateway", fmt.Sprintf("%s/%s", routeDetails.gwNamespace, routeDetails.gwName),
				"error", err.Error())
			// Return error to trigger controller-runtime's exponential backoff
			return ctrl.Result{}, err
		}
		slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Error("failed to set route", "error", err)
		return ctrl.Result{}, err
	}
	slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Debug("route set successfully")
	return ctrl.Result{RequeueAfter: routeReconcilePeriod}, nil
}

func (r *TLSRouteReconciler) handleDeletion(ctx context.Context, req ctrl.Request, k8sRoute *gatewayv1alpha3.TLSRoute, routeDetails *routeDetails) (ctrl.Result, error) {
	// Handle deletion
	slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Info("processing TLSRoute deletion")
	if containsString(k8sRoute.Finalizers, getTLSRouteFinalizer()) {
		err := r.GatewayReconciler.RemoveRoute(
			ctx,
			routeDetails.gwNamespace, routeDetails.gwName,
			routeDetails.listenerName,
			routeDetails.routeName, routeDetails.routeNamespace,
			routeDetails.backendHost, routeDetails.backendPort)
		if err != nil {
			var gwNotFoundErr *ErrGatewayNotFound
			var routeNotFoundErr *ErrRouteNotFound
			if !errors.As(err, &gwNotFoundErr) && !errors.As(err, &routeNotFoundErr) {
				slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Error("failed to remove route", "error", err)
				return ctrl.Result{}, err
			}
		}
		// Gateway or route were deleted, no need to requeue

		k8sRoute.Finalizers = removeString(k8sRoute.Finalizers, getTLSRouteFinalizer())
		if err := r.Update(ctx, k8sRoute); err != nil {
			slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Error("failed to remove finalizer", "error", err)
			return ctrl.Result{}, err
		}
		slog.With("function", "Reconcile", "tlsRoute", req.NamespacedName).Debug("route removed successfully")
	}
	return ctrl.Result{RequeueAfter: routeReconcilePeriod}, nil
}
