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
	"strings"
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

// sshDialFunc is a function type for establishing SSH connections.
type sshDialFunc func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error)

// netDialFunc is a function type for establishing TCP connections.
type netDialFunc func(network string, address string) (net.Conn, error)

// Default dial functions - can be overridden via config for testing.
var (
	defaultSSHDial sshDialFunc = func(network, addr string, cfg *ssh.ClientConfig) (sshClient, error) {
		return ssh.Dial(network, addr, cfg)
	}
	defaultNetDial netDialFunc = func(network string, address string) (net.Conn, error) {
		return net.Dial(network, address)
	}
)

// Legacy global variables for backward compatibility with existing tests.
// Deprecated: Use SSHConnectionConfig.SSHDialFunc and NetDialFunc instead.
var (
	sshDial = defaultSSHDial
	netDial = defaultNetDial
)

// ExtractAddrFunc is a function type used to pass a callback that takes text
// returned to ssh server and returns a string containing a uri.
type ExtractAddrFunc func(string) ([]string, error)

// SSHConnectionConfig contains configuration for SSH connection.
type SSHConnectionConfig struct {
	RemoteAddrFunc    ExtractAddrFunc
	SSHDialFunc       sshDialFunc
	NetDialFunc       netDialFunc
	ServerAddress     string
	Username          string
	HostKey           string
	PrivateKey        []byte
	ConnectTimeout    time.Duration
	FwdReqTimeout     time.Duration
	KeepAliveInterval time.Duration
	BackoffInterval   time.Duration
}

// ForwardingConfig defines configuration for a single forwarding.
type ForwardingConfig struct {
	RemoteHost   string
	InternalHost string
	RemotePort   int
	InternalPort int
}

// SSHTunnelManager manages SSH tunnels and forwardings.
type SSHTunnelManager struct {
	externalCtx       context.Context
	client            sshClient
	connectionCtx     context.Context
	signer            ssh.Signer
	forwardings       map[string]*ForwardingConfig
	remoteAddrFunc    ExtractAddrFunc
	netDialFunc       netDialFunc
	sshDialFunc       sshDialFunc
	addrNotifications map[string]chan []string
	assignedAddrs     map[string][]string
	connectionCancel  context.CancelFunc
	hostKey           string
	sshUser           string
	sshServerAddress  string
	fwdReqTimeout     time.Duration
	connTimeout       time.Duration
	backoffInterval   time.Duration
	keepAliveInterval time.Duration
	clientMu          sync.RWMutex
	addrNotifMu       sync.Mutex
	connected         bool
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

	// Use provided dial functions or fall back to defaults/globals
	sshDialFn := config.SSHDialFunc
	if sshDialFn == nil {
		sshDialFn = sshDial // Use global for backward compatibility
	}
	netDialFn := config.NetDialFunc
	if netDialFn == nil {
		netDialFn = netDial // Use global for backward compatibility
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
		assignedAddrs:     make(map[string][]string),
		addrNotifications: make(map[string]chan []string),
		sshDialFunc:       sshDialFn,
		netDialFunc:       netDialFn,
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
			return fmt.Errorf("ssh connection canceled: %w", m.externalCtx.Err())
		default:
		}
		m.clientMu.RLock()
		connected := m.connected
		m.clientMu.RUnlock()
		if connected {
			break
		}
		time.Sleep(connectionWaitPollInterval)
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
				slog.With("function", "monitorConnection").Debug("ssh connection context canceled")
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
		if err := m.client.Close(); err != nil {
			slog.With("function", "disconnect").Error("failed to close SSH client", "error", err)
		}
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

	// Clean up assigned addresses
	m.addrNotifMu.Lock()
	delete(m.assignedAddrs, key)
	m.addrNotifMu.Unlock()

	err := m.sendForwarding(forwardingSession, ForwardCancel)
	if err != nil {
		slog.With("function", "StopForwarding").Error("failed to send cancel request", "error", err)
		return err
	}

	slog.With("function", "StopForwarding").Info("stopped forwarding", "key", key)

	return nil
}

// GetAssignedAddresses returns the assigned addresses (URIs) for a forwarding configuration.
// Returns nil if no addresses have been assigned yet or the forwarding doesn't exist.
func (m *SSHTunnelManager) GetAssignedAddresses(remoteHost string, remotePort int) []string {
	key := forwardingKey(remoteHost, remotePort)
	m.addrNotifMu.Lock()
	defer m.addrNotifMu.Unlock()

	if addrs, ok := m.assignedAddrs[key]; ok {
		// Return a copy to avoid race conditions
		result := make([]string, len(addrs))
		copy(result, addrs)
		return result
	}
	return nil
}

// ForwardRequest is the type of forwarding request to send.
type ForwardRequest string

const (
	ForwardStart  ForwardRequest = "start"
	ForwardCancel ForwardRequest = "cancel"
)

// Constants for forwarding retry and verification logic
const (
	maxForwardingRetries        = 30
	forwardingRetryDelay        = 15 * time.Second
	addressVerificationTimeout  = 30 * time.Second
	connectionWaitPollInterval  = 100 * time.Millisecond
	addrNotificationChannelSize = 5
)

// matchesRequestedHost checks if any of the extracted URIs match the requested hostname.
// For HTTP/HTTPS URIs, it checks if the hostname contains the requested host.
// For TCP URIs, it checks if the host:port matches.
func matchesRequestedHost(uris []string, requestedHost string, requestedPort int) bool {
	if requestedHost == "" || requestedHost == "0.0.0.0" {
		// No specific hostname requested, any assignment is fine
		return true
	}

	for _, uri := range uris {
		// For HTTP/HTTPS: check if hostname contains requested host
		// e.g., requested "dev", got "https://user-dev.tuns.sh" -> match
		if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
			if strings.Contains(uri, requestedHost) {
				return true
			}
		}
		// For TCP: check if it contains host:port
		// e.g., requested "example.com", got "tcp://example.com:8080" -> match
		expectedTCP := fmt.Sprintf("%s:%d", requestedHost, requestedPort)
		if strings.Contains(uri, expectedTCP) {
			return true
		}
	}
	return false
}

// sendForwarding sends a request to the SSH server to start or cancel a TCP forwarding.
// 'req' controls the request: ForwardStart -> "tcpip-forward", ForwardCancel -> "cancel-tcpip-forward".
// For ForwardStart requests, it waits for the SSH server to report the assigned address and verifies
// it matches the requested hostname. If a mismatch is detected, it retries the request.
// It returns an error if the request fails or is denied by the server.
// Must be called with a lock on m.clientMu
func (m *SSHTunnelManager) sendForwarding(fwd *ForwardingConfig, req ForwardRequest) error {
	for attempt := 0; attempt < maxForwardingRetries; attempt++ {
		if attempt > 0 {
			slog.With("function", "sendForwarding").Info("retrying forwarding request",
				"attempt", attempt+1, "max_retries", maxForwardingRetries, "remote_host", fwd.RemoteHost)
			time.Sleep(forwardingRetryDelay)
		}

		err := m.sendForwardingOnce(fwd, req)
		if err != nil {
			return err
		}

		// For ForwardCancel, we're done after successful request
		if req == ForwardCancel {
			return nil
		}

		// For ForwardStart, verify we got the correct hostname (if remoteAddrFunc is available)
		if m.remoteAddrFunc != nil && (fwd.RemoteHost != "" && fwd.RemoteHost != "0.0.0.0") {
			key := forwardingKey(fwd.RemoteHost, fwd.RemotePort)

			// Register for address notifications
			notifCh := make(chan []string, addrNotificationChannelSize)
			m.addrNotifMu.Lock()
			m.addrNotifications[key] = notifCh
			m.addrNotifMu.Unlock()

			// Wait for address assignment with timeout
			verifyCtx, verifyCancel := context.WithTimeout(m.externalCtx, addressVerificationTimeout)
			matched := false

		waitLoop:
			for {
				select {
				case <-verifyCtx.Done():
					verifyCancel()
					// Timeout waiting for address - continue anyway
					slog.With("function", "sendForwarding").Warn("timeout waiting for address verification, continuing anyway",
						"remote_host", fwd.RemoteHost, "remote_port", fwd.RemotePort)
					matched = true
					break waitLoop
				case uris := <-notifCh:
					if matchesRequestedHost(uris, fwd.RemoteHost, fwd.RemotePort) {
						slog.With("function", "sendForwarding").Info("verified correct hostname assigned",
							"remote_host", fwd.RemoteHost, "uris", uris)
						matched = true
						// Store the assigned addresses
						m.addrNotifMu.Lock()
						m.assignedAddrs[key] = uris
						m.addrNotifMu.Unlock()
						verifyCancel()
						break waitLoop
					} else {
						slog.With("function", "sendForwarding").Warn("wrong hostname assigned, will retry",
							"requested_host", fwd.RemoteHost, "received_uris", uris, "attempt", attempt+1)
						verifyCancel()
						// Cancel this forwarding and retry
						_ = m.sendForwardingOnce(fwd, ForwardCancel)
						break waitLoop
					}
				}
			}

			// Cleanup notification channel
			m.addrNotifMu.Lock()
			delete(m.addrNotifications, key)
			m.addrNotifMu.Unlock()
			close(notifCh)

			if matched {
				return nil
			}
			// Continue to next retry attempt
		} else {
			// No verification needed
			return nil
		}
	}

	return fmt.Errorf("failed to get correct hostname after %d attempts", maxForwardingRetries)
}

// sendForwardingOnce sends a single forwarding request without retry logic.
// Must be called with a lock on m.clientMu
func (m *SSHTunnelManager) sendForwardingOnce(fwd *ForwardingConfig, req ForwardRequest) error {
	// Validate port range to prevent integer overflow
	if fwd.RemotePort < 0 || fwd.RemotePort > 65535 {
		return fmt.Errorf("invalid remote port %d: must be between 0 and 65535", fwd.RemotePort)
	}

	forwardMessage := channelForwardMsg{
		addr:  fwd.RemoteHost,
		rport: uint32(fwd.RemotePort), // #nosec G115 -- Port validated above
	}

	ctx, cancel := context.WithTimeout(m.externalCtx, m.fwdReqTimeout)
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
		err error
		ok  bool
	})

	go func() {
		ok, _, err := m.client.SendRequest(reqType, true, ssh.Marshal(&forwardMessage))
		select {
		case resCh <- struct {
			err error
			ok  bool
		}{err, ok}:
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
		slog.With("function", "sendForwardingOnce").Debug("request accepted",
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
			slog.With("function", "handleChannels").Debug("connection context canceled, stopping channel handling")
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
					_ = ch.Reject(ssh.ConnectionFailed, "could not parse forwarded-tcpip payload: "+err.Error()) // #nosec G104 -- Best effort rejection in error path
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
									_ = req.Reply(false, nil) // #nosec G104 -- Best effort reply, already in goroutine
								}
							}
						}()

						go func() {
							defer func() {
								_ = remoteConn.Close() // #nosec G104 -- Cleanup in defer, error logged below
							}()

							addr := net.JoinHostPort(fwd.InternalHost, strconv.Itoa(fwd.InternalPort))
							localConn, localErr := m.netDialFunc("tcp", addr)
							if localErr != nil {
								logger.Error("failed to connect to local address", "error", localErr)
								return
							}

							defer func() {
								_ = localConn.Close() // #nosec G104 -- Cleanup in defer, error logged below
							}()

							wg := &sync.WaitGroup{}
							wg.Add(2)

							go func() {
								defer wg.Done()
								n, err := io.Copy(remoteConn, localConn)
								slog.With("function", "handleChannels").Debug("copied data from local to remote", "bytes", n, "error", err)
								_ = remoteConn.CloseWrite() // #nosec G104 -- Best effort shutdown, error logged in io.Copy above
							}()

							go func() {
								defer wg.Done()
								n, err := io.Copy(localConn, remoteConn)
								slog.With("function", "handleChannels").Debug("copied data from remote to local", "bytes", n, "error", err)
								if cw, ok := localConn.(interface{ CloseWrite() error }); ok {
									_ = cw.CloseWrite() // #nosec G104 -- Best effort shutdown, error logged in io.Copy above
								}
							}()

							wg.Wait()
							slog.With("function", "handleChannels").Debug("channel closed")
						}()
					}(ch)
					logger.Debug("forwarding established", "key", key)
				} else {
					logger.Warn("unable to find forwarding session")
					_ = ch.Reject(ssh.ConnectionFailed, "unable to find forwarding session") // #nosec G104 -- Best effort rejection
				}
			default:
				logger.Warn("unknown channel type received", "channel_type", channelType)
				_ = ch.Reject(ssh.UnknownChannelType, "unknown channel type") // #nosec G104 -- Best effort rejection
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
		// Security: Using InsecureIgnoreHostKey is acceptable here because:
		// 1. User explicitly chose not to provide SSH_HOST_KEY environment variable
		// 2. This is typically used for development/testing environments
		// 3. We log a warning to make the user aware of the security implication
		// For production use, SSH_HOST_KEY should always be set
		slog.With("function", "connect").Warn("no host key provided, falling back to InsecureIgnoreHostKey")
		config.HostKeyCallback = ssh.InsecureIgnoreHostKey() // #nosec G106 -- Intentional fallback for dev/test
	}

	client, err := m.sshDialFunc("tcp", m.sshServerAddress, config)
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
	defer func() {
		if err := session.Close(); err != nil {
			slog.With("function", "handleConnection").Error("failed to close session", "error", err)
		}
	}()

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

				// Notify any waiting goroutines about the extracted URIs
				if len(uris) > 0 {
					m.addrNotifMu.Lock()
					for key, ch := range m.addrNotifications {
						select {
						case ch <- uris:
							slog.With("function", "captureServerOutput").Debug("notified waiter about addresses", "key", key, "uris", uris)
						default:
							// Channel full or no receiver, skip
						}
					}
					m.addrNotifMu.Unlock()
				}
			}
		}
	}()

	<-m.connectionCtx.Done()
	_ = session.Close() // #nosec G104 -- Cleanup on shutdown, error not actionable
}
