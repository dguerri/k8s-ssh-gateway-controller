package controllers

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// errorReturningClient is a mock client that can return specific errors for Get operations
type errorReturningClient struct {
	client.Client
	gatewayError      error
	gatewayClassError error
}

// Get wraps the underlying client's Get method and returns configured errors for specific resource types
func (e *errorReturningClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	switch obj.(type) {
	case *gatewayv1.Gateway:
		if e.gatewayError != nil {
			return e.gatewayError
		}
	case *gatewayv1.GatewayClass:
		if e.gatewayClassError != nil {
			return e.gatewayClassError
		}
	}
	return e.Client.Get(ctx, key, obj, opts...)
}

func TestIsGatewayManaged(t *testing.T) {
	// Register schemes
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = gatewayv1.Install(scheme)
	_ = gatewayv1alpha2.Install(scheme)

	// Set environment variable for controller name
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	gwClassManaged := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "managed-class",
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: "example.com/gateway-controller",
		},
	}

	gwClassUnmanaged := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "unmanaged-class",
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: "other.com/controller",
		},
	}

	gwManaged := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managed-gateway",
			Namespace: "default",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "managed-class",
		},
	}

	gwUnmanaged := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unmanaged-gateway",
			Namespace: "default",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "unmanaged-class",
		},
	}

	gwMissingClass := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "missing-class-gateway",
			Namespace: "default",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "non-existent-class",
		},
	}

	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gwClassManaged, gwClassUnmanaged, gwManaged, gwUnmanaged, gwMissingClass).
		Build()

	ctx := context.Background()

	tests := []struct {
		name           string
		gwName         string
		gwNamespace    string
		clientModifier func(client.Client) client.Client
		expected       bool
		expectedError  bool
	}{
		{
			name:           "managed gateway",
			gwName:         "managed-gateway",
			gwNamespace:    "default",
			clientModifier: nil,
			expected:       true,
			expectedError:  false,
		},
		{
			name:           "unmanaged gateway",
			gwName:         "unmanaged-gateway",
			gwNamespace:    "default",
			clientModifier: nil,
			expected:       false,
			expectedError:  false,
		},
		{
			name:           "gateway with missing class",
			gwName:         "missing-class-gateway",
			gwNamespace:    "default",
			clientModifier: nil,
			expected:       false,
			expectedError:  false, // No error - just not managed
		},
		{
			name:           "missing gateway",
			gwName:         "non-existent-gateway",
			gwNamespace:    "default",
			clientModifier: nil,
			expected:       false,
			expectedError:  false, // No error - just not managed
		},
		{
			name:        "API error when fetching gateway",
			gwName:      "managed-gateway",
			gwNamespace: "default",
			clientModifier: func(c client.Client) client.Client {
				return &errorReturningClient{
					Client:       c,
					gatewayError: fmt.Errorf("simulated API error: permission denied"),
				}
			},
			expected:      false,
			expectedError: true, // Should return error
		},
		{
			name:        "API error when fetching gateway class",
			gwName:      "managed-gateway",
			gwNamespace: "default",
			clientModifier: func(c client.Client) client.Client {
				return &errorReturningClient{
					Client:            c,
					gatewayClassError: fmt.Errorf("simulated API error: network timeout"),
				}
			},
			expected:      false,
			expectedError: true, // Should return error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var testClient client.Client = baseClient
			if tt.clientModifier != nil {
				testClient = tt.clientModifier(baseClient)
			}

			isManaged, err := IsGatewayManaged(ctx, testClient, tt.gwNamespace, tt.gwName)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, isManaged)
			}
		})
	}
}

// MockGatewayReconciler is a simple mock to capture calls
type MockGatewayReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	setRouteErr      error
	removeRouteErr   error
	setRouteCalls    int
	removeRouteCalls int
}

// These methods are required to satisfy the interface expected by tests,
// even if they don't do anything in this mock.
func (m *MockGatewayReconciler) SetRoute(ctx context.Context, gwNamespace, gwName, listenerName, routeName, routeNamespace, backendHost string, backendPort int) error {
	m.setRouteCalls++
	return m.setRouteErr
}

func (m *MockGatewayReconciler) RemoveRoute(ctx context.Context, gwNamespace, gwName, listenerName, routeName, routeNamespace, backendHost string, backendPort int) error {
	m.removeRouteCalls++
	return m.removeRouteErr
}

func (m *MockGatewayReconciler) IsForwardingValid(l *Listener) bool {
	return true
}

func TestHTTPRouteReconcile_ManagedCheck(t *testing.T) {
	// Register schemes
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = gatewayv1.Install(scheme)

	// Set environment variable
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	// Create managed resources
	gwClassManaged := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "managed-class"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: "example.com/gateway-controller"},
	}
	gwManaged := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "managed-gw", Namespace: "default"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "managed-class"},
	}

	// Create unmanaged resources
	gwClassUnmanaged := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "unmanaged-class"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: "other.com/controller"},
	}
	gwUnmanaged := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "unmanaged-gw", Namespace: "default"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "unmanaged-class"},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gwClassManaged, gwClassUnmanaged, gwManaged, gwUnmanaged).
		Build()

	ctx := context.Background()

	t.Run("IsGatewayManaged returns true for managed gateway", func(t *testing.T) {
		managed, err := IsGatewayManaged(ctx, client, "default", "managed-gw")
		assert.NoError(t, err)
		assert.True(t, managed)
	})

	t.Run("IsGatewayManaged returns false for unmanaged gateway", func(t *testing.T) {
		managed, err := IsGatewayManaged(ctx, client, "default", "unmanaged-gw")
		assert.NoError(t, err)
		assert.False(t, managed)
	})
}

// newRouteTestScheme creates a scheme with all required types registered for route tests,
// including gatewayv1alpha2 for TCPRoute support.
func newRouteTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = gatewayv1.Install(s)
	_ = gatewayv1alpha2.Install(s)
	return s
}

// newGatewayReconcilerForTest creates a real GatewayReconciler with mock SSH manager
// and pre-populated gateways map for use in route reconciler tests.
// The Client field is intentionally left nil so that SetRoute/RemoveRoute skip
// the updateGatewayStatus call. For tests that need a real client, set it after creation.
func newGatewayReconcilerForTest(connected bool, listeners map[string]*Listener) *GatewayReconciler {
	return &GatewayReconciler{
		manager: &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     connected,
		},
		gateways: map[string]*gateway{
			"default/test-gw": {
				listeners: listeners,
			},
		},
	}
}

// --- HTTPRoute Reconciler full flow tests ---

func TestHTTPRouteReconcile_RouteNotFound(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	s := newRouteTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	gwReconciler := newGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &HTTPRouteReconciler{
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

func TestHTTPRouteReconcile_UnmanagedGateway(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	s := newRouteTestScheme()

	// GatewayClass with a different controller name
	gwClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "other-class"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: "other.com/controller"},
	}
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "other-class"},
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

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, httpRoute).Build()
	gwReconciler := newGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &HTTPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-route", Namespace: "default"},
	})

	assert.NoError(t, err, "unmanaged gateway should not produce an error")
	assert.Equal(t, ctrl.Result{}, result, "should return empty result for unmanaged gateway")

	// Verify no finalizer was added
	var updatedRoute gatewayv1.HTTPRoute
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-route", Namespace: "default"}, &updatedRoute)
	require.NoError(t, err)
	assert.Empty(t, updatedRoute.Finalizers, "no finalizer should be added for unmanaged gateway")
}

func TestHTTPRouteReconcile_SuccessfulAdd(t *testing.T) {
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

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, httpRoute).Build()

	gwReconciler := newGatewayReconcilerForTest(true, map[string]*Listener{
		"http-listener": {
			Hostname: "example.com",
			Port:     80,
			Protocol: "HTTP",
		},
	})

	reconciler := &HTTPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-route", Namespace: "default"},
	})

	assert.NoError(t, err, "successful add should not return error")
	assert.Equal(t, routeReconcilePeriod, result.RequeueAfter, "should requeue after routeReconcilePeriod")

	// Verify finalizer was added
	var updatedRoute gatewayv1.HTTPRoute
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-route", Namespace: "default"}, &updatedRoute)
	require.NoError(t, err)
	assert.Contains(t, updatedRoute.Finalizers, getHTTPRouteFinalizer(), "finalizer should be added")

	// Verify route was set on the listener
	gw2 := gwReconciler.gateways["default/test-gw"]
	listener := gw2.listeners["http-listener"]
	require.NotNil(t, listener.route, "route should be attached to listener")
	assert.Equal(t, "test-route", listener.route.Name)
	assert.Equal(t, "default", listener.route.Namespace)
	assert.Equal(t, int(port), listener.route.Port)
}

func TestHTTPRouteReconcile_DeletionWithFinalizer(t *testing.T) {
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
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-route",
			Namespace:  "default",
			Finalizers: []string{getHTTPRouteFinalizer()},
		},
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

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, httpRoute).Build()

	// Issue a delete to set the DeletionTimestamp (the finalizer keeps the object alive)
	err := fakeClient.Delete(context.Background(), httpRoute)
	require.NoError(t, err, "delete should succeed")

	backendHost := getSvcHostname("backend-svc", "default")
	gwReconciler := newGatewayReconcilerForTest(true, map[string]*Listener{
		"http-listener": {
			Hostname: "example.com",
			Port:     80,
			Protocol: "HTTP",
			route: &Route{
				Name:      "test-route",
				Namespace: "default",
				Host:      backendHost,
				Port:      int(port),
			},
		},
	})

	reconciler := &HTTPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-route", Namespace: "default"},
	})

	assert.NoError(t, err, "deletion with finalizer should not return error")
	assert.Equal(t, routeReconcilePeriod, result.RequeueAfter, "should requeue after routeReconcilePeriod")

	// Verify route was removed from listener
	gw2 := gwReconciler.gateways["default/test-gw"]
	listener := gw2.listeners["http-listener"]
	assert.Nil(t, listener.route, "route should be removed from listener")
}

func TestHTTPRouteReconcile_DeletionWithoutFinalizer(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	s := newRouteTestScheme()

	sectionName := gatewayv1.SectionName("http-listener")
	port := gatewayv1.PortNumber(8080)
	httpRoute := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-route",
			Namespace:  "default",
			Finalizers: []string{"some-other-finalizer"}, // Not our finalizer, keeps object alive
		},
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

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(httpRoute).Build()

	// Issue a delete to set the DeletionTimestamp (some-other-finalizer keeps the object alive)
	err := fakeClient.Delete(context.Background(), httpRoute)
	require.NoError(t, err, "delete should succeed")

	gwReconciler := newGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &HTTPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-route", Namespace: "default"},
	})

	assert.NoError(t, err, "deletion without our finalizer should succeed without error")
	assert.Equal(t, ctrl.Result{}, result, "should return empty result when route is being deleted without our finalizer")
}

func TestHTTPRouteReconcile_SetRouteReturnsGatewayNotReady(t *testing.T) {
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

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, httpRoute).Build()

	// Use a GatewayReconciler whose SSH manager is not connected, which will
	// cause SetRoute -> setupRouteForwarding to return ErrGatewayNotReady
	gwReconciler := newGatewayReconcilerForTest(false, map[string]*Listener{
		"http-listener": {
			Hostname: "example.com",
			Port:     80,
			Protocol: "HTTP",
		},
	})

	reconciler := &HTTPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-route", Namespace: "default"},
	})

	assert.Error(t, err, "should return error when gateway is not ready")
	assert.Equal(t, ctrl.Result{}, result, "result should be empty when error is returned for backoff")

	// Verify the error is ErrGatewayNotReady
	var notReadyErr *ErrGatewayNotReady
	assert.ErrorAs(t, err, &notReadyErr, "error should be ErrGatewayNotReady")
}

func TestHTTPRouteReconcile_SetRouteReturnsGatewayNotFound(t *testing.T) {
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

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, httpRoute).Build()

	// Use a GatewayReconciler with empty gateways map so gateway is not found
	gwReconciler := &GatewayReconciler{
		manager: &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		},
		gateways: map[string]*gateway{},
	}

	reconciler := &HTTPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-route", Namespace: "default"},
	})

	assert.Error(t, err, "should return error when gateway is not found in internal registry")
	assert.Equal(t, ctrl.Result{}, result)

	// Verify the error is ErrGatewayNotFound
	var notFoundErr *ErrGatewayNotFound
	assert.ErrorAs(t, err, &notFoundErr, "error should be ErrGatewayNotFound")
}

// --- TCPRoute Reconciler full flow tests ---

func TestTCPRouteReconcile_RouteNotFound(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	s := newRouteTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	gwReconciler := newGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &TCPRouteReconciler{
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

func TestTCPRouteReconcile_UnmanagedGateway(t *testing.T) {
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

	sectionName := gatewayv1.SectionName("tcp-listener")
	port := gatewayv1.PortNumber(3306)
	tcpRoute := &gatewayv1alpha2.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-tcp-route", Namespace: "default"},
		Spec: gatewayv1alpha2.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "test-gw",
					SectionName: &sectionName,
				}},
			},
			Rules: []gatewayv1alpha2.TCPRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "backend-svc",
						Port: &port,
					},
				}},
			}},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tcpRoute).Build()
	gwReconciler := newGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &TCPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tcp-route", Namespace: "default"},
	})

	assert.NoError(t, err, "unmanaged gateway should not produce an error")
	assert.Equal(t, ctrl.Result{}, result)

	// Verify no finalizer was added
	var updatedRoute gatewayv1alpha2.TCPRoute
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-tcp-route", Namespace: "default"}, &updatedRoute)
	require.NoError(t, err)
	assert.Empty(t, updatedRoute.Finalizers, "no finalizer should be added for unmanaged gateway")
}

func TestTCPRouteReconcile_SuccessfulAdd(t *testing.T) {
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

	sectionName := gatewayv1.SectionName("tcp-listener")
	port := gatewayv1.PortNumber(3306)
	tcpRoute := &gatewayv1alpha2.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-tcp-route", Namespace: "default"},
		Spec: gatewayv1alpha2.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "test-gw",
					SectionName: &sectionName,
				}},
			},
			Rules: []gatewayv1alpha2.TCPRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "backend-svc",
						Port: &port,
					},
				}},
			}},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tcpRoute).Build()

	gwReconciler := newGatewayReconcilerForTest(true, map[string]*Listener{
		"tcp-listener": {
			Hostname: "0.0.0.0",
			Port:     3306,
			Protocol: "TCP",
		},
	})

	reconciler := &TCPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tcp-route", Namespace: "default"},
	})

	assert.NoError(t, err, "successful add should not return error")
	assert.Equal(t, routeReconcilePeriod, result.RequeueAfter, "should requeue after routeReconcilePeriod")

	// Verify finalizer was added
	var updatedRoute gatewayv1alpha2.TCPRoute
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-tcp-route", Namespace: "default"}, &updatedRoute)
	require.NoError(t, err)
	assert.Contains(t, updatedRoute.Finalizers, getTCPRouteFinalizer(), "finalizer should be added")

	// Verify route was set on the listener
	gw2 := gwReconciler.gateways["default/test-gw"]
	listener := gw2.listeners["tcp-listener"]
	require.NotNil(t, listener.route, "route should be attached to listener")
	assert.Equal(t, "test-tcp-route", listener.route.Name)
	assert.Equal(t, "default", listener.route.Namespace)
	assert.Equal(t, int(port), listener.route.Port)
}

func TestTCPRouteReconcile_DeletionWithFinalizer(t *testing.T) {
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

	sectionName := gatewayv1.SectionName("tcp-listener")
	port := gatewayv1.PortNumber(3306)
	tcpRoute := &gatewayv1alpha2.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-tcp-route",
			Namespace:  "default",
			Finalizers: []string{getTCPRouteFinalizer()},
		},
		Spec: gatewayv1alpha2.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "test-gw",
					SectionName: &sectionName,
				}},
			},
			Rules: []gatewayv1alpha2.TCPRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "backend-svc",
						Port: &port,
					},
				}},
			}},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tcpRoute).Build()

	// Issue a delete to set the DeletionTimestamp (the finalizer keeps the object alive)
	err := fakeClient.Delete(context.Background(), tcpRoute)
	require.NoError(t, err, "delete should succeed")

	backendHost := getSvcHostname("backend-svc", "default")
	gwReconciler := newGatewayReconcilerForTest(true, map[string]*Listener{
		"tcp-listener": {
			Hostname: "0.0.0.0",
			Port:     3306,
			Protocol: "TCP",
			route: &Route{
				Name:      "test-tcp-route",
				Namespace: "default",
				Host:      backendHost,
				Port:      int(port),
			},
		},
	})

	reconciler := &TCPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tcp-route", Namespace: "default"},
	})

	assert.NoError(t, err, "deletion with finalizer should not return error")
	assert.Equal(t, routeReconcilePeriod, result.RequeueAfter, "should requeue after routeReconcilePeriod")

	// Verify route was removed from listener
	gw2 := gwReconciler.gateways["default/test-gw"]
	listener := gw2.listeners["tcp-listener"]
	assert.Nil(t, listener.route, "route should be removed from listener")
}

func TestTCPRouteReconcile_DeletionWithoutFinalizer(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")

	s := newRouteTestScheme()

	sectionName := gatewayv1.SectionName("tcp-listener")
	port := gatewayv1.PortNumber(3306)
	tcpRoute := &gatewayv1alpha2.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-tcp-route",
			Namespace:  "default",
			Finalizers: []string{"some-other-finalizer"}, // Not our finalizer
		},
		Spec: gatewayv1alpha2.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "test-gw",
					SectionName: &sectionName,
				}},
			},
			Rules: []gatewayv1alpha2.TCPRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "backend-svc",
						Port: &port,
					},
				}},
			}},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(tcpRoute).Build()

	// Issue a delete to set the DeletionTimestamp (some-other-finalizer keeps the object alive)
	err := fakeClient.Delete(context.Background(), tcpRoute)
	require.NoError(t, err, "delete should succeed")

	gwReconciler := newGatewayReconcilerForTest(true, map[string]*Listener{})

	reconciler := &TCPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tcp-route", Namespace: "default"},
	})

	assert.NoError(t, err, "deletion without our finalizer should succeed without error")
	assert.Equal(t, ctrl.Result{}, result, "should return empty result when route is being deleted without our finalizer")
}

func TestTCPRouteReconcile_SetRouteReturnsGatewayNotReady(t *testing.T) {
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

	sectionName := gatewayv1.SectionName("tcp-listener")
	port := gatewayv1.PortNumber(3306)
	tcpRoute := &gatewayv1alpha2.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-tcp-route", Namespace: "default"},
		Spec: gatewayv1alpha2.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "test-gw",
					SectionName: &sectionName,
				}},
			},
			Rules: []gatewayv1alpha2.TCPRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "backend-svc",
						Port: &port,
					},
				}},
			}},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(gwClass, gw, tcpRoute).Build()

	// SSH manager is not connected -> SetRoute returns ErrGatewayNotReady
	gwReconciler := newGatewayReconcilerForTest(false, map[string]*Listener{
		"tcp-listener": {
			Hostname: "0.0.0.0",
			Port:     3306,
			Protocol: "TCP",
		},
	})

	reconciler := &TCPRouteReconciler{
		Client:            fakeClient,
		Scheme:            s,
		GatewayReconciler: gwReconciler,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-tcp-route", Namespace: "default"},
	})

	assert.Error(t, err, "should return error when gateway is not ready")
	assert.Equal(t, ctrl.Result{}, result)

	var notReadyErr *ErrGatewayNotReady
	assert.ErrorAs(t, err, &notReadyErr, "error should be ErrGatewayNotReady")
}

// --- MockGatewayReconciler validation tests ---

func TestMockGatewayReconciler_SetRouteTracksCalls(t *testing.T) {
	mock := &MockGatewayReconciler{}
	ctx := context.Background()

	err := mock.SetRoute(ctx, "ns", "gw", "listener", "route", "ns", "host", 8080)
	assert.NoError(t, err)
	assert.Equal(t, 1, mock.setRouteCalls)

	err = mock.SetRoute(ctx, "ns", "gw", "listener", "route2", "ns", "host", 9090)
	assert.NoError(t, err)
	assert.Equal(t, 2, mock.setRouteCalls)
}

func TestMockGatewayReconciler_SetRouteReturnsError(t *testing.T) {
	mock := &MockGatewayReconciler{
		setRouteErr: fmt.Errorf("set route failed"),
	}

	err := mock.SetRoute(context.Background(), "ns", "gw", "listener", "route", "ns", "host", 8080)
	assert.Error(t, err)
	assert.Equal(t, "set route failed", err.Error())
	assert.Equal(t, 1, mock.setRouteCalls)
}

func TestMockGatewayReconciler_RemoveRouteTracksCalls(t *testing.T) {
	mock := &MockGatewayReconciler{}
	ctx := context.Background()

	err := mock.RemoveRoute(ctx, "ns", "gw", "listener", "route", "ns", "host", 8080)
	assert.NoError(t, err)
	assert.Equal(t, 1, mock.removeRouteCalls)
}

func TestMockGatewayReconciler_RemoveRouteReturnsError(t *testing.T) {
	mock := &MockGatewayReconciler{
		removeRouteErr: fmt.Errorf("remove route failed"),
	}

	err := mock.RemoveRoute(context.Background(), "ns", "gw", "listener", "route", "ns", "host", 8080)
	assert.Error(t, err)
	assert.Equal(t, "remove route failed", err.Error())
	assert.Equal(t, 1, mock.removeRouteCalls)
}
