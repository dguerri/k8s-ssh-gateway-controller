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
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeAddr represents a fake network address.
type fakeAddr struct{}

// Network returns the network type.
func (a *fakeAddr) Network() string { return "tcp" }

// String returns the address as a string.
func (a *fakeAddr) String() string { return "" }

// fakeNetConn represents a fake network connection.
type fakeNetConn struct{}

// Close closes the connection.
func (f *fakeNetConn) Close() error { return nil }

// Read reads data from the connection.
func (f *fakeNetConn) Read([]byte) (int, error) { return 0, nil }

// Write writes data to the connection.
func (f *fakeNetConn) Write([]byte) (int, error) { return 0, nil }

// LocalAddr returns the local address.
func (f *fakeNetConn) LocalAddr() net.Addr { return &fakeAddr{} }

// RemoteAddr returns the remote address.
func (f *fakeNetConn) RemoteAddr() net.Addr { return &fakeAddr{} }

// SetDeadline sets the deadline for the connection.
func (f *fakeNetConn) SetDeadline(time.Time) error { return nil }

// SetReadDeadline sets the read deadline for the connection.
func (f *fakeNetConn) SetReadDeadline(time.Time) error { return nil }

// SetWriteDeadline sets the write deadline for the connection.
func (f *fakeNetConn) SetWriteDeadline(time.Time) error { return nil }

// fakeClient represents a fake SSH client.
type fakeClient struct{}

// Listen listens for incoming connections.
func (f *fakeClient) Listen(network, addr string) (net.Listener, error) { return &fakeListener{}, nil }

// SendRequest sends a request to the server.
func (f *fakeClient) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	return true, nil, nil
}

// Close closes the client.
func (f *fakeClient) Close() error { return nil }

// fakeListener represents a fake listener.
type fakeListener struct {
	acceptOnce sync.Once
	firstConn  net.Conn
}

// Accept accepts an incoming connection.
func (l *fakeListener) Accept() (net.Conn, error) {
	l.firstConn = nil
	l.acceptOnce.Do(func() {
		clientConn, serverConn := net.Pipe()
		l.firstConn = clientConn
		go func() {
			defer clientConn.Close()
			defer serverConn.Close()
			io.Copy(serverConn, clientConn)
		}()
	})
	if l.firstConn != nil {
		return l.firstConn, nil
	}
	// Simulate blocking forever on subsequent calls.
	select {}
}

// Close closes the listener.
func (l *fakeListener) Close() error { return nil }

// Addr returns the listener's address.
func (l *fakeListener) Addr() net.Addr { return &fakeAddr{} }

// SetupTest sets up the test environment.
func SetupTest(t *testing.T) {
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

	manager, err := NewSSHTunnelManager(ctx, sshConfig)
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

	manager, err := NewSSHTunnelManager(ctx, sshConfig)
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

	manager, err := NewSSHTunnelManager(ctx, sshConfig)
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

	if err := manager.StopForwarding(fwd); err != nil {
		t.Errorf("Unexpected error on StopForwarding")
	}

	if err := manager.StopForwarding(fwd); err == nil {
		t.Errorf("Should fail because of non-existing forewarding")
	} else {
		var notFoundErr *ErrSSHForwardingNotFound
		if !errors.As(err, &notFoundErr) {
			t.Errorf("Expected SSHForwardingNotFoundError, got %T", err)
		}
	}

}

// TestNewSSHTunnelManager tests creating an SSH tunnel manager with failing Dial.
func TestNewSSHTunnelManagerFailingDialKeepsTrying(t *testing.T) {
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

	manager, err := NewSSHTunnelManager(ctx, sshConfig)
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

	manager, err := NewSSHTunnelManager(ctx, sshConfig)
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

	manager, err := NewSSHTunnelManager(ctx, sshConfig)
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

	manager, err := NewSSHTunnelManager(ctx, sshConfig)
	if err != nil {
		t.Fatalf("Failed to create SSH Tunnel Manager: %v", err)
	}

	manager.WaitConnection()

	fwd := &ForwardingConfig{
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
