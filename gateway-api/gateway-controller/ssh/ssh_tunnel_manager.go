package ssh

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshClient defines the methods used by the SSH tunnel manager.
type sshClient interface {
	Listen(network, addr string) (net.Listener, error)
	SendRequest(string, bool, []byte) (bool, []byte, error)
	Close() error
}

// sshDial is used to establish SSH connections; override in tests.
var sshDial = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
	return ssh.Dial(network, addr, cfg)
}

// netDial is used to establish internal TCP connections; override in tests.
var netDial = func(network string, address string) (net.Conn, error) {
	return net.Dial(network, address)
}

// SSHTunnelManager manages SSH tunnels and forwardings.
type SSHTunnelManager struct {
	sshServerAddress  string
	sshUser           string
	hostKey           string
	signer            ssh.Signer
	timeout           time.Duration
	keepAliveInterval time.Duration
	backoffInterval   time.Duration

	ctx           context.Context
	cancel        context.CancelFunc
	connected     bool
	clientMu      sync.RWMutex
	client        sshClient
	forwardingsMu sync.Mutex
	forwardings   map[string]*forwardingSession
}

// SSHConnectionConfig contains configuration for SSH connection.
type SSHConnectionConfig struct {
	PrivateKey        []byte
	ServerAddress     string
	Username          string
	HostKey           string
	ConnectTimeout    time.Duration
	KeepAliveInterval time.Duration
	BackoffInterval   time.Duration
}

// NewSSHTunnelManager creates a new SSHTunnelManager with the specified configuration.
func NewSSHTunnelManager(ctx context.Context, config SSHConnectionConfig) (*SSHTunnelManager, error) {
	signer, err := ssh.ParsePrivateKey(config.PrivateKey)
	if err != nil {
		slog.With("function", "NewSSHTunnelManager").Error("unable to parse private key", "error", err)
		return nil, fmt.Errorf("unable to parse private key: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)

	m := &SSHTunnelManager{
		sshServerAddress:  config.ServerAddress,
		sshUser:           config.Username,
		hostKey:           config.HostKey,
		signer:            signer,
		timeout:           config.ConnectTimeout,
		keepAliveInterval: config.KeepAliveInterval,
		backoffInterval:   config.BackoffInterval,
		ctx:               ctx,
		cancel:            cancel,
		connected:         false,
		forwardings:       make(map[string]*forwardingSession),
	}
	go m.connectionManager()

	return m, nil
}

// WaitConnection blocks until the ssh connection is established
func (m *SSHTunnelManager) WaitConnection() {
	for {
		select {
		case <-m.ctx.Done():
			m.closeClient()
			return
		default:
		}
		if m.connected {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// connectionManager manages the SSH connection lifecycle and reconnections.
func (m *SSHTunnelManager) connectionManager() {
	for {
		select {
		case <-m.ctx.Done():
			m.closeClient()
			return
		default:
		}

		client, err := m.connect()
		if err != nil {
			slog.With("function", "connectionManager").Error("ssh connection failed", "error", err)
			time.Sleep(m.backoffInterval)
			continue
		}

		slog.With("function", "connectionManager").Info("ssh connection established")

		m.clientMu.Lock()
		m.client = client
		m.connected = true
		m.clientMu.Unlock()

		m.forwardingsMu.Lock()
		// Restart existing forwardings, in case of reconnection
		for _, session := range m.forwardings {
			go m.handleForwarding(session)
		}
		m.forwardingsMu.Unlock()

		// Monitor connection (keepalive and disconnect detection)
		m.monitorConnection(client)

		m.clientMu.Lock()
		m.closeClient()
		m.connected = false
		m.clientMu.Unlock()

		time.Sleep(m.backoffInterval)
	}
}

// monitorConnection monitors the SSH connection and sends keepalive requests.
func (m *SSHTunnelManager) monitorConnection(client sshClient) {
	ticker := time.NewTicker(m.keepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				slog.With("function", "monitorConnection").Warn("ssh keepalive failed, reconnecting", "error", err)
				return
			}
		}
	}
}

// closeClient closes the current SSH client connection.
func (m *SSHTunnelManager) closeClient() {
	m.clientMu.Lock()
	defer m.clientMu.Unlock()
	if m.client != nil {
		m.client.Close()
		m.client = nil
	}
}

// ForwardingConfig defines configuration for a single forwarding.
type ForwardingConfig struct {
	RemoteHost   string
	RemotePort   int
	InternalHost string
	InternalPort int
}

type forwardingSession struct {
	remoteHost   string
	remotePort   int
	internalHost string
	internalPort int

	ctx    context.Context
	cancel context.CancelFunc
}

// StartForwarding starts a new forwarding based on the provided configuration.
func (m *SSHTunnelManager) StartForwarding(fwd *ForwardingConfig) error {
	if !m.connected {
		slog.With("function", "StartForwarding").Warn("client not ready")
		return &ErrSSHClientNotReady{}
	}
	key := forwardingKey(fwd)
	m.forwardingsMu.Lock()
	defer m.forwardingsMu.Unlock()

	if _, exists := m.forwardings[key]; exists {
		err := &ErrSSHForwardingExists{Key: key}
		slog.With("function", "StartForwarding").Warn(err.Error())
		return err
	}

	ctx, cancel := context.WithCancel(m.ctx)
	session := &forwardingSession{
		remoteHost:   fwd.RemoteHost,
		remotePort:   fwd.RemotePort,
		internalHost: fwd.InternalHost,
		internalPort: fwd.InternalPort,

		ctx:    ctx,
		cancel: cancel,
	}
	m.forwardings[key] = session

	go m.handleForwarding(session)

	return nil
}

// StopForwarding stops an existing forwarding based on the provided configuration.
func (m *SSHTunnelManager) StopForwarding(fwd *ForwardingConfig) (err error) {
	if !m.connected {
		return &ErrSSHClientNotReady{}
	}
	key := forwardingKey(fwd)
	m.forwardingsMu.Lock()
	defer m.forwardingsMu.Unlock()

	session, exists := m.forwardings[key]
	if !exists {
		err = &ErrSSHForwardingNotFound{Key: key}
		slog.With("function", "StopForwarding").Warn(err.Error())
		return
	}

	session.cancel()
	delete(m.forwardings, key)
	slog.With("function", "StopForwarding").Info("stopped forwarding", "key", key)

	return nil
}

func forwardingKey(fwd *ForwardingConfig) string {
	return fmt.Sprintf("%s:%d->%s:%d", fwd.RemoteHost, fwd.RemotePort, fwd.InternalHost, fwd.InternalPort)
}

// handleForwarding manages the lifecycle of a forwarding session.
func (m *SSHTunnelManager) handleForwarding(fwdSession *forwardingSession) {
	addr := fmt.Sprintf("%s:%d", fwdSession.remoteHost, fwdSession.remotePort)

	for {
		select {
		case <-fwdSession.ctx.Done():
			return
		default:
		}

		m.clientMu.RLock()
		client := m.client
		m.clientMu.RUnlock()

		if client == nil {
			time.Sleep(m.backoffInterval)
			continue
		}

		listener, err := client.Listen("tcp", addr)
		if err != nil {
			slog.With("function", "handleForwarding").Warn("listener setup failed, retrying", "address", addr, "error", err)
			time.Sleep(m.backoffInterval)
			continue
		}

		slog.With("function", "handleForwarding").Info("forwarding started", "remote", addr, "internal", fmt.Sprintf("%s:%d", fwdSession.internalHost, fwdSession.internalPort))

		m.handleForwardingTraffic(fwdSession.ctx, listener, fwdSession.internalHost, fwdSession.internalPort)

		listener.Close()
		time.Sleep(m.backoffInterval)
	}
}

// handleForwardingTraffic forwards traffic between the remote listener and internal host.
func (m *SSHTunnelManager) handleForwardingTraffic(ctx context.Context, listener net.Listener, internalHost string, internalPort int) {
	// Context-aware shutdown, to avoid waiting forever on Accept()
	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	// keep waiting for "forwarding requests" while checking context cancellation
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		remoteConn, err := listener.Accept()
		if err != nil {
			slog.With("function", "handleForwardingTraffic").Warn("listener accept error", "error", err)
			return
		}

		internalConn, err := netDial("tcp", net.JoinHostPort(internalHost, fmt.Sprintf("%d", internalPort)))
		if err != nil {
			slog.With("function", "handleForwardingTraffic").Warn("internal connection failed", "error", err)
			remoteConn.Close()
			return
		}
		go proxyConnection(ctx, remoteConn, internalConn)
	}
}

// connect establishes a new SSH client connection.
func (m *SSHTunnelManager) connect() (sshClient, error) {
	config := &ssh.ClientConfig{
		User:    m.sshUser,
		Auth:    []ssh.AuthMethod{ssh.PublicKeys(m.signer)},
		Timeout: m.timeout,
	}

	if m.hostKey != "" {
		config.HostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			actual := sha256.Sum256(key.Marshal())
			actualFingerprint := "SHA256:" + base64.StdEncoding.EncodeToString(actual[:])

			if actualFingerprint != m.hostKey {
				slog.With("function", "connect").Error("host key verification failed", "expected", m.hostKey, "got", actualFingerprint)
				return fmt.Errorf("host key verification failed: expected %s, got %s", m.hostKey, actualFingerprint)
			}
			return nil
		}
	} else {
		slog.With("function", "connect").Warn("no host key provided, falling back to InsecureIgnoreHostKey")
		config.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	client, err := sshDial("tcp", m.sshServerAddress, config)
	if err != nil {
		rerr := &ErrSSHConnectionFailed{Err: err}
		slog.With("function", "connect").Error(rerr.Error())
		return nil, rerr
	}

	return client, nil
}

// Stop cleanly stops all forwardings and closes the SSH client connection.
func (m *SSHTunnelManager) Stop() {
	m.forwardingsMu.Lock()
	defer m.forwardingsMu.Unlock()
	for _, session := range m.forwardings {
		session.cancel()
	}
	m.forwardings = make(map[string]*forwardingSession) // Clear forwardings map

	m.cancel()      // Cancel global context
	m.closeClient() // Close SSH client immediately

	slog.Info("ssh tunnel manager stopped, all forwardings and connections closed")
}

// proxyConnection proxies traffic between two io.ReadWriteClosers.
func proxyConnection(ctx context.Context, a io.ReadWriteCloser, b io.ReadWriteCloser) {
	// Automatically close both endpoints if the context is canceled
	go func() {
		<-ctx.Done()
		a.Close()
		b.Close()
	}()

	logger := slog.With("function", "proxyConnection")
	logger.Info("starting proxy connection")

	defer func() {
		a.Close()
		b.Close()
		logger.Info("closed connections")
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// Channel to signal when one connection drops
	errChan := make(chan struct{}, 1)

	// internal -> remote
	go func() {
		defer wg.Done()
		n, err := io.Copy(a, b)
		if err != nil {
			logger.Error("copy internal->remote failed", "bytes", n, "error", err)
		} else {
			logger.Info("copy internal->remote complete", "bytes", n)
		}
		// Signal that one connection has dropped
		select {
		case errChan <- struct{}{}:
		default:
		}
	}()

	// remote -> internal
	go func() {
		defer wg.Done()
		n, err := io.Copy(b, a)
		if err != nil {
			logger.Error("copy remote->internal failed", "bytes", n, "error", err)
		} else {
			logger.Info("copy remote->internal complete", "bytes", n)
		}
		// Signal that one connection has dropped
		select {
		case errChan <- struct{}{}:
		default:
		}
	}()

	// Wait for one connection to drop or context cancellation
	select {
	case <-ctx.Done():
		logger.Info("context canceled, closing connections")
	case <-errChan:
		logger.Warn("one connection dropped, closing the other")
	}

	// Wait for both goroutines to finish
	wg.Wait()
	logger.Info("finished proxy connection")
}
