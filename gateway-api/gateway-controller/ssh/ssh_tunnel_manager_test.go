package ssh

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

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

	manager.StartForwarding(fwd)

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
	manager.StartForwarding(fwd)

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
			name:          "TCP URI with wrong port",
			uris:          []string{"tcp://example.com:9090"},
			requestedHost: "example.com",
			requestedPort: 8080,
			expected:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchesRequestedHost(tt.uris, tt.requestedHost, tt.requestedPort)
			if result != tt.expected {
				t.Errorf("matchesRequestedHost(%v, %q, %d) = %v, want %v",
					tt.uris, tt.requestedHost, tt.requestedPort, result, tt.expected)
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
