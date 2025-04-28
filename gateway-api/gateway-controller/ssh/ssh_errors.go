package ssh

import "fmt"

type ErrSSHClientNotReady struct {
}

func (e *ErrSSHClientNotReady) Error() string {
	return "ssh client not connected"
}

type ErrSSHForwardingExists struct {
	Key string
}

func (e *ErrSSHForwardingExists) Error() string {
	return fmt.Sprintf("forwarding already exists: %s", e.Key)
}

type ErrSSHForwardingNotFound struct {
	Key string
}

func (e *ErrSSHForwardingNotFound) Error() string {
	return fmt.Sprintf("forwarding not found: %s", e.Key)
}

type ErrSSHConnectionFailed struct {
	Err error
}

func (e *ErrSSHConnectionFailed) Error() string {
	return fmt.Sprintf("failed to connect to SSH server: %v", e.Err)
}
