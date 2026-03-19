package ssh

// ErrSSHClientNotReady indicates the SSH client is not yet connected.
type ErrSSHClientNotReady struct{}

func (e *ErrSSHClientNotReady) Error() string {
	return "ssh client not connected"
}

// ErrSSHForwardingExists indicates a forwarding with the given key already exists.
type ErrSSHForwardingExists struct {
	Key string
}

func (e *ErrSSHForwardingExists) Error() string {
	return "forwarding already exists: " + e.Key
}

// ErrSSHForwardingNotFound indicates a forwarding with the given key was not found.
type ErrSSHForwardingNotFound struct {
	Key string
}

func (e *ErrSSHForwardingNotFound) Error() string {
	return "forwarding not found: " + e.Key
}

// ErrSSHConnectionFailed indicates the SSH connection failed.
// It wraps the underlying error for proper error chain handling.
type ErrSSHConnectionFailed struct {
	Err error
}

func (e *ErrSSHConnectionFailed) Error() string {
	if e.Err != nil {
		return "failed to connect to SSH server: " + e.Err.Error()
	}
	return "failed to connect to SSH server"
}

// Unwrap returns the underlying error for error chain handling.
func (e *ErrSSHConnectionFailed) Unwrap() error {
	return e.Err
}
