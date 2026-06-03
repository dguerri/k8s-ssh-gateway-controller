package ssh

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// decodeChannelForwardMsg decodes the payload of a tcpip-forward /
// cancel-tcpip-forward SSH global request. The wire format is:
//
//	string  bind_address
//	uint32  bind_port
//
// channelForwardMsg has unexported fields so ssh.Unmarshal cannot set them via
// reflection; we parse the wire format manually instead.
func decodeChannelForwardMsg(payload []byte) (addr string, port uint32, ok bool) {
	if len(payload) < 4 {
		return "", 0, false
	}
	addrLen := binary.BigEndian.Uint32(payload[0:4])
	if uint32(len(payload)) < 4+addrLen+4 {
		return "", 0, false
	}
	addr = string(payload[4 : 4+addrLen])
	port = binary.BigEndian.Uint32(payload[4+addrLen : 8+addrLen])
	return addr, port, true
}

// forwardRequestRecord captures one tcpip-forward or cancel-tcpip-forward call
// observed by a test fakeClient, with the decoded address and port.
type forwardRequestRecord struct {
	name string
	addr string
	port uint32
}

// recordingSendRequest returns a sendRequestFunc that appends each
// tcpip-forward and cancel-tcpip-forward call (with its decoded payload) to
// the slice guarded by mu, and forwards every call to the wrapped function
// (or returns success when wrapped is nil).
func recordingSendRequest(mu *sync.Mutex, calls *[]forwardRequestRecord,
	wrapped func(name string, wantReply bool, payload []byte) (bool, []byte, error),
) func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	return func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
		if name == "tcpip-forward" || name == "cancel-tcpip-forward" {
			addr, port, ok := decodeChannelForwardMsg(payload)
			if ok {
				mu.Lock()
				*calls = append(*calls, forwardRequestRecord{name: name, addr: addr, port: port})
				mu.Unlock()
			}
		}
		if wrapped != nil {
			return wrapped(name, wantReply, payload)
		}
		return true, nil, nil
	}
}

// TestNewSSHTunnelManagerWithInvalidKey tests creating an SSH tunnel manager with an invalid key.
func TestNewSSHTunnelManagerWithInvalidKey(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        []byte("gibberish"),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err == nil {
		t.Fatalf("Expected an error")
	}

	if manager != nil {
		t.Fatal("Expected a nil manager")
	}
}

// TestNewSSHTunnelManager tests creating an SSH tunnel manager.
func TestNewSSHTunnelManager(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if manager == nil {
		t.Fatal("Expected nil manager")
	}
}

// TestForwardingManagement tests forwarding management.
func TestForwardingManagement(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}
	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	fwd := ForwardingConfig{
		RemoteHost:   "0.0.0.0",
		RemotePort:   2222,
		InternalHost: "localhost",
		InternalPort: 8080,
	}

	if err := manager.StartForwarding(fwd); err != nil {
		t.Errorf("Unexpected error on StartForwarding")
	}

	if err := manager.StartForwarding(fwd); err == nil {
		t.Errorf("Should fail because duplicate forwarding")
	} else {
		var existsErr *ErrSSHForwardingExists
		if !errors.As(err, &existsErr) {
			t.Errorf("Expected SSHForwardingExistsError, got %T", err)
		}
	}

	// Give time to the forwarding goroutine to start.
	time.Sleep(100 * time.Millisecond)

	if err := manager.StopForwarding(&fwd); err != nil {
		t.Errorf("Unexpected error on StopForwarding")
	}

	if err := manager.StopForwarding(&fwd); err == nil {
		t.Errorf("Should fail because of non-existing forewarding")
	} else {
		var notFoundErr *ErrSSHForwardingNotFound
		if !errors.As(err, &notFoundErr) {
			t.Errorf("Expected SSHForwardingNotFoundError, got %T", err)
		}
	}

}

// TestKeepAlive tests forwarding management.
func TestKeepAlive(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 80 * time.Millisecond, // Shorter keep alive for testing
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}
	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Give time to send some keep alives.
	time.Sleep(50 * time.Millisecond)

}

// TestConnectFailsImmediatelyOnDialError tests creating an SSH tunnel manager with failing Dial.
func TestConnectFailsImmediatelyOnDialError(t *testing.T) {
	SetupTest(t)
	sshDialCalledTimes := 0
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		sshDialCalledTimes += 1
		return nil, fmt.Errorf("oh noes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if manager == nil {
		t.Fatal("Expected non-nil manager")
	}

	// Expect Connect to fail immediately
	err = manager.Connect()
	if err == nil {
		t.Fatal("Expected Connect to fail")
	}

	if sshDialCalledTimes != 1 {
		t.Fatalf("Expected exactly 1 Dial call, got %d", sshDialCalledTimes)
	}
}

// TestDuplicateForwarding tests duplicate forwarding.
func TestDuplicateForwarding(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	fwd := ForwardingConfig{
		RemoteHost:   "0.0.0.0",
		RemotePort:   2222,
		InternalHost: "localhost",
		InternalPort: 8080,
	}

	if err := manager.StartForwarding(fwd); err != nil {
		t.Fatalf("Unexpected error on StartForwarding")
	}

	if err := manager.StartForwarding(fwd); err == nil {
		t.Fatalf("Duplicate forwarding should fail")
	} else {
		var existsErr *ErrSSHForwardingExists
		if !errors.As(err, &existsErr) {
			t.Fatalf("Expected SSHForwardingExistsError, got %T", err)
		}
	}
}

// TestStopForwardingNonExisting tests stopping a non-existing forwarding.
func TestStopForwardingNonExisting(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	fwd := &ForwardingConfig{
		RemoteHost:   "0.0.0.0",
		RemotePort:   2222,
		InternalHost: "localhost",
		InternalPort: 8080,
	}

	if err := manager.StopForwarding(fwd); err == nil {
		t.Errorf("Expected an error for non-existing forwarding")
	} else {
		var notFoundErr *ErrSSHForwardingNotFound
		if !errors.As(err, &notFoundErr) {
			t.Errorf("Expected SSHForwardingNotFoundError, got %T", err)
		}
	}
}

// TestCloseStopsForwardings tests closing the manager stops forwardings.
func TestCloseStopsForwardings(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	fwd := ForwardingConfig{
		RemoteHost:   "0.0.0.0",
		RemotePort:   2223,
		InternalHost: "localhost",
		InternalPort: 8081,
	}

	if err := manager.StartForwarding(fwd); err != nil {
		t.Fatalf("Failed to start forwarding: %v", err)
	}

	manager.Stop()

	if len(manager.forwardings) != 0 {
		t.Errorf("Expected all forwardings to be cleared on Close")
	}
}

// TestGetAssignedAddresses tests retrieving assigned addresses for forwardings.
func TestGetAssignedAddresses(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Test getting addresses for non-existent forwarding
	addrs := manager.GetAssignedAddresses("example.com", 8080)
	if addrs != nil {
		t.Errorf("Expected nil for non-existent forwarding, got %v", addrs)
	}

	// Add a forwarding and manually set assigned addresses
	fwd := ForwardingConfig{
		RemoteHost:   "example.com",
		RemotePort:   8080,
		InternalHost: "localhost",
		InternalPort: 8080,
	}
	if err := manager.StartForwarding(fwd); err != nil {
		t.Fatalf("Failed to start forwarding: %v", err)
	}

	// Manually populate assigned addresses
	key := forwardingKey(fwd.RemoteHost, fwd.RemotePort)
	testAddrs := []string{"https://example.com:8080", "tcp://example.com:8080"}
	manager.addrNotifMu.Lock()
	manager.assignedAddrs[key] = testAddrs
	manager.addrNotifMu.Unlock()

	// Test getting addresses for existing forwarding
	addrs = manager.GetAssignedAddresses("example.com", 8080)
	if addrs == nil {
		t.Fatal("Expected non-nil addresses")
	}
	if len(addrs) != 2 {
		t.Errorf("Expected 2 addresses, got %d", len(addrs))
	}
	if addrs[0] != "https://example.com:8080" || addrs[1] != "tcp://example.com:8080" {
		t.Errorf("Unexpected addresses: %v", addrs)
	}

	// Verify we got a copy (modifying returned slice shouldn't affect internal state)
	addrs[0] = "modified"
	addrs2 := manager.GetAssignedAddresses("example.com", 8080)
	if addrs2[0] == "modified" {
		t.Error("GetAssignedAddresses should return a copy, not original slice")
	}
}

// TestMatchesRequestedHost tests the hostname matching logic.
func TestMatchesRequestedHost(t *testing.T) {
	tests := []struct {
		name          string
		requestedHost string
		uris          []string
		requestedPort int
		enforcePort   bool
		expected      bool
	}{
		{
			name:          "empty host allows any",
			uris:          []string{"https://example.com:8080"},
			requestedHost: "",
			requestedPort: 8080,
			expected:      true,
		},
		{
			name:          "0.0.0.0 allows any",
			uris:          []string{"https://example.com:8080"},
			requestedHost: "0.0.0.0",
			requestedPort: 8080,
			expected:      true,
		},
		{
			name:          "HTTP URI with matching hostname",
			uris:          []string{"http://user-dev.example.com"},
			requestedHost: "dev",
			requestedPort: 8080,
			expected:      true,
		},
		{
			name:          "HTTPS URI with matching hostname",
			uris:          []string{"https://user-dev.example.com"},
			requestedHost: "dev",
			requestedPort: 8080,
			expected:      true,
		},
		{
			name:          "TCP URI with exact match",
			uris:          []string{"tcp://example.com:8080"},
			requestedHost: "example.com",
			requestedPort: 8080,
			expected:      true,
		},
		{
			name:          "no match",
			uris:          []string{"https://other.com"},
			requestedHost: "example",
			requestedPort: 8080,
			expected:      false,
		},
		{
			name:          "multiple URIs with one match",
			uris:          []string{"https://other.com", "tcp://example.com:8080"},
			requestedHost: "example.com",
			requestedPort: 8080,
			expected:      true,
		},
		{
			name:          "TCP URI with wrong port (specific hostname)",
			uris:          []string{"tcp://example.com:9090"},
			requestedHost: "example.com",
			requestedPort: 8080,
			expected:      false,
		},
		{
			name:          "TCP URI with wildcard hostname, enforcePort, correct port",
			uris:          []string{"tcp://nue.tuns.sh:27101"},
			requestedHost: "localhost",
			requestedPort: 27101,
			enforcePort:   true,
			expected:      true,
		},
		{
			name:          "TCP URI with wildcard hostname, enforcePort, wrong port",
			uris:          []string{"tcp://nue.tuns.sh:31879"},
			requestedHost: "localhost",
			requestedPort: 27101,
			enforcePort:   true,
			expected:      false,
		},
		{
			name:          "TCP URI with wildcard hostname, no enforcePort, wrong port",
			uris:          []string{"tcp://nue.tuns.sh:31879"},
			requestedHost: "localhost",
			requestedPort: 27101,
			enforcePort:   false,
			expected:      true,
		},
		{
			name:          "TCP URI with 0.0.0.0 hostname, enforcePort, correct port",
			uris:          []string{"tcp://nue.tuns.sh:27101"},
			requestedHost: "0.0.0.0",
			requestedPort: 27101,
			enforcePort:   true,
			expected:      true,
		},
		{
			name:          "TCP URI with 0.0.0.0 hostname, enforcePort, wrong port",
			uris:          []string{"tcp://nue.tuns.sh:31879"},
			requestedHost: "0.0.0.0",
			requestedPort: 27101,
			enforcePort:   true,
			expected:      false,
		},
		{
			name:          "HTTPS URI ignores enforcePort",
			uris:          []string{"https://random.tuns.sh"},
			requestedHost: "localhost",
			requestedPort: 80,
			enforcePort:   true,
			expected:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MatchesRequestedHost(tt.uris, tt.requestedHost, tt.requestedPort, tt.enforcePort)
			if result != tt.expected {
				t.Errorf("MatchesRequestedHost(%v, %q, %d, %v) = %v, want %v",
					tt.uris, tt.requestedHost, tt.requestedPort, tt.enforcePort, result, tt.expected)
			}
		})
	}
}

// TestConnectClientWithHostKeyVerification tests host key verification logic.
func TestConnectClientWithHostKeyVerification(t *testing.T) {
	SetupTest(t)

	// Generate a test key for host key verification
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create signer from key: %v", err)
	}

	// Calculate the expected fingerprint
	pubKey := signer.PublicKey()
	hash := sha256.Sum256(pubKey.Marshal())
	expectedFingerprint := "SHA256:" + base64.StdEncoding.EncodeToString(hash[:])

	t.Run("correct host key", func(t *testing.T) {
		hostKeyVerified := false
		sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
			// Call the host key callback to verify it works
			err := cfg.HostKeyCallback("example.com:22", &fakeAddr{}, pubKey)
			if err == nil {
				hostKeyVerified = true
			}
			return &fakeClient{}, err
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sshConfig := SSHConnectionConfig{
			PrivateKey:        GenerateTestPrivateKey(t),
			ServerAddress:     "example.com:22",
			Username:          "testuser",
			HostKey:           expectedFingerprint,
			ConnectTimeout:    5 * time.Second,
			FwdReqTimeout:     2 * time.Second,
			KeepAliveInterval: 5 * time.Second,
		}

		manager, err := NewSSHTunnelManager(ctx, &sshConfig)
		if err != nil {
			t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
		}

		if err := manager.Connect(); err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}

		if !hostKeyVerified {
			t.Error("Expected host key to be verified")
		}
	})

	t.Run("incorrect host key", func(t *testing.T) {
		connectionFailed := false
		sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
			// Call the host key callback with the key
			err := cfg.HostKeyCallback("example.com:22", &fakeAddr{}, pubKey)
			if err != nil {
				connectionFailed = true
			}
			return nil, err
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sshConfig := SSHConnectionConfig{
			PrivateKey:        GenerateTestPrivateKey(t),
			ServerAddress:     "example.com:22",
			Username:          "testuser",
			HostKey:           "SHA256:wrongfingerprint",
			ConnectTimeout:    5 * time.Second,
			FwdReqTimeout:     2 * time.Second,
			KeepAliveInterval: 5 * time.Second,
		}

		manager, err := NewSSHTunnelManager(ctx, &sshConfig)
		if err != nil {
			t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
		}

		err = manager.Connect()
		if err == nil {
			t.Fatal("Expected Connect to fail")
		}

		if !connectionFailed {
			t.Error("Expected connection to fail with incorrect host key")
		}
	})
}

// TestStartForwardingWithoutConnection tests starting forwarding before connection is ready.
func TestStartForwardingWithoutConnection(t *testing.T) {
	SetupTest(t)

	// Use a dial function that never succeeds to keep the connection not ready
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		return nil, fmt.Errorf("connection refused")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	// Do NOT call Connect() here, or call it and expect error.
	// The test wants to verify StartForwarding fails if not connected.

	// Try to start forwarding without waiting for connection
	fwd := ForwardingConfig{
		RemoteHost:   "0.0.0.0",
		RemotePort:   8080,
		InternalHost: "localhost",
		InternalPort: 8080,
	}

	err = manager.StartForwarding(fwd)
	if err == nil {
		t.Error("Expected error when starting forwarding without connection")
	}

	var notReadyErr *ErrSSHClientNotReady
	if !errors.As(err, &notReadyErr) {
		t.Errorf("Expected ErrSSHClientNotReady, got %T: %v", err, err)
	}
}

// TestStopForwardingWithoutConnection tests stopping forwarding when client is not ready.
func TestStopForwardingWithoutConnection(t *testing.T) {
	SetupTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	fwd := ForwardingConfig{
		RemoteHost:   "0.0.0.0",
		RemotePort:   8080,
		InternalHost: "localhost",
		InternalPort: 8080,
	}

	// Start a forwarding
	err = manager.StartForwarding(fwd)
	if err != nil {
		t.Fatalf("Failed to start forwarding: %v", err)
	}

	// Disconnect the client
	manager.clientMu.Lock()
	manager.closeClient()
	manager.clientMu.Unlock()

	// Try to stop forwarding without connection
	err = manager.StopForwarding(&fwd)
	if err == nil {
		t.Error("Expected error when stopping forwarding without connection")
	}

	var notReadyErr *ErrSSHClientNotReady
	if !errors.As(err, &notReadyErr) {
		t.Errorf("Expected ErrSSHClientNotReady, got %T: %v", err, err)
	}
}

// TestSendForwardingWithAddressVerification tests forwarding with hostname verification and retry logic.
func TestSendForwardingWithAddressVerification(t *testing.T) {
	t.Run("successful verification on first attempt", func(t *testing.T) {
		SetupTest(t)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Mock remoteAddrFunc that extracts URIs
		remoteAddrFunc := func(data string) ([]string, error) {
			return []string{"https://user-dev.example.com"}, nil
		}

		sshConfig := SSHConnectionConfig{
			PrivateKey:                 GenerateTestPrivateKey(t),
			ServerAddress:              "example.com:22",
			Username:                   "testuser",
			HostKey:                    "",
			ConnectTimeout:             5 * time.Second,
			FwdReqTimeout:              2 * time.Second,
			KeepAliveInterval:          5 * time.Second,
			RemoteAddrFunc:             remoteAddrFunc,
			AddressVerificationTimeout: 200 * time.Millisecond,
		}

		manager, err := NewSSHTunnelManager(ctx, &sshConfig)
		if err != nil {
			t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
		}

		if err := manager.Connect(); err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}

		fwd := ForwardingConfig{
			RemoteHost:   "dev",
			RemotePort:   8080,
			InternalHost: "localhost",
			InternalPort: 8080,
		}

		// Simulate sending URIs to the notification channel
		go func() {
			time.Sleep(50 * time.Millisecond)
			key := forwardingKey(fwd.RemoteHost, fwd.RemotePort)
			manager.addrNotifMu.Lock()
			if ch, ok := manager.addrNotifications[key]; ok {
				ch <- []string{"https://user-dev.example.com"}
			}
			manager.addrNotifMu.Unlock()
		}()

		err = manager.StartForwarding(fwd)
		if err != nil {
			t.Errorf("Expected successful forwarding with verification, got error: %v", err)
		}

		// Verify assigned addresses were stored
		addrs := manager.GetAssignedAddresses("dev", 8080)
		if len(addrs) != 1 || addrs[0] != "https://user-dev.example.com" {
			t.Errorf("Expected assigned addresses to be stored, got: %v", addrs)
		}
	})

	t.Run("verification timeout fails for specific hostname", func(t *testing.T) {
		SetupTest(t)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		remoteAddrFunc := func(data string) ([]string, error) {
			return []string{"https://user-dev.example.com"}, nil
		}

		sshConfig := SSHConnectionConfig{
			PrivateKey:                 GenerateTestPrivateKey(t),
			ServerAddress:              "example.com:22",
			Username:                   "testuser",
			HostKey:                    "",
			ConnectTimeout:             5 * time.Second,
			FwdReqTimeout:              2 * time.Second,
			KeepAliveInterval:          5 * time.Second,
			RemoteAddrFunc:             remoteAddrFunc,
			AddressVerificationTimeout: 50 * time.Millisecond,
		}

		manager, err := NewSSHTunnelManager(ctx, &sshConfig)
		if err != nil {
			t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
		}

		if err := manager.Connect(); err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}

		fwd := ForwardingConfig{
			RemoteHost:   "dev",
			RemotePort:   8080,
			InternalHost: "localhost",
			InternalPort: 8080,
		}

		// Don't send any URIs - let it timeout
		// For specific hostnames, verification timeout should fail

		err = manager.StartForwarding(fwd)
		// Should fail with timeout for specific hostname
		if err == nil {
			t.Error("Expected forwarding to fail with verification timeout for specific hostname")
		}
		if err != nil && err.Error() != "timeout waiting for address verification for dev" {
			t.Errorf("Expected timeout error, got: %v", err)
		}
	})

	t.Run("wrong hostname fails immediately", func(t *testing.T) {
		SetupTest(t)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		remoteAddrFunc := func(data string) ([]string, error) {
			return []string{"https://user-prod.example.com"}, nil
		}

		attemptCount := 0
		var currentManager *SSHTunnelManager
		sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
			client := &fakeClient{
				sendRequestFunc: func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
					if name == "tcpip-forward" {
						attemptCount++
						// Send wrong hostname notification quickly after forwarding request
						go func() {
							if currentManager != nil {
								time.Sleep(5 * time.Millisecond)
								key := "dev:8080"
								currentManager.addrNotifMu.Lock()
								if ch, ok := currentManager.addrNotifications[key]; ok {
									select {
									case ch <- []string{"https://user-prod.example.com"}:
									default:
									}
								}
								currentManager.addrNotifMu.Unlock()
							}
						}()
					}
					// Also handle cancel requests
					if name == "cancel-tcpip-forward" {
						return true, nil, nil
					}
					return true, nil, nil
				},
			}
			return client, nil
		}

		sshConfig := SSHConnectionConfig{
			PrivateKey:                 GenerateTestPrivateKey(t),
			ServerAddress:              "example.com:22",
			Username:                   "testuser",
			HostKey:                    "",
			ConnectTimeout:             5 * time.Second,
			FwdReqTimeout:              2 * time.Second,
			KeepAliveInterval:          5 * time.Second,
			RemoteAddrFunc:             remoteAddrFunc,
			AddressVerificationTimeout: 200 * time.Millisecond,
		}

		manager, err := NewSSHTunnelManager(ctx, &sshConfig)
		if err != nil {
			t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
		}
		currentManager = manager

		if err := manager.Connect(); err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}

		fwd := ForwardingConfig{
			RemoteHost:   "dev", // Requesting "dev" but will get "prod"
			RemotePort:   8080,
			InternalHost: "localhost",
			InternalPort: 8080,
		}

		err = manager.StartForwarding(fwd)
		if err == nil {
			t.Error("Expected error when wrong hostname is assigned")
		}

		expectedErr := fmt.Errorf("wrong hostname assigned: [https://user-prod.example.com]")
		if err != nil && err.Error() != expectedErr.Error() {
			t.Errorf("Expected error %q, got: %q", expectedErr.Error(), err.Error())
		}

		if attemptCount != 1 {
			t.Errorf("Expected exactly 1 forwarding attempt, got %d", attemptCount)
		}
	})
}

// TestSendForwardingRequestTimeout tests timeout handling in sendForwardingOnce.
func TestSendForwardingRequestTimeout(t *testing.T) {
	SetupTest(t)

	// Create a client that blocks on SendRequest
	blockingClient := &fakeClient{
		sendRequestFunc: func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
			if name == "tcpip-forward" {
				// Block forever
				select {}
			}
			return true, nil, nil
		},
	}

	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		return blockingClient, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     100 * time.Millisecond, // Short timeout
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	fwd := ForwardingConfig{
		RemoteHost:   "0.0.0.0",
		RemotePort:   8080,
		InternalHost: "localhost",
		InternalPort: 8080,
	}

	err = manager.StartForwarding(fwd)
	if err == nil {
		t.Error("Expected timeout error")
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && err.Error() != "ssh: tcpip-forward request timed out" {
		t.Errorf("Expected timeout error, got: %v", err)
	}
}

// TestSendForwardingRequestDenied tests handling of denied forwarding requests.
func TestSendForwardingRequestDenied(t *testing.T) {
	SetupTest(t)

	deniedClient := &fakeClient{
		sendRequestFunc: func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
			if name == "tcpip-forward" {
				return false, nil, nil // Server denied the request
			}
			return true, nil, nil
		},
	}

	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		return deniedClient, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	fwd := ForwardingConfig{
		RemoteHost:   "0.0.0.0",
		RemotePort:   8080,
		InternalHost: "localhost",
		InternalPort: 8080,
	}

	err = manager.StartForwarding(fwd)
	if err == nil {
		t.Error("Expected error when server denies forwarding request")
	}
	if err != nil && err.Error() != "ssh: tcpip-forward request denied by server" {
		t.Errorf("Expected 'request denied' error, got: %v", err)
	}
}

// TestMonitorConnectionWithFailure tests connection monitoring with keepalive failure.
func TestMonitorConnectionWithFailure(t *testing.T) {
	SetupTest(t)

	keepaliveCallCount := 0
	failingClient := &fakeClient{
		sendRequestFunc: func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
			if name == "keepalive@openssh.com" {
				keepaliveCallCount++
				if keepaliveCallCount >= 2 {
					return false, nil, fmt.Errorf("keepalive failed")
				}
			}
			return true, nil, nil
		},
	}

	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		return failingClient, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 50 * time.Millisecond, // Fast keepalive for testing
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Wait for multiple keepalive attempts
	time.Sleep(200 * time.Millisecond)

	if keepaliveCallCount < 2 {
		t.Errorf("Expected at least 2 keepalive attempts, got %d", keepaliveCallCount)
	}

	// Verify connection is closed
	if manager.IsConnected() {
		t.Error("Expected manager to be disconnected after keepalive failure")
	}
}

// TestWildcardForwardingStoresAddresses tests that wildcard (0.0.0.0) forwardings store assigned addresses
func TestWildcardForwardingStoresAddresses(t *testing.T) {
	SetupTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Mock remoteAddrFunc that extracts TCP URIs
	remoteAddrFunc := func(data string) ([]string, error) {
		return []string{"tcp://nue.tuns.sh:34012"}, nil
	}

	var currentManager *SSHTunnelManager
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		client := &fakeClient{
			sendRequestFunc: func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
				if name == "tcpip-forward" {
					// Send TCP URI notification after forwarding request
					go func() {
						if currentManager != nil {
							time.Sleep(5 * time.Millisecond)
							key := "0.0.0.0:8080"
							currentManager.addrNotifMu.Lock()
							if ch, ok := currentManager.addrNotifications[key]; ok {
								select {
								case ch <- []string{"tcp://nue.tuns.sh:34012"}:
								default:
								}
							}
							currentManager.addrNotifMu.Unlock()
						}
					}()
				}
				return true, nil, nil
			},
		}
		return client, nil
	}

	sshConfig := SSHConnectionConfig{
		PrivateKey:                 GenerateTestPrivateKey(t),
		ServerAddress:              "example.com:22",
		Username:                   "testuser",
		HostKey:                    "",
		ConnectTimeout:             5 * time.Second,
		FwdReqTimeout:              2 * time.Second,
		KeepAliveInterval:          5 * time.Second,
		RemoteAddrFunc:             remoteAddrFunc,
		AddressVerificationTimeout: 200 * time.Millisecond,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}
	currentManager = manager

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	fwd := ForwardingConfig{
		RemoteHost:   "0.0.0.0", // Wildcard - should skip verification but still store addresses
		RemotePort:   8080,
		InternalHost: "localhost",
		InternalPort: 8080,
	}

	err = manager.StartForwarding(fwd)
	if err != nil {
		t.Fatalf("Unexpected error on wildcard StartForwarding: %v", err)
	}

	// Verify assigned addresses were stored for wildcard forwarding
	addrs := manager.GetAssignedAddresses("0.0.0.0", 8080)
	if len(addrs) != 1 {
		t.Fatalf("Expected 1 assigned address for wildcard forwarding, got %d", len(addrs))
	}
	if addrs[0] != "tcp://nue.tuns.sh:34012" {
		t.Errorf("Expected assigned address tcp://nue.tuns.sh:34012, got: %s", addrs[0])
	}
}

// TestEmptyHostnameForwardingStoresAddresses tests that empty hostname forwardings store assigned addresses
func TestEmptyHostnameForwardingStoresAddresses(t *testing.T) {
	SetupTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Mock remoteAddrFunc that extracts HTTP URIs
	remoteAddrFunc := func(data string) ([]string, error) {
		return []string{"http://example.com", "https://example.com"}, nil
	}

	var currentManager *SSHTunnelManager
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		client := &fakeClient{
			sendRequestFunc: func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
				if name == "tcpip-forward" {
					// Send HTTP URI notification after forwarding request
					go func() {
						if currentManager != nil {
							time.Sleep(5 * time.Millisecond)
							key := ":80"
							currentManager.addrNotifMu.Lock()
							if ch, ok := currentManager.addrNotifications[key]; ok {
								select {
								case ch <- []string{"http://example.com", "https://example.com"}:
								default:
								}
							}
							currentManager.addrNotifMu.Unlock()
						}
					}()
				}
				return true, nil, nil
			},
		}
		return client, nil
	}

	sshConfig := SSHConnectionConfig{
		PrivateKey:                 GenerateTestPrivateKey(t),
		ServerAddress:              "example.com:22",
		Username:                   "testuser",
		HostKey:                    "",
		ConnectTimeout:             5 * time.Second,
		FwdReqTimeout:              2 * time.Second,
		KeepAliveInterval:          5 * time.Second,
		RemoteAddrFunc:             remoteAddrFunc,
		AddressVerificationTimeout: 200 * time.Millisecond,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}
	currentManager = manager

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	fwd := ForwardingConfig{
		RemoteHost:   "", // Empty hostname - should skip verification but still store addresses
		RemotePort:   80,
		InternalHost: "localhost",
		InternalPort: 8080,
	}

	err = manager.StartForwarding(fwd)
	if err != nil {
		t.Fatalf("Unexpected error on empty hostname StartForwarding: %v", err)
	}

	// Verify assigned addresses were stored for empty hostname forwarding
	addrs := manager.GetAssignedAddresses("", 80)
	if len(addrs) != 2 {
		t.Fatalf("Expected 2 assigned addresses for empty hostname forwarding, got %d", len(addrs))
	}
	if addrs[0] != "http://example.com" || addrs[1] != "https://example.com" {
		t.Errorf("Expected assigned addresses http://example.com and https://example.com, got: %v", addrs)
	}
}

func TestSessionFlags_BuildExecCommand(t *testing.T) {
	tests := []struct {
		name     string
		flags    SessionFlags
		expected string
		useShell bool
	}{
		{"none", SessionFlags{}, "", true},
		{"pp v1", SessionFlags{ProxyProtocolVersion: 1}, "proxy-protocol=1", false},
		{"pp v2", SessionFlags{ProxyProtocolVersion: 2}, "proxy-protocol=2", false},
		{"sni only", SessionFlags{SNIProxy: true}, "sni-proxy=true", false},
		{"pp + sni", SessionFlags{ProxyProtocolVersion: 2, SNIProxy: true}, "proxy-protocol=2 sni-proxy=true", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, useShell := tt.flags.execCommand()
			if cmd != tt.expected {
				t.Errorf("expected cmd=%q, got %q", tt.expected, cmd)
			}
			if useShell != tt.useShell {
				t.Errorf("expected useShell=%v, got %v", tt.useShell, useShell)
			}
		})
	}
}

func TestSessionFlags_DefaultLabel(t *testing.T) {
	cases := []struct {
		name  string
		flags SessionFlags
		want  string
	}{
		{"plain", SessionFlags{}, "plain"},
		{"pp v1", SessionFlags{ProxyProtocolVersion: 1}, "pp:v1"},
		{"pp v2", SessionFlags{ProxyProtocolVersion: 2}, "pp:v2"},
		{"sni only", SessionFlags{SNIProxy: true}, "sni"},
		{"sni wins over pp", SessionFlags{ProxyProtocolVersion: 2, SNIProxy: true}, "sni"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.flags.DefaultLabel()
			if got != tt.want {
				t.Errorf("flags=%+v: expected label %q, got %q", tt.flags, tt.want, got)
			}
		})
	}
}

// TestAuthenticationMethods verifies that the SSH client config includes both
// publickey and keyboard-interactive authentication methods.
func TestAuthenticationMethods(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Capture the ClientConfig when sshDial is called
	var capturedConfig *ssh.ClientConfig
	mockDialFunc := func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		capturedConfig = cfg
		return &fakeClient{}, nil
	}

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "serveo.net:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
		SSHDialFunc:       mockDialFunc,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	// Connect to trigger the auth methods setup
	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Verify ClientConfig was captured
	if capturedConfig == nil {
		t.Fatal("ClientConfig was not captured")
	}

	// Verify that we have at least 2 auth methods
	// The first should be publickey, the second should be keyboard-interactive
	if len(capturedConfig.Auth) < 2 {
		t.Fatalf("Expected at least 2 auth methods (publickey + keyboard-interactive), got %d", len(capturedConfig.Auth))
	}

	t.Logf("Successfully verified SSH config has %d auth methods (publickey + keyboard-interactive)", len(capturedConfig.Auth))
}

// TestSendForwardingURIRaceCondition is a regression test for an infinite
// reconcile loop caused by losing the SSH server's URI notification.
//
// The SSH server can emit the assigned forwarding URI on its stdout
// concurrently with returning the tcpip-forward response. If the notification
// channel is registered AFTER sendForwardingOnce returns, the URI can arrive
// while no channel is registered and be silently dropped. The forwarding then
// ends up with m.forwardings[key] populated but m.assignedAddrs[key] empty,
// causing the gateway controller's isForwardingValid to keep returning false
// and StartForwarding to keep returning ErrSSHForwardingExists in a loop.
//
// This test simulates the race by emitting the URI from inside
// sendRequestFunc (i.e., during the SSH server response) and verifies that
// the URI is still captured in assignedAddrs.
func TestSendForwardingURIRaceCondition(t *testing.T) {
	SetupTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	remoteAddrFunc := func(data string) ([]string, error) {
		return []string{"tcp://nue.tuns.sh:27202"}, nil
	}

	var currentManager *SSHTunnelManager
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		client := &fakeClient{
			sendRequestFunc: func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
				if name == "tcpip-forward" && currentManager != nil {
					// Emit URI BEFORE returning the SSH response — this is the
					// race window where the old code would drop the URI.
					currentManager.processServerData([]byte("tcp://nue.tuns.sh:27202\n"), "stdout")
				}
				return true, nil, nil
			},
		}
		return client, nil
	}

	sshConfig := SSHConnectionConfig{
		PrivateKey:                 GenerateTestPrivateKey(t),
		ServerAddress:              "example.com:22",
		Username:                   "testuser",
		ConnectTimeout:             5 * time.Second,
		FwdReqTimeout:              2 * time.Second,
		KeepAliveInterval:          5 * time.Second,
		RemoteAddrFunc:             remoteAddrFunc,
		AddressVerificationTimeout: 200 * time.Millisecond,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}
	currentManager = manager

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	fwd := ForwardingConfig{
		RemoteHost:   "localhost",
		RemotePort:   27202,
		InternalHost: "note-manager-svc",
		InternalPort: 1337,
	}

	if err := manager.StartForwarding(fwd); err != nil {
		t.Fatalf("Unexpected error on StartForwarding: %v", err)
	}

	addrs := manager.GetAssignedAddresses("localhost", 27202)
	if len(addrs) == 0 {
		t.Fatal("URI emitted during tcpip-forward response was dropped; assignedAddrs is empty (race condition regressed)")
	}
	if addrs[0] != "tcp://nue.tuns.sh:27202" {
		t.Errorf("Expected captured URI 'tcp://nue.tuns.sh:27202', got: %v", addrs)
	}
}

// TestHandleChannelsClosesBothEndsOnOneSidedEOF verifies that when the backend
// connection EOFs (e.g. the upstream server program exited) handleChannels
// fully closes both the SSH channel and the backend connection, even if the
// remote peer has not yet hung up.
//
// Regression: previously the goroutines only called CloseWrite, leaving the
// SSH channel half-closed. The peer goroutine then blocked indefinitely on
// Read, wg.Wait() never returned, and upstream SSH servers like tuns.sh kept
// the public-facing TCP connection open until something else broke.
func TestHandleChannelsClosesBothEndsOnOneSidedEOF(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Backend connection: EOFs on first Read (server program exited / socat
	// closed the TCP connection).
	localConn := newTrackingNetConn()

	// Remote SSH channel: blocks on Read until Close is called (peer hasn't
	// hung up). With the bug this would never unblock; with the fix it
	// unblocks because the other goroutine fully closes the channel.
	remoteConn := newBlockingSshChannel()

	newChan := &fakeNewSshChannelWithAccept{
		channelType: "forwarded-tcpip",
		extraData: ssh.Marshal(forwardedTCPPayload{
			Addr: "0.0.0.0",
			Port: 2222,
		}),
		channel: remoteConn,
	}

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
		SSHDialFunc: func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
			return &fakeClient{customNewChannel: newChan}, nil
		},
		NetDialFunc: func(network, address string) (net.Conn, error) {
			return localConn, nil
		},
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}
	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Register the forwarding so handleChannels accepts the inbound channel.
	if err := manager.StartForwarding(ForwardingConfig{
		RemoteHost:   "0.0.0.0",
		RemotePort:   2222,
		InternalHost: "localhost",
		InternalPort: 8080,
	}); err != nil {
		t.Fatalf("Failed to start forwarding: %v", err)
	}

	// With the fix both connections must be fully closed shortly after the
	// backend EOFs. 2s is generous — the fix should close in microseconds.
	timeout := 2 * time.Second

	select {
	case <-localConn.closed:
	case <-time.After(timeout):
		t.Fatalf("local connection was not closed within %s (Close calls: %d)",
			timeout, localConn.closeCalls.Load())
	}

	select {
	case <-remoteConn.closed:
	case <-time.After(timeout):
		t.Fatalf("remote SSH channel was not fully closed within %s (Close: %d, CloseWrite: %d)",
			timeout, remoteConn.closeCalls.Load(), remoteConn.closeWriteCalls.Load())
	}

	if got := remoteConn.closeCalls.Load(); got == 0 {
		t.Errorf("expected remote SSH channel Close() to be called, got 0 calls")
	}
	if got := localConn.closeCalls.Load(); got == 0 {
		t.Errorf("expected local connection Close() to be called, got 0 calls")
	}
}

// TestWrongPortCancelUsesBoundPort verifies that when the SSH server returns a
// TCP URI bound to a port different from the one we requested
// (EnforcePort=true), the resulting cancel-tcpip-forward targets the
// server-bound port rather than the originally-requested port. Canceling the
// requested port would leak the random port the server actually bound.
//
// Regression: previously handleAssignedURIs sent cancel for fwd.RemotePort,
// causing observed tuns.sh behavior where each retry leaked another random
// port on the server.
func TestWrongPortCancelUsesBoundPort(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const requestedPort = 27202
	const boundPort = 49468

	var (
		callsMu sync.Mutex
		calls   []forwardRequestRecord
	)

	remoteAddrFunc := func(data string) ([]string, error) {
		return []string{fmt.Sprintf("tcp://nue.tuns.sh:%d", boundPort)}, nil
	}

	var currentManager *SSHTunnelManager
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		client := &fakeClient{}
		client.sendRequestFunc = recordingSendRequest(&callsMu, &calls,
			func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
				if name == "tcpip-forward" {
					// Deliver the wrong-port URI via the notification channel
					// shortly after the tcpip-forward request is accepted.
					go func() {
						time.Sleep(5 * time.Millisecond)
						if currentManager == nil {
							return
						}
						key := forwardingKey("localhost", requestedPort)
						currentManager.addrNotifMu.Lock()
						if ch, ok := currentManager.addrNotifications[key]; ok {
							select {
							case ch <- []string{fmt.Sprintf("tcp://nue.tuns.sh:%d", boundPort)}:
							default:
							}
						}
						currentManager.addrNotifMu.Unlock()
					}()
				}
				return true, nil, nil
			})
		return client, nil
	}

	sshConfig := SSHConnectionConfig{
		PrivateKey:                 GenerateTestPrivateKey(t),
		ServerAddress:              "example.com:22",
		Username:                   "testuser",
		ConnectTimeout:             5 * time.Second,
		FwdReqTimeout:              2 * time.Second,
		KeepAliveInterval:          5 * time.Second,
		RemoteAddrFunc:             remoteAddrFunc,
		AddressVerificationTimeout: 500 * time.Millisecond,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}
	currentManager = manager

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	err = manager.StartForwarding(ForwardingConfig{
		RemoteHost:   "localhost",
		RemotePort:   requestedPort,
		InternalHost: "localhost",
		InternalPort: 8080,
		EnforcePort:  true,
	})
	if err == nil {
		t.Fatal("Expected error from StartForwarding on wrong port assignment")
	}

	// Locate the wrong-hostname-cleanup cancel call. We tolerate extra
	// cancels for the pre-emptive cleanup (requestedPort) and orphan
	// cancels for the default fake channel (0.0.0.0:2222) — only the
	// cleanup of the actually-bound port is what we assert here.
	callsMu.Lock()
	defer callsMu.Unlock()

	var foundBoundCancel, foundRequestedCancelAfterTcpip bool
	tcpipSeen := false
	for _, c := range calls {
		if c.name == "tcpip-forward" && c.port == requestedPort {
			tcpipSeen = true
			continue
		}
		if c.name != "cancel-tcpip-forward" {
			continue
		}
		if c.port == boundPort {
			foundBoundCancel = true
		}
		if c.port == requestedPort && tcpipSeen {
			foundRequestedCancelAfterTcpip = true
		}
	}

	if !foundBoundCancel {
		t.Errorf("expected cancel-tcpip-forward for bound port %d, calls=%+v", boundPort, calls)
	}
	if foundRequestedCancelAfterTcpip {
		t.Errorf("did not expect cancel-tcpip-forward for requested port %d after tcpip-forward (would leak bound port), calls=%+v", requestedPort, calls)
	}
}

// TestPreemptiveCancelOnEnforcePort verifies that StartForwarding with
// EnforcePort=true sends a best-effort cancel-tcpip-forward for the requested
// address:port before sending the tcpip-forward request, to clear any stale
// reservation left on the SSH server by a previous session. With
// EnforcePort=false no pre-emptive cancel must be sent.
func TestPreemptiveCancelOnEnforcePort(t *testing.T) {
	t.Run("EnforcePort=true sends pre-emptive cancel before tcpip-forward", func(t *testing.T) {
		SetupTest(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		const requestedPort = 27202

		var (
			callsMu sync.Mutex
			calls   []forwardRequestRecord
		)

		remoteAddrFunc := func(data string) ([]string, error) {
			return []string{fmt.Sprintf("tcp://nue.tuns.sh:%d", requestedPort)}, nil
		}

		var currentManager *SSHTunnelManager
		sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
			client := &fakeClient{}
			client.sendRequestFunc = recordingSendRequest(&callsMu, &calls,
				func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
					if name == "tcpip-forward" {
						go func() {
							time.Sleep(5 * time.Millisecond)
							if currentManager == nil {
								return
							}
							key := forwardingKey("localhost", requestedPort)
							currentManager.addrNotifMu.Lock()
							if ch, ok := currentManager.addrNotifications[key]; ok {
								select {
								case ch <- []string{fmt.Sprintf("tcp://nue.tuns.sh:%d", requestedPort)}:
								default:
								}
							}
							currentManager.addrNotifMu.Unlock()
						}()
					}
					return true, nil, nil
				})
			return client, nil
		}

		sshConfig := SSHConnectionConfig{
			PrivateKey:                 GenerateTestPrivateKey(t),
			ServerAddress:              "example.com:22",
			Username:                   "testuser",
			ConnectTimeout:             5 * time.Second,
			FwdReqTimeout:              2 * time.Second,
			KeepAliveInterval:          5 * time.Second,
			RemoteAddrFunc:             remoteAddrFunc,
			AddressVerificationTimeout: 500 * time.Millisecond,
		}

		manager, err := NewSSHTunnelManager(ctx, &sshConfig)
		if err != nil {
			t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
		}
		currentManager = manager

		if err := manager.Connect(); err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}

		if err := manager.StartForwarding(ForwardingConfig{
			RemoteHost:   "localhost",
			RemotePort:   requestedPort,
			InternalHost: "localhost",
			InternalPort: 8080,
			EnforcePort:  true,
		}); err != nil {
			t.Fatalf("Expected successful forwarding, got: %v", err)
		}

		// Find the index of the first tcpip-forward for our port, and the
		// index of the first cancel-tcpip-forward for the same port. The
		// cancel must precede the tcpip-forward.
		callsMu.Lock()
		defer callsMu.Unlock()

		firstCancelIdx, firstTcpipIdx := -1, -1
		for i, c := range calls {
			if c.port != requestedPort {
				continue
			}
			if firstCancelIdx == -1 && c.name == "cancel-tcpip-forward" {
				firstCancelIdx = i
			}
			if firstTcpipIdx == -1 && c.name == "tcpip-forward" {
				firstTcpipIdx = i
			}
		}
		if firstCancelIdx == -1 {
			t.Fatalf("expected a pre-emptive cancel-tcpip-forward for port %d, calls=%+v", requestedPort, calls)
		}
		if firstTcpipIdx == -1 {
			t.Fatalf("expected a tcpip-forward for port %d, calls=%+v", requestedPort, calls)
		}
		if firstCancelIdx >= firstTcpipIdx {
			t.Errorf("expected pre-emptive cancel to precede tcpip-forward: cancelIdx=%d tcpipIdx=%d calls=%+v",
				firstCancelIdx, firstTcpipIdx, calls)
		}
	})

	t.Run("EnforcePort=false sends no pre-emptive cancel", func(t *testing.T) {
		SetupTest(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		const requestedPort = 27202

		var (
			callsMu sync.Mutex
			calls   []forwardRequestRecord
		)

		sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
			client := &fakeClient{}
			client.sendRequestFunc = recordingSendRequest(&callsMu, &calls, nil)
			return client, nil
		}

		sshConfig := SSHConnectionConfig{
			PrivateKey:        GenerateTestPrivateKey(t),
			ServerAddress:     "example.com:22",
			Username:          "testuser",
			ConnectTimeout:    5 * time.Second,
			FwdReqTimeout:     2 * time.Second,
			KeepAliveInterval: 5 * time.Second,
			// No RemoteAddrFunc → no URI verification, sendForwarding
			// short-circuits after sendForwardingOnce.
		}

		manager, err := NewSSHTunnelManager(ctx, &sshConfig)
		if err != nil {
			t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
		}

		if err := manager.Connect(); err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}

		if err := manager.StartForwarding(ForwardingConfig{
			RemoteHost:   "localhost",
			RemotePort:   requestedPort,
			InternalHost: "localhost",
			InternalPort: 8080,
			EnforcePort:  false,
		}); err != nil {
			t.Fatalf("Expected successful forwarding, got: %v", err)
		}

		callsMu.Lock()
		defer callsMu.Unlock()

		for _, c := range calls {
			if c.name == "cancel-tcpip-forward" && c.port == requestedPort {
				t.Errorf("did not expect any pre-emptive cancel-tcpip-forward for port %d when EnforcePort=false, calls=%+v",
					requestedPort, calls)
				break
			}
		}
	})
}

// TestOrphanForwardedChannelTriggersCancel verifies that when the SSH server
// opens a forwarded-tcpip channel for an address:port we have no registered
// forwarding for (e.g. a stale reservation from a previous SSH session that
// the server is still announcing), the manager sends a cancel-tcpip-forward
// for that address:port to ask the server to release it.
func TestOrphanForwardedChannelTriggersCancel(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const orphanAddr = "localhost"
	const orphanPort uint32 = 27202

	orphanChan := &fakeNewSshChannel{
		channelType: "forwarded-tcpip",
		extraData: ssh.Marshal(forwardedTCPPayload{
			Addr:       orphanAddr,
			Port:       orphanPort,
			OriginAddr: orphanAddr,
			OriginPort: orphanPort,
		}),
	}

	cancelSeen := make(chan struct{})
	var cancelOnce sync.Once
	var (
		callsMu sync.Mutex
		calls   []forwardRequestRecord
	)

	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		client := &fakeClient{customNewChannel: orphanChan}
		client.sendRequestFunc = recordingSendRequest(&callsMu, &calls,
			func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
				if name == "cancel-tcpip-forward" {
					if a, p, ok := decodeChannelForwardMsg(payload); ok && a == orphanAddr && p == orphanPort {
						cancelOnce.Do(func() { close(cancelSeen) })
					}
				}
				return true, nil, nil
			})
		return client, nil
	}

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	select {
	case <-cancelSeen:
	case <-time.After(2 * time.Second):
		callsMu.Lock()
		defer callsMu.Unlock()
		t.Fatalf("expected cancel-tcpip-forward for orphan %s:%d within 2s, calls=%+v",
			orphanAddr, orphanPort, calls)
	}
}

// TestTcpURIPort covers the error branches of tcpURIPort (missing port,
// non-numeric port, non-tcp scheme) in addition to the happy path.
func TestTcpURIPort(t *testing.T) {
	cases := []struct {
		name     string
		uri      string
		wantPort int
		wantOK   bool
	}{
		{"valid tcp URI", "tcp://example.com:8080", 8080, true},
		{"valid tcp URI with IPv6", "tcp://[::1]:8080", 8080, true},
		{"missing port", "tcp://example.com", 0, false},
		{"non-numeric port", "tcp://example.com:notaport", 0, false},
		{"empty string", "", 0, false},
		{"http scheme (prefix not stripped, parse fails)", "http://example.com:8080", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPort, gotOK := tcpURIPort(tc.uri)
			if gotPort != tc.wantPort || gotOK != tc.wantOK {
				t.Errorf("tcpURIPort(%q) = (%d, %v), want (%d, %v)",
					tc.uri, gotPort, gotOK, tc.wantPort, tc.wantOK)
			}
		})
	}
}

// TestCancelForwardingLogsOnError ensures the helper executes the error-log
// branch when the underlying SendRequest fails (e.g. the server denies the
// cancel because no such forwarding exists). Failure is swallowed; no panic.
func TestCancelForwardingLogsOnError(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	denyErr := errors.New("simulated server error")
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		return &fakeClient{
			sendRequestFunc: func(name string, wantReply bool, payload []byte) (bool, []byte, error) {
				if name == "cancel-tcpip-forward" {
					return false, nil, denyErr
				}
				return true, nil, nil
			},
		}, nil
	}

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}
	if err := manager.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Direct invocation under the read lock (matches the helper's contract).
	manager.clientMu.RLock()
	defer manager.clientMu.RUnlock()
	// Must not panic; error is logged and swallowed.
	manager.cancelForwarding("localhost", 27202, "test-error-path")
}

// TestHandleVerificationTimeoutWildcardReturnsNil covers the warn-only branch
// of handleVerificationTimeout: when the requested host is a wildcard
// ("0.0.0.0") and EnforcePort is false, a timeout is non-fatal — the helper
// logs a warning and returns nil without canceling.
func TestHandleVerificationTimeoutWildcardReturnsNil(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	}
	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	fwd := &ForwardingConfig{RemoteHost: "0.0.0.0", RemotePort: 2222}
	if err := manager.handleVerificationTimeout(fwd, false); err != nil {
		t.Errorf("expected nil error for wildcard host with needsVerification=false, got %v", err)
	}
}

// TestProcessServerDataExtractorError verifies that when the extractor
// callback returns an error, processServerData logs and returns without
// notifying URI waiters. Pairs with the happy-path coverage exercised by the
// other tests.
func TestProcessServerDataExtractorError(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register a waiter; if extractor errored we must NOT receive on this
	// channel.
	const key = "localhost:1234"
	notif := make(chan []string, 1)

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
		RemoteAddrFunc: func(data string) ([]string, error) {
			return nil, errors.New("simulated extractor failure")
		},
	}
	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	manager.addrNotifMu.Lock()
	manager.addrNotifications[key] = notif
	manager.addrNotifMu.Unlock()

	manager.processServerData([]byte("garbage server output"), "stdout")

	select {
	case got := <-notif:
		t.Errorf("did not expect notification on extractor error, got %v", got)
	case <-time.After(20 * time.Millisecond):
		// Expected: no notification.
	}
}

// TestProcessServerDataEmptyURIs covers the early-return branch when the
// extractor succeeds but returns no URIs — waiters should not be notified.
func TestProcessServerDataEmptyURIs(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const key = "localhost:1234"
	notif := make(chan []string, 1)

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
		RemoteAddrFunc: func(data string) ([]string, error) {
			return nil, nil // No URIs extracted, no error.
		},
	}
	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	manager.addrNotifMu.Lock()
	manager.addrNotifications[key] = notif
	manager.addrNotifMu.Unlock()

	manager.processServerData([]byte("non-URI server output"), "stdout")

	select {
	case got := <-notif:
		t.Errorf("did not expect notification when no URIs were extracted, got %v", got)
	case <-time.After(20 * time.Millisecond):
		// Expected: no notification.
	}
}

// recordingNewChannel is an ssh.NewChannel that captures the reason and message
// passed to Reject, and exposes a rejectSignal channel that fires the first
// time Reject is invoked. Used to assert that handleChannels rejects channels
// it cannot process (unknown type, malformed payload, etc.).
type recordingNewChannel struct {
	channelType string
	extraData   []byte
	channel     ssh.Channel

	mu            sync.Mutex
	rejectReason  ssh.RejectionReason
	rejectMessage string
	rejectOnce    sync.Once
	rejectSignal  chan struct{}
}

func newRecordingNewChannel(channelType string, extraData []byte, channel ssh.Channel) *recordingNewChannel {
	return &recordingNewChannel{
		channelType:  channelType,
		extraData:    extraData,
		channel:      channel,
		rejectSignal: make(chan struct{}),
	}
}

func (r *recordingNewChannel) ChannelType() string { return r.channelType }
func (r *recordingNewChannel) ExtraData() []byte   { return r.extraData }
func (r *recordingNewChannel) Accept() (ssh.Channel, <-chan *ssh.Request, error) {
	requests := make(chan *ssh.Request)
	close(requests)
	return r.channel, requests, nil
}
func (r *recordingNewChannel) Reject(reason ssh.RejectionReason, message string) error {
	r.mu.Lock()
	r.rejectReason = reason
	r.rejectMessage = message
	r.mu.Unlock()
	r.rejectOnce.Do(func() { close(r.rejectSignal) })
	return nil
}

// TestConnectIdempotent covers the already-connected early return in Connect():
// calling Connect() on a connected manager returns nil without re-dialing.
func TestConnectIdempotent(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dialCalls := 0
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		dialCalls++
		return &fakeClient{}, nil
	}

	manager, err := NewSSHTunnelManager(ctx, &SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSSHTunnelManager: %v", err)
	}
	if err := manager.Connect(); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	if err := manager.Connect(); err != nil {
		t.Errorf("second Connect on a connected manager should be a no-op, got %v", err)
	}
	if dialCalls != 1 {
		t.Errorf("expected exactly 1 dial, got %d", dialCalls)
	}
}

// TestStartForwardingInvalidPort covers the port-range validation in
// sendForwardingOnce: ports outside [0, 65535] must be rejected with an
// "invalid remote port" error before any SSH request is sent.
func TestStartForwardingInvalidPort(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager, err := NewSSHTunnelManager(ctx, &SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSSHTunnelManager: %v", err)
	}
	if err := manager.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	for _, port := range []int{-1, 65536, 70000} {
		err := manager.StartForwarding(ForwardingConfig{
			RemoteHost:   "0.0.0.0",
			RemotePort:   port,
			InternalHost: "localhost",
			InternalPort: 8080,
		})
		if err == nil || !strings.Contains(err.Error(), "invalid remote port") {
			t.Errorf("port=%d: expected error containing 'invalid remote port', got %v", port, err)
		}
	}
}

// TestHandleChannelsRejectsUnknownChannelType covers the default-branch in
// handleChannels: a channel whose type is not "forwarded-tcpip" must be
// rejected with UnknownChannelType.
func TestHandleChannelsRejectsUnknownChannelType(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := newRecordingNewChannel("session", nil, nil)
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		return &fakeClient{customNewChannel: rec}, nil
	}

	manager, err := NewSSHTunnelManager(ctx, &SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSSHTunnelManager: %v", err)
	}
	if err := manager.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case <-rec.rejectSignal:
	case <-time.After(2 * time.Second):
		t.Fatalf("Reject not called within 2s for unknown channel type")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.rejectReason != ssh.UnknownChannelType {
		t.Errorf("expected UnknownChannelType, got %v", rec.rejectReason)
	}
}

// TestHandleChannelsRejectsMalformedForwardedTCPPayload covers the
// payload-unmarshal failure branch in handleChannels: a "forwarded-tcpip"
// channel whose ExtraData cannot be decoded must be rejected with
// ConnectionFailed.
func TestHandleChannelsRejectsMalformedForwardedTCPPayload(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ssh.Unmarshal expects a length-prefixed string + two uint32s; a couple
	// of stray bytes cannot decode into forwardedTCPPayload.
	rec := newRecordingNewChannel("forwarded-tcpip", []byte{0x01, 0x02}, nil)
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		return &fakeClient{customNewChannel: rec}, nil
	}

	manager, err := NewSSHTunnelManager(ctx, &SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSSHTunnelManager: %v", err)
	}
	if err := manager.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case <-rec.rejectSignal:
	case <-time.After(2 * time.Second):
		t.Fatalf("Reject not called within 2s for malformed payload")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.rejectReason != ssh.ConnectionFailed {
		t.Errorf("expected ConnectionFailed, got %v", rec.rejectReason)
	}
	if !strings.Contains(rec.rejectMessage, "could not parse forwarded-tcpip payload") {
		t.Errorf("expected rejection message to mention payload parse failure, got %q", rec.rejectMessage)
	}
}

// TestHandleChannelsClosesChannelOnLocalDialError covers the dial-failure
// branch inside the accept goroutine: when the backend dial fails, the
// accepted SSH channel must be closed (via the deferred remoteConn.Close()),
// not left dangling.
func TestHandleChannelsClosesChannelOnLocalDialError(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	remoteConn := newBlockingSshChannel()
	newChan := &fakeNewSshChannelWithAccept{
		channelType: "forwarded-tcpip",
		extraData: ssh.Marshal(forwardedTCPPayload{
			Addr: "0.0.0.0",
			Port: 2222,
		}),
		channel: remoteConn,
	}

	sshConfig := SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
		SSHDialFunc: func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
			return &fakeClient{customNewChannel: newChan}, nil
		},
		NetDialFunc: func(network, address string) (net.Conn, error) {
			return nil, errors.New("simulated local-dial failure")
		},
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("NewSSHTunnelManager: %v", err)
	}
	if err := manager.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Register the matching forwarding so handleChannels enters the accept
	// branch (rather than the orphan/cancel branch).
	if err := manager.StartForwarding(ForwardingConfig{
		RemoteHost:   "0.0.0.0",
		RemotePort:   2222,
		InternalHost: "localhost",
		InternalPort: 8080,
	}); err != nil {
		t.Fatalf("StartForwarding: %v", err)
	}

	select {
	case <-remoteConn.closed:
	case <-time.After(2 * time.Second):
		t.Fatalf("remote SSH channel was not closed within 2s after local-dial failure (Close: %d)",
			remoteConn.closeCalls.Load())
	}
}

// TestNotifyURIWaitersSkipsFullChannels covers the `default` branch in
// notifyURIWaiters: when a registered notification channel is already full,
// the send must fall through to default instead of blocking.
func TestNotifyURIWaitersSkipsFullChannels(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager, err := NewSSHTunnelManager(ctx, &SSHConnectionConfig{
		PrivateKey:        GenerateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		ConnectTimeout:    5 * time.Second,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSSHTunnelManager: %v", err)
	}

	// Register a capacity-1 notification channel and pre-fill it so any send
	// would block. notifyURIWaiters must skip it via the default branch.
	full := make(chan []string, 1)
	full <- []string{"http://placeholder"}

	manager.addrNotifMu.Lock()
	manager.addrNotifications["test-key"] = full
	manager.addrNotifMu.Unlock()

	done := make(chan struct{})
	go func() {
		manager.notifyURIWaiters([]string{"http://new"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("notifyURIWaiters blocked instead of skipping a full channel")
	}

	// The original value must still be in the channel (no overwrite).
	got := <-full
	if len(got) != 1 || got[0] != "http://placeholder" {
		t.Errorf("expected channel to still hold the placeholder, got %v", got)
	}
}
