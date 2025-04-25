package ssh

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHTunnelSession struct {
	client *ssh.Client
	cancel context.CancelFunc
}

type SSHTunnelManager struct {
	PrivateKeyPath    string
	SSHServerAddress  string
	SSHUser           string
	HostKey           string
	Timeout           time.Duration
	KeepAliveInterval time.Duration
	BackoffInterval   time.Duration
	RemotePort        int
	sessionsMu        sync.Mutex
	sessions          map[string]*SSHTunnelSession
}

func NewSSHTunnelManager(privateKeyPath string, remotePort int) *SSHTunnelManager {
	return &SSHTunnelManager{
		PrivateKeyPath:    privateKeyPath,
		SSHServerAddress:  "tuns.sh:22",
		SSHUser:           "ssh-tunnel",
		HostKey:           "",
		Timeout:           5 * time.Second,
		KeepAliveInterval: 20 * time.Second,
		BackoffInterval:   3 * time.Second,
		RemotePort:        remotePort,
		sessions:          make(map[string]*SSHTunnelSession),
	}
}

// StartRemotePortForwarding sets up ssh -R style remote port forwarding with automatic reconnects
func (m *SSHTunnelManager) StartRemotePortForwarding(ctx context.Context, name string, remoteHost string, internalHost string, internalPort int) (*SSHTunnelSession, error) {
	// create a cancellable sub-context for this session
	loopCtx, cancel := context.WithCancel(ctx)
	session := &SSHTunnelSession{cancel: cancel}
	// register session under its key
	m.sessionsMu.Lock()
	m.sessions[getSessionKey(name, internalHost, internalPort)] = session
	m.sessionsMu.Unlock()

	go func() {
		logger := slog.With(
			"function", "StartRemotePortForwarding",
			"name", name,
			"remotePort", m.RemotePort,
			"internalHost", internalHost,
			"internalPort", internalPort,
		)
		var client *ssh.Client
		var listener net.Listener

		for {
			// stop if parent context is done
			select {
			case <-loopCtx.Done():
				if client != nil {
					client.Close()
				}
				return
			default:
			}

			// (re)connect SSH
			var err error
			client, err = m.connect()
			if err != nil {
				logger.Warn("ssh reconnect failed, retrying", "error", err)
				time.Sleep(m.BackoffInterval)
				continue
			}
			session.client = client

			// start keepalive
			go func(c *ssh.Client) {
				ticker := time.NewTicker(m.KeepAliveInterval)
				defer ticker.Stop()
				for {
					select {
					case <-loopCtx.Done():
						return
					case <-ticker.C:
						_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
						if err != nil {
							slog.Warn("keepalive failed", "error", err)
							return
						}
					}
				}
			}(client)

			addr := fmt.Sprintf("%s:%d", func() string {
				if remoteHost == "" {
					return "0.0.0.0"
				}
				return remoteHost
			}(), m.RemotePort)
			listener, err = client.Listen("tcp", addr)
			if err != nil {
				slog.Warn("failed to start remote listener, reconnecting", "error", err)
				client.Close()
				time.Sleep(m.BackoffInterval)
				continue
			}

			slog.Info("forwarding configured", "remoteHost", remoteHost, "remotePort", m.RemotePort, "internalHost", internalHost, "internalPort", internalPort)

			// accept loop
			for {
				select {
				case <-loopCtx.Done():
					listener.Close()
					client.Close()
					return
				default:
				}
				logger.Info("waiting for remote connection")
				remoteConn, err := listener.Accept()
				if err != nil {
					logger.Warn("listener accept error, restarting listener", "error", err)
					listener.Close()
					break
				}
				logger.Info("accepted remote connection", "remoteAddr", remoteConn.RemoteAddr().String())
				go func() {
					dialAddr := fmt.Sprintf("%s:%d", internalHost, internalPort)
					logger.Info("dialing internal service", "internalAddr", dialAddr)
					internalConn, err := (&net.Dialer{}).DialContext(loopCtx, "tcp", dialAddr)
					if err != nil {
						logger.Warn("failed to connect to internal service", "error", err)
						remoteConn.Close()
						return
					}
					logger.Info("internal service connection established", "localAddr", internalConn.LocalAddr().String())
					logger.Info("invoking proxyConnection", "remoteAddr", remoteConn.RemoteAddr().String())
					proxyConnection(remoteConn, internalConn)
				}()
			}
			// closed listener or accept failed: loop to reconnect
			time.Sleep(m.BackoffInterval)
		}
	}()

	return session, nil
}

// CleanupSessions removes all sessions except the one identified by session key
func (m *SSHTunnelManager) cleanupSessions(sessionKeyToKeep string) {
	var toRemove []string
	for sessionKey := range m.sessions {
		if sessionKey != sessionKeyToKeep {
			toRemove = append(toRemove, sessionKey)
		}
	}
	for _, sessionKey := range toRemove {
		m.removeSession(sessionKey)
	}
}

// CleanupSessions removes all sessions except the one identified by name, internalHost. and internalPort
func (m *SSHTunnelManager) CleanupSessions(name string, internalHost string, internalPort int) {
	m.sessionsMu.Lock()
	defer m.sessionsMu.Unlock()

	m.cleanupSessions(getSessionKey(name, internalHost, internalPort))
}

// removeSession closes and removes a single SSH session by session key
func (m *SSHTunnelManager) removeSession(sessionKey string) {

	sess, exists := m.sessions[sessionKey]
	if exists {
		delete(m.sessions, sessionKey)
	}
	if exists {
		sess.Close()
	}
}

// RemoveSession closes and removes a single SSH session by name
func (m *SSHTunnelManager) RemoveSession(name string, internalHost string, internalPort int) {
	m.sessionsMu.Lock()
	defer m.sessionsMu.Unlock()

	m.removeSession(getSessionKey(name, internalHost, internalPort))
}

// Close shuts down all active SSH sessions managed by this SSHTunnelManager
func (m *SSHTunnelManager) Close() {
	m.sessionsMu.Lock()
	defer m.sessionsMu.Unlock()
	for name, sess := range m.sessions {
		sess.Close()
		delete(m.sessions, name)
	}
}

// loadPrivateKey loads an SSH private key from file
func (m *SSHTunnelManager) loadPrivateKey() (ssh.Signer, error) {
	key, err := os.ReadFile(m.PrivateKeyPath)
	if err != nil {
		slog.Error("unable to read private key", "path", m.PrivateKeyPath, "error", err)
		return nil, fmt.Errorf("unable to read private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		slog.Error("unable to parse private key", "error", err)
		return nil, fmt.Errorf("unable to parse private key: %w", err)
	}
	return signer, nil
}

// connect establishes an SSH connection
func (m *SSHTunnelManager) connect() (*ssh.Client, error) {
	signer, err := m.loadPrivateKey()
	if err != nil {
		return nil, err
	}
	timeout := m.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	config := &ssh.ClientConfig{
		User: m.SSHUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		Timeout: timeout,
	}

	if m.HostKey != "" {
		config.HostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			actual := sha256.Sum256(key.Marshal())
			actualFingerprint := "SHA256:" + base64.StdEncoding.EncodeToString(actual[:])

			if actualFingerprint != m.HostKey {
				slog.Error("host key verification failed", "expected", m.HostKey, "got", actualFingerprint)
				return fmt.Errorf("host key verification failed: expected %s, got %s", m.HostKey, actualFingerprint)
			}
			return nil
		}
	} else {
		slog.Warn("no hostkey provided, falling back to InsecureIgnoreHostKey")
		config.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	client, err := ssh.Dial("tcp", m.SSHServerAddress, config)
	if err != nil {
		slog.Error("failed to dial ssh server", "error", err)
		return nil, fmt.Errorf("failed to dial ssh server: %w", err)
	}

	slog.Info("ssh connection established", "server", m.SSHServerAddress)
	return client, nil
}

// Close cleanly shuts down an SSH session
func (s *SSHTunnelSession) Close() {
	// signal the background loop to stop
	s.cancel()
	if s.client != nil {
		if err := s.client.Close(); err != nil {
			slog.Warn("failed to close ssh session cleanly", "error", err)
		}
		s.client = nil
	}
}

func getSessionKey(name string, internalHost string, internalPort int) string {
	return fmt.Sprintf("%s/%s/%d", name, internalHost, internalPort)
}

// proxyConnection proxies traffic between two io.ReadWriteClosers
func proxyConnection(a io.ReadWriteCloser, b io.ReadWriteCloser) {
	// extract addresses for context (if available)
	var localAddr, remoteAddr string
	if conn, ok := a.(net.Conn); ok {
		localAddr = conn.LocalAddr().String()
		remoteAddr = conn.RemoteAddr().String()
	}
	logger := slog.With("function", "proxyConnection", "local", localAddr, "remote", remoteAddr)
	logger.Info("starting proxyConnection")

	defer func() {
		a.Close()
		b.Close()
		logger.Info("closed connections")
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// internal -> remote
	go func() {
		defer wg.Done()
		n, err := io.Copy(a, b)
		if err != nil {
			logger.Error("copy internal->remote failed", "bytes", n, "error", err)
		} else {
			logger.Info("copy internal->remote complete", "bytes", n)
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
	}()

	wg.Wait()
	logger.Info("finished proxyConnection")
}
