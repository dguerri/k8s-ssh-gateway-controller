package controllers

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	sshmgr "github.com/dguerri/k8s-ssh-gateway-controller/ssh"
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
	assignedAddrs        map[string][]string
	connected            bool
	startForwardingErr   error
	stopForwardingErr    error
	connectErr           error
	proxyProtocolVersion int
	stopForwardingCalls  []sshmgr.ForwardingConfig
	startForwardingCalls []sshmgr.ForwardingConfig
}

func (m *mockSSHTunnelManager) GetAssignedAddresses(hostname string, port int) []string {
	key := forwardingKey(hostname, port)
	return m.assignedAddrs[key]
}

func (m *mockSSHTunnelManager) StartForwarding(config sshmgr.ForwardingConfig) error {
	m.startForwardingCalls = append(m.startForwardingCalls, config)
	return m.startForwardingErr
}

func (m *mockSSHTunnelManager) StopForwarding(config *sshmgr.ForwardingConfig) error {
	m.stopForwardingCalls = append(m.stopForwardingCalls, *config)
	return m.stopForwardingErr
}

func (m *mockSSHTunnelManager) Stop() {}

func (m *mockSSHTunnelManager) Connect() error {
	if m.connectErr != nil {
		return m.connectErr
	}
	m.connected = true
	return nil
}

func (m *mockSSHTunnelManager) IsConnected() bool {
	return m.connected
}

func (m *mockSSHTunnelManager) SetProxyProtocol(version int) {
	m.proxyProtocolVersion = version
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

// --- Tests for handleForwardingError ---

func TestHandleForwardingError(t *testing.T) {
	t.Run("ErrSSHClientNotReady returns ErrGatewayNotReady", func(t *testing.T) {
		reconciler := &GatewayReconciler{
			gateways: map[string]*gateway{},
		}
		l := &Listener{Hostname: "example.com", Port: 80}
		route := &Route{Name: "test-route", Namespace: "test-ns", Host: "backend", Port: 8080}

		err := reconciler.handleForwardingError(
			&sshmgr.ErrSSHClientNotReady{},
			l, route, "test-ns/test-gw", "test-route", "test-ns", "http",
		)

		assert.Error(t, err)
		var notReadyErr *ErrGatewayNotReady
		assert.True(t, errors.As(err, &notReadyErr))
	})

	t.Run("ErrSSHForwardingExists returns nil and sets route", func(t *testing.T) {
		reconciler := &GatewayReconciler{
			gateways: map[string]*gateway{},
		}
		l := &Listener{Hostname: "example.com", Port: 80}
		route := &Route{Name: "test-route", Namespace: "test-ns", Host: "backend", Port: 8080}

		err := reconciler.handleForwardingError(
			&sshmgr.ErrSSHForwardingExists{Key: "example.com:80"},
			l, route, "test-ns/test-gw", "test-route", "test-ns", "http",
		)

		assert.NoError(t, err)
		assert.NotNil(t, l.route)
		assert.Equal(t, "test-route", l.route.Name)
		assert.Equal(t, "test-ns", l.route.Namespace)
	})

	t.Run("generic error returns wrapped error", func(t *testing.T) {
		reconciler := &GatewayReconciler{
			gateways: map[string]*gateway{},
		}
		l := &Listener{Hostname: "example.com", Port: 80}
		route := &Route{Name: "test-route", Namespace: "test-ns", Host: "backend", Port: 8080}

		genericErr := fmt.Errorf("some network error")
		err := reconciler.handleForwardingError(
			genericErr,
			l, route, "test-ns/test-gw", "test-route", "test-ns", "http",
		)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to start forwarding")
		assert.True(t, errors.Is(err, genericErr))
	})
}

// --- Tests for handleAddOrUpdateGateway ---

func TestHandleAddOrUpdateGateway(t *testing.T) {
	t.Run("new gateway creates gateway entry with correct listeners", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}
		reconciler := &GatewayReconciler{
			manager:  mockManager,
			gateways: map[string]*gateway{},
		}

		hostname := gatewayv1.Hostname("example.com")
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{
					{
						Name:     "http",
						Hostname: &hostname,
						Port:     80,
						Protocol: "HTTP",
					},
					{
						Name:     "tcp",
						Port:     3306,
						Protocol: "TCP",
					},
				},
			},
		}

		err := reconciler.handleAddOrUpdateGateway(context.TODO(), k8sGw)
		assert.NoError(t, err)

		gw, exists := reconciler.gateways["test-ns/test-gw"]
		assert.True(t, exists)
		assert.Len(t, gw.listeners, 2)
		assert.Equal(t, "example.com", gw.listeners["http"].Hostname)
		assert.Equal(t, 80, gw.listeners["http"].Port)
		assert.Equal(t, "localhost", gw.listeners["tcp"].Hostname)
		assert.Equal(t, 3306, gw.listeners["tcp"].Port)
	})

	t.Run("existing gateway unchanged listeners preserves existing routes", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}
		existingRoute := &Route{Name: "my-route", Namespace: "test-ns", Host: "backend", Port: 8080}
		reconciler := &GatewayReconciler{
			manager: mockManager,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname: "example.com",
							Port:     80,
							Protocol: "HTTP",
							route:    existingRoute,
						},
					},
				},
			},
		}

		hostname := gatewayv1.Hostname("example.com")
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{
					{
						Name:     "http",
						Hostname: &hostname,
						Port:     80,
						Protocol: "HTTP",
					},
				},
			},
		}

		err := reconciler.handleAddOrUpdateGateway(context.TODO(), k8sGw)
		assert.NoError(t, err)

		gw := reconciler.gateways["test-ns/test-gw"]
		assert.NotNil(t, gw.listeners["http"].route)
		assert.Equal(t, "my-route", gw.listeners["http"].route.Name)
	})

	t.Run("existing gateway changed listener port stops forwarding and creates new listener", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}
		existingRoute := &Route{Name: "my-route", Namespace: "test-ns", Host: "backend", Port: 8080}
		reconciler := &GatewayReconciler{
			manager: mockManager,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname: "example.com",
							Port:     80,
							Protocol: "HTTP",
							route:    existingRoute,
						},
					},
				},
			},
		}

		hostname := gatewayv1.Hostname("example.com")
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{
					{
						Name:     "http",
						Hostname: &hostname,
						Port:     8080, // Changed port
						Protocol: "HTTP",
					},
				},
			},
		}

		err := reconciler.handleAddOrUpdateGateway(context.TODO(), k8sGw)
		assert.NoError(t, err)

		// Should have called StopForwarding for the old config
		assert.Len(t, mockManager.stopForwardingCalls, 1)
		assert.Equal(t, "example.com", mockManager.stopForwardingCalls[0].RemoteHost)
		assert.Equal(t, 80, mockManager.stopForwardingCalls[0].RemotePort)

		// New listener should have updated port and no route
		gw := reconciler.gateways["test-ns/test-gw"]
		assert.Equal(t, 8080, gw.listeners["http"].Port)
		assert.Nil(t, gw.listeners["http"].route)
	})

	t.Run("existing gateway new listener added", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
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
						},
					},
				},
			},
		}

		hostname := gatewayv1.Hostname("example.com")
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{
					{
						Name:     "http",
						Hostname: &hostname,
						Port:     80,
						Protocol: "HTTP",
					},
					{
						Name:     "tcp",
						Port:     3306,
						Protocol: "TCP",
					},
				},
			},
		}

		err := reconciler.handleAddOrUpdateGateway(context.TODO(), k8sGw)
		assert.NoError(t, err)

		gw := reconciler.gateways["test-ns/test-gw"]
		assert.Len(t, gw.listeners, 2)
		assert.NotNil(t, gw.listeners["http"])
		assert.NotNil(t, gw.listeners["tcp"])
		assert.Equal(t, 3306, gw.listeners["tcp"].Port)
		assert.Equal(t, "localhost", gw.listeners["tcp"].Hostname)
	})

	t.Run("existing gateway listener removed only keeps listeners from spec", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
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
						},
						"tcp": {
							Hostname: "localhost",
							Port:     3306,
							Protocol: "TCP",
						},
					},
				},
			},
		}

		hostname := gatewayv1.Hostname("example.com")
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{
					{
						Name:     "http",
						Hostname: &hostname,
						Port:     80,
						Protocol: "HTTP",
					},
					// "tcp" listener removed from spec
				},
			},
		}

		err := reconciler.handleAddOrUpdateGateway(context.TODO(), k8sGw)
		assert.NoError(t, err)

		gw := reconciler.gateways["test-ns/test-gw"]
		assert.Len(t, gw.listeners, 1)
		assert.NotNil(t, gw.listeners["http"])
		_, hasTCP := gw.listeners["tcp"]
		assert.False(t, hasTCP)
	})
}

// --- Tests for handleDeleteGateway ---

func TestHandleDeleteGateway(t *testing.T) {
	t.Run("deletes gateway with active routes and calls StopForwarding", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
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
								Name:      "my-route",
								Namespace: "test-ns",
								Host:      "backend",
								Port:      8080,
							},
						},
					},
				},
			},
		}

		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
		}

		reconciler.handleDeleteGateway(context.TODO(), k8sGw)

		// Gateway should be removed
		_, exists := reconciler.gateways["test-ns/test-gw"]
		assert.False(t, exists)

		// StopForwarding should have been called
		assert.Len(t, mockManager.stopForwardingCalls, 1)
		assert.Equal(t, "example.com", mockManager.stopForwardingCalls[0].RemoteHost)
		assert.Equal(t, 80, mockManager.stopForwardingCalls[0].RemotePort)
		assert.Equal(t, "backend", mockManager.stopForwardingCalls[0].InternalHost)
		assert.Equal(t, 8080, mockManager.stopForwardingCalls[0].InternalPort)
	})

	t.Run("deletes gateway with no routes", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
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
							// no route
						},
					},
				},
			},
		}

		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
		}

		reconciler.handleDeleteGateway(context.TODO(), k8sGw)

		_, exists := reconciler.gateways["test-ns/test-gw"]
		assert.False(t, exists)

		// StopForwarding should NOT have been called (no route)
		assert.Len(t, mockManager.stopForwardingCalls, 0)
	})

	t.Run("deletes non-existent gateway without panic", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}
		reconciler := &GatewayReconciler{
			manager:  mockManager,
			gateways: map[string]*gateway{},
		}

		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "non-existent",
				Namespace: "test-ns",
			},
		}

		// Should not panic
		assert.NotPanics(t, func() {
			reconciler.handleDeleteGateway(context.TODO(), k8sGw)
		})
	})
}

// --- Tests for RemoveRoute ---

func TestRemoveRoute(t *testing.T) {
	t.Run("remove existing route successfully", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
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
								Host:      "backend",
								Port:      8080,
							},
						},
					},
				},
			},
		}

		err := reconciler.RemoveRoute(context.TODO(), "test-ns", "test-gw", "http", "test-route", "test-ns", "backend", 8080)
		assert.NoError(t, err)

		// Route should be nil now
		gw := reconciler.gateways["test-ns/test-gw"]
		assert.Nil(t, gw.listeners["http"].route)

		// StopForwarding should have been called
		assert.Len(t, mockManager.stopForwardingCalls, 1)
		assert.Equal(t, "example.com", mockManager.stopForwardingCalls[0].RemoteHost)
		assert.Equal(t, 80, mockManager.stopForwardingCalls[0].RemotePort)
	})

	t.Run("gateway not found returns ErrGatewayNotFound", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}
		reconciler := &GatewayReconciler{
			manager:  mockManager,
			gateways: map[string]*gateway{},
		}

		err := reconciler.RemoveRoute(context.TODO(), "test-ns", "test-gw", "http", "test-route", "test-ns", "backend", 8080)
		assert.Error(t, err)
		var notFoundErr *ErrGatewayNotFound
		assert.True(t, errors.As(err, &notFoundErr))
	})

	t.Run("route not found returns ErrRouteNotFound", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
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
							// no route attached
						},
					},
				},
			},
		}

		err := reconciler.RemoveRoute(context.TODO(), "test-ns", "test-gw", "http", "test-route", "test-ns", "backend", 8080)
		assert.Error(t, err)
		var notFoundErr *ErrRouteNotFound
		assert.True(t, errors.As(err, &notFoundErr))
	})

	t.Run("StopForwarding fails returns error", func(t *testing.T) {
		mockManager := &mockSSHTunnelManager{
			assignedAddrs:     map[string][]string{},
			connected:         true,
			stopForwardingErr: fmt.Errorf("SSH tunnel broken"),
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
								Host:      "backend",
								Port:      8080,
							},
						},
					},
				},
			},
		}

		err := reconciler.RemoveRoute(context.TODO(), "test-ns", "test-gw", "http", "test-route", "test-ns", "backend", 8080)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to stop forwarding")
	})
}

// --- Tests for GatewayClassReconciler.Reconcile ---

func newGatewayTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = gatewayv1.Install(s)
	return s
}

func TestGatewayClassReconciler_Reconcile(t *testing.T) {
	t.Run("matching controller name sets Accepted=True condition", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		gc := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-class",
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: "test-controller",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(gc).
			WithStatusSubresource(&gatewayv1.GatewayClass{}).
			Build()

		reconciler := &GatewayClassReconciler{
			Client: fakeClient,
			Scheme: s,
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "my-class"},
		})

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify status was updated
		var updated gatewayv1.GatewayClass
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "my-class"}, &updated)
		assert.NoError(t, err)
		assert.Len(t, updated.Status.Conditions, 1)
		assert.Equal(t, string(gatewayv1.GatewayClassConditionStatusAccepted), updated.Status.Conditions[0].Type)
		assert.Equal(t, metav1.ConditionTrue, updated.Status.Conditions[0].Status)
	})

	t.Run("non-matching controller name does not update status", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		gc := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "other-class",
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: "other-controller",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(gc).
			WithStatusSubresource(&gatewayv1.GatewayClass{}).
			Build()

		reconciler := &GatewayClassReconciler{
			Client: fakeClient,
			Scheme: s,
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "other-class"},
		})

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Status should not have been updated
		var updated gatewayv1.GatewayClass
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "other-class"}, &updated)
		assert.NoError(t, err)
		assert.Len(t, updated.Status.Conditions, 0)
	})

	t.Run("GatewayClass not found returns no error", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			Build()

		reconciler := &GatewayClassReconciler{
			Client: fakeClient,
			Scheme: s,
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "non-existent"},
		})

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
	})
}

// --- Tests for GatewayReconciler.Reconcile ---

func TestGatewayReconciler_Reconcile(t *testing.T) {
	t.Run("SSH disconnected reconnect succeeds proceeds normally", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		gc := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-class",
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: "test-controller",
			},
		}

		hostname := gatewayv1.Hostname("example.com")
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "my-class",
				Listeners: []gatewayv1.Listener{
					{
						Name:     "http",
						Hostname: &hostname,
						Port:     80,
						Protocol: "HTTP",
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(gc, k8sGw).
			WithStatusSubresource(&gatewayv1.Gateway{}).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     false, // SSH disconnected
		}

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			manager:  mockManager,
			gateways: map[string]*gateway{},
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
		})

		assert.NoError(t, err)
		assert.Equal(t, gatewayReconcilePeriod, result.RequeueAfter)
		// Connect should have been called and succeeded
		assert.True(t, mockManager.connected)
	})

	t.Run("SSH disconnected reconnect fails requeues after 10s", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     false,
			connectErr:    fmt.Errorf("connection refused"),
		}

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			manager:  mockManager,
			gateways: map[string]*gateway{},
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
		})

		assert.NoError(t, err)
		assert.Equal(t, 10*time.Second, result.RequeueAfter)
	})

	t.Run("Gateway not found returns no error", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			manager:  mockManager,
			gateways: map[string]*gateway{},
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "non-existent"},
		})

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
	})

	t.Run("Gateway being deleted with finalizer calls handleDeleteGateway and removes finalizer", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		now := metav1.Now()
		finalizer := getGatewayFinalizer()
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-gw",
				Namespace:         "test-ns",
				DeletionTimestamp: &now,
				Finalizers:        []string{finalizer},
			},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "my-class",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(k8sGw).
			WithStatusSubresource(&gatewayv1.Gateway{}).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}

		reconciler := &GatewayReconciler{
			Client:  fakeClient,
			Scheme:  s,
			manager: mockManager,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname: "example.com",
							Port:     80,
							route: &Route{
								Name:      "my-route",
								Namespace: "test-ns",
								Host:      "backend",
								Port:      8080,
							},
						},
					},
				},
			},
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
		})

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Gateway should be removed from internal state
		_, exists := reconciler.gateways["test-ns/test-gw"]
		assert.False(t, exists)

		// StopForwarding should have been called for the route
		assert.Len(t, mockManager.stopForwardingCalls, 1)

		// After removing the last finalizer from a deleted object, the fake client
		// garbage-collects it, so we verify via internal state instead.
	})

	t.Run("Gateway being deleted without our finalizer skips", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		now := metav1.Now()
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-gw",
				Namespace:         "test-ns",
				DeletionTimestamp: &now,
				Finalizers:        []string{"other-controller/finalizer"}, // Has a finalizer, but not ours
			},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "my-class",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(k8sGw).
			WithStatusSubresource(&gatewayv1.Gateway{}).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			manager:  mockManager,
			gateways: map[string]*gateway{},
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
		})

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
		// StopForwarding should NOT have been called (our finalizer was not present)
		assert.Len(t, mockManager.stopForwardingCalls, 0)
	})

	t.Run("non-matching controller name skips", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		gc := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "other-class",
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: "other-controller", // Not matching
			},
		}

		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "other-class",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(gc, k8sGw).
			WithStatusSubresource(&gatewayv1.Gateway{}).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			manager:  mockManager,
			gateways: map[string]*gateway{},
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
		})

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
		// Gateway should NOT be in internal state
		_, exists := reconciler.gateways["test-ns/test-gw"]
		assert.False(t, exists)
	})

	t.Run("normal add/update adds finalizer and processes gateway", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		gc := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-class",
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: "test-controller",
			},
		}

		hostname := gatewayv1.Hostname("example.com")
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "my-class",
				Listeners: []gatewayv1.Listener{
					{
						Name:     "http",
						Hostname: &hostname,
						Port:     80,
						Protocol: "HTTP",
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(gc, k8sGw).
			WithStatusSubresource(&gatewayv1.Gateway{}).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			manager:  mockManager,
			gateways: map[string]*gateway{},
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
		})

		assert.NoError(t, err)
		assert.Equal(t, gatewayReconcilePeriod, result.RequeueAfter)

		// Finalizer should have been added
		var updated gatewayv1.Gateway
		err = fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &updated)
		assert.NoError(t, err)
		assert.Contains(t, updated.Finalizers, getGatewayFinalizer())

		// Gateway should be in internal state
		gw, exists := reconciler.gateways["test-ns/test-gw"]
		assert.True(t, exists)
		assert.Len(t, gw.listeners, 1)
		assert.Equal(t, "example.com", gw.listeners["http"].Hostname)
	})
}

// --- Tests for updateGatewayStatus ---

func TestUpdateGatewayStatus(t *testing.T) {
	t.Run("successful update", func(t *testing.T) {
		s := newGatewayTestScheme()

		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "my-class",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(k8sGw).
			WithStatusSubresource(&gatewayv1.Gateway{}).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{
				"example.com:80": {"http://example.com"},
			},
			connected: true,
		}

		reconciler := &GatewayReconciler{
			Client:  fakeClient,
			Scheme:  s,
			manager: mockManager,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname: "example.com",
							Port:     80,
						},
					},
				},
			},
		}

		err := reconciler.updateGatewayStatus(context.Background(), "test-ns", "test-gw")
		assert.NoError(t, err)

		// Verify status was updated
		var updated gatewayv1.Gateway
		err = fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &updated)
		assert.NoError(t, err)
		assert.Len(t, updated.Status.Addresses, 1)
		assert.Equal(t, "example.com", updated.Status.Addresses[0].Value)
	})

	t.Run("gateway not found returns error", func(t *testing.T) {
		s := newGatewayTestScheme()

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			manager:  mockManager,
			gateways: map[string]*gateway{},
		}

		err := reconciler.updateGatewayStatus(context.Background(), "test-ns", "non-existent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch Gateway")
	})
}

// --- Tests for updateGatewayStatusIfChanged ---

func TestUpdateGatewayStatusIfChanged(t *testing.T) {
	t.Run("conditions changed updates status", func(t *testing.T) {
		s := newGatewayTestScheme()

		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "my-class",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(k8sGw).
			WithStatusSubresource(&gatewayv1.Gateway{}).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{},
			connected:     true,
		}

		reconciler := &GatewayReconciler{
			Client:  fakeClient,
			Scheme:  s,
			manager: mockManager,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{},
				},
			},
		}

		// Fetch the k8sGw fresh (so the resourceVersion is correct)
		var fresh gatewayv1.Gateway
		err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &fresh)
		assert.NoError(t, err)

		err = reconciler.updateGatewayStatusIfChanged(context.Background(), &fresh, "test-ns/test-gw")
		assert.NoError(t, err)

		// Verify conditions were set
		var updated gatewayv1.Gateway
		err = fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &updated)
		assert.NoError(t, err)
		assert.Len(t, updated.Status.Conditions, 2)
	})

	t.Run("addresses changed updates status", func(t *testing.T) {
		s := newGatewayTestScheme()

		// Pre-set Accepted+Programmed conditions so they don't change
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "my-class",
			},
			Status: gatewayv1.GatewayStatus{
				Conditions: []metav1.Condition{
					{
						Type:               string(gatewayv1.GatewayConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             "Accepted",
						Message:            "Gateway accepted by controller",
						LastTransitionTime: metav1.Now(),
					},
					{
						Type:               string(gatewayv1.GatewayConditionProgrammed),
						Status:             metav1.ConditionTrue,
						Reason:             "Programmed",
						Message:            "Gateway programmed successfully",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(k8sGw).
			WithStatusSubresource(&gatewayv1.Gateway{}).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{
				"example.com:80": {"http://example.com"},
			},
			connected: true,
		}

		reconciler := &GatewayReconciler{
			Client:  fakeClient,
			Scheme:  s,
			manager: mockManager,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname: "example.com",
							Port:     80,
						},
					},
				},
			},
		}

		// Fetch fresh so resourceVersion is correct
		var fresh gatewayv1.Gateway
		err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &fresh)
		assert.NoError(t, err)

		err = reconciler.updateGatewayStatusIfChanged(context.Background(), &fresh, "test-ns/test-gw")
		assert.NoError(t, err)

		// Verify addresses were updated
		var updated gatewayv1.Gateway
		err = fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &updated)
		assert.NoError(t, err)
		assert.Len(t, updated.Status.Addresses, 1)
		assert.Equal(t, "example.com", updated.Status.Addresses[0].Value)
	})

	t.Run("nothing changed skips update", func(t *testing.T) {
		s := newGatewayTestScheme()

		hostnameType := gatewayv1.HostnameAddressType
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "my-class",
			},
			Status: gatewayv1.GatewayStatus{
				Conditions: []metav1.Condition{
					{
						Type:               string(gatewayv1.GatewayConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             "Accepted",
						Message:            "Gateway accepted by controller",
						LastTransitionTime: metav1.Now(),
					},
					{
						Type:               string(gatewayv1.GatewayConditionProgrammed),
						Status:             metav1.ConditionTrue,
						Reason:             "Programmed",
						Message:            "Gateway programmed successfully",
						LastTransitionTime: metav1.Now(),
					},
				},
				Addresses: []gatewayv1.GatewayStatusAddress{
					{
						Type:  &hostnameType,
						Value: "example.com",
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(k8sGw).
			WithStatusSubresource(&gatewayv1.Gateway{}).
			Build()

		mockManager := &mockSSHTunnelManager{
			assignedAddrs: map[string][]string{
				"example.com:80": {"http://example.com"},
			},
			connected: true,
		}

		reconciler := &GatewayReconciler{
			Client:  fakeClient,
			Scheme:  s,
			manager: mockManager,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname: "example.com",
							Port:     80,
						},
					},
				},
			},
		}

		// Fetch fresh so resourceVersion is correct
		var fresh gatewayv1.Gateway
		err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &fresh)
		assert.NoError(t, err)

		// This should succeed without error; conditions and addresses unchanged
		err = reconciler.updateGatewayStatusIfChanged(context.Background(), &fresh, "test-ns/test-gw")
		assert.NoError(t, err)
	})
}
