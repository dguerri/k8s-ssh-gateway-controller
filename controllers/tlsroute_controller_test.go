package controllers

import (
	"context"
	"fmt"
	"testing"

	sshmgr "github.com/dguerri/k8s-ssh-gateway-controller/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestExtractTLSRouteDetails(t *testing.T) {
	namespace := "test-namespace"
	routeName := "test-route"
	gwName := "test-gateway"
	listenerName := "tls-listener"
	backendHost := "test-service"
	backendPort := 8443

	valid := &gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: routeName, Namespace: namespace},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName(backendHost),
						Port: (*gatewayv1.PortNumber)(ptr.To(int32(backendPort))),
					},
				}},
			}},
		},
	}

	rd, err := extractTLSRouteDetails(valid)
	if err != nil {
		t.Fatal(err)
	}
	if rd.listenerName != listenerName || rd.backendPort != backendPort {
		t.Fatalf("unexpected details: %+v", rd)
	}
	if rd.routeName != routeName || rd.routeNamespace != namespace {
		t.Fatalf("unexpected route identity: %+v", rd)
	}
	if rd.gwName != gwName || rd.gwNamespace != namespace {
		t.Fatalf("unexpected gateway identity: %+v", rd)
	}
	if rd.backendHost != getSvcHostname(backendHost, namespace) {
		t.Fatalf("unexpected backend host: %s", rd.backendHost)
	}
}

func TestExtractTLSRouteDetails_NoParentRefs(t *testing.T) {
	rd, err := extractTLSRouteDetails(&gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec:       gatewayv1.TLSRouteSpec{},
	})
	if err == nil {
		t.Fatal("expected error for no parent refs")
	}
	if rd != nil {
		t.Fatal("expected nil details on error")
	}
	if err.Error() != "TLSRoute ns/r must have at least one ParentRef" {
		t.Fatalf("unexpected message: %s", err.Error())
	}
}

func TestExtractTLSRouteDetails_NoRules(t *testing.T) {
	gwName := "gw"
	listenerName := "tls"
	rd, err := extractTLSRouteDetails(&gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error and nil details for no rules")
	}
}

func TestExtractTLSRouteDetails_MultipleParentRefs(t *testing.T) {
	// Exercises the "more than one ParentRef" debug log path — should still succeed
	// using the first ref.
	gwName := "gw"
	listenerName := "tls"
	backendHost := "svc"
	backendPort := 443
	rd, err := extractTLSRouteDetails(&gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:        gatewayv1.ObjectName(gwName),
						SectionName: (*gatewayv1.SectionName)(&listenerName),
					},
					{
						Name:        gatewayv1.ObjectName("other-gw"),
						SectionName: (*gatewayv1.SectionName)(&listenerName),
					},
				},
			},
			Rules: []gatewayv1.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName(backendHost),
						Port: (*gatewayv1.PortNumber)(ptr.To(int32(backendPort))),
					},
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rd.gwName != gwName {
		t.Fatalf("expected gwName %s, got %s", gwName, rd.gwName)
	}
}

func TestExtractTLSRouteDetails_EmptyParentName(t *testing.T) {
	listenerName := "tls"
	rd, err := extractTLSRouteDetails(&gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "",
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error for empty parent name")
	}
}

func TestExtractTLSRouteDetails_ExplicitParentNamespace(t *testing.T) {
	// Exercises the parent.Namespace != nil branch.
	gwName := "gw"
	listenerName := "tls"
	gwNamespace := "gw-ns"
	backendHost := "svc"
	backendPort := 443
	gwNS := gatewayv1.Namespace(gwNamespace)
	rd, err := extractTLSRouteDetails(&gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					Namespace:   &gwNS,
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName(backendHost),
						Port: (*gatewayv1.PortNumber)(ptr.To(int32(backendPort))),
					},
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rd.gwNamespace != gwNamespace {
		t.Fatalf("expected gwNamespace %s, got %s", gwNamespace, rd.gwNamespace)
	}
}

func TestExtractTLSRouteDetails_NilSectionName(t *testing.T) {
	gwName := "gw"
	rd, err := extractTLSRouteDetails(&gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name: gatewayv1.ObjectName(gwName),
					// SectionName intentionally nil
				}},
			},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error for nil sectionName")
	}
}

func TestExtractTLSRouteDetails_NoBackendRefs(t *testing.T) {
	gwName := "gw"
	listenerName := "tls"
	rd, err := extractTLSRouteDetails(&gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1.TLSRouteRule{{}},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error for no backend refs")
	}
}

func TestExtractTLSRouteDetails_EmptyBackendName(t *testing.T) {
	gwName := "gw"
	listenerName := "tls"
	rd, err := extractTLSRouteDetails(&gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "",
					},
				}},
			}},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error for empty backend name")
	}
}

func TestExtractTLSRouteDetails_NilBackendPort(t *testing.T) {
	gwName := "gw"
	listenerName := "tls"
	rd, err := extractTLSRouteDetails(&gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "svc",
						// Port intentionally nil
					},
				}},
			}},
		},
	})
	if err == nil || rd != nil {
		t.Fatal("expected error for nil backend port")
	}
}

func TestExtractTLSRouteDetails_ExplicitBackendNamespace(t *testing.T) {
	// Exercises the k8Svc.Namespace != nil branch.
	gwName := "gw"
	listenerName := "tls"
	backendHost := "svc"
	backendPort := 443
	backendNS := gatewayv1.Namespace("backend-ns")
	rd, err := extractTLSRouteDetails(&gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: (*gatewayv1.SectionName)(&listenerName),
				}},
			},
			Rules: []gatewayv1.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name:      gatewayv1.ObjectName(backendHost),
						Namespace: &backendNS,
						Port:      (*gatewayv1.PortNumber)(ptr.To(int32(backendPort))),
					},
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedHost := getSvcHostname(backendHost, "backend-ns")
	if rd.backendHost != expectedHost {
		t.Fatalf("expected backendHost %s, got %s", expectedHost, rd.backendHost)
	}
}

func TestGetTLSRouteFinalizer(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "pico.sh/test-controller")
	got := getTLSRouteFinalizer()
	want := "tlsroute.gateway.networking.k8s.io/finalizer-pico.sh-test-controller"
	if got != want {
		t.Fatalf("unexpected finalizer: %s (want %s)", got, want)
	}
}

// --- TLSRoute Reconciler full flow tests ---

// newTLSRouteForTest returns a valid TLSRoute pointing at the named gateway/listener
// with a single backend service reference.
func newTLSRouteForTest(name, namespace, gwName, listenerName, backendName string, backendPort int32, finalizers []string) *gatewayv1.TLSRoute {
	sn := gatewayv1.SectionName(listenerName)
	p := gatewayv1.PortNumber(backendPort)
	return &gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Finalizers: finalizers,
		},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(gwName),
					SectionName: &sn,
				}},
			},
			Rules: []gatewayv1.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName(backendName),
						Port: &p,
					},
				}},
			}},
		},
	}
}

// newTLSGatewayReconcilerForTest builds a GatewayReconciler whose mock pool has
// the SNI passthrough session marked connected (or not, when connected=false).
// The single TLS listener is registered under default/test-gw.
func newTLSGatewayReconcilerForTest(connected bool, listeners map[string]*Listener) (*GatewayReconciler, *mockSSHSessionPool) {
	pool := newMockPool()
	if connected {
		pool.connectedKinds[sshmgr.SessionSNIProxy] = true
	} else {
		delete(pool.connectedKinds, sshmgr.SessionSNIProxy)
		pool.connectShouldFail = true
	}
	gw := &GatewayReconciler{
		pool: pool,
		gateways: map[string]*gateway{
			"default/test-gw": {
				listeners: listeners,
			},
		},
	}
	return gw, pool
}

func TestTLSRouteReconcile_RouteNotFound(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	s := newRouteTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	gwReconciler, _ := newTLSGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &TLSRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent-route", Namespace: "default"},
	})

	assert.NoError(t, err, "reconcile should return no error for not-found route")
	assert.Equal(t, ctrl.Result{}, result)
}

func TestTLSRouteReconcile_UnmanagedGateway(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	s := newRouteTestScheme()

	gwClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "other-class"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: "other.com/controller"},
	}
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "other-class"},
	}
	tlsRoute := newTLSRouteForTest("test-tls-route", "default", "test-gw", "tls-listener", "backend-svc", 443, nil)

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tlsRoute).Build()
	gwReconciler, _ := newTLSGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &TLSRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tls-route", Namespace: "default"},
	})

	assert.NoError(t, err, "unmanaged gateway should not produce an error")
	assert.Equal(t, ctrl.Result{}, result)

	// Verify no finalizer was added
	var updatedRoute gatewayv1.TLSRoute
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-tls-route", Namespace: "default"}, &updatedRoute)
	require.NoError(t, err)
	assert.Empty(t, updatedRoute.Finalizers, "no finalizer should be added for unmanaged gateway")
}

func TestTLSRouteReconcile_SuccessfulAdd(t *testing.T) {
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
	tlsRoute := newTLSRouteForTest("test-tls-route", "default", "test-gw", "tls-listener", "backend-svc", 443, nil)

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tlsRoute).Build()

	gwReconciler, pool := newTLSGatewayReconcilerForTest(true, map[string]*Listener{
		"tls-listener": {
			Hostname:    "example.com",
			Port:        443,
			Protocol:    "TLS",
			SessionKind: sshmgr.SessionSNIProxy,
		},
	})

	reconciler := &TLSRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tls-route", Namespace: "default"},
	})

	assert.NoError(t, err, "successful add should not return error")
	assert.Equal(t, routeReconcilePeriod, result.RequeueAfter, "should requeue after routeReconcilePeriod")

	// Verify finalizer was added
	var updatedRoute gatewayv1.TLSRoute
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-tls-route", Namespace: "default"}, &updatedRoute)
	require.NoError(t, err)
	assert.Contains(t, updatedRoute.Finalizers, getTLSRouteFinalizer(), "finalizer should be added")

	// Verify route was set on the listener
	gw2 := gwReconciler.gateways["default/test-gw"]
	listener := gw2.listeners["tls-listener"]
	require.NotNil(t, listener.route, "route should be attached to listener")
	assert.Equal(t, "test-tls-route", listener.route.Name)
	assert.Equal(t, "default", listener.route.Namespace)
	assert.Equal(t, 443, listener.route.Port)

	// Verify StartForwarding was called against the SNI session kind
	require.Len(t, pool.startForwardingCalls, 1, "StartForwarding should be invoked exactly once")
	assert.Equal(t, sshmgr.SessionSNIProxy, pool.startForwardingCalls[0].Kind, "StartForwarding should target SNI session")
}

func TestTLSRouteReconcile_DeletionWithFinalizer(t *testing.T) {
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
	tlsRoute := newTLSRouteForTest("test-tls-route", "default", "test-gw", "tls-listener", "backend-svc", 443, []string{getTLSRouteFinalizer()})

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tlsRoute).Build()

	// Issue a delete to set the DeletionTimestamp (the finalizer keeps the object alive)
	err := fakeClient.Delete(context.Background(), tlsRoute)
	require.NoError(t, err, "delete should succeed")

	backendHost := getSvcHostname("backend-svc", "default")
	gwReconciler, pool := newTLSGatewayReconcilerForTest(true, map[string]*Listener{
		"tls-listener": {
			Hostname:    "example.com",
			Port:        443,
			Protocol:    "TLS",
			SessionKind: sshmgr.SessionSNIProxy,
			route: &Route{
				Name:      "test-tls-route",
				Namespace: "default",
				Host:      backendHost,
				Port:      443,
			},
		},
	})

	reconciler := &TLSRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tls-route", Namespace: "default"},
	})

	assert.NoError(t, err, "deletion with finalizer should not return error")
	assert.Equal(t, routeReconcilePeriod, result.RequeueAfter, "should requeue after routeReconcilePeriod")

	// Verify route was removed from listener
	gw2 := gwReconciler.gateways["default/test-gw"]
	listener := gw2.listeners["tls-listener"]
	assert.Nil(t, listener.route, "route should be removed from listener")

	// Verify StopForwarding was called against the SNI session kind
	require.Len(t, pool.stopForwardingCalls, 1, "StopForwarding should be invoked exactly once")
	assert.Equal(t, sshmgr.SessionSNIProxy, pool.stopForwardingCalls[0].Kind, "StopForwarding should target SNI session")
}

func TestTLSRouteReconcile_GatewayNotReadyRetries(t *testing.T) {
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
	tlsRoute := newTLSRouteForTest("test-tls-route", "default", "test-gw", "tls-listener", "backend-svc", 443, nil)

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tlsRoute).Build()

	// SNI session not connected -> SetRoute -> setupRouteForwarding returns ErrGatewayNotReady
	gwReconciler, _ := newTLSGatewayReconcilerForTest(false, map[string]*Listener{
		"tls-listener": {
			Hostname:    "example.com",
			Port:        443,
			Protocol:    "TLS",
			SessionKind: sshmgr.SessionSNIProxy,
		},
	})

	reconciler := &TLSRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tls-route", Namespace: "default"},
	})

	assert.Error(t, err, "should return error when SNI session is not ready")
	assert.Equal(t, ctrl.Result{}, result, "result should be empty when error is returned for backoff")

	var notReadyErr *ErrGatewayNotReady
	assert.ErrorAs(t, err, &notReadyErr, "error should be ErrGatewayNotReady")
}

// --- TLSRoute Reconciler branch-coverage tests ---

// TestTLSRouteReconcile_NoParentRef verifies a TLSRoute with no ParentRefs is
// skipped without error rather than surfacing as a reconcile error.
func TestTLSRouteReconcile_NoParentRef(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	s := newRouteTestScheme()

	tlsRoute := &gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan-route", Namespace: "default"},
		Spec:       gatewayv1.TLSRouteSpec{},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(tlsRoute).Build()
	gwReconciler, _ := newTLSGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &TLSRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "orphan-route", Namespace: "default"},
	})

	assert.NoError(t, err, "route without a ParentRef must be skipped, not error")
	assert.Equal(t, ctrl.Result{}, result)

	var updatedRoute gatewayv1.TLSRoute
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Name: "orphan-route", Namespace: "default"}, &updatedRoute))
	assert.Empty(t, updatedRoute.Finalizers)
}

func TestTLSRouteReconcile_DeletionWithoutFinalizer(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	s := newRouteTestScheme()

	tlsRoute := newTLSRouteForTest("test-tls-route", "default", "test-gw", "tls-listener", "backend-svc", 443, []string{"some-other-finalizer"})

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(tlsRoute).Build()

	// Issue a delete to set the DeletionTimestamp (some-other-finalizer keeps the object alive)
	err := fakeClient.Delete(context.Background(), tlsRoute)
	require.NoError(t, err, "delete should succeed")

	gwReconciler, _ := newTLSGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &TLSRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tls-route", Namespace: "default"},
	})

	assert.NoError(t, err, "deletion without our finalizer should succeed without error")
	assert.Equal(t, ctrl.Result{}, result, "should return empty result when route is being deleted without our finalizer")
}

// TestTLSRouteReconcile_ExtractDetailsFails verifies that once a route is known
// to target a managed Gateway, invalid route details produce a reconcile error.
func TestTLSRouteReconcile_ExtractDetailsFails(t *testing.T) {
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

	// Route targets a managed Gateway but is missing Rules, so full extraction fails.
	sectionName := gatewayv1.SectionName("tls-listener")
	tlsRoute := &gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-route", Namespace: "default"},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "test-gw",
					SectionName: &sectionName,
				}},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tlsRoute).Build()

	gwReconciler, _ := newTLSGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &TLSRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "bad-route", Namespace: "default"},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must have at least one Rule")
	assert.Equal(t, ctrl.Result{}, result)
}

func TestTLSRouteReconcile_IsGatewayManagedError(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	s := newRouteTestScheme()

	tlsRoute := newTLSRouteForTest("test-tls-route", "default", "test-gw", "tls-listener", "backend-svc", 443, nil)

	baseFakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(tlsRoute).Build()

	// errorReturningClient is defined in route_controller_test.go and shared package-wide.
	errClient := &errorReturningClient{
		Client:       baseFakeClient,
		gatewayError: fmt.Errorf("simulated API error: permission denied"),
	}

	gwReconciler, _ := newTLSGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &TLSRouteReconciler{
		Client:            errClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tls-route", Namespace: "default"},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "simulated API error")
	assert.Equal(t, ctrl.Result{}, result)
}

func TestTLSRouteReconcile_HandleAddOrUpdate_FinalizerUpdateFails(t *testing.T) {
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
	tlsRoute := newTLSRouteForTest("test-tls-route", "default", "test-gw", "tls-listener", "backend-svc", 443, nil)

	baseFakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tlsRoute).Build()

	wrappedClient := interceptor.NewClient(baseFakeClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			return fmt.Errorf("simulated finalizer update failure")
		},
	})

	gwReconciler, _ := newTLSGatewayReconcilerForTest(true, map[string]*Listener{
		"tls-listener": {Hostname: "example.com", Port: 443, Protocol: "TLS", SessionKind: sshmgr.SessionSNIProxy},
	})

	reconciler := &TLSRouteReconciler{
		Client:            wrappedClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tls-route", Namespace: "default"},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "simulated finalizer update failure")
	assert.Equal(t, ctrl.Result{}, result)
}

func TestTLSRouteReconcile_HandleAddOrUpdate_SetRouteGenericError(t *testing.T) {
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
	tlsRoute := newTLSRouteForTest("test-tls-route", "default", "test-gw", "tls-listener", "backend-svc", 443, nil)

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tlsRoute).Build()

	mockPool := newMockPool()
	mockPool.connectedKinds[sshmgr.SessionSNIProxy] = true
	mockPool.startForwardingErr = fmt.Errorf("generic SSH error")
	gwReconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"default/test-gw": {
				listeners: map[string]*Listener{
					"tls-listener": {Hostname: "example.com", Port: 443, Protocol: "TLS", SessionKind: sshmgr.SessionSNIProxy},
				},
			},
		},
	}

	reconciler := &TLSRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tls-route", Namespace: "default"},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start forwarding")
	assert.Equal(t, ctrl.Result{}, result)
}

func TestTLSRouteReconcile_HandleDeletion_RemoveRouteGenericError(t *testing.T) {
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
	tlsRoute := newTLSRouteForTest("test-tls-route", "default", "test-gw", "tls-listener", "backend-svc", 443, []string{getTLSRouteFinalizer()})

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tlsRoute).Build()

	err := fakeClient.Delete(context.Background(), tlsRoute)
	require.NoError(t, err)

	backendHost := getSvcHostname("backend-svc", "default")
	mockPool := newMockPool()
	mockPool.connectedKinds[sshmgr.SessionSNIProxy] = true
	mockPool.stopForwardingErr = fmt.Errorf("SSH tunnel broken")
	gwReconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"default/test-gw": {
				listeners: map[string]*Listener{
					"tls-listener": {
						Hostname:    "example.com",
						Port:        443,
						Protocol:    "TLS",
						SessionKind: sshmgr.SessionSNIProxy,
						route: &Route{
							Name:      "test-tls-route",
							Namespace: "default",
							Host:      backendHost,
							Port:      443,
						},
					},
				},
			},
		},
	}

	reconciler := &TLSRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tls-route", Namespace: "default"},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to stop forwarding")
	assert.Equal(t, ctrl.Result{}, result)
}

func TestTLSRouteReconcile_HandleDeletion_UpdateFinalizerFails(t *testing.T) {
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
	tlsRoute := newTLSRouteForTest("test-tls-route", "default", "test-gw", "tls-listener", "backend-svc", 443, []string{getTLSRouteFinalizer()})

	baseFakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tlsRoute).Build()

	err := baseFakeClient.Delete(context.Background(), tlsRoute)
	require.NoError(t, err)

	wrappedClient := interceptor.NewClient(baseFakeClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			return fmt.Errorf("simulated update failure")
		},
	})

	// Empty gateways map -> RemoveRoute returns ErrGatewayNotFound (tolerated by handleDeletion)
	gwReconciler := &GatewayReconciler{
		pool:     newMockPool(),
		gateways: map[string]*gateway{},
	}

	reconciler := &TLSRouteReconciler{
		Client:            wrappedClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tls-route", Namespace: "default"},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "simulated update failure")
	assert.Equal(t, ctrl.Result{}, result)
}
