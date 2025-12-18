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
	RemoteAddrFunc             ExtractAddrFunc
	SSHDialFunc                sshDialFunc
	NetDialFunc                netDialFunc
	ServerAddress              string
	Username                   string
	HostKey                    string
	PrivateKey                 []byte
	ConnectTimeout             time.Duration
	FwdReqTimeout              time.Duration
	KeepAliveInterval          time.Duration
	AddressVerificationTimeout time.Duration // Optional: timeout for address verification (default: 30s)
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
	externalCtx                context.Context
	client                     sshClient
	connectionCtx              context.Context
	signer                     ssh.Signer
	forwardings                map[string]*ForwardingConfig
	remoteAddrFunc             ExtractAddrFunc
	netDialFunc                netDialFunc
	sshDialFunc                sshDialFunc
	addrNotifications          map[string]chan []string
	assignedAddrs              map[string][]string
	connectionCancel           context.CancelFunc
	hostKey                    string
	sshUser                    string
	sshServerAddress           string
	fwdReqTimeout              time.Duration
	connTimeout                time.Duration
	keepAliveInterval          time.Duration
	addressVerificationTimeout time.Duration
	clientMu                   sync.RWMutex
	addrNotifMu                sync.RWMutex
	connected                  bool
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

	addrVerifyTimeout := config.AddressVerificationTimeout
	if addrVerifyTimeout == 0 {
		addrVerifyTimeout = addressVerificationTimeout
	}

	m := &SSHTunnelManager{
		sshServerAddress:           config.ServerAddress,
		sshUser:                    config.Username,
		hostKey:                    config.HostKey,
		signer:                     signer,
		connTimeout:                config.ConnectTimeout,
		fwdReqTimeout:              config.FwdReqTimeout,
		keepAliveInterval:          config.KeepAliveInterval,
		addressVerificationTimeout: addrVerifyTimeout,
		externalCtx:                externalCtx,
		connected:                  false,
		remoteAddrFunc:             config.RemoteAddrFunc,
		forwardings:                make(map[string]*ForwardingConfig),
		assignedAddrs:              make(map[string][]string),
		addrNotifications:          make(map[string]chan []string),
		sshDialFunc:                sshDialFn,
		netDialFunc:                netDialFn,
	}

	return m, nil
}

// Connect attempts to establish the SSH connection.
// It returns nil if already connected or if connection succeeds.
func (m *SSHTunnelManager) Connect() error {
	m.clientMu.Lock()
	defer m.clientMu.Unlock()

	if m.connected {
		return nil
	}

	if err := m.connectClient(); err != nil {
		return err
	}

	// Start background tasks
	go m.handleChannels()
	go m.monitorConnection()

	slog.With("function", "Connect").Info("ssh connection established")
	return nil
}

// monitorConnection monitors the SSH connection and sends keepalive requests.
// If a keepalive fails, it closes the connection.
func (m *SSHTunnelManager) monitorConnection() {
	ticker := time.NewTicker(m.keepAliveInterval)
	defer ticker.Stop()

	// Capture the context we are monitoring
	m.clientMu.RLock()
	ctx := m.connectionCtx
	m.clientMu.RUnlock()

	if ctx == nil {
		return
	}

	for {
		select {
		case <-m.externalCtx.Done():
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.clientMu.RLock()
			if !m.connected || m.client == nil || m.connectionCtx.Err() != nil {
				m.clientMu.RUnlock()
				return
			}
			client := m.client
			m.clientMu.RUnlock()

			// Send keepalive with timeout to avoid hanging indefinitely
			type keepaliveResult struct {
				err error
			}
			resultCh := make(chan keepaliveResult, 1)

			go func() {
				_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
				resultCh <- keepaliveResult{err: err}
			}()

			// Wait for keepalive response with timeout
			keepaliveTimeout := m.keepAliveInterval * 2 // 2x keepalive interval
			select {
			case result := <-resultCh:
				if result.err != nil {
					slog.With("function", "monitorConnection").Error("ssh keepalive failed, closing connection", "error", result.err)
					m.clientMu.Lock()
					m.closeClient()
					m.clientMu.Unlock()
					return
				}
				slog.With("function", "monitorConnection").Debug("ssh keepalive sent")
			case <-time.After(keepaliveTimeout):
				slog.With("function", "monitorConnection").Error("ssh keepalive timeout, closing connection", "timeout", keepaliveTimeout)
				m.clientMu.Lock()
				m.closeClient()
				m.clientMu.Unlock()
				return
			}
		}
	}
}

// closeClient closes the current SSH client connection.
// Must be called with a write lock on m.clientMu
func (m *SSHTunnelManager) closeClient() {
	if !m.connected {
		return
	}
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

	// Clear assigned addresses as they are invalid on disconnect
	m.addrNotifMu.Lock()
	m.assignedAddrs = make(map[string][]string)
	m.addrNotifMu.Unlock()

	// Clear forwardings as they need to be re-established
	m.forwardings = make(map[string]*ForwardingConfig)
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
	m.addrNotifMu.RLock()
	defer m.addrNotifMu.RUnlock()

	if addrs, ok := m.assignedAddrs[key]; ok {
		// Return a copy to avoid race conditions
		result := make([]string, len(addrs))
		copy(result, addrs)
		return result
	}
	return nil
}

// IsConnected checks if the SSH client is connected.
func (m *SSHTunnelManager) IsConnected() bool {
	m.clientMu.RLock()
	defer m.clientMu.RUnlock()
	return m.connected
}

// ForwardRequest is the type of forwarding request to send.
type ForwardRequest string

const (
	ForwardStart  ForwardRequest = "start"
	ForwardCancel ForwardRequest = "cancel"
)

// Constants for forwarding retry and verification logic
const (
	addressVerificationTimeout  = 30 * time.Second
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
// it matches the requested hostname.
// It returns an error if the request fails or is denied by the server.
// Must be called with a lock on m.clientMu
func (m *SSHTunnelManager) sendForwarding(fwd *ForwardingConfig, req ForwardRequest) error {
	// Send the request once
	err := m.sendForwardingOnce(fwd, req)
	if err != nil {
		return err
	}

	// For ForwardCancel, we're done after successful request
	if req == ForwardCancel {
		return nil
	}

	// For ForwardStart, collect assigned addresses (if remoteAddrFunc is available)
	if m.remoteAddrFunc != nil {
		key := forwardingKey(fwd.RemoteHost, fwd.RemotePort)

		// Register for address notifications
		notifCh := make(chan []string, addrNotificationChannelSize)
		m.addrNotifMu.Lock()
		m.addrNotifications[key] = notifCh
		m.addrNotifMu.Unlock()

		defer func() {
			m.addrNotifMu.Lock()
			delete(m.addrNotifications, key)
			m.addrNotifMu.Unlock()
			close(notifCh)
		}()

		// Wait for address assignment with timeout
		verifyCtx, verifyCancel := context.WithTimeout(m.externalCtx, m.addressVerificationTimeout)
		defer verifyCancel()

		needsVerification := fwd.RemoteHost != "" && fwd.RemoteHost != "0.0.0.0" && fwd.RemoteHost != "localhost"

		select {
		case <-verifyCtx.Done():
			if needsVerification {
				// Timeout waiting for address - cancel the forwarding to avoid orphaned forwardings
				slog.With("function", "sendForwarding").Error("timeout waiting for address verification, canceling forwarding",
					"remote_host", fwd.RemoteHost, "remote_port", fwd.RemotePort)
				_ = m.sendForwardingOnce(fwd, ForwardCancel)
				return fmt.Errorf("timeout waiting for address verification for %s", fwd.RemoteHost)
			}
			// For wildcard/generic, we might proceed without specific verification if timeout hits,
			// but usually we expect *some* address.
			slog.With("function", "sendForwarding").Warn("timeout waiting for address verification",
				"remote_host", fwd.RemoteHost, "remote_port", fwd.RemotePort)
			return nil

		case uris := <-notifCh:
			if needsVerification {
				// Verify hostname matches for specific hostnames
				if matchesRequestedHost(uris, fwd.RemoteHost, fwd.RemotePort) {
					slog.With("function", "sendForwarding").Info("verified correct hostname assigned",
						"remote_host", fwd.RemoteHost, "uris", uris)
					// Store the assigned addresses
					m.addrNotifMu.Lock()
					m.assignedAddrs[key] = uris
					m.addrNotifMu.Unlock()
					return nil
				} else {
					slog.With("function", "sendForwarding").Warn("wrong hostname assigned",
						"requested_host", fwd.RemoteHost, "received_uris", uris)
					// Cancel this forwarding
					_ = m.sendForwardingOnce(fwd, ForwardCancel)
					return fmt.Errorf("wrong hostname assigned: %v", uris)
				}
			} else {
				// For wildcard (0.0.0.0), just store whatever address we got
				slog.With("function", "sendForwarding").Debug("storing assigned addresses for wildcard forwarding",
					"remote_host", fwd.RemoteHost, "remote_port", fwd.RemotePort, "uris", uris)
				m.addrNotifMu.Lock()
				m.assignedAddrs[key] = uris
				m.addrNotifMu.Unlock()
				return nil
			}
		}
	}

	// No remoteAddrFunc provided, assume success
	return nil
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

	slog.With("function", "sendForwardingOnce").Info("sending SSH request",
		"request_type", reqType,
		"remote_host", fwd.RemoteHost,
		"remote_port", fwd.RemotePort,
		"internal_host", fwd.InternalHost,
		"internal_port", fwd.InternalPort,
		"marshaled_addr", forwardMessage.addr,
		"marshaled_port", forwardMessage.rport)

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
		slog.With("function", "sendForwardingOnce").Error("request timed out",
			"request", reqType, "remote_host", fwd.RemoteHost, "remote_port", fwd.RemotePort)
		return fmt.Errorf("ssh: %s request timed out", reqType)
	case res := <-resCh:
		if res.err != nil {
			slog.With("function", "sendForwardingOnce").Error("request failed with error",
				"request", reqType, "remote_host", fwd.RemoteHost, "remote_port", fwd.RemotePort, "error", res.err)
			return res.err
		}
		if !res.ok {
			slog.With("function", "sendForwardingOnce").Error("request denied by server",
				"request", reqType, "remote_host", fwd.RemoteHost, "remote_port", fwd.RemotePort)
			return fmt.Errorf("ssh: %s request denied by server", reqType)
		}
		slog.With("function", "sendForwardingOnce").Info("request accepted by server",
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
	// Clear forwardings map
	m.forwardings = make(map[string]*ForwardingConfig)

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
		// slog.Debug("waiting for new channels")
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
		User: m.sshUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(m.signer),
			ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
				// serveo.net and similar services use keyboard-interactive for authentication
				// but don't actually require answers to questions
				answers := make([]string, len(questions))
				return answers, nil
			}),
		},
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
// It reads from the server's stdout and stderr and applies the remoteAddrFunc to extract URIs from the output.
// This function runs in a goroutine and will stop when the connection context is done.
func (m *SSHTunnelManager) captureServerOutput() {
	session, err := m.createSSHSession()
	if err != nil {
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

	stderr, err := session.StderrPipe()
	if err != nil {
		slog.With("function", "captureServerOutput").Error("failed to get stderr pipe", "error", err)
		return
	}

	if err := session.Shell(); err != nil {
		slog.With("function", "captureServerOutput").Error("failed to start remote session", "error", err)
		return
	}

	go m.readServerOutput(stdout, "stdout")
	go m.readServerOutput(stderr, "stderr")

	<-m.connectionCtx.Done()
	_ = session.Close() // #nosec G104 -- Cleanup on shutdown, error not actionable
}

// createSSHSession creates a new SSH session from the current client.
func (m *SSHTunnelManager) createSSHSession() (*ssh.Session, error) {
	realClient, ok := m.client.(*ssh.Client)
	if !ok {
		slog.With("function", "captureServerOutput").Warn("cannot capture server output, client is not *ssh.Client")
		return nil, fmt.Errorf("client is not *ssh.Client")
	}

	session, err := realClient.NewSession()
	if err != nil {
		slog.With("function", "captureServerOutput").Error("failed to create SSH session", "error", err)
		return nil, err
	}

	return session, nil
}

// readServerOutput reads and processes output from the SSH server.
func (m *SSHTunnelManager) readServerOutput(reader io.Reader, streamName string) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-m.connectionCtx.Done():
			slog.With("function", "captureServerOutput").Debug("connection context canceled, stopping server output capture")
			return
		default:
			n, err := reader.Read(buf)
			if err != nil {
				if err != io.EOF {
					slog.With("function", "captureServerOutput", "stream", streamName).Error("read error", "error", err)
				}
				return
			}
			if n > 0 {
				m.processServerData(buf[:n], streamName)
			}
		}
	}
}

// processServerData extracts URIs from server output and notifies waiting goroutines.
func (m *SSHTunnelManager) processServerData(data []byte, streamName string) {
	dataStr := string(data)
	slog.With("function", "captureServerOutput", "stream", streamName).Debug(dataStr)

	uris, err := m.remoteAddrFunc(dataStr)
	if err != nil {
		slog.With("function", "captureServerOutput", "stream", streamName).Error("failed to extract URIs from data", "error", err)
		return
	}

	if len(uris) == 0 {
		return
	}

	slog.With("function", "captureServerOutput", "stream", streamName).Debug("extracted URIs from server output", "uris_count", len(uris))
	for _, uri := range uris {
		slog.With("function", "captureServerOutput", "stream", streamName).Info("extracted URI from server output", "uri", uri)
	}

	m.notifyURIWaiters(uris)
}

// notifyURIWaiters sends extracted URIs to all registered notification channels.
func (m *SSHTunnelManager) notifyURIWaiters(uris []string) {
	// Copy channels under read lock to avoid holding lock during I/O
	m.addrNotifMu.RLock()
	channels := make(map[string]chan []string, len(m.addrNotifications))
	for key, ch := range m.addrNotifications {
		channels[key] = ch
	}
	m.addrNotifMu.RUnlock()

	// Send to channels outside the lock
	for key, ch := range channels {
		select {
		case ch <- uris:
			slog.With("function", "captureServerOutput").Debug("notified waiter about addresses", "key", key, "uris", uris)
		default:
			// Channel full or no receiver, skip
		}
	}
}
