package ssh

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
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

// ExtractAddrFunc is a function type used to pass a callback that takes text
// returned to ssh server and returns a string containing a uri.
type ExtractAddrFunc func(string) ([]string, error)

// SSHConnectionConfig contains configuration for SSH connection.
type SSHConnectionConfig struct {
	PrivateKey        []byte
	ServerAddress     string
	Username          string
	HostKey           string
	ConnectTimeout    time.Duration
	FwdReqTimeout     time.Duration
	KeepAliveInterval time.Duration
	BackoffInterval   time.Duration
	RemoteAddrFunc    ExtractAddrFunc
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
	connTimeout       time.Duration
	fwdReqTimeout     time.Duration
	keepAliveInterval time.Duration
	backoffInterval   time.Duration
	externalCtx       context.Context
	connectionCtx     context.Context
	connectionCancel  context.CancelFunc
	connected         bool
	clientMu          sync.RWMutex
	client            sshClient
	remoteAddrFunc    ExtractAddrFunc
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
		connTimeout:       config.ConnectTimeout,
		fwdReqTimeout:     config.FwdReqTimeout,
		keepAliveInterval: config.KeepAliveInterval,
		backoffInterval:   config.BackoffInterval,
		externalCtx:       externalCtx,
		connected:         false,
		remoteAddrFunc:    config.RemoteAddrFunc,
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
		m.clientMu.RLock()
		connected := m.connected
		m.clientMu.RUnlock()
		if connected {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}

// handleConnection manages the SSH connection lifecycle and reconnections. Blocks until externalCtx is done.
func (m *SSHTunnelManager) handleConnection() {
	for {
		select {
		case <-m.externalCtx.Done():
			m.clientMu.Lock()
			m.closeClient()
			m.clientMu.Unlock()
			return
		default:
		}

		m.clientMu.Lock()
		err := m.connectClient()
		if err != nil {
			m.clientMu.Unlock()
			slog.With("function", "handleConnection").Error("ssh connection failed", "error", err)
			time.Sleep(m.backoffInterval)
			continue
		}
		slog.With("function", "handleConnection").Debug("ssh connection established")

		// Restart existing forwardings, in case of reconnection
		for key, forwardingSession := range m.forwardings {
			slog.With("function", "handleConnection").Debug("restarting forwarding", "key", key)
			err := m.sendForwarding(forwardingSession, ForwardStart)
			if err != nil {
				slog.With("function", "handleConnection").Error("failed to send forwarding request, removing forwarding", "error", err, "key", key)
				delete(m.forwardings, key)
				continue
			}
		}

		// Handle incoming channels
		go m.handleChannels()

		m.clientMu.Unlock()

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
			m.clientMu.RLock()
			if m.connectionCtx == nil || m.connectionCtx.Err() != nil {
				m.clientMu.RUnlock()
				slog.With("function", "monitorConnection").Debug("ssh connection context cancelled")
				return
			}
			if m.client == nil {
				m.clientMu.RUnlock()
				slog.With("function", "monitorConnection").Error("ssh client is nil, reconnecting")
				return
			}
			_, _, err := m.client.SendRequest("keepalive@openssh.com", true, nil)
			m.clientMu.RUnlock()
			if err != nil {
				slog.With("function", "monitorConnection").Error("ssh keepalive failed, reconnecting", "error", err)
				return
			}
			slog.With("function", "monitorConnection").Debug("ssh keepalive sent")
		}
	}
}

// closeClient closes the current SSH client connection.
// Must be called with a write lock on m.clientMu
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
	m.clientMu.Lock()
	defer m.clientMu.Unlock()

	if !m.connected || m.client == nil {
		slog.With("function", "StartForwarding").Warn("client not ready")
		return &ErrSSHClientNotReady{}
	}
	key := forwardingKey(fwd.RemoteHost, fwd.RemotePort)

	if _, exists := m.forwardings[key]; exists {
		err := &ErrSSHForwardingExists{Key: key}
		slog.With("function", "StartForwarding").Error(err.Error())
		return err
	}

	err := m.sendForwarding(&fwd, ForwardStart)
	if err != nil {
		slog.With("function", "StartForwarding").Error("failed to send forwarding request", "error", err)
		return err
	}

	// Store the forwarding session
	m.forwardings[key] = &fwd

	slog.With("function", "StartForwarding").Info("started forwarding", "key", key)

	return nil
}

// StopForwarding stops an existing forwarding based on the provided configuration.
func (m *SSHTunnelManager) StopForwarding(fwd *ForwardingConfig) error {
	m.clientMu.Lock()
	defer m.clientMu.Unlock()

	if !m.connected || m.client == nil {
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

	err := m.sendForwarding(forwardingSession, ForwardCancel)
	if err != nil {
		slog.With("function", "StopForwarding").Error("failed to send cancel request", "error", err)
		return err
	}

	slog.With("function", "StopForwarding").Info("stopped forwarding", "key", key)

	return nil
}

// ForwardRequest is the type of forwarding request to send.
type ForwardRequest string

const (
	ForwardStart  ForwardRequest = "start"
	ForwardCancel ForwardRequest = "cancel"
)

// sendForwarding sends a request to the SSH server to start or cancel a TCP forwarding.
// 'req' controls the request: ForwardStart -> "tcpip-forward", ForwardCancel -> "cancel-tcpip-forward".
// It returns an error if the request fails or is denied by the server.
// Must be called with a lock on m.clientMu
func (m *SSHTunnelManager) sendForwarding(fwd *ForwardingConfig, req ForwardRequest) error {
	forwardMessage := channelForwardMsg{
		addr:  fwd.RemoteHost,
		rport: uint32(fwd.RemotePort),
	}

	ctx, cancel := context.WithTimeout(context.Background(), m.fwdReqTimeout)
	defer cancel()

	var reqType string
	switch req {
	case ForwardStart:
		reqType = "tcpip-forward"
	case ForwardCancel:
		reqType = "cancel-tcpip-forward"
	default:
		return fmt.Errorf("ssh: unknown forwarding request type: %q", req)
	}

	resCh := make(chan struct {
		ok  bool
		err error
	})

	go func() {
		ok, _, err := m.client.SendRequest(reqType, true, ssh.Marshal(&forwardMessage))
		select {
		case resCh <- struct {
			ok  bool
			err error
		}{ok, err}:
		case <-ctx.Done():
		}
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("ssh: %s request timed out", reqType)
	case res := <-resCh:
		if res.err != nil {
			return res.err
		}
		if !res.ok {
			return fmt.Errorf("ssh: %s request denied by server", reqType)
		}
		slog.With("function", "sendForwarding").Debug("request accepted",
			"request", reqType, "remote_host", fwd.RemoteHost, "remote_port", fwd.RemotePort)
		return nil
	}
}

// Stop gracefully shuts down the SSHTunnelManager.
func (m *SSHTunnelManager) Stop() {
	m.clientMu.Lock()
	defer m.clientMu.Unlock()

	if m.client != nil && m.connected {
		for key, forwardingSession := range m.forwardings {
			if err := m.sendForwarding(forwardingSession, ForwardCancel); err != nil {
				slog.With("function", "Stop").Error("failed to stop forwarding", "key", key, "error", err)
			} else {
				slog.With("function", "Stop").Info("stopped forwarding", "key", key)
			}
		}
	}
	m.forwardings = make(map[string]*ForwardingConfig) // Clear forwardings map

	m.closeClient()

	slog.Info("ssh tunnel manager stopped, all forwardings and connections closed")
}

// handleChannels manages the lifecycle of a forwarding sessions
func (m *SSHTunnelManager) handleChannels() {
	m.clientMu.RLock()
	if m.client == nil {
		m.clientMu.RUnlock()
		slog.With("function", "handleChannels").Error("client is nil, cannot handle channels")
		return
	}
	tcpipChan := m.client.HandleChannelOpen("forwarded-tcpip")
	connectionCtx := m.connectionCtx
	m.clientMu.RUnlock()

	for {
		slog.Debug("waiting for new channels")
		select {
		case <-connectionCtx.Done():
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
			logger.Debug("received new channel from SSH server")

			switch channelType := ch.ChannelType(); channelType {
			case "forwarded-tcpip":
				var payload forwardedTCPPayload
				if err := ssh.Unmarshal(ch.ExtraData(), &payload); err != nil {
					logger.Error("unable to parse forwarded-tcpip payload", slog.Any("error", err))
					ch.Reject(ssh.ConnectionFailed, "could not parse forwarded-tcpip payload: "+err.Error())
					continue
				}

				logger.Debug("forwarded-tcpip channel opened", "remote_addr", payload.Addr, "remote_port", payload.Port, "origin_addr", payload.OriginAddr, "origin_port", payload.OriginPort)

				key := forwardingKey(payload.Addr, int(payload.Port))
				m.clientMu.RLock()
				fwd, exists := m.forwardings[key]
				m.clientMu.RUnlock()
				if exists {
					go func(ch ssh.NewChannel) {
						remoteConn, reqs, acceptErr := ch.Accept()
						if acceptErr != nil {
							logger.Error("failed to accept channel", "error", acceptErr)
							return
						}
						logger.Debug("channel accepted")

						// Log all requests sent by the SSH server on this channel, and discard them.
						go func() {
							for req := range reqs {
								logger.Debug("received request on channel", "request_type", req.Type, "want_reply", req.WantReply, "payload", string(req.Payload))
								if req.WantReply {
									req.Reply(false, nil)
								}
							}
						}()

						go func() {
							defer remoteConn.Close()

							addr := net.JoinHostPort(fwd.InternalHost, strconv.Itoa(fwd.InternalPort))
							localConn, localErr := netDial("tcp", addr)
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
								slog.With("function", "handleChannels").Debug("copied data from local to remote", "bytes", n, "error", err)
								remoteConn.CloseWrite()
							}()

							go func() {
								defer wg.Done()
								n, err := io.Copy(localConn, remoteConn)
								slog.With("function", "handleChannels").Debug("copied data from remote to local", "bytes", n, "error", err)
								if cw, ok := localConn.(interface{ CloseWrite() error }); ok {
									cw.CloseWrite()
								}
							}()

							wg.Wait()
							slog.With("function", "handleChannels").Debug("channel closed")
						}()
					}(ch)
					logger.Debug("forwarding established", "key", key)
				} else {
					logger.Warn("unable to find forwarding session")
					ch.Reject(ssh.ConnectionFailed, "unable to find forwarding session")
				}
			default:
				logger.Warn("unknown channel type received", "channel_type", channelType)
				ch.Reject(ssh.UnknownChannelType, "unknown channel type")
			}
		}
	}
}

// connectClient establishes a new SSH client connection.
// Must be called with a write lock on m.clientMu
func (m *SSHTunnelManager) connectClient() error {
	config := &ssh.ClientConfig{
		User:    m.sshUser,
		Auth:    []ssh.AuthMethod{ssh.PublicKeys(m.signer)},
		Timeout: m.connTimeout,
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

	if m.remoteAddrFunc == nil {
		slog.With("function", "connect").Debug("remoteAddrFunc is not set, skipping server output capture")
	} else {
		slog.With("function", "connect").Debug("remoteAddrFunc is set, capturing server output")
		go m.captureServerOutput()
	}

	return nil
}

// captureServerOutput captures the output from the SSH server and processes it using the remoteAddrFunc.
// It reads from the server's stdout and applies the remoteAddrFunc to extract URIs from the output.
// This function runs in a goroutine and will stop when the connection context is done.
func (m *SSHTunnelManager) captureServerOutput() {
	realClient, ok := m.client.(*ssh.Client)
	if !ok {
		slog.With("function", "captureServerOutput").Warn("cannot capture server output, client is not *ssh.Client")
		return
	}

	session, err := realClient.NewSession()
	if err != nil {
		slog.With("function", "captureServerOutput").Error("failed to create SSH session", "error", err)
		return
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		slog.With("function", "captureServerOutput").Error("failed to get stdout pipe", "error", err)
		return
	}

	if err := session.Shell(); err != nil {
		slog.With("function", "captureServerOutput").Error("failed to start remote session", "error", err)
		return
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if err != nil {
				if err != io.EOF {
					slog.With("function", "captureServerOutput", "stream", "stdout").Error("read error", "error", err)
				}
				break
			}
			if n > 0 {
				data := string(buf[:n])
				slog.With("function", "captureServerOutput", "stream", "stdout").Debug(data)

				uris, err := m.remoteAddrFunc(data)
				if err != nil {
					slog.With("function", "captureServerOutput", "stream", "stdout").Error("failed to extract URIs from data", "error", err)
					continue
				}
				slog.With("function", "captureServerOutput", "stream", "stdout").Debug("extracted URIs from server output", "uris_count", len(uris))
				for _, uri := range uris {
					slog.With("function", "captureServerOutput", "stream", "stdout").Info("extracted URI from server output", "uri", uri)
				}
			}
		}
	}()

	<-m.connectionCtx.Done()
	session.Close()
}
