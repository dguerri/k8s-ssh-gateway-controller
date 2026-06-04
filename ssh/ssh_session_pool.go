package ssh

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// SessionKind enumerates the sish session flavours the pool may open.
// Mirrors controllers.SessionKind; the pool exposes its own enum so the ssh/
// package can stay self-contained.
type SessionKind int

const (
	SessionPlain SessionKind = iota
	SessionProxyProto
	SessionSNIProxy
)

func (k SessionKind) String() string {
	switch k {
	case SessionPlain:
		return "plain"
	case SessionProxyProto:
		return "pp"
	case SessionSNIProxy:
		return "sni"
	default:
		return "unknown"
	}
}

// sessionManager is the subset of *SSHTunnelManager the pool needs. Lets tests
// inject stubs.
type sessionManager interface {
	StartForwarding(ForwardingConfig) error
	StopForwarding(*ForwardingConfig) error
	GetAssignedAddresses(string, int) []string
	IsConnected() bool
	Stop()
	Connect() error
}

// managerFactory builds a sessionManager. Production code uses NewSSHTunnelManager;
// tests inject a stub-producing factory via newSessionPoolWithFactory.
type managerFactory func(ctx context.Context, flags SessionFlags, label string) (sessionManager, error)

// SSHSessionPool owns up to three sessionManagers keyed by SessionKind.
type SSHSessionPool struct {
	ctx      context.Context
	factory  managerFactory
	managers map[SessionKind]sessionManager
	flags    map[SessionKind]SessionFlags // remembered to detect PP version changes
	mu       sync.RWMutex
}

// NewSSHSessionPool builds the pool and eagerly constructs the plain manager.
// Returns an error if the plain manager cannot be constructed.
func NewSSHSessionPool(ctx context.Context, baseConfig *SSHConnectionConfig) (*SSHSessionPool, error) {
	factory := func(c context.Context, flags SessionFlags, label string) (sessionManager, error) {
		cfg := *baseConfig
		cfg.SessionFlags = flags
		cfg.SessionLabel = label
		return NewSSHTunnelManager(c, &cfg)
	}
	return newSessionPoolWithFactory(ctx, factory)
}

func newSessionPoolWithFactory(ctx context.Context, factory managerFactory) (*SSHSessionPool, error) {
	p := &SSHSessionPool{
		ctx:      ctx,
		factory:  factory,
		managers: make(map[SessionKind]sessionManager),
		flags:    make(map[SessionKind]SessionFlags),
	}
	plain, err := factory(ctx, SessionFlags{}, "plain")
	if err != nil {
		return nil, fmt.Errorf("build plain manager: %w", err)
	}
	p.managers[SessionPlain] = plain
	p.flags[SessionPlain] = SessionFlags{}
	if err := plain.Connect(); err != nil {
		slog.With("session", "plain").Warn("initial Connect failed, will retry", "error", err)
	}
	return p, nil
}

// ConfigureSessions reconciles the open set of PP/SNI managers with the desired
// state. The plain manager is always present and never closed by this method.
func (p *SSHSessionPool) ConfigureSessions(ppVersion int, sniEnabled bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.reconcileKind(SessionProxyProto, ppVersion > 0, SessionFlags{ProxyProtocolVersion: ppVersion}); err != nil {
		return err
	}
	return p.reconcileKind(SessionSNIProxy, sniEnabled, SessionFlags{SNIProxy: true})
}

func (p *SSHSessionPool) reconcileKind(kind SessionKind, want bool, desired SessionFlags) error {
	existing, present := p.managers[kind]
	switch {
	case want && !present:
		return p.openLocked(kind, desired)
	case !want && present:
		existing.Stop()
		delete(p.managers, kind)
		delete(p.flags, kind)
	case want && present && p.flags[kind] != desired:
		// PP version flipped — tear down and rebuild.
		existing.Stop()
		delete(p.managers, kind)
		delete(p.flags, kind)
		return p.openLocked(kind, desired)
	}
	return nil
}

func (p *SSHSessionPool) openLocked(kind SessionKind, flags SessionFlags) error {
	m, err := p.factory(p.ctx, flags, flags.DefaultLabel())
	if err != nil {
		return fmt.Errorf("build %s manager: %w", kind, err)
	}
	if err := m.Connect(); err != nil {
		slog.With("session", flags.DefaultLabel()).Warn("initial Connect failed, will retry", "error", err)
	}
	p.managers[kind] = m
	p.flags[kind] = flags
	return nil
}

func (p *SSHSessionPool) StartForwarding(kind SessionKind, cfg ForwardingConfig) error {
	p.mu.RLock()
	m, ok := p.managers[kind]
	p.mu.RUnlock()
	if !ok {
		return &ErrSessionNotEnabled{Kind: kind.String()}
	}
	return m.StartForwarding(cfg)
}

func (p *SSHSessionPool) StopForwarding(kind SessionKind, cfg *ForwardingConfig) error {
	p.mu.RLock()
	m, ok := p.managers[kind]
	p.mu.RUnlock()
	if !ok {
		return &ErrSessionNotEnabled{Kind: kind.String()}
	}
	return m.StopForwarding(cfg)
}

func (p *SSHSessionPool) GetAssignedAddresses(kind SessionKind, hostname string, port int) []string {
	p.mu.RLock()
	m, ok := p.managers[kind]
	p.mu.RUnlock()
	if !ok {
		return nil
	}
	return m.GetAssignedAddresses(hostname, port)
}

func (p *SSHSessionPool) IsConnected(kind SessionKind) bool {
	p.mu.RLock()
	m, ok := p.managers[kind]
	p.mu.RUnlock()
	if !ok {
		return false
	}
	return m.IsConnected()
}

// Connect attempts to (re)connect every currently-open manager. Errors are
// logged but not aggregated.
func (p *SSHSessionPool) Connect() {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for kind, m := range p.managers {
		if m.IsConnected() {
			continue
		}
		if err := m.Connect(); err != nil {
			slog.With("session", kind.String()).Error("connect failed", "error", err)
		}
	}
}

func (p *SSHSessionPool) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.managers {
		m.Stop()
	}
	p.managers = map[SessionKind]sessionManager{}
	p.flags = map[SessionKind]SessionFlags{}
}
