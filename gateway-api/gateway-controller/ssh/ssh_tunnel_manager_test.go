package ssh

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// SetupTest sets up the test environment.
func SetupTest(t *testing.T) {
	// Set slog level to Error and above
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError,
	})))

	// Mock sshDial and netDial for testing
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) { return &fakeClient{}, nil }
	netDial = func(network string, address string) (net.Conn, error) { return &fakeNetConn{}, nil }
}

// generateTestPrivateKey generates a test private key.
func generateTestPrivateKey(t *testing.T) []byte {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate test private key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(privKey)
	pemBlock := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	return pem.EncodeToMemory(pemBlock)
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
		KeepAliveInterval: 5 * time.Second,
		BackoffInterval:   2 * time.Second,
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
		PrivateKey:        generateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		KeepAliveInterval: 5 * time.Second,
		BackoffInterval:   2 * time.Second,
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
		PrivateKey:        generateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		KeepAliveInterval: 5 * time.Second,
		BackoffInterval:   2 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}
	manager.WaitConnection()

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
		PrivateKey:        generateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		KeepAliveInterval: 80 * time.Millisecond, // Shorter keep alive for testing
		BackoffInterval:   2 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}
	manager.WaitConnection()

	// Give time to send some keep alives.
	time.Sleep(50 * time.Millisecond)

}

// TestNewSSHTunnelManager tests creating an SSH tunnel manager with failing Dial.
func TestNewSSHTunnelManagerFailingDialKeepsTrying(t *testing.T) {
	SetupTest(t)
	sshDialCalledTimes := 0
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		sshDialCalledTimes += 1
		return nil, fmt.Errorf("oh noes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        generateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		KeepAliveInterval: 5 * time.Second,
		BackoffInterval:   10 * time.Millisecond, // Quicker re-Dial
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	if manager == nil {
		t.Fatal("Expected non-nil manager")
	}

	// Give time to do at least 1 retry.
	time.Sleep(100 * time.Millisecond)

	if sshDialCalledTimes < 2 {
		t.Fatal("Expected retreying connection")
	}
}

// TestDuplicateForwarding tests duplicate forwarding.
func TestDuplicateForwarding(t *testing.T) {
	SetupTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sshConfig := SSHConnectionConfig{
		PrivateKey:        generateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		KeepAliveInterval: 5 * time.Second,
		BackoffInterval:   2 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	manager.WaitConnection()

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
		PrivateKey:        generateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		KeepAliveInterval: 5 * time.Second,
		BackoffInterval:   2 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	manager.WaitConnection()

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
		PrivateKey:        generateTestPrivateKey(t),
		ServerAddress:     "example.com:22",
		Username:          "testuser",
		HostKey:           "",
		ConnectTimeout:    5 * time.Second,
		KeepAliveInterval: 5 * time.Second,
		BackoffInterval:   2 * time.Second,
	}

	manager, err := NewSSHTunnelManager(ctx, &sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	manager.WaitConnection()

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
