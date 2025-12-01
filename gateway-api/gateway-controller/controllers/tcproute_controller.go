package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
		slog.With("function", "Reconcile", "httpRoute", req.NamespacedName).Debug("adding or updating TCPRoute")
		// Add a finalizer so we can correctly clean up the route when it's deleted
		if !containsString(k8sRoute.Finalizers, tcpRouteFinalizer) {
			k8sRoute.Finalizers = append(k8sRoute.Finalizers, tcpRouteFinalizer)
			if err := r.Update(ctx, &k8sRoute); err != nil {
				slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Error("failed to add finalizer", "error", err)
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
				slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Warn("gateway not ready or not found, will retry with backoff",
					"gateway", fmt.Sprintf("%s/%s", routeDetails.gwNamespace, routeDetails.gwName),
					"error", err.Error())
				// Return error to trigger controller-runtime's exponential backoff
				return ctrl.Result{}, err
			}
			slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Error("failed to set route", "error", err)
			return ctrl.Result{}, err
		}
		slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Debug("route set successfully")
	} else {
		// Handle deletion
		slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Info("processing TCPRoute deletion")
		if containsString(k8sRoute.Finalizers, tcpRouteFinalizer) {
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
					slog.With("function", "Reconcile", "tcpRoute", req.NamespacedName).Error("failed to remove route", "error", err)
					return ctrl.Result{}, err
				}
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

	// Periodically reconcile to retry failed route attachments and ensure routes are active
	return ctrl.Result{RequeueAfter: routeReconcilePeriod}, nil
}

// extractTCPRouteDetails extracts common route details from the resource.
func extractTCPRouteDetails(k8sRoute *gatewayv1alpha2.TCPRoute) (*routeDetails, error) {
	// Validate ParentRefs
	if len(k8sRoute.Spec.ParentRefs) < 1 {
		return nil, fmt.Errorf("TCPRoute %s/%s must have at least one ParentRef", k8sRoute.Namespace, k8sRoute.Name)
	}
	if len(k8sRoute.Spec.ParentRefs) > 1 {
		slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
			Debug("TCPRoute has more than one ParentRef, only the first will be used")
	}
	parent := k8sRoute.Spec.ParentRefs[0]

	// Validate ParentRef.Name
	if parent.Name == "" {
		return nil, fmt.Errorf("ParentRef name is empty for TCPRoute %s/%s", k8sRoute.Namespace, k8sRoute.Name)
	}

	// Default ParentRef.Namespace
	parentNamespace := k8sRoute.Namespace // Default to the TCPRoute's namespace
	if parent.Namespace != nil {
		parentNamespace = string(*parent.Namespace)
	} else {
		slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
			Debug("ParentRef namespace is nil, defaulting to TCPRoute namespace")
	}

	// Validate ParentRef.SectionName
	if parent.SectionName == nil {
		return nil, fmt.Errorf("ParentRef sectionName is nil for TCPRoute %s/%s", k8sRoute.Namespace, k8sRoute.Name)
	}

	// Validate Rules
	if len(k8sRoute.Spec.Rules) < 1 {
		return nil, fmt.Errorf("TCPRoute %s/%s must have at least one Rule", k8sRoute.Namespace, k8sRoute.Name)
	}
	if len(k8sRoute.Spec.Rules) > 1 {
		slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
			Debug("TCPRoute has more than one Rule, only the first will be used")
	}
	rule := k8sRoute.Spec.Rules[0]

	// Validate BackendRefs
	if len(rule.BackendRefs) < 1 {
		return nil, fmt.Errorf("TCPRoute %s/%s must have at least one BackendRef", k8sRoute.Namespace, k8sRoute.Name)
	}
	if len(rule.BackendRefs) > 1 {
		slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
			Debug("TCPRoute Rule has more than one BackendRef, only the first will be used")
	}
	k8Svc := rule.BackendRefs[0]

	// Validate BackendRef.Name
	if k8Svc.Name == "" {
		return nil, fmt.Errorf("BackendRef name is empty for TCPRoute %s/%s", k8sRoute.Namespace, k8sRoute.Name)
	}

	// Validate BackendRef.Port
	if k8Svc.Port == nil {
		return nil, fmt.Errorf("BackendRef port is nil for TCPRoute %s/%s", k8sRoute.Namespace, k8sRoute.Name)
	}

	// Validate TCPRoute.Namespace
	if k8sRoute.Namespace == "" {
		return nil, fmt.Errorf("TCPRoute namespace is nil or empty for TCPRoute %s", k8sRoute.Name)
	}

	// Default BackendRef.Namespace
	backendNamespace := k8sRoute.Namespace // Default to the TCPRoute's namespace
	if k8Svc.Namespace != nil {
		backendNamespace = string(*k8Svc.Namespace)
	} else {
		slog.With("route", fmt.Sprintf("%s/%s", k8sRoute.Namespace, k8sRoute.Name)).
			Debug("BackendRef namespace is nil, defaulting to TCPRoute namespace")
	}

	return &routeDetails{
		routeName:      string(k8sRoute.Name),
		routeNamespace: string(k8sRoute.Namespace),
		gwName:         string(parent.Name),
		gwNamespace:    parentNamespace,
		listenerName:   string(*parent.SectionName),
		backendHost:    getSvcHostname(string(k8Svc.Name), backendNamespace),
		backendPort:    int(*k8Svc.Port),
	}, nil
}
