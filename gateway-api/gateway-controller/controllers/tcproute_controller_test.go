package controllers

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func TestExtractTCPRouteDetails(t *testing.T) {
	namespace := "test-namespace"
	routeName := "test-route"
	gwName := "test-gateway"
	listenerName := "test-listener"
	backendHost := "test-service"
	backendPort := 8080

	tests := []struct {
		k8sRoute      *gatewayv1alpha2.TCPRoute
		expected      *routeDetails
		name          string
		expectedError string
		expectError   bool
	}{
		{
			name: "valid tcproute",
			k8sRoute: &gatewayv1alpha2.TCPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace,
				},
				Spec: gatewayv1alpha2.TCPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{
							{
								Name:        gatewayv1.ObjectName(gwName),
								SectionName: (*gatewayv1.SectionName)(&listenerName),
							},
						},
					},
					Rules: []gatewayv1alpha2.TCPRouteRule{
						{
							BackendRefs: []gatewayv1.BackendRef{
								{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Name: gatewayv1.ObjectName(backendHost),
										Port: (*gatewayv1.PortNumber)(ptr.To(int32(backendPort))),
									},
								},
							},
						},
					},
				},
			},
			expected: &routeDetails{
				routeName:      routeName,
				routeNamespace: namespace,
				gwName:         gwName,
				gwNamespace:    namespace,
				listenerName:   listenerName,
				backendHost:    getSvcHostname(backendHost, namespace),
				backendPort:    backendPort,
			},
			expectError: false,
		},
		{
			name: "no parent refs",
			k8sRoute: &gatewayv1alpha2.TCPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      routeName,
					Namespace: namespace,
				},
				Spec: gatewayv1alpha2.TCPRouteSpec{},
			},
			expectError:   true,
			expectedError: "TCPRoute test-namespace/test-route must have at least one ParentRef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details, err := extractTCPRouteDetails(tt.k8sRoute)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected an error but got none")
				}
				if err.Error() != tt.expectedError {
					t.Fatalf("expected error: %s, got: %s", tt.expectedError, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if details.routeName != tt.expected.routeName {
				t.Errorf("expected routeName %s, got %s", tt.expected.routeName, details.routeName)
			}

			if details.routeNamespace != tt.expected.routeNamespace {
				t.Errorf("expected routeNamespace %s, got %s", tt.expected.routeNamespace, details.routeNamespace)
			}

			if details.gwName != tt.expected.gwName {
				t.Errorf("expected gwName %s, got %s", tt.expected.gwName, details.gwName)
			}

			if details.gwNamespace != tt.expected.gwNamespace {
				t.Errorf("expected gwNamespace %s, got %s", tt.expected.gwNamespace, details.gwNamespace)
			}

			if details.listenerName != tt.expected.listenerName {
				t.Errorf("expected listenerName %s, got %s", tt.expected.listenerName, details.listenerName)
			}

			if details.backendHost != tt.expected.backendHost {
				t.Errorf("expected backendHost %s, got %s", tt.expected.backendHost, details.backendHost)
			}

			if details.backendPort != tt.expected.backendPort {
				t.Errorf("expected backendPort %d, got %d", tt.expected.backendPort, details.backendPort)
			}
		})
	}
}
