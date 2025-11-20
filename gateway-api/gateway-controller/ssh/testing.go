package ssh

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

// SetupTest sets up the test environment.
// It configures logging to discard output and mocks SSH/network dial functions.
func SetupTest(t *testing.T) {
	t.Helper()

	// Set slog level to Error and above, discard output
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError,
	})))

	// Mock sshDial and netDial for testing
	sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		return &fakeClient{}, nil
	}
	netDial = func(network string, address string) (net.Conn, error) {
		return &fakeNetConn{}, nil
	}
}

// GenerateTestPrivateKey generates a test RSA private key.
func GenerateTestPrivateKey(t *testing.T) []byte {
	t.Helper()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate test private key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(privKey)
	pemBlock := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	return pem.EncodeToMemory(pemBlock)
}
