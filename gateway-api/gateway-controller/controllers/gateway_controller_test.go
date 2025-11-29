package controllers

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	sshmgr "github.com/dguerri/ssh-gateway-api-controller/ssh"
)

// TestUpdateGatewayAddresses_HTTPAddresses tests address extraction for HTTP/HTTPS URIs
func TestUpdateGatewayAddresses_HTTPAddresses(t *testing.T) {
	// Create a mock SSH manager that returns HTTP URIs
	mockManager := &mockSSHTunnelManager{
		assignedAddrs: map[string][]string{
			"example.com:80": {"http://example.com", "https://example.com"},
		},
	}

	reconciler := &GatewayReconciler{
		manager: mockManager,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http-listener": {
						Hostname: "example.com",
						Port:     80,
					},
				},
			},
		},
	}

	k8sGw := &gatewayv1.Gateway{}
	k8sGw.Name = "test-gw"
	k8sGw.Namespace = "test-ns"

	reconciler.updateGatewayAddresses(k8sGw)

	// Should have one address (hostname only, no port)
	assert.Len(t, k8sGw.Status.Addresses, 1)
	assert.Equal(t, gatewayv1.HostnameAddressType, *k8sGw.Status.Addresses[0].Type)
	assert.Equal(t, "example.com", k8sGw.Status.Addresses[0].Value)
}

// TestUpdateGatewayAddresses_TCPAddresses tests address extraction for TCP URIs
func TestUpdateGatewayAddresses_TCPAddresses(t *testing.T) {
	// Create a mock SSH manager that returns TCP URIs
	mockManager := &mockSSHTunnelManager{
		assignedAddrs: map[string][]string{
			"0.0.0.0:8080": {"tcp://nue.tuns.sh:34012"},
		},
	}

	reconciler := &GatewayReconciler{
		manager: mockManager,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"tcp-listener": {
						Hostname: "0.0.0.0",
						Port:     8080,
					},
				},
			},
		},
	}

	k8sGw := &gatewayv1.Gateway{}
	k8sGw.Name = "test-gw"
	k8sGw.Namespace = "test-ns"

	reconciler.updateGatewayAddresses(k8sGw)

	// Should have one address with hostname:port
	assert.Len(t, k8sGw.Status.Addresses, 1)
	assert.Equal(t, gatewayv1.NamedAddressType, *k8sGw.Status.Addresses[0].Type)
	assert.Equal(t, "nue.tuns.sh:34012", k8sGw.Status.Addresses[0].Value)
}

// TestUpdateGatewayAddresses_MixedAddresses tests multiple listeners with different address types
func TestUpdateGatewayAddresses_MixedAddresses(t *testing.T) {
	// Create a mock SSH manager that returns both HTTP and TCP URIs
	mockManager := &mockSSHTunnelManager{
		assignedAddrs: map[string][]string{
			"example.com:80":      {"http://example.com", "https://example.com"},
			"0.0.0.0:8080":        {"tcp://nue.tuns.sh:34012"},
			"api.example.com:443": {"https://api.example.com"},
		},
	}

	reconciler := &GatewayReconciler{
		manager: mockManager,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http-listener": {
						Hostname: "example.com",
						Port:     80,
					},
					"tcp-listener": {
						Hostname: "0.0.0.0",
						Port:     8080,
					},
					"https-listener": {
						Hostname: "api.example.com",
						Port:     443,
					},
				},
			},
		},
	}

	k8sGw := &gatewayv1.Gateway{}
	k8sGw.Name = "test-gw"
	k8sGw.Namespace = "test-ns"

	reconciler.updateGatewayAddresses(k8sGw)

	// Should have 3 addresses
	assert.Len(t, k8sGw.Status.Addresses, 3)

	// Build a map for easier assertions
	addrMap := make(map[string]gatewayv1.AddressType)
	for _, addr := range k8sGw.Status.Addresses {
		addrMap[addr.Value] = *addr.Type
	}

	// Verify each address has the correct type
	assert.Equal(t, gatewayv1.HostnameAddressType, addrMap["example.com"])
	assert.Equal(t, gatewayv1.NamedAddressType, addrMap["nue.tuns.sh:34012"])
	assert.Equal(t, gatewayv1.HostnameAddressType, addrMap["api.example.com"])
}

// TestUpdateGatewayAddresses_NoDuplicates tests that duplicate addresses are filtered
func TestUpdateGatewayAddresses_NoDuplicates(t *testing.T) {
	// Create a mock SSH manager that returns duplicate URIs
	mockManager := &mockSSHTunnelManager{
		assignedAddrs: map[string][]string{
			"example.com:80": {
				"http://example.com",
				"https://example.com",
			},
			"example.com:443": {
				"https://example.com", // Duplicate - should be filtered
			},
		},
	}

	reconciler := &GatewayReconciler{
		manager: mockManager,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http-listener": {
						Hostname: "example.com",
						Port:     80,
					},
					"https-listener": {
						Hostname: "example.com",
						Port:     443,
					},
				},
			},
		},
	}

	k8sGw := &gatewayv1.Gateway{}
	k8sGw.Name = "test-gw"
	k8sGw.Namespace = "test-ns"

	reconciler.updateGatewayAddresses(k8sGw)

	// Should have only one unique address
	assert.Len(t, k8sGw.Status.Addresses, 1)
	assert.Equal(t, "example.com", k8sGw.Status.Addresses[0].Value)
}

// TestUpdateGatewayAddresses_NoAddresses tests behavior when no addresses are assigned
func TestUpdateGatewayAddresses_NoAddresses(t *testing.T) {
	mockManager := &mockSSHTunnelManager{
		assignedAddrs: map[string][]string{},
	}

	reconciler := &GatewayReconciler{
		manager: mockManager,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http-listener": {
						Hostname: "example.com",
						Port:     80,
					},
				},
			},
		},
	}

	k8sGw := &gatewayv1.Gateway{}
	k8sGw.Name = "test-gw"
	k8sGw.Namespace = "test-ns"

	reconciler.updateGatewayAddresses(k8sGw)

	// Should have empty addresses
	assert.Len(t, k8sGw.Status.Addresses, 0)
}

// TestUpdateGatewayAddresses_GatewayNotFound tests behavior when gateway doesn't exist
func TestUpdateGatewayAddresses_GatewayNotFound(t *testing.T) {
	mockManager := &mockSSHTunnelManager{
		assignedAddrs: map[string][]string{},
	}

	reconciler := &GatewayReconciler{
		manager:  mockManager,
		gateways: map[string]*gateway{},
	}

	k8sGw := &gatewayv1.Gateway{}
	k8sGw.Name = "nonexistent"
	k8sGw.Namespace = "test-ns"

	reconciler.updateGatewayAddresses(k8sGw)

	// Should have empty addresses (no panic)
	assert.Len(t, k8sGw.Status.Addresses, 0)
}

// Mock SSH Tunnel Manager for testing
type mockSSHTunnelManager struct {
	assignedAddrs map[string][]string
	connected     bool
}

func (m *mockSSHTunnelManager) GetAssignedAddresses(hostname string, port int) []string {
	key := forwardingKey(hostname, port)
	return m.assignedAddrs[key]
}

func (m *mockSSHTunnelManager) StartForwarding(config sshmgr.ForwardingConfig) error {
	return nil
}

func (m *mockSSHTunnelManager) StopForwarding(config *sshmgr.ForwardingConfig) error {
	return nil
}

func (m *mockSSHTunnelManager) Stop() {}

func (m *mockSSHTunnelManager) Connect() error {
	m.connected = true
	return nil
}

func (m *mockSSHTunnelManager) IsConnected() bool {
	return true
}

func forwardingKey(hostname string, port int) string {
	return fmt.Sprintf("%s:%d", hostname, port)
}

// TestGatewayAddressesEqual tests the address comparison function
func TestGatewayAddressesEqual(t *testing.T) {
	hostnameType := gatewayv1.HostnameAddressType
	namedType := gatewayv1.NamedAddressType

	tests := []struct {
		name     string
		a        []gatewayv1.GatewayStatusAddress
		b        []gatewayv1.GatewayStatusAddress
		expected bool
	}{
		{
			name:     "both empty",
			a:        []gatewayv1.GatewayStatusAddress{},
			b:        []gatewayv1.GatewayStatusAddress{},
			expected: true,
		},
		{
			name: "same addresses same order",
			a: []gatewayv1.GatewayStatusAddress{
				{Type: &hostnameType, Value: "example.com"},
				{Type: &namedType, Value: "server:8080"},
			},
			b: []gatewayv1.GatewayStatusAddress{
				{Type: &hostnameType, Value: "example.com"},
				{Type: &namedType, Value: "server:8080"},
			},
			expected: true,
		},
		{
			name: "same addresses different order",
			a: []gatewayv1.GatewayStatusAddress{
				{Type: &hostnameType, Value: "example.com"},
				{Type: &namedType, Value: "server:8080"},
			},
			b: []gatewayv1.GatewayStatusAddress{
				{Type: &namedType, Value: "server:8080"},
				{Type: &hostnameType, Value: "example.com"},
			},
			expected: true,
		},
		{
			name: "different lengths",
			a: []gatewayv1.GatewayStatusAddress{
				{Type: &hostnameType, Value: "example.com"},
			},
			b: []gatewayv1.GatewayStatusAddress{
				{Type: &hostnameType, Value: "example.com"},
				{Type: &namedType, Value: "server:8080"},
			},
			expected: false,
		},
		{
			name: "different values",
			a: []gatewayv1.GatewayStatusAddress{
				{Type: &hostnameType, Value: "example.com"},
			},
			b: []gatewayv1.GatewayStatusAddress{
				{Type: &hostnameType, Value: "different.com"},
			},
			expected: false,
		},
		{
			name: "different types",
			a: []gatewayv1.GatewayStatusAddress{
				{Type: &hostnameType, Value: "example.com"},
			},
			b: []gatewayv1.GatewayStatusAddress{
				{Type: &namedType, Value: "example.com"},
			},
			expected: false,
		},
		{
			name: "nil type defaults to Hostname",
			a: []gatewayv1.GatewayStatusAddress{
				{Value: "example.com"},
			},
			b: []gatewayv1.GatewayStatusAddress{
				{Type: &hostnameType, Value: "example.com"},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gatewayAddressesEqual(tt.a, tt.b)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestIsForwardingValid tests the forwarding validation logic
func TestIsForwardingValid(t *testing.T) {
	tests := []struct {
		name          string
		listener      *Listener
		assignedAddrs map[string][]string
		expected      bool
	}{
		{
			name: "no addresses assigned",
			listener: &Listener{
				Hostname: "example.com",
				Port:     80,
			},
			assignedAddrs: map[string][]string{},
			expected:      false,
		},
		{
			name: "hostname matches in HTTP URI",
			listener: &Listener{
				Hostname: "example.com",
				Port:     80,
			},
			assignedAddrs: map[string][]string{
				"example.com:80": {"http://example.com", "https://example.com"},
			},
			expected: true,
		},
		{
			name: "hostname matches in HTTPS URI",
			listener: &Listener{
				Hostname: "api.example.com",
				Port:     443,
			},
			assignedAddrs: map[string][]string{
				"api.example.com:443": {"https://api.example.com"},
			},
			expected: true,
		},
		{
			name: "hostname mismatch - wrong subdomain assigned",
			listener: &Listener{
				Hostname: "requested.example.com",
				Port:     80,
			},
			assignedAddrs: map[string][]string{
				"requested.example.com:80": {"http://random-abc123.example.com", "https://random-abc123.example.com"},
			},
			expected: false,
		},
		{
			name: "wildcard accepts any address",
			listener: &Listener{
				Hostname: "0.0.0.0",
				Port:     8080,
			},
			assignedAddrs: map[string][]string{
				"0.0.0.0:8080": {"http://random-subdomain.example.com"},
			},
			expected: true,
		},
		{
			name: "wildcard with TCP URI",
			listener: &Listener{
				Hostname: "0.0.0.0",
				Port:     3306,
			},
			assignedAddrs: map[string][]string{
				"0.0.0.0:3306": {"tcp://nue.tuns.sh:34567"},
			},
			expected: true,
		},
		{
			name: "wildcard but no addresses",
			listener: &Listener{
				Hostname: "0.0.0.0",
				Port:     8080,
			},
			assignedAddrs: map[string][]string{},
			expected:      false,
		},
		{
			name: "partial hostname match",
			listener: &Listener{
				Hostname: "api.example.com",
				Port:     80,
			},
			assignedAddrs: map[string][]string{
				"api.example.com:80": {"http://my-api.example.com"},
			},
			expected: true, // Contains "api.example.com"
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockManager := &mockSSHTunnelManager{
				assignedAddrs: tt.assignedAddrs,
			}

			reconciler := &GatewayReconciler{
				manager: mockManager,
			}

			result := reconciler.isForwardingValid(tt.listener)
			assert.Equal(t, tt.expected, result, "isForwardingValid() mismatch")
		})
	}
}

// TestSetRoute_ValidatesForwarding tests that SetRoute validates forwarding exists
func TestSetRoute_ValidatesForwarding(t *testing.T) {
	t.Run("detects invalid forwarding and clears stale state", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{
				// No addresses for this listener
			},
			connected: true,
		}

		reconciler := &GatewayReconciler{
			manager: mockManager,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname: "example.com",
							Port:     80,
							Protocol: "HTTP",
							route: &Route{
								Name:      "test-route",
								Namespace: "test-ns",
								Host:      "backend.svc.cluster.local",
								Port:      8080,
							},
						},
					},
				},
			},
		}

		// Call SetRoute with same route - should detect forwarding is invalid
		err := reconciler.SetRoute(context.TODO(), "test-ns", "test-gw", "http", "test-route", "test-ns", "backend.svc.cluster.local", 8080)

		// Should return nil after clearing stale state and establishing forwarding
		assert.NoError(t, err)

		// Route should still be attached after successful forwarding
		gw := reconciler.gateways["test-ns/test-gw"]
		listener := gw.listeners["http"]
		assert.NotNil(t, listener.route)
		assert.Equal(t, "test-route", listener.route.Name)
	})

	t.Run("detects hostname mismatch and retries", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{
				"requested.example.com:80": {"http://random-abc123.example.com"}, // Wrong hostname!
			},
			connected: true,
		}

		reconciler := &GatewayReconciler{
			manager: mockManager,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname: "requested.example.com",
							Port:     80,
							Protocol: "HTTP",
							route: &Route{
								Name:      "test-route",
								Namespace: "test-ns",
								Host:      "backend.svc.cluster.local",
								Port:      8080,
							},
						},
					},
				},
			},
		}

		// Call SetRoute - should detect hostname mismatch and retry
		err := reconciler.SetRoute(context.TODO(), "test-ns", "test-gw", "http", "test-route", "test-ns", "backend.svc.cluster.local", 8080)

		// Should succeed after retry
		assert.NoError(t, err)
	})

	t.Run("accepts valid forwarding without retry", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{
				"example.com:80": {"http://example.com"}, // Correct hostname
			},
			connected: true,
		}

		reconciler := &GatewayReconciler{
			manager: mockManager,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname: "example.com",
							Port:     80,
							Protocol: "HTTP",
							route: &Route{
								Name:      "test-route",
								Namespace: "test-ns",
								Host:      "backend.svc.cluster.local",
								Port:      8080,
							},
						},
					},
				},
			},
		}

		// Call SetRoute - should validate forwarding is valid and return immediately
		err := reconciler.SetRoute(context.TODO(), "test-ns", "test-gw", "http", "test-route", "test-ns", "backend.svc.cluster.local", 8080)

		// Should succeed without calling StartForwarding
		assert.NoError(t, err)

		// Route should still be attached
		gw := reconciler.gateways["test-ns/test-gw"]
		listener := gw.listeners["http"]
		assert.NotNil(t, listener.route)
		assert.Equal(t, "test-route", listener.route.Name)
	})

	t.Run("wildcard accepts any hostname", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{
				"0.0.0.0:8080": {"http://random-subdomain.example.com"}, // Any hostname OK
			},
			connected: true,
		}

		reconciler := &GatewayReconciler{
			manager: mockManager,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname: "0.0.0.0",
							Port:     8080,
							Protocol: "HTTP",
							route: &Route{
								Name:      "test-route",
								Namespace: "test-ns",
								Host:      "backend.svc.cluster.local",
								Port:      8080,
							},
						},
					},
				},
			},
		}

		// Call SetRoute - wildcard should accept any hostname
		err := reconciler.SetRoute(context.TODO(), "test-ns", "test-gw", "http", "test-route", "test-ns", "backend.svc.cluster.local", 8080)

		// Should succeed
		assert.NoError(t, err)

		// Route should still be attached
		gw := reconciler.gateways["test-ns/test-gw"]
		listener := gw.listeners["http"]
		assert.NotNil(t, listener.route)
	})
}
