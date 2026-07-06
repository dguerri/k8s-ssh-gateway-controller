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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	sshmgr "github.com/dguerri/k8s-ssh-gateway-controller/ssh"
)

// TestUpdateGatewayAddresses_HTTPAddresses tests address extraction for HTTP/HTTPS URIs
func TestUpdateGatewayAddresses_HTTPAddresses(t *testing.T) {
	mockPool := newMockPool()
	mockPool.assignedAddrs[sshmgr.SessionPlain] = map[string][]string{
		"example.com:80": {"http://example.com", "https://example.com"},
	}

	reconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http-listener": {
						Hostname:    "example.com",
						Port:        80,
						SessionKind: sshmgr.SessionPlain,
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
	mockPool := newMockPool()
	mockPool.assignedAddrs[sshmgr.SessionPlain] = map[string][]string{
		"0.0.0.0:8080": {"tcp://nue.tuns.sh:34012"},
	}

	reconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"tcp-listener": {
						Hostname:    "0.0.0.0",
						Port:        8080,
						SessionKind: sshmgr.SessionPlain,
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
	mockPool := newMockPool()
	mockPool.assignedAddrs[sshmgr.SessionPlain] = map[string][]string{
		"example.com:80":      {"http://example.com", "https://example.com"},
		"0.0.0.0:8080":        {"tcp://nue.tuns.sh:34012"},
		"api.example.com:443": {"https://api.example.com"},
	}

	reconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http-listener": {
						Hostname:    "example.com",
						Port:        80,
						SessionKind: sshmgr.SessionPlain,
					},
					"tcp-listener": {
						Hostname:    "0.0.0.0",
						Port:        8080,
						SessionKind: sshmgr.SessionPlain,
					},
					"https-listener": {
						Hostname:    "api.example.com",
						Port:        443,
						SessionKind: sshmgr.SessionPlain,
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
	mockPool := newMockPool()
	mockPool.assignedAddrs[sshmgr.SessionPlain] = map[string][]string{
		"example.com:80": {
			"http://example.com",
			"https://example.com",
		},
		"example.com:443": {
			"https://example.com", // Duplicate - should be filtered
		},
	}

	reconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http-listener": {
						Hostname:    "example.com",
						Port:        80,
						SessionKind: sshmgr.SessionPlain,
					},
					"https-listener": {
						Hostname:    "example.com",
						Port:        443,
						SessionKind: sshmgr.SessionPlain,
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
	mockPool := newMockPool()

	reconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http-listener": {
						Hostname:    "example.com",
						Port:        80,
						SessionKind: sshmgr.SessionPlain,
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
	mockPool := newMockPool()

	reconciler := &GatewayReconciler{
		pool:     mockPool,
		gateways: map[string]*gateway{},
	}

	k8sGw := &gatewayv1.Gateway{}
	k8sGw.Name = "nonexistent"
	k8sGw.Namespace = "test-ns"

	reconciler.updateGatewayAddresses(k8sGw)

	// Should have empty addresses (no panic)
	assert.Len(t, k8sGw.Status.Addresses, 0)
}

// startCall and stopCall record per-kind forwarding operations.
type startCall struct {
	Kind sshmgr.SessionKind
	Cfg  sshmgr.ForwardingConfig
}
type stopCall struct {
	Kind sshmgr.SessionKind
	Cfg  sshmgr.ForwardingConfig
}

// mockSSHSessionPool is the test double for SSHSessionPoolInterface.
type mockSSHSessionPool struct {
	assignedAddrs   map[sshmgr.SessionKind]map[string][]string
	connectedKinds  map[sshmgr.SessionKind]bool
	configuredKinds map[sshmgr.SessionKind]bool
	configureCalls  []struct {
		PP  int
		SNI bool
	}
	startForwardingCalls []startCall
	stopForwardingCalls  []stopCall
	startForwardingErr   error
	stopForwardingErr    error
	configureErr         error
	connectShouldFail    bool // when true Connect() does not establish configured sessions
}

func newMockPool() *mockSSHSessionPool {
	return &mockSSHSessionPool{
		assignedAddrs:   map[sshmgr.SessionKind]map[string][]string{},
		connectedKinds:  map[sshmgr.SessionKind]bool{sshmgr.SessionPlain: true},
		configuredKinds: map[sshmgr.SessionKind]bool{sshmgr.SessionPlain: true},
	}
}

func (m *mockSSHSessionPool) ConfigureSessions(pp int, sni bool) error {
	m.configureCalls = append(m.configureCalls, struct {
		PP  int
		SNI bool
	}{pp, sni})
	if m.configureErr != nil {
		return m.configureErr
	}
	if pp > 0 {
		if !m.configuredKinds[sshmgr.SessionProxyProto] && !m.connectShouldFail {
			m.connectedKinds[sshmgr.SessionProxyProto] = true
		}
		m.configuredKinds[sshmgr.SessionProxyProto] = true
	} else {
		delete(m.connectedKinds, sshmgr.SessionProxyProto)
		delete(m.configuredKinds, sshmgr.SessionProxyProto)
	}
	if sni {
		if !m.configuredKinds[sshmgr.SessionSNIProxy] && !m.connectShouldFail {
			m.connectedKinds[sshmgr.SessionSNIProxy] = true
		}
		m.configuredKinds[sshmgr.SessionSNIProxy] = true
	} else {
		delete(m.connectedKinds, sshmgr.SessionSNIProxy)
		delete(m.configuredKinds, sshmgr.SessionSNIProxy)
	}
	return nil
}

func (m *mockSSHSessionPool) StartForwarding(kind sshmgr.SessionKind, cfg sshmgr.ForwardingConfig) error {
	m.startForwardingCalls = append(m.startForwardingCalls, startCall{kind, cfg})
	return m.startForwardingErr
}

func (m *mockSSHSessionPool) StopForwarding(kind sshmgr.SessionKind, cfg *sshmgr.ForwardingConfig) error {
	m.stopForwardingCalls = append(m.stopForwardingCalls, stopCall{kind, *cfg})
	return m.stopForwardingErr
}

func (m *mockSSHSessionPool) GetAssignedAddresses(kind sshmgr.SessionKind, hostname string, port int) []string {
	if byHost, ok := m.assignedAddrs[kind]; ok {
		return byHost[forwardingKey(hostname, port)]
	}
	return nil
}

func (m *mockSSHSessionPool) IsConnected(kind sshmgr.SessionKind) bool {
	return m.connectedKinds[kind]
}

func (m *mockSSHSessionPool) Connect() {
	if !m.connectShouldFail {
		for kind := range m.configuredKinds {
			m.connectedKinds[kind] = true
		}
	}
}
func (m *mockSSHSessionPool) Stop() {}

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
				Hostname:    "example.com",
				Port:        80,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{},
			expected:      false,
		},
		{
			name: "hostname matches in HTTP URI",
			listener: &Listener{
				Hostname:    "example.com",
				Port:        80,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{
				"example.com:80": {"http://example.com", "https://example.com"},
			},
			expected: true,
		},
		{
			name: "hostname matches in HTTPS URI",
			listener: &Listener{
				Hostname:    "api.example.com",
				Port:        443,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{
				"api.example.com:443": {"https://api.example.com"},
			},
			expected: true,
		},
		{
			name: "hostname mismatch - wrong subdomain assigned",
			listener: &Listener{
				Hostname:    "requested.example.com",
				Port:        80,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{
				"requested.example.com:80": {"http://random-abc123.example.com", "https://random-abc123.example.com"},
			},
			expected: false,
		},
		{
			name: "wildcard accepts any address",
			listener: &Listener{
				Hostname:    "0.0.0.0",
				Port:        8080,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{
				"0.0.0.0:8080": {"http://random-subdomain.example.com"},
			},
			expected: true,
		},
		{
			name: "wildcard with TCP URI",
			listener: &Listener{
				Hostname:    "0.0.0.0",
				Port:        3306,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{
				"0.0.0.0:3306": {"tcp://nue.tuns.sh:34567"},
			},
			expected: true,
		},
		{
			name: "wildcard but no addresses",
			listener: &Listener{
				Hostname:    "0.0.0.0",
				Port:        8080,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{},
			expected:      false,
		},
		{
			name: "partial hostname match",
			listener: &Listener{
				Hostname:    "api.example.com",
				Port:        80,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{
				"api.example.com:80": {"http://my-api.example.com"},
			},
			// "api.example.com" must NOT match the host "my-api.example.com":
			// the requested host has to be the full URI host, not a substring.
			expected: false,
		},
		{
			name: "exact hostname match without trailing slash",
			listener: &Listener{
				Hostname:    "api.example.com",
				Port:        80,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{
				"api.example.com:80": {"http://api.example.com"},
			},
			expected: true,
		},
		{
			name: "TCP listener with localhost and matching port",
			listener: &Listener{
				Hostname:    "localhost",
				Protocol:    "TCP",
				Port:        27101,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{
				"localhost:27101": {"tcp://nue.tuns.sh:27101"},
			},
			expected: true,
		},
		{
			name: "TCP listener with localhost and mismatched port",
			listener: &Listener{
				Hostname:    "localhost",
				Protocol:    "TCP",
				Port:        27101,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{
				"localhost:27101": {"tcp://nue.tuns.sh:31879"},
			},
			expected: false,
		},
		{
			name: "TCP listener with 0.0.0.0 and mismatched port",
			listener: &Listener{
				Hostname:    "0.0.0.0",
				Protocol:    "TCP",
				Port:        27101,
				SessionKind: sshmgr.SessionPlain,
			},
			assignedAddrs: map[string][]string{
				"0.0.0.0:27101": {"tcp://nue.tuns.sh:31879"},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockPool := newMockPool()
			if len(tt.assignedAddrs) > 0 {
				mockPool.assignedAddrs[tt.listener.SessionKind] = tt.assignedAddrs
			}

			reconciler := &GatewayReconciler{
				pool: mockPool,
			}

			result := reconciler.isForwardingValid(tt.listener)
			assert.Equal(t, tt.expected, result, "isForwardingValid() mismatch")
		})
	}
}

// TestSetRoute_ValidatesForwarding tests that SetRoute validates forwarding exists
func TestSetRoute_ValidatesForwarding(t *testing.T) {
	t.Run("detects invalid forwarding and clears stale state", func(t *testing.T) {
		mockPool := newMockPool()

		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
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
		mockPool := newMockPool()
		mockPool.assignedAddrs[sshmgr.SessionPlain] = map[string][]string{
			"requested.example.com:80": {"http://random-abc123.example.com"}, // Wrong hostname!
		}

		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "requested.example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
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
		mockPool := newMockPool()
		mockPool.assignedAddrs[sshmgr.SessionPlain] = map[string][]string{
			"example.com:80": {"http://example.com"}, // Correct hostname
		}

		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
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
		mockPool := newMockPool()
		mockPool.assignedAddrs[sshmgr.SessionPlain] = map[string][]string{
			"0.0.0.0:8080": {"http://random-subdomain.example.com"}, // Any hostname OK
		}

		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "0.0.0.0",
							Port:        8080,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
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
		mockPool := newMockPool()
		reconciler := &GatewayReconciler{
			pool:     mockPool,
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
		mockPool := newMockPool()
		existingRoute := &Route{Name: "my-route", Namespace: "test-ns", Host: "backend", Port: 8080}
		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
							route:       existingRoute,
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
		mockPool := newMockPool()
		existingRoute := &Route{Name: "my-route", Namespace: "test-ns", Host: "backend", Port: 8080}
		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
							route:       existingRoute,
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
		assert.Len(t, mockPool.stopForwardingCalls, 1)
		assert.Equal(t, "example.com", mockPool.stopForwardingCalls[0].Cfg.RemoteHost)
		assert.Equal(t, 80, mockPool.stopForwardingCalls[0].Cfg.RemotePort)

		// New listener should have updated port and no route
		gw := reconciler.gateways["test-ns/test-gw"]
		assert.Equal(t, 8080, gw.listeners["http"].Port)
		assert.Nil(t, gw.listeners["http"].route)
	})

	t.Run("existing gateway new listener added", func(t *testing.T) {
		mockPool := newMockPool()
		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
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
		mockPool := newMockPool()
		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
						},
						"tcp": {
							Hostname:    "localhost",
							Port:        3306,
							Protocol:    "TCP",
							SessionKind: sshmgr.SessionPlain,
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
		mockPool := newMockPool()
		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
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
		assert.Len(t, mockPool.stopForwardingCalls, 1)
		assert.Equal(t, "example.com", mockPool.stopForwardingCalls[0].Cfg.RemoteHost)
		assert.Equal(t, 80, mockPool.stopForwardingCalls[0].Cfg.RemotePort)
		assert.Equal(t, "backend", mockPool.stopForwardingCalls[0].Cfg.InternalHost)
		assert.Equal(t, 8080, mockPool.stopForwardingCalls[0].Cfg.InternalPort)
	})

	t.Run("deletes gateway with no routes", func(t *testing.T) {
		mockPool := newMockPool()
		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
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
		assert.Len(t, mockPool.stopForwardingCalls, 0)
	})

	t.Run("deletes non-existent gateway without panic", func(t *testing.T) {
		mockPool := newMockPool()
		reconciler := &GatewayReconciler{
			pool:     mockPool,
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
		mockPool := newMockPool()
		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
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
		assert.Len(t, mockPool.stopForwardingCalls, 1)
		assert.Equal(t, "example.com", mockPool.stopForwardingCalls[0].Cfg.RemoteHost)
		assert.Equal(t, 80, mockPool.stopForwardingCalls[0].Cfg.RemotePort)
	})

	t.Run("gateway not found returns ErrGatewayNotFound", func(t *testing.T) {
		mockPool := newMockPool()
		reconciler := &GatewayReconciler{
			pool:     mockPool,
			gateways: map[string]*gateway{},
		}

		err := reconciler.RemoveRoute(context.TODO(), "test-ns", "test-gw", "http", "test-route", "test-ns", "backend", 8080)
		assert.Error(t, err)
		var notFoundErr *ErrGatewayNotFound
		assert.True(t, errors.As(err, &notFoundErr))
	})

	t.Run("route not found returns ErrRouteNotFound", func(t *testing.T) {
		mockPool := newMockPool()
		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
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
		mockPool := newMockPool()
		mockPool.stopForwardingErr = fmt.Errorf("SSH tunnel broken")
		reconciler := &GatewayReconciler{
			pool: mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							Protocol:    "HTTP",
							SessionKind: sshmgr.SessionPlain,
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

		mockPool := newMockPool()
		delete(mockPool.connectedKinds, sshmgr.SessionPlain) // SSH disconnected
		// Connect() will succeed (connectShouldFail == false) and set plain session connected

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			pool:     mockPool,
			gateways: map[string]*gateway{},
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
		})

		assert.NoError(t, err)
		assert.Equal(t, gatewayReconcilePeriod, result.RequeueAfter)
		assert.True(t, mockPool.connectedKinds[sshmgr.SessionPlain], "plain session should be connected after reconnect")
	})

	t.Run("configured proxy-protocol session disconnected reconnects", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		gc := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-class",
				Annotations: map[string]string{
					annotationProxyProtocol: "2",
				},
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: "test-controller",
			},
		}
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
			WithObjects(gc, k8sGw).
			WithStatusSubresource(&gatewayv1.Gateway{}).
			Build()

		mockPool := newMockPool()
		mockPool.configuredKinds[sshmgr.SessionProxyProto] = true
		delete(mockPool.connectedKinds, sshmgr.SessionProxyProto)

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			pool:     mockPool,
			gateways: map[string]*gateway{},
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
		})

		assert.NoError(t, err)
		assert.Equal(t, gatewayReconcilePeriod, result.RequeueAfter)
		assert.True(t, mockPool.connectedKinds[sshmgr.SessionProxyProto], "proxy-protocol session should be connected after reconnect")
	})

	t.Run("configured sni session disconnected reconnect fails requeues after 10s", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		gc := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-class",
				Annotations: map[string]string{
					annotationSNIProxy: "true",
				},
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: "test-controller",
			},
		}
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
			WithObjects(gc, k8sGw).
			WithStatusSubresource(&gatewayv1.Gateway{}).
			Build()

		mockPool := newMockPool()
		mockPool.configuredKinds[sshmgr.SessionSNIProxy] = true
		delete(mockPool.connectedKinds, sshmgr.SessionSNIProxy)
		mockPool.connectShouldFail = true

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			pool:     mockPool,
			gateways: map[string]*gateway{},
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
		})

		assert.NoError(t, err)
		assert.Equal(t, 10*time.Second, result.RequeueAfter)
		assert.False(t, mockPool.connectedKinds[sshmgr.SessionSNIProxy], "sni session should remain disconnected when reconnect fails")
	})

	t.Run("SSH disconnected reconnect fails requeues after 10s", func(t *testing.T) {
		t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
		s := newGatewayTestScheme()

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			Build()

		mockPool := newMockPool()
		delete(mockPool.connectedKinds, sshmgr.SessionPlain) // SSH disconnected
		mockPool.connectShouldFail = true                    // Connect() will not establish connection

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			pool:     mockPool,
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

		mockPool := newMockPool()

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			pool:     mockPool,
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

		mockPool := newMockPool()

		reconciler := &GatewayReconciler{
			Client: fakeClient,
			Scheme: s,
			pool:   mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							SessionKind: sshmgr.SessionPlain,
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
		assert.Len(t, mockPool.stopForwardingCalls, 1)

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

		mockPool := newMockPool()

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			pool:     mockPool,
			gateways: map[string]*gateway{},
		}

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
		})

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
		// StopForwarding should NOT have been called (our finalizer was not present)
		assert.Len(t, mockPool.stopForwardingCalls, 0)
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

		mockPool := newMockPool()

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			pool:     mockPool,
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

		mockPool := newMockPool()

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			pool:     mockPool,
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

		mockPool := newMockPool()
		mockPool.assignedAddrs[sshmgr.SessionPlain] = map[string][]string{
			"example.com:80": {"http://example.com"},
		}

		reconciler := &GatewayReconciler{
			Client: fakeClient,
			Scheme: s,
			pool:   mockPool,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							SessionKind: sshmgr.SessionPlain,
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

		mockPool := newMockPool()

		reconciler := &GatewayReconciler{
			Client:   fakeClient,
			Scheme:   s,
			pool:     mockPool,
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

		mockPool := newMockPool()

		reconciler := &GatewayReconciler{
			Client: fakeClient,
			Scheme: s,
			pool:   mockPool,
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

		gc := &gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "my-class"}}
		err = reconciler.updateGatewayStatusIfChanged(context.Background(), &fresh, gc, "test-ns/test-gw")
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

		mockPool2 := newMockPool()
		mockPool2.assignedAddrs[sshmgr.SessionPlain] = map[string][]string{
			"example.com:80": {"http://example.com"},
		}

		reconciler2 := &GatewayReconciler{
			Client: fakeClient,
			Scheme: s,
			pool:   mockPool2,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							SessionKind: sshmgr.SessionPlain,
						},
					},
				},
			},
		}

		// Fetch fresh so resourceVersion is correct
		var fresh gatewayv1.Gateway
		err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &fresh)
		assert.NoError(t, err)

		gc2 := &gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "my-class"}}
		err = reconciler2.updateGatewayStatusIfChanged(context.Background(), &fresh, gc2, "test-ns/test-gw")
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

		mockPool3 := newMockPool()
		mockPool3.assignedAddrs[sshmgr.SessionPlain] = map[string][]string{
			"example.com:80": {"http://example.com"},
		}

		reconciler3 := &GatewayReconciler{
			Client: fakeClient,
			Scheme: s,
			pool:   mockPool3,
			gateways: map[string]*gateway{
				"test-ns/test-gw": {
					listeners: map[string]*Listener{
						"http": {
							Hostname:    "example.com",
							Port:        80,
							SessionKind: sshmgr.SessionPlain,
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
		gc3 := &gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "my-class"}}
		err = reconciler3.updateGatewayStatusIfChanged(context.Background(), &fresh, gc3, "test-ns/test-gw")
		assert.NoError(t, err)
	})
}

func TestGatewayReconciler_EnsureGatewayFinalizer(t *testing.T) {
	t.Run("already present does not update", func(t *testing.T) {
		s := newGatewayTestScheme()
		finalizer := getGatewayFinalizer()
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-gw",
				Namespace:  "test-ns",
				Finalizers: []string{finalizer},
			},
		}

		baseFakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(k8sGw).
			Build()
		wrappedClient := interceptor.NewClient(baseFakeClient, interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				return errors.New("unexpected update")
			},
		})

		reconciler := &GatewayReconciler{
			Client: wrappedClient,
			Scheme: s,
		}

		err := reconciler.ensureGatewayFinalizer(context.Background(), k8sGw, "test-ns/test-gw")

		assert.NoError(t, err)
		assert.Equal(t, []string{finalizer}, k8sGw.Finalizers)
	})

	t.Run("adds missing finalizer", func(t *testing.T) {
		s := newGatewayTestScheme()
		finalizer := getGatewayFinalizer()
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(k8sGw).
			Build()

		reconciler := &GatewayReconciler{
			Client: fakeClient,
			Scheme: s,
		}

		err := reconciler.ensureGatewayFinalizer(context.Background(), k8sGw, "test-ns/test-gw")

		assert.NoError(t, err)
		assert.Contains(t, k8sGw.Finalizers, finalizer)

		var updated gatewayv1.Gateway
		err = fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &updated)
		assert.NoError(t, err)
		assert.Contains(t, updated.Finalizers, finalizer)
	})

	t.Run("returns update error", func(t *testing.T) {
		s := newGatewayTestScheme()
		k8sGw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gw",
				Namespace: "test-ns",
			},
		}
		updateErr := errors.New("simulated update failure")

		baseFakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(k8sGw).
			Build()
		wrappedClient := interceptor.NewClient(baseFakeClient, interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				return updateErr
			},
		})

		reconciler := &GatewayReconciler{
			Client: wrappedClient,
			Scheme: s,
		}

		err := reconciler.ensureGatewayFinalizer(context.Background(), k8sGw, "test-ns/test-gw")

		assert.ErrorIs(t, err, updateErr)
		assert.Contains(t, k8sGw.Finalizers, getGatewayFinalizer())
	})
}

func TestGatewayReconciler_ReconcileGatewayDelete_UpdateFails(t *testing.T) {
	s := newGatewayTestScheme()
	finalizer := getGatewayFinalizer()
	k8sGw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-gw",
			Namespace:  "test-ns",
			Finalizers: []string{finalizer},
		},
	}
	updateErr := errors.New("simulated finalizer removal failure")

	baseFakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(k8sGw).
		Build()
	wrappedClient := interceptor.NewClient(baseFakeClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			return updateErr
		},
	})

	reconciler := &GatewayReconciler{
		Client: wrappedClient,
		Scheme: s,
		pool:   newMockPool(),
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{},
			},
		},
	}

	result, err := reconciler.reconcileGatewayDelete(
		context.Background(),
		k8sGw,
		types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
		"test-ns/test-gw",
		true,
	)

	assert.Equal(t, ctrl.Result{}, result)
	assert.ErrorIs(t, err, updateErr)
	assert.NotContains(t, k8sGw.Finalizers, finalizer)
}

// --- Tests for setupRouteForwarding edge cases ---

func TestSetupRouteForwarding(t *testing.T) {
	t.Run("existing route stops old forwarding and starts new", func(t *testing.T) {
		mockPool := newMockPool()

		listener := &Listener{
			Hostname:    "example.com",
			Port:        80,
			Protocol:    "HTTP",
			SessionKind: sshmgr.SessionPlain,
			route: &Route{
				Name:      "old-route",
				Namespace: "test-ns",
				Host:      "old-backend",
				Port:      8080,
			},
		}

		reconciler := &GatewayReconciler{
			pool:     mockPool,
			gateways: map[string]*gateway{},
		}

		err := reconciler.setupRouteForwarding(listener, "test-ns/test-gw", "new-route", "test-ns", "new-backend", 9090, "http")
		assert.NoError(t, err)

		// StopForwarding should have been called for the old route
		assert.Len(t, mockPool.stopForwardingCalls, 1)
		assert.Equal(t, "old-backend", mockPool.stopForwardingCalls[0].Cfg.InternalHost)
		assert.Equal(t, 8080, mockPool.stopForwardingCalls[0].Cfg.InternalPort)

		// StartForwarding should have been called for the new route
		assert.Len(t, mockPool.startForwardingCalls, 1)
		assert.Equal(t, "new-backend", mockPool.startForwardingCalls[0].Cfg.InternalHost)
		assert.Equal(t, 9090, mockPool.startForwardingCalls[0].Cfg.InternalPort)

		// Listener should reference the new route
		assert.Equal(t, "new-route", listener.route.Name)
		assert.Equal(t, "new-backend", listener.route.Host)
	})

	t.Run("existing route StopForwarding fails returns error", func(t *testing.T) {
		mockPool := newMockPool()
		mockPool.stopForwardingErr = fmt.Errorf("tunnel broken")

		listener := &Listener{
			Hostname:    "example.com",
			Port:        80,
			Protocol:    "HTTP",
			SessionKind: sshmgr.SessionPlain,
			route: &Route{
				Name:      "old-route",
				Namespace: "test-ns",
				Host:      "old-backend",
				Port:      8080,
			},
		}

		reconciler := &GatewayReconciler{
			pool:     mockPool,
			gateways: map[string]*gateway{},
		}

		err := reconciler.setupRouteForwarding(listener, "test-ns/test-gw", "new-route", "test-ns", "new-backend", 9090, "http")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to stop forwarding")

		// StartForwarding should NOT have been called
		assert.Len(t, mockPool.startForwardingCalls, 0)
	})

	t.Run("SSH not connected returns ErrGatewayNotReady", func(t *testing.T) {
		mockPool := newMockPool()
		delete(mockPool.connectedKinds, sshmgr.SessionPlain) // not connected
		mockPool.connectShouldFail = true

		listener := &Listener{
			Hostname:    "example.com",
			Port:        80,
			Protocol:    "HTTP",
			SessionKind: sshmgr.SessionPlain,
		}

		reconciler := &GatewayReconciler{
			pool:     mockPool,
			gateways: map[string]*gateway{},
		}

		err := reconciler.setupRouteForwarding(listener, "test-ns/test-gw", "test-route", "test-ns", "backend", 8080, "http")
		assert.Error(t, err)

		var notReadyErr *ErrGatewayNotReady
		assert.ErrorAs(t, err, &notReadyErr)
	})

	t.Run("StartForwarding generic error returns wrapped error", func(t *testing.T) {
		mockPool := newMockPool()
		mockPool.startForwardingErr = fmt.Errorf("network timeout")

		listener := &Listener{
			Hostname:    "example.com",
			Port:        80,
			Protocol:    "HTTP",
			SessionKind: sshmgr.SessionPlain,
		}

		reconciler := &GatewayReconciler{
			pool:     mockPool,
			gateways: map[string]*gateway{},
		}

		err := reconciler.setupRouteForwarding(listener, "test-ns/test-gw", "test-route", "test-ns", "backend", 8080, "http")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to start forwarding")
	})
}

// --- Tests for SetRoute listener not found ---

func TestSetRoute_ListenerNotFound(t *testing.T) {
	mockPool := newMockPool()

	reconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http": {
						Hostname:    "example.com",
						Port:        80,
						Protocol:    "HTTP",
						SessionKind: sshmgr.SessionPlain,
					},
				},
			},
		},
	}

	// Try to set route on a listener that doesn't exist
	err := reconciler.SetRoute(context.TODO(), "test-ns", "test-gw", "non-existent-listener", "test-route", "test-ns", "backend", 8080)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "listener not found: non-existent-listener")
}

// --- Tests for SetRoute rejected listener guard ---

func TestSetRoute_RejectedListener(t *testing.T) {
	mockPool := newMockPool()

	reconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"tls-terminate": {
						Hostname:    "example.com",
						Port:        443,
						Protocol:    "TLS",
						SessionKind: SessionPlain, // zero-value (Rejected listener)
						Rejected:    true,
						Reason:      ReasonUnsupportedTLSMode,
					},
				},
			},
		},
	}

	err := reconciler.SetRoute(context.TODO(), "test-ns", "test-gw", "tls-terminate", "test-route", "test-ns", "backend", 8443)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not programmed")
	assert.Contains(t, err.Error(), ReasonUnsupportedTLSMode)
}

// --- Tests for handleDeleteGateway with StopForwarding error ---

func TestHandleDeleteGateway_StopForwardingError(t *testing.T) {
	mockPool := newMockPool()
	mockPool.stopForwardingErr = fmt.Errorf("SSH tunnel broken")
	reconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http": {
						Hostname:    "example.com",
						Port:        80,
						Protocol:    "HTTP",
						SessionKind: sshmgr.SessionPlain,
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

	// Should not panic even when StopForwarding fails
	assert.NotPanics(t, func() {
		reconciler.handleDeleteGateway(context.TODO(), k8sGw)
	})

	// Gateway should still be removed from internal state despite the error
	_, exists := reconciler.gateways["test-ns/test-gw"]
	assert.False(t, exists, "gateway should be deleted despite StopForwarding error")

	// StopForwarding should have been called
	assert.Len(t, mockPool.stopForwardingCalls, 1)
}

// --- Tests for updateGatewayStatus Status().Update failure ---

func TestUpdateGatewayStatus_StatusUpdateFails(t *testing.T) {
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

	// Build a fake client WITHOUT WithStatusSubresource so status updates fail
	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(k8sGw).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				return fmt.Errorf("simulated status update failure")
			},
		}).
		Build()

	mockPool := newMockPool()
	mockPool.assignedAddrs[sshmgr.SessionPlain] = map[string][]string{
		"example.com:80": {"http://example.com"},
	}

	reconciler := &GatewayReconciler{
		Client: fakeClient,
		Scheme: s,
		pool:   mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http": {
						Hostname:    "example.com",
						Port:        80,
						SessionKind: sshmgr.SessionPlain,
					},
				},
			},
		},
	}

	err := reconciler.updateGatewayStatus(context.Background(), "test-ns", "test-gw")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update Gateway status")
}

// --- Tests for updateGatewayStatusIfChanged Status().Update failure ---

func TestUpdateGatewayStatusIfChanged_StatusUpdateFails(t *testing.T) {
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
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				return fmt.Errorf("simulated status update failure")
			},
		}).
		Build()

	mockPool := newMockPool()

	reconciler := &GatewayReconciler{
		Client: fakeClient,
		Scheme: s,
		pool:   mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{},
			},
		},
	}

	// Fetch the k8sGw fresh
	var fresh gatewayv1.Gateway
	err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &fresh)
	assert.NoError(t, err)

	// This should fail because conditions will change (first time setting Accepted/Programmed)
	gcFail := &gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "my-class"}}
	err = reconciler.updateGatewayStatusIfChanged(context.Background(), &fresh, gcFail, "test-ns/test-gw")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "simulated status update failure")
}

// --- Tests for GatewayClassReconciler.Reconcile Status().Update failure ---

func TestGatewayClassReconciler_Reconcile_StatusUpdateFails(t *testing.T) {
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
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				return fmt.Errorf("simulated status update failure")
			},
		}).
		Build()

	reconciler := &GatewayClassReconciler{
		Client: fakeClient,
		Scheme: s,
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-class"},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update GatewayClass status")
	assert.Equal(t, ctrl.Result{}, result)
}

// --- Tests for listenerProgrammedCondition (Task 7) ---

func TestListenerProgrammed_RejectsUnsupportedTLSMode(t *testing.T) {
	mockPool := newMockPool()
	reconciler := &GatewayReconciler{pool: mockPool}

	l := &Listener{
		Hostname:    "example.com",
		Port:        443,
		Protocol:    "TLS",
		SessionKind: SessionPlain,
		Rejected:    true,
		Reason:      ReasonUnsupportedTLSMode,
	}
	sessions := ClassSessionConfig{}

	cond := reconciler.listenerProgrammedCondition(l, sessions)

	assert.Equal(t, string(gatewayv1.ListenerConditionProgrammed), cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonUnsupportedTLSMode, cond.Reason)
	assert.Contains(t, cond.Message, "Listener rejected")
}

func TestListenerProgrammed_RejectsHTTPS(t *testing.T) {
	mockPool := newMockPool()
	reconciler := &GatewayReconciler{pool: mockPool}

	l := &Listener{
		Hostname:    "example.com",
		Port:        443,
		Protocol:    "HTTPS",
		SessionKind: SessionPlain,
		Rejected:    true,
		Reason:      ReasonUnsupportedListenerProtocol,
	}
	sessions := ClassSessionConfig{}

	cond := reconciler.listenerProgrammedCondition(l, sessions)

	assert.Equal(t, string(gatewayv1.ListenerConditionProgrammed), cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonUnsupportedListenerProtocol, cond.Reason)
}

func TestListenerProgrammed_SessionNotEnabledForPP(t *testing.T) {
	mockPool := newMockPool()
	reconciler := &GatewayReconciler{pool: mockPool}

	// TCP listener that requires PP but GatewayClass has ProxyProtocolVersion=0
	l := &Listener{
		Hostname:    "localhost",
		Port:        3306,
		Protocol:    "TCP",
		SessionKind: SessionProxyProto,
	}
	sessions := ClassSessionConfig{ProxyProtocolVersion: 0, SNIProxyEnabled: false}

	cond := reconciler.listenerProgrammedCondition(l, sessions)

	assert.Equal(t, string(gatewayv1.ListenerConditionProgrammed), cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonSessionNotEnabled, cond.Reason)
	assert.Contains(t, cond.Message, "proxy-protocol")
}

func TestListenerProgrammed_SessionNotEnabledForSNI(t *testing.T) {
	mockPool := newMockPool()
	reconciler := &GatewayReconciler{pool: mockPool}

	// TLS Passthrough listener requiring SNI but GatewayClass has SNIProxyEnabled=false
	l := &Listener{
		Hostname:    "example.com",
		Port:        443,
		Protocol:    "TLS",
		SessionKind: SessionSNIProxy,
	}
	sessions := ClassSessionConfig{ProxyProtocolVersion: 0, SNIProxyEnabled: false}

	cond := reconciler.listenerProgrammedCondition(l, sessions)

	assert.Equal(t, string(gatewayv1.ListenerConditionProgrammed), cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonSessionNotEnabled, cond.Reason)
	assert.Contains(t, cond.Message, "sni-proxy")
}

func TestListenerProgrammed_PendingWhenPoolNotConnected(t *testing.T) {
	mockPool := newMockPool()
	// Disconnect the plain session
	delete(mockPool.connectedKinds, sshmgr.SessionPlain)
	reconciler := &GatewayReconciler{pool: mockPool}

	l := &Listener{
		Hostname:    "example.com",
		Port:        80,
		Protocol:    "HTTP",
		SessionKind: SessionPlain,
	}
	sessions := ClassSessionConfig{}

	cond := reconciler.listenerProgrammedCondition(l, sessions)

	assert.Equal(t, string(gatewayv1.ListenerConditionProgrammed), cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, string(gatewayv1.ListenerReasonPending), cond.Reason)
}

func TestListenerProgrammed_HappyPath(t *testing.T) {
	mockPool := newMockPool()
	reconciler := &GatewayReconciler{pool: mockPool}

	l := &Listener{
		Hostname:    "example.com",
		Port:        80,
		Protocol:    "HTTP",
		SessionKind: SessionPlain,
	}
	sessions := ClassSessionConfig{}

	cond := reconciler.listenerProgrammedCondition(l, sessions)

	assert.Equal(t, string(gatewayv1.ListenerConditionProgrammed), cond.Type)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, string(gatewayv1.ListenerReasonProgrammed), cond.Reason)
}

// --- Tests for Gateway-level Programmed aggregation (Task 7) ---

// TestSupportedKindsFor verifies supportedKindsFor helper covers all branches.
func TestSupportedKindsFor(t *testing.T) {
	tests := []struct {
		protocol string
		wantKind string
		wantNil  bool
	}{
		{"HTTP", "HTTPRoute", false},
		{"TCP", "TCPRoute", false},
		{"TLS", "TLSRoute", false},
		{"HTTPS", "", true}, // unknown → nil
		{"UDP", "", true},   // unknown → nil
	}
	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
			l := &Listener{Protocol: tt.protocol}
			got := supportedKindsFor(l)
			if tt.wantNil {
				assert.Nil(t, got)
			} else {
				assert.Len(t, got, 1)
				assert.Equal(t, gatewayv1.Kind(tt.wantKind), got[0].Kind)
			}
		})
	}
}

// TestAttachedRouteCount verifies the per-listener attached-route count.
func TestAttachedRouteCount(t *testing.T) {
	assert.Equal(t, int32(0), attachedRouteCount(&Listener{}))
	assert.Equal(t, int32(1), attachedRouteCount(&Listener{route: &Route{Name: "r", Namespace: "ns"}}))
}

// TestPopulateListenerStatuses_AttachedRoutes verifies AttachedRoutes is
// reported per listener based on whether a route is attached.
func TestPopulateListenerStatuses_AttachedRoutes(t *testing.T) {
	mockPool := newMockPool() // plain connected by default
	reconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"attached": {Hostname: "example.com", Port: 80, Protocol: "HTTP", SessionKind: sshmgr.SessionPlain, route: &Route{Name: "r", Namespace: "test-ns"}},
					"empty":    {Hostname: "other.com", Port: 80, Protocol: "HTTP", SessionKind: sshmgr.SessionPlain},
				},
			},
		},
	}

	k8sGw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "test-ns"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "my-class",
			Listeners: []gatewayv1.Listener{
				{Name: "attached", Port: 80, Protocol: "HTTP"},
				{Name: "empty", Port: 80, Protocol: "HTTP"},
			},
		},
	}
	gc := &gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "my-class"}}

	reconciler.populateListenerStatuses(k8sGw, gc)

	counts := map[gatewayv1.SectionName]int32{}
	for _, ls := range k8sGw.Status.Listeners {
		counts[ls.Name] = ls.AttachedRoutes
	}
	assert.Equal(t, int32(1), counts["attached"])
	assert.Equal(t, int32(0), counts["empty"])
}

// TestPopulateListenerStatuses_GatewayNotFound verifies the early-return path when
// the gateway key is absent from r.gateways.
func TestPopulateListenerStatuses_GatewayNotFound(t *testing.T) {
	mockPool := newMockPool()
	reconciler := &GatewayReconciler{
		pool:     mockPool,
		gateways: map[string]*gateway{}, // empty — key won't be found
	}

	k8sGw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "missing-gw", Namespace: "test-ns"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "my-class"},
	}
	gc := &gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "my-class"}}

	allProgrammed := reconciler.populateListenerStatuses(k8sGw, gc)
	// Early return → allProgrammed=true, no listener statuses written
	assert.True(t, allProgrammed)
	assert.Nil(t, k8sGw.Status.Listeners)
}

func TestGatewayProgrammed_AllListenersProgrammed(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
	s := newGatewayTestScheme()

	gc := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "my-class"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: "test-controller"},
	}
	hostname := gatewayv1.Hostname("example.com")
	k8sGw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "test-ns"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "my-class",
			Listeners: []gatewayv1.Listener{
				{Name: "http", Hostname: &hostname, Port: 80, Protocol: "HTTP"},
				{Name: "tcp", Port: 3306, Protocol: "TCP"},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, k8sGw).
		WithStatusSubresource(&gatewayv1.Gateway{}).
		Build()

	mockPool := newMockPool() // plain connected by default

	reconciler := &GatewayReconciler{
		Client: fakeClient,
		Scheme: s,
		pool:   mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http": {Hostname: "example.com", Port: 80, Protocol: "HTTP", SessionKind: sshmgr.SessionPlain},
					"tcp":  {Hostname: "localhost", Port: 3306, Protocol: "TCP", SessionKind: sshmgr.SessionPlain},
				},
			},
		},
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
	})
	assert.NoError(t, err)
	assert.Equal(t, gatewayReconcilePeriod, result.RequeueAfter)

	var updated gatewayv1.Gateway
	err = fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &updated)
	assert.NoError(t, err)

	// Gateway-level Programmed should be True
	var progCond *metav1.Condition
	for i := range updated.Status.Conditions {
		if updated.Status.Conditions[i].Type == string(gatewayv1.GatewayConditionProgrammed) {
			progCond = &updated.Status.Conditions[i]
			break
		}
	}
	assert.NotNil(t, progCond, "Programmed condition should be present")
	assert.Equal(t, metav1.ConditionTrue, progCond.Status)
	assert.Equal(t, "Programmed", progCond.Reason)

	// Both listener conditions should be True
	assert.Len(t, updated.Status.Listeners, 2)
	for _, ls := range updated.Status.Listeners {
		assert.Len(t, ls.Conditions, 1)
		assert.Equal(t, metav1.ConditionTrue, ls.Conditions[0].Status)
	}
}

func TestGatewayProgrammed_OneListenerNotProgrammed(t *testing.T) {
	t.Setenv("GATEWAY_CONTROLLER_NAME", "test-controller")
	s := newGatewayTestScheme()

	// GatewayClass without sni-proxy annotation, so SNI listener will get SessionNotEnabled
	gc := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "my-class"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: "test-controller"},
	}
	tlsMode := gatewayv1.TLSModePassthrough
	k8sGw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "test-ns"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "my-class",
			Listeners: []gatewayv1.Listener{
				{Name: "http", Port: 80, Protocol: "HTTP"},
				{
					Name:     "tls",
					Port:     443,
					Protocol: "TLS",
					TLS:      &gatewayv1.ListenerTLSConfig{Mode: &tlsMode},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, k8sGw).
		WithStatusSubresource(&gatewayv1.Gateway{}).
		Build()

	mockPool := newMockPool() // plain connected; SNI not in connectedKinds

	reconciler := &GatewayReconciler{
		Client: fakeClient,
		Scheme: s,
		pool:   mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": {
				listeners: map[string]*Listener{
					"http": {Hostname: "localhost", Port: 80, Protocol: "HTTP", SessionKind: sshmgr.SessionPlain},
					// TLS Passthrough listener requires SNI session
					"tls": {Hostname: "localhost", Port: 443, Protocol: "TLS", SessionKind: sshmgr.SessionSNIProxy},
				},
			},
		},
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-gw"},
	})
	assert.NoError(t, err)
	assert.Equal(t, gatewayReconcilePeriod, result.RequeueAfter)

	var updated gatewayv1.Gateway
	err = fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "test-ns", Name: "test-gw"}, &updated)
	assert.NoError(t, err)

	// Gateway-level Programmed should be False with reason ListenersNotProgrammed
	var progCond *metav1.Condition
	for i := range updated.Status.Conditions {
		if updated.Status.Conditions[i].Type == string(gatewayv1.GatewayConditionProgrammed) {
			progCond = &updated.Status.Conditions[i]
			break
		}
	}
	assert.NotNil(t, progCond, "Programmed condition should be present")
	assert.Equal(t, metav1.ConditionFalse, progCond.Status)
	assert.Equal(t, "ListenersNotProgrammed", progCond.Reason)

	// Verify per-listener conditions
	assert.Len(t, updated.Status.Listeners, 2)
	listenerCondMap := make(map[string]metav1.Condition)
	for _, ls := range updated.Status.Listeners {
		if len(ls.Conditions) > 0 {
			listenerCondMap[string(ls.Name)] = ls.Conditions[0]
		}
	}
	assert.Equal(t, metav1.ConditionTrue, listenerCondMap["http"].Status)
	assert.Equal(t, metav1.ConditionFalse, listenerCondMap["tls"].Status)
	assert.Equal(t, ReasonSessionNotEnabled, listenerCondMap["tls"].Reason)
}

// TestHandleDeleteGateway_DeletesListenerByName is a regression test for a latent
// bug where handleDeleteGateway deleted listener map entries by Hostname instead
// of by the map key (the listener Name). For TCP/TLS listeners the Hostname
// differs from the Name (or is empty), so the inner delete was a no-op that would
// leak listener entries if listener lifetime were ever extended past gateway
// deletion. We hold a reference to the gateway struct so we can assert the inner
// delete actually clears gw.listeners, independent of the outer map cleanup.
func TestHandleDeleteGateway_DeletesListenerByName(t *testing.T) {
	mockPool := newMockPool()
	gw := &gateway{
		listeners: map[string]*Listener{
			"tcp-listener": {
				Hostname:    "", // TCP listener: hostname differs from the map key
				Port:        8080,
				Protocol:    "TCP",
				SessionKind: sshmgr.SessionPlain,
				route: &Route{
					Name:      "my-tcp-route",
					Namespace: "test-ns",
					Host:      "backend",
					Port:      9090,
				},
			},
		},
	}
	reconciler := &GatewayReconciler{
		pool: mockPool,
		gateways: map[string]*gateway{
			"test-ns/test-gw": gw,
		},
	}

	k8sGw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "test-ns",
		},
	}

	reconciler.handleDeleteGateway(context.TODO(), k8sGw)

	// Gateway should be removed from the registry.
	_, exists := reconciler.gateways["test-ns/test-gw"]
	assert.False(t, exists)

	// The inner delete must have cleared the listener entry even though the
	// listener's Hostname does not match its map key.
	assert.Empty(t, gw.listeners, "listener entry should be removed by name")

	// StopForwarding should still have been called for the active route.
	assert.Len(t, mockPool.stopForwardingCalls, 1)
	assert.Equal(t, 8080, mockPool.stopForwardingCalls[0].Cfg.RemotePort)
}
