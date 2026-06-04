package ssh

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// stubManager is a minimal stand-in used by pool tests.
type stubManager struct {
	label              string
	started, stopped   []ForwardingConfig
	connected          bool
	stopForwardingErr  error
	startForwardingErr error
	stopCalls          int
	mu                 sync.Mutex
}

func (s *stubManager) StartForwarding(c ForwardingConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = append(s.started, c)
	return s.startForwardingErr
}
func (s *stubManager) StopForwarding(c *ForwardingConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = append(s.stopped, *c)
	return s.stopForwardingErr
}
func (s *stubManager) GetAssignedAddresses(h string, p int) []string { return nil }
func (s *stubManager) IsConnected() bool                             { return s.connected }
func (s *stubManager) Stop()                                         { s.mu.Lock(); s.stopCalls++; s.mu.Unlock() }
func (s *stubManager) Connect() error                                { s.connected = true; return nil }

// newTestPool wires up the pool with a stub-producing factory.
func newTestPool() (*SSHSessionPool, *stubManager, map[SessionFlags]*stubManager) {
	plain := &stubManager{label: "plain", connected: true}
	built := map[SessionFlags]*stubManager{{}: plain}
	factory := func(_ context.Context, flags SessionFlags, label string) (sessionManager, error) {
		if m, ok := built[flags]; ok {
			return m, nil
		}
		m := &stubManager{label: label, connected: true}
		built[flags] = m
		return m, nil
	}
	p, err := newSessionPoolWithFactory(context.Background(), factory)
	if err != nil {
		panic(err)
	}
	return p, plain, built
}

func TestSSHSessionPool_OpensPlainEagerly(t *testing.T) {
	p, plain, _ := newTestPool()
	if !p.IsConnected(SessionPlain) {
		t.Fatal("plain session should be connected eagerly")
	}
	_ = plain
}

func TestSSHSessionPool_ConfigureSessionsOpensPP(t *testing.T) {
	p, _, built := newTestPool()
	if err := p.ConfigureSessions(2, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := built[SessionFlags{ProxyProtocolVersion: 2}]; !ok {
		t.Fatal("PP manager should have been constructed")
	}
	if !p.IsConnected(SessionProxyProto) {
		t.Fatal("pp session should be connected")
	}
}

func TestSSHSessionPool_ConfigureSessionsOpensSNI(t *testing.T) {
	p, _, built := newTestPool()
	if err := p.ConfigureSessions(0, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := built[SessionFlags{SNIProxy: true}]; !ok {
		t.Fatal("SNI manager should have been constructed")
	}
	if !p.IsConnected(SessionSNIProxy) {
		t.Fatal("sni session should be connected")
	}
}

func TestSSHSessionPool_ConfigureSessionsTearsDownDisabled(t *testing.T) {
	p, _, built := newTestPool()
	if err := p.ConfigureSessions(2, true); err != nil {
		t.Fatal(err)
	}
	pp := built[SessionFlags{ProxyProtocolVersion: 2}]
	if err := p.ConfigureSessions(0, true); err != nil {
		t.Fatal(err)
	}
	if pp.stopCalls == 0 {
		t.Fatal("PP manager should have been Stop()-ed when disabled")
	}
	if p.IsConnected(SessionProxyProto) {
		t.Fatal("pp session should report not connected after teardown")
	}
}

func TestSSHSessionPool_ConfigureSessionsPPVersionChangeRebuilds(t *testing.T) {
	p, _, built := newTestPool()
	if err := p.ConfigureSessions(1, false); err != nil {
		t.Fatal(err)
	}
	v1 := built[SessionFlags{ProxyProtocolVersion: 1}]
	if err := p.ConfigureSessions(2, false); err != nil {
		t.Fatal(err)
	}
	if v1.stopCalls == 0 {
		t.Fatal("v1 manager should be stopped when version changes")
	}
	if _, ok := built[SessionFlags{ProxyProtocolVersion: 2}]; !ok {
		t.Fatal("v2 manager should be constructed")
	}
}

func TestSSHSessionPool_StartForwardingRoutesByKind(t *testing.T) {
	p, plain, built := newTestPool()
	if err := p.ConfigureSessions(2, true); err != nil {
		t.Fatal(err)
	}
	pp := built[SessionFlags{ProxyProtocolVersion: 2}]
	sni := built[SessionFlags{SNIProxy: true}]

	cfg := ForwardingConfig{RemoteHost: "h", RemotePort: 1, InternalHost: "i", InternalPort: 2}
	if err := p.StartForwarding(SessionPlain, cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.StartForwarding(SessionProxyProto, cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.StartForwarding(SessionSNIProxy, cfg); err != nil {
		t.Fatal(err)
	}
	if len(plain.started) != 1 || len(pp.started) != 1 || len(sni.started) != 1 {
		t.Fatalf("unexpected routing: plain=%d pp=%d sni=%d", len(plain.started), len(pp.started), len(sni.started))
	}
}

func TestSSHSessionPool_StartForwardingDisabledReturnsTypedError(t *testing.T) {
	p, _, _ := newTestPool()
	err := p.StartForwarding(SessionSNIProxy, ForwardingConfig{})
	var notEnabled *ErrSessionNotEnabled
	if !errors.As(err, &notEnabled) {
		t.Fatalf("expected ErrSessionNotEnabled, got %v", err)
	}
	if notEnabled.Kind != "sni" {
		t.Errorf("expected Kind=sni, got %s", notEnabled.Kind)
	}
}

func TestSSHSessionPool_StopForwardingDisabledReturnsTypedError(t *testing.T) {
	p, _, _ := newTestPool()
	err := p.StopForwarding(SessionSNIProxy, &ForwardingConfig{})
	var notEnabled *ErrSessionNotEnabled
	if !errors.As(err, &notEnabled) {
		t.Fatalf("expected ErrSessionNotEnabled, got %v", err)
	}
}

func TestSSHSessionPool_GetAssignedAddressesDisabledReturnsNil(t *testing.T) {
	p, _, _ := newTestPool()
	if got := p.GetAssignedAddresses(SessionSNIProxy, "h", 1); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestSSHSessionPool_StopClosesAll(t *testing.T) {
	p, plain, built := newTestPool()
	if err := p.ConfigureSessions(2, true); err != nil {
		t.Fatal(err)
	}
	p.Stop()
	if plain.stopCalls == 0 || built[SessionFlags{ProxyProtocolVersion: 2}].stopCalls == 0 || built[SessionFlags{SNIProxy: true}].stopCalls == 0 {
		t.Fatal("Stop should tear down all managers")
	}
}

func TestSessionKindString_PoolPackage(t *testing.T) {
	// Verifies the package-local SessionKind enum's stringer.
	if SessionPlain.String() != "plain" || SessionProxyProto.String() != "pp" || SessionSNIProxy.String() != "sni" {
		t.Fatal("unexpected SessionKind strings")
	}
	if SessionKind(99).String() != "unknown" {
		t.Fatal("unexpected default")
	}
}

// newTestPoolWithFactory wires up a pool around a caller-provided factory,
// useful when a test needs control over the per-call factory behavior
// (e.g. simulating a Connect failure on a specific kind).
func newTestPoolWithFactory(t *testing.T, factory managerFactory) *SSHSessionPool {
	t.Helper()
	p, err := newSessionPoolWithFactory(context.Background(), factory)
	if err != nil {
		t.Fatalf("pool constructor failed: %v", err)
	}
	return p
}

func TestNewSSHSessionPool_InvalidPrivateKeyReturnsError(t *testing.T) {
	// Exercises the production NewSSHSessionPool: with an invalid key the
	// factory delegate (NewSSHTunnelManager) returns an error, so the pool
	// constructor must surface it.
	cfg := &SSHConnectionConfig{
		ServerAddress: "localhost:22",
		Username:      "u",
		PrivateKey:    []byte("not a key"),
	}
	if _, err := NewSSHSessionPool(context.Background(), cfg); err == nil {
		t.Fatal("expected NewSSHSessionPool to fail with invalid key")
	}
}

func TestSSHSessionPool_NewPoolPropagatesFactoryErrorForPlain(t *testing.T) {
	wantErr := errors.New("boom")
	factory := func(_ context.Context, _ SessionFlags, _ string) (sessionManager, error) {
		return nil, wantErr
	}
	_, err := newSessionPoolWithFactory(context.Background(), factory)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, err)
	}
}

func TestSSHSessionPool_NewPoolLogsConnectFailureForPlain(t *testing.T) {
	// Plain manager whose Connect() always errors — the constructor must
	// still succeed (initial Connect failures are non-fatal; we retry later).
	failing := &failingConnectStub{}
	factory := func(_ context.Context, _ SessionFlags, _ string) (sessionManager, error) {
		return failing, nil
	}
	p := newTestPoolWithFactory(t, factory)
	if p == nil {
		t.Fatal("pool should be returned even when plain Connect fails")
	}
	if failing.connectCalls != 1 {
		t.Fatalf("expected one Connect attempt on plain, got %d", failing.connectCalls)
	}
}

func TestSSHSessionPool_ConfigureSessionsFactoryErrorPropagated(t *testing.T) {
	// Plain succeeds, PP fails to construct.
	call := 0
	wantErr := errors.New("pp factory failure")
	factory := func(_ context.Context, _ SessionFlags, _ string) (sessionManager, error) {
		call++
		if call == 1 {
			return &stubManager{connected: true}, nil
		}
		return nil, wantErr
	}
	p := newTestPoolWithFactory(t, factory)
	err := p.ConfigureSessions(2, false)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped factory error, got %v", err)
	}
}

func TestSSHSessionPool_ConnectReconnectsOpenManagers(t *testing.T) {
	// Two stubs: one already connected (skipped), one disconnected (reconnected).
	plain := &stubManager{connected: true}
	pp := &reconnectStub{}
	call := 0
	factory := func(_ context.Context, _ SessionFlags, _ string) (sessionManager, error) {
		call++
		if call == 1 {
			return plain, nil
		}
		return pp, nil
	}
	p := newTestPoolWithFactory(t, factory)
	if err := p.ConfigureSessions(2, false); err != nil {
		t.Fatal(err)
	}
	// Reset counters after construction (which calls Connect once).
	plainBefore := plain.stopCalls // (unused; placeholder if needed)
	_ = plainBefore
	pp.connectCallsAfterCtor = 0
	pp.connected = false
	p.Connect()
	if pp.connectCallsAfterCtor != 1 {
		t.Fatalf("expected disconnected manager to be reconnected; got %d calls", pp.connectCallsAfterCtor)
	}
}

func TestSSHSessionPool_ConnectLogsErrors(t *testing.T) {
	// A manager whose Connect always fails — Connect() must continue without panicking.
	failing := &failingConnectStub{}
	factory := func(_ context.Context, _ SessionFlags, _ string) (sessionManager, error) {
		return failing, nil
	}
	p := newTestPoolWithFactory(t, factory)
	failing.connectCalls = 0
	p.Connect()
	if failing.connectCalls != 1 {
		t.Fatalf("expected one Connect retry, got %d", failing.connectCalls)
	}
}

// failingConnectStub is like stubManager but Connect always errors.
type failingConnectStub struct {
	connectCalls int
}

func (s *failingConnectStub) StartForwarding(ForwardingConfig) error    { return nil }
func (s *failingConnectStub) StopForwarding(*ForwardingConfig) error    { return nil }
func (s *failingConnectStub) GetAssignedAddresses(string, int) []string { return nil }
func (s *failingConnectStub) IsConnected() bool                         { return false }
func (s *failingConnectStub) Stop()                                     {}
func (s *failingConnectStub) Connect() error {
	s.connectCalls++
	return errors.New("connect refused")
}

// reconnectStub starts disconnected after construction so that pool.Connect()
// triggers a real Connect call we can count.
type reconnectStub struct {
	connected             bool
	connectCallsAfterCtor int
}

func (s *reconnectStub) StartForwarding(ForwardingConfig) error    { return nil }
func (s *reconnectStub) StopForwarding(*ForwardingConfig) error    { return nil }
func (s *reconnectStub) GetAssignedAddresses(string, int) []string { return nil }
func (s *reconnectStub) IsConnected() bool                         { return s.connected }
func (s *reconnectStub) Stop()                                     {}
func (s *reconnectStub) Connect() error {
	s.connectCallsAfterCtor++
	s.connected = true
	return nil
}
