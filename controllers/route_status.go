package controllers

import (
	"context"
	"log/slog"
	"reflect"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// ptrEqual reports whether two pointers reference equal values (nil == nil).
func ptrEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// parentRefEqual reports whether two ParentReferences identify the same parent.
func parentRefEqual(a, b gatewayv1.ParentReference) bool {
	return ptrEqual(a.Group, b.Group) &&
		ptrEqual(a.Kind, b.Kind) &&
		ptrEqual(a.Namespace, b.Namespace) &&
		a.Name == b.Name &&
		ptrEqual(a.SectionName, b.SectionName) &&
		ptrEqual(a.Port, b.Port)
}

// acceptedRouteConditions returns the conditions for a route that has been
// accepted and whose references resolved successfully.
func acceptedRouteConditions() []metav1.Condition {
	return []metav1.Condition{
		{
			Type:    string(gatewayv1.RouteConditionAccepted),
			Status:  metav1.ConditionTrue,
			Reason:  string(gatewayv1.RouteReasonAccepted),
			Message: "Route accepted",
		},
		{
			Type:    string(gatewayv1.RouteConditionResolvedRefs),
			Status:  metav1.ConditionTrue,
			Reason:  string(gatewayv1.RouteReasonResolvedRefs),
			Message: "All references resolved",
		},
	}
}

// pendingRouteConditions returns the conditions for a route whose parent Gateway
// is not yet ready, so attachment is still pending.
func pendingRouteConditions(msg string) []metav1.Condition {
	return []metav1.Condition{
		{
			Type:    string(gatewayv1.RouteConditionAccepted),
			Status:  metav1.ConditionFalse,
			Reason:  string(gatewayv1.RouteReasonPending),
			Message: msg,
		},
	}
}

// setOurRouteParentStatus upserts this controller's RouteParentStatus entry for
// the given parentRef, applying the supplied conditions. Entries written by
// other controllers are left untouched. It returns true if rs.Parents changed.
func setOurRouteParentStatus(rs *gatewayv1.RouteStatus, parentRef gatewayv1.ParentReference, generation int64, conditions []metav1.Condition) bool {
	controllerName := gatewayv1.GatewayController(getGatewayControllerName())

	idx := -1
	for i := range rs.Parents {
		if rs.Parents[i].ControllerName == controllerName && parentRefEqual(rs.Parents[i].ParentRef, parentRef) {
			idx = i
			break
		}
	}

	var conds []metav1.Condition
	if idx >= 0 {
		conds = append(conds, rs.Parents[idx].Conditions...)
	}
	for _, c := range conditions {
		c.ObservedGeneration = generation
		apiMeta.SetStatusCondition(&conds, c)
	}

	parent := gatewayv1.RouteParentStatus{
		ParentRef:      parentRef,
		ControllerName: controllerName,
		Conditions:     conds,
	}

	if idx >= 0 {
		if reflect.DeepEqual(rs.Parents[idx], parent) {
			return false
		}
		rs.Parents[idx] = parent
		return true
	}
	rs.Parents = append(rs.Parents, parent)
	return true
}

// writeRouteParentStatus updates the route's parent status for this controller
// and persists it, but only when something actually changed. Status update
// failures are logged and swallowed: the route forwarding is already set up, so
// a transient status write error should not fail reconciliation.
func writeRouteParentStatus[T client.Object](
	ctx context.Context,
	c client.Client,
	route T,
	rs *gatewayv1.RouteStatus,
	parentRef gatewayv1.ParentReference,
	generation int64,
	conditions []metav1.Condition,
	logKind string,
) {
	if c == nil || ctx == nil {
		return
	}
	if !setOurRouteParentStatus(rs, parentRef, generation, conditions) {
		return
	}
	if err := c.Status().Update(ctx, route); err != nil {
		slog.With("function", "writeRouteParentStatus", logKind, route.GetNamespace()+"/"+route.GetName()).
			Warn("failed to update route status", "error", err)
	}
}
