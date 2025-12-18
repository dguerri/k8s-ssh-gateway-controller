package controllers

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
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
	_ = gatewayv1.AddToScheme(scheme)
	_ = gatewayv1alpha2.AddToScheme(scheme)

	// Set environment variable for controller name
	os.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")
	defer os.Unsetenv("GATEWAY_CONTROLLER_NAME")

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
}

// These methods are required to satisfy the interface expected by tests,
// even if they don't do anything in this mock.
func (m *MockGatewayReconciler) SetRoute(ctx context.Context, gwNamespace, gwName, listenerName, routeName, routeNamespace, backendHost string, backendPort int) error {
	return nil
}

func (m *MockGatewayReconciler) RemoveRoute(ctx context.Context, gwNamespace, gwName, listenerName, routeName, routeNamespace, backendHost string, backendPort int) error {
	return nil
}

func (m *MockGatewayReconciler) IsForwardingValid(l *Listener) bool {
	return true
}

func TestHTTPRouteReconcile_ManagedCheck(t *testing.T) {
	// Register schemes
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = gatewayv1.AddToScheme(scheme)

	// Set environment variable
	os.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/gateway-controller")
	defer os.Unsetenv("GATEWAY_CONTROLLER_NAME")

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
