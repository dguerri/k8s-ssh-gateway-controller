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
	HandleChannelOpen(string) <-chan ssh.NewChannel
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

// ForwardingConfig defines configuration for a single forwarding.
type ForwardingConfig struct {
	RemoteHost   string
	RemotePort   int
	InternalHost string
	InternalPort int
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
	externalCtx       context.Context
	connectionCtx     context.Context
	connectionCancel  context.CancelFunc
	connected         bool
	clientMu          sync.RWMutex
	client            sshClient
	forwardingsMu     sync.Mutex
	forwardings       map[string]*ForwardingConfig
}

// forwardingKey generates a unique key for the forwarding session based on remote host and port.
func forwardingKey(remoteHost string, remotePort int) string {
	return fmt.Sprintf("%s:%d", remoteHost, remotePort)
}

// NewSSHTunnelManager creates a new SSHTunnelManager with the specified configuration.
// externalCtx is used to cancel the connection and forwardings when the context is done.
func NewSSHTunnelManager(externalCtx context.Context, config *SSHConnectionConfig) (*SSHTunnelManager, error) {
	signer, err := ssh.ParsePrivateKey(config.PrivateKey)
	if err != nil {
		slog.With("function", "NewSSHTunnelManager").Error("unable to parse private key", "error", err)
		return nil, fmt.Errorf("unable to parse private key: %w", err)
	}

	m := &SSHTunnelManager{
		sshServerAddress:  config.ServerAddress,
		sshUser:           config.Username,
		hostKey:           config.HostKey,
		signer:            signer,
		timeout:           config.ConnectTimeout,
		keepAliveInterval: config.KeepAliveInterval,
		backoffInterval:   config.BackoffInterval,
		externalCtx:       externalCtx,
		connected:         false,
		forwardings:       make(map[string]*ForwardingConfig),
	}
	go m.handleConnection()

	return m, nil
}

// WaitConnection blocks until the ssh connection is established
func (m *SSHTunnelManager) WaitConnection() error {
	for {
		select {
		case <-m.externalCtx.Done():
			m.clientMu.Lock()
			defer m.clientMu.Unlock()
			m.closeClient()
			return fmt.Errorf("ssh connection cancelled: %w", m.externalCtx.Err())
		default:
		}
		if m.connected {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}

// handleConnection manages the SSH connection lifecycle and reconnections. Blocks until externalCtx is done.
func (m *SSHTunnelManager) handleConnection() {
	for {
		m.clientMu.Lock()
		err := m.connectClient()
		m.clientMu.Unlock()
		if err != nil {
			slog.With("function", "handleConnection").Error("ssh connection failed", "error", err)
			time.Sleep(m.backoffInterval)
			continue
		}
		slog.With("function", "handleConnection").Debug("ssh connection established")

		select {
		case <-m.externalCtx.Done():
			m.clientMu.Lock()
			defer m.clientMu.Unlock()
			m.closeClient()
			return
		default:
		}

		m.forwardingsMu.Lock()
		// Restart existing forwardings, in case of reconnection
		for key, forwardingSession := range m.forwardings {
			slog.With("function", "handleConnection").Debug("restarting forwarding", "key", key)
			err := m.sendForwardingRequest(forwardingSession)
			if err != nil {
				slog.With("function", "handleConnection").Error("failed to send forwarding request, removing forwarding", "error", err, "key", key)
				delete(m.forwardings, key)
				continue
			}
		}
		m.forwardingsMu.Unlock()

		// Handle incoming channels
		go m.handleChannels()

		// Monitor connection (keepalive and disconnect detection)
		m.monitorConnection()

		m.clientMu.Lock()
		m.closeClient()
		m.clientMu.Unlock()

		time.Sleep(m.backoffInterval)
	}
}

// monitorConnection monitors the SSH connection and sends keepalive requests.
func (m *SSHTunnelManager) monitorConnection() {
	ticker := time.NewTicker(m.keepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.externalCtx.Done():
			return
		case <-ticker.C:
			if m.connectionCtx.Err() != nil {
				slog.With("function", "monitorConnection").Debug("ssh connection context cancelled")
				return
			}
			if _, _, err := m.client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				slog.With("function", "monitorConnection").Error("ssh keepalive failed, reconnecting", "error", err)
				return
			}
			slog.With("function", "monitorConnection").Debug("ssh keepalive sent")
		}
	}
}

// closeClient closes the current SSH client connection.
func (m *SSHTunnelManager) closeClient() {
	m.connected = false

	if m.connectionCancel != nil {
		m.connectionCancel()
	}
	if m.client != nil {
		m.client.Close()
	}
	m.client = nil
	m.connectionCancel = nil
}

type channelForwardMsg struct {
	addr  string
	rport uint32
}

type forwardedTCPPayload struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

// StartForwarding starts a new forwarding based on the provided configuration.
func (m *SSHTunnelManager) StartForwarding(fwd ForwardingConfig) error {
	m.forwardingsMu.Lock()
	defer m.forwardingsMu.Unlock()

	if !m.connected {
		slog.With("function", "StartForwarding").Warn("client not ready")
		return &ErrSSHClientNotReady{}
	}
	key := forwardingKey(fwd.RemoteHost, fwd.RemotePort)

	if _, exists := m.forwardings[key]; exists {
		err := &ErrSSHForwardingExists{Key: key}
		slog.With("function", "StartForwarding").Error(err.Error())
		return err
	}

	err := m.sendForwardingRequest(&fwd)
	if err != nil {
		slog.With("function", "StartForwarding").Error("failed to send forwarding request", "error", err)
		return err
	}

	// Store the forwarding session
	m.forwardings[key] = &fwd

	slog.With("function", "StartForwarding").Info("started forwarding", "key", key)

	return nil
}

// sendForwardingRequest sends a request to the SSH server to set up a TCP forwarding.
// It returns an error if the request fails or is denied by the server.
func (m *SSHTunnelManager) sendForwardingRequest(fwd *ForwardingConfig) error {
	forwardMessage := channelForwardMsg{
		addr:  fwd.RemoteHost,
		rport: uint32(fwd.RemotePort),
	}

	ok, _, err := m.client.SendRequest("tcpip-forward", true, ssh.Marshal(&forwardMessage))
	if err != nil {
		slog.With("function", "sendForwardingRequest").Error("tcpip-forward request failed", "error", err)
		return err
	}
	if !ok {
		slog.With("function", "sendForwardingRequest").Error("tcpip-forward request denied by server")
		return fmt.Errorf("ssh: tcpip-forward request denied by server")
	}
	slog.With("function", "sendForwardingRequest").Debug("tcpip-forward request accepted", "remote_host", fwd.RemoteHost, "remote_port", fwd.RemotePort)
	return nil
}

// StopForwarding stops an existing forwarding based on the provided configuration.
func (m *SSHTunnelManager) StopForwarding(fwd *ForwardingConfig) error {
	m.forwardingsMu.Lock()
	defer m.forwardingsMu.Unlock()

	if !m.connected {
		return &ErrSSHClientNotReady{}
	}
	key := forwardingKey(fwd.RemoteHost, fwd.RemotePort)
	forwardingSession, exists := m.forwardings[key]
	if !exists {
		err := &ErrSSHForwardingNotFound{Key: key}
		slog.With("function", "StopForwarding").Warn(err.Error())
		return err
	}

	// Cancel forwarding may still fail, but we should remove the forwardingSession anyway.
	delete(m.forwardings, key)

	err := m.sendForwardingCancel(forwardingSession)
	if err != nil {
		slog.With("function", "StopForwarding").Error("failed to send cancel request", "error", err)
		return err
	}

	slog.With("function", "StopForwarding").Info("stopped forwarding", "key", key)

	return nil
}

func (m *SSHTunnelManager) sendForwardingCancel(fwd *ForwardingConfig) error {
	forwardMessage := channelForwardMsg{
		addr:  fwd.RemoteHost,
		rport: uint32(fwd.RemotePort),
	}

	ok, _, err := m.client.SendRequest("cancel-tcpip-forward", true, ssh.Marshal(&forwardMessage))
	if err != nil {
		slog.With("function", "StopForwarding").Error("cancel request failed", "error", err)
		return err
	}
	if !ok {
		slog.With("function", "StopForwarding").Error("request to cancel rejected by peer")
		return fmt.Errorf("ssh: cancel-tcpip-forward request denied by peer")
	}

	return nil
}

// handleForwarding manages the lifecycle of a forwarding sessions
func (m *SSHTunnelManager) handleChannels() {
	tcpipChan := m.client.HandleChannelOpen("forwarded-tcpip")
	for {
		slog.Debug("waiting for new channels")
		select {
		case <-m.connectionCtx.Done():
			slog.With("function", "handleChannels").Debug("connection context cancelled, stopping channel handling")
			return
		case ch := <-tcpipChan:
			if ch == nil {
				slog.With("function", "handleChannels").Debug("received nil channel, stopping channel handling")
				return
			}

			logger := slog.With(
				slog.String("channel_type", ch.ChannelType()),
				slog.String("extra_data", string(ch.ExtraData())),
			)
			switch channelType := ch.ChannelType(); channelType {
			case "forwarded-tcpip":
				var payload forwardedTCPPayload
				if err := ssh.Unmarshal(ch.ExtraData(), &payload); err != nil {
					logger.Error("Unable to parse forwarded-tcpip payload", slog.Any("error", err))
					ch.Reject(ssh.ConnectionFailed, "could not parse forwarded-tcpip payload: "+err.Error())
					continue
				}

				logger.Debug("forwarded-tcpip channel opened", "remote_addr", payload.Addr, "remote_port", payload.Port, "origin_addr", payload.OriginAddr, "origin_port", payload.OriginPort)

				key := forwardingKey(payload.Addr, int(payload.Port))
				m.forwardingsMu.Lock()
				fwd, exists := m.forwardings[key]
				m.forwardingsMu.Unlock()
				if exists {
					go func(ch ssh.NewChannel) {
						remoteConn, reqs, acceptErr := ch.Accept()
						if acceptErr != nil {
							logger.Error("failed to accept channel", "error", acceptErr)
							return
						}
						logger.Debug("channel accepted")

						// Avoid resource leak by discarding requests
						go ssh.DiscardRequests(reqs)

						go func() {
							defer remoteConn.Close()

							localConn, localErr := netDial("tcp", fwd.InternalHost+":"+fmt.Sprint(fwd.InternalPort))
							if localErr != nil {
								logger.Error("failed to connect to local address", "error", localErr)
								return
							}

							defer localConn.Close()

							wg := &sync.WaitGroup{}
							wg.Add(2)

							go func() {
								defer wg.Done()
								n, err := io.Copy(remoteConn, localConn)
								slog.With("function", "HandleChannels").Debug("copied data from local to remote", "bytes", n, "error", err)
								remoteConn.CloseWrite()
							}()

							go func() {
								defer wg.Done()
								n, err := io.Copy(localConn, remoteConn)
								slog.With("function", "HandleChannels").Debug("copied data from remote to local", "bytes", n, "error", err)
								if cw, ok := localConn.(interface{ CloseWrite() error }); ok {
									cw.CloseWrite()
								}
							}()

							wg.Wait()
							slog.With("function", "HandleChannels").Debug("channel closed")
						}()
					}(ch)
					logger.Debug("forwarding established", "key", key)
				} else {
					logger.Warn("unable to find forwarding session")
					ch.Reject(ssh.ConnectionFailed, "unable to find forwarding session")
				}
			}
		}
	}
}

// connectClient establishes a new SSH client connection.
// Must be called with a lock on m.clientMu
func (m *SSHTunnelManager) connectClient() error {
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
		return rerr
	}

	m.client = client
	m.connected = true
	m.connectionCtx, m.connectionCancel = context.WithCancel(m.externalCtx)

	return nil
}

func (m *SSHTunnelManager) Stop() {
	m.forwardingsMu.Lock()
	defer m.forwardingsMu.Unlock()
	m.clientMu.Lock()
	defer m.clientMu.Unlock()

	for _, forwardingSession := range m.forwardings {
		m.sendForwardingCancel(forwardingSession)
		slog.With("function", "Stop").Info("stopped forwarding", "key", forwardingSession)
	}
	m.forwardings = make(map[string]*ForwardingConfig) // Clear forwardings map

	m.closeClient()

	slog.Info("ssh tunnel manager stopped, all forwardings and connections closed")
}
