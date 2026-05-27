package ssh

import (
	"errors"
	"testing"
)

func TestErrSSHClientNotReady(t *testing.T) {
	err := &ErrSSHClientNotReady{}
	if got, want := err.Error(), "ssh client not connected"; got != want {
		t.Errorf("unexpected error message: got %q, want %q", got, want)
	}
}

func TestErrSSHForwardingExists(t *testing.T) {
	err := &ErrSSHForwardingExists{Key: "localhost:2222"}
	if got, want := err.Error(), "forwarding already exists: localhost:2222"; got != want {
		t.Errorf("unexpected error message: got %q, want %q", got, want)
	}
}

func TestErrSSHForwardingNotFound(t *testing.T) {
	err := &ErrSSHForwardingNotFound{Key: "localhost:2222"}
	if got, want := err.Error(), "forwarding not found: localhost:2222"; got != want {
		t.Errorf("unexpected error message: got %q, want %q", got, want)
	}
}

func TestErrSSHConnectionFailedWithWrappedErr(t *testing.T) {
	inner := errors.New("dial tcp: connection refused")
	err := &ErrSSHConnectionFailed{Err: inner}
	wantMsg := "failed to connect to SSH server: dial tcp: connection refused"
	if got := err.Error(); got != wantMsg {
		t.Errorf("unexpected error message: got %q, want %q", got, wantMsg)
	}
	if !errors.Is(err, inner) {
		t.Errorf("errors.Is(err, inner) = false, want true (Unwrap should expose the inner error)")
	}
	if errors.Unwrap(err) != inner {
		t.Errorf("errors.Unwrap returned a different error than the wrapped one")
	}
}

func TestErrSSHConnectionFailedWithNilErr(t *testing.T) {
	err := &ErrSSHConnectionFailed{Err: nil}
	if got, want := err.Error(), "failed to connect to SSH server"; got != want {
		t.Errorf("unexpected error message: got %q, want %q", got, want)
	}
	if errors.Unwrap(err) != nil {
		t.Errorf("errors.Unwrap on nil-wrapped ErrSSHConnectionFailed should return nil")
	}
}
