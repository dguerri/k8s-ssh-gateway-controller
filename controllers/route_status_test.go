package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestParentRefEqual(t *testing.T) {
	section1 := gatewayv1.SectionName("http")
	section2 := gatewayv1.SectionName("tcp")
	ns := gatewayv1.Namespace("default")

	base := gatewayv1.ParentReference{Name: "gw", Namespace: &ns, SectionName: &section1}

	assert.True(t, parentRefEqual(base, gatewayv1.ParentReference{Name: "gw", Namespace: &ns, SectionName: &section1}))
	assert.False(t, parentRefEqual(base, gatewayv1.ParentReference{Name: "other", Namespace: &ns, SectionName: &section1}))
	assert.False(t, parentRefEqual(base, gatewayv1.ParentReference{Name: "gw", Namespace: &ns, SectionName: &section2}))
	assert.False(t, parentRefEqual(base, gatewayv1.ParentReference{Name: "gw", SectionName: &section1}))
}

func TestSetOurRouteParentStatus_AddsEntry(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	rs := &gatewayv1.RouteStatus{}
	parentRef := gatewayv1.ParentReference{Name: "gw"}

	changed := setOurRouteParentStatus(rs, parentRef, 3, acceptedRouteConditions())
	require.True(t, changed)
	require.Len(t, rs.Parents, 1)

	p := rs.Parents[0]
	assert.Equal(t, gatewayv1.GatewayController("example.com/gateway-controller"), p.ControllerName)
	assert.Equal(t, gatewayv1.ObjectName("gw"), p.ParentRef.Name)

	accepted := apiMetaCond(p.Conditions, string(gatewayv1.RouteConditionAccepted))
	require.NotNil(t, accepted)
	assert.Equal(t, metav1.ConditionTrue, accepted.Status)
	assert.Equal(t, int64(3), accepted.ObservedGeneration)
	assert.NotNil(t, apiMetaCond(p.Conditions, string(gatewayv1.RouteConditionResolvedRefs)))
}

func TestSetOurRouteParentStatus_PreservesOtherControllers(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	rs := &gatewayv1.RouteStatus{
		Parents: []gatewayv1.RouteParentStatus{{
			ControllerName: "other.com/controller",
			ParentRef:      gatewayv1.ParentReference{Name: "gw"},
			Conditions: []metav1.Condition{{
				Type:   string(gatewayv1.RouteConditionAccepted),
				Status: metav1.ConditionTrue,
				Reason: "Accepted",
			}},
		}},
	}
	parentRef := gatewayv1.ParentReference{Name: "gw"}

	changed := setOurRouteParentStatus(rs, parentRef, 1, acceptedRouteConditions())
	require.True(t, changed)
	require.Len(t, rs.Parents, 2, "other controller's entry must be preserved")

	// The foreign entry is untouched.
	assert.Equal(t, gatewayv1.GatewayController("other.com/controller"), rs.Parents[0].ControllerName)
}

func TestSetOurRouteParentStatus_NoChangeOnRepeat(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	rs := &gatewayv1.RouteStatus{}
	parentRef := gatewayv1.ParentReference{Name: "gw"}

	require.True(t, setOurRouteParentStatus(rs, parentRef, 1, acceptedRouteConditions()))
	// Re-applying identical conditions at the same generation must be a no-op.
	assert.False(t, setOurRouteParentStatus(rs, parentRef, 1, acceptedRouteConditions()))
}

// apiMetaCond returns a pointer to the named condition, or nil.
func apiMetaCond(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
}

// TestHTTPRouteReconcile_WritesAcceptedStatus verifies that a successfully
// attached HTTPRoute has its parent status populated with Accepted=True.
func TestHTTPRouteReconcile_WritesAcceptedStatus(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	s := newRouteTestScheme()

	gwClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "managed-class"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: "example.com/gateway-controller"},
	}
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "managed-class"},
	}

	sectionName := gatewayv1.SectionName("http-listener")
	port := gatewayv1.PortNumber(8080)
	httpRoute := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "test-gw",
					SectionName: &sectionName,
				}},
			},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "backend-svc",
							Port: &port,
						},
					},
				}},
			}},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gwClass, gw, httpRoute).
		WithStatusSubresource(&gatewayv1.HTTPRoute{}).
		Build()

	gwReconciler := newGatewayReconcilerForTest(true, map[string]*Listener{
		"http-listener": {Hostname: "example.com", Port: 80, Protocol: "HTTP"},
	})

	reconciler := &HTTPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-route", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated gatewayv1.HTTPRoute
	require.NoError(t, fakeClient.Get(context.Background(),
		types.NamespacedName{Name: "test-route", Namespace: "default"}, &updated))

	require.Len(t, updated.Status.Parents, 1, "route parent status should be written")
	p := updated.Status.Parents[0]
	assert.Equal(t, gatewayv1.GatewayController("example.com/gateway-controller"), p.ControllerName)

	accepted := apiMetaCond(p.Conditions, string(gatewayv1.RouteConditionAccepted))
	require.NotNil(t, accepted, "Accepted condition should be present")
	assert.Equal(t, metav1.ConditionTrue, accepted.Status)

	resolved := apiMetaCond(p.Conditions, string(gatewayv1.RouteConditionResolvedRefs))
	require.NotNil(t, resolved, "ResolvedRefs condition should be present")
	assert.Equal(t, metav1.ConditionTrue, resolved.Status)
}
