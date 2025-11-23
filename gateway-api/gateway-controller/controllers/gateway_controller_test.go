package controllers

import (
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

func (m *mockSSHTunnelManager) WaitConnection() error {
	return nil
}

func forwardingKey(hostname string, port int) string {
	return fmt.Sprintf("%s:%d", hostname, port)
}
