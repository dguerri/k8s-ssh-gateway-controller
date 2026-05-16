package ssh

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// This file contains fake/mock implementations used for testing.
// These implementations provide minimal functionality to support unit tests
// without requiring actual network connections or SSH servers.

// fakeAddr represents a fake network address for testing.
type fakeAddr struct{}

// Network returns the network type.
func (a *fakeAddr) Network() string { return "tcp" }

// String returns the address as a string.
func (a *fakeAddr) String() string { return "" }

// fakeNetConn represents a fake network connection.
type fakeNetConn struct {
	readOnce sync.Once
}

// Close closes the connection.
func (f *fakeNetConn) Close() error { return nil }

// Read reads data from the connection.
func (f *fakeNetConn) Read([]byte) (int, error) {
	var err error
	f.readOnce.Do(func() {
		err = io.EOF
	})
	if err != nil {
		return 0, err
	}
	return 0, nil
}

// Write writes data to the connection.
func (f *fakeNetConn) Write([]byte) (int, error) { return 0, nil }

// LocalAddr returns the local address.
func (f *fakeNetConn) LocalAddr() net.Addr { return &fakeAddr{} }

// RemoteAddr returns the remote address.
func (f *fakeNetConn) RemoteAddr() net.Addr { return &fakeAddr{} }

// SetDeadline sets the deadline for the connection.
func (f *fakeNetConn) SetDeadline(time.Time) error { return nil }

// SetReadDeadline sets the read deadline for the connection.
func (f *fakeNetConn) SetReadDeadline(time.Time) error { return nil }

// SetWriteDeadline sets the write deadline for the connection.
func (f *fakeNetConn) SetWriteDeadline(time.Time) error { return nil }

// fakeClient represents a fake SSH client for testing.
// It implements the sshClient interface with customizable behavior.
type fakeClient struct {
	// sendRequestFunc allows tests to customize the behavior of SendRequest.
	// If nil, SendRequest returns success by default.
	sendRequestFunc func(name string, wantReply bool, payload []byte) (bool, []byte, error)

	// customNewChannel, if set, is the ssh.NewChannel emitted by
	// HandleChannelOpen instead of the default fakeNewSshChannel.
	customNewChannel ssh.NewChannel
}

// Listen listens for incoming connections.
func (f *fakeClient) Listen(network, addr string) (net.Listener, error) { return &fakeListener{}, nil }

// SendRequest sends a request to the server.
// If sendRequestFunc is set, it uses that; otherwise returns success.
func (f *fakeClient) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	// Use custom function if provided
	if f.sendRequestFunc != nil {
		return f.sendRequestFunc(name, wantReply, payload)
	}

	return true, nil, nil
}

// fakeNewSshChannel is a mock implementation of ssh.NewChannel.
type fakeNewSshChannel struct {
	channelType string
	extraData   []byte
}

type fakeSshChannel struct {
	readOnce sync.Once
}

func (f *fakeSshChannel) Read(b []byte) (int, error) {
	var err error
	f.readOnce.Do(func() {
		err = io.EOF
	})
	if err != nil {
		return 0, err
	}
	return 0, nil
}

func (f *fakeSshChannel) Write(b []byte) (int, error) {
	return 0, nil
}

func (f *fakeSshChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return true, nil
}

func (f *fakeSshChannel) Close() error {
	return nil
}
func (f *fakeSshChannel) CloseWrite() error {
	return nil
}

func (f *fakeSshChannel) Stderr() io.ReadWriter {
	return &fakeReadWriter{}
}

// fakeReadWriter is a helper struct to implement io.ReadWriter.
type fakeReadWriter struct{}

func (rw *fakeReadWriter) Read(p []byte) (n int, err error) {
	return 0, nil
}

func (rw *fakeReadWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}

// ChannelType returns the type of the channel.
func (f *fakeNewSshChannel) ChannelType() string {
	return f.channelType
}

// ExtraData returns the extra data sent with the channel request.
func (f *fakeNewSshChannel) ExtraData() []byte {
	return f.extraData
}

// Accept simulates accepting the channel and returns a fake connection.
func (f *fakeNewSshChannel) Accept() (ssh.Channel, <-chan *ssh.Request, error) {
	fakeConn := &fakeSshChannel{}
	requests := make(chan *ssh.Request)
	close(requests) // Close the requests channel immediately as we don't send any fake requests
	return fakeConn, requests, nil
}

// Reject simulates rejecting the channel.
func (f *fakeNewSshChannel) Reject(reason ssh.RejectionReason, message string) error {
	return fmt.Errorf("channel rejected: %s", message)
}

// HandleChannelOpen simulates handling channel open requests and keeps producing fakeNewSshChannels.
func (f *fakeClient) HandleChannelOpen(channelType string) <-chan ssh.NewChannel {
	ch := make(chan ssh.NewChannel, 1) // Buffered channel for one message
	go func() {
		defer close(ch) // Close the channel when done
		if f.customNewChannel != nil {
			ch <- f.customNewChannel
			return
		}
		ch <- &fakeNewSshChannel{channelType: channelType, extraData: ssh.Marshal(forwardedTCPPayload{
			Addr: "0.0.0.0",
			Port: 2222,
		})}
	}()
	return ch
}

// Close closes the client.
func (f *fakeClient) Close() error { return nil }

// fakeListener represents a fake listener.
type fakeListener struct {
	firstConn  net.Conn
	acceptOnce sync.Once
}

// Accept accepts an incoming connection.
func (l *fakeListener) Accept() (net.Conn, error) {
	l.firstConn = nil
	l.acceptOnce.Do(func() {
		clientConn, serverConn := net.Pipe()
		l.firstConn = clientConn
		go func() {
			defer func() { _ = clientConn.Close() }()
			defer func() { _ = serverConn.Close() }()
			_, _ = io.Copy(serverConn, clientConn)
		}()
	})
	if l.firstConn != nil {
		return l.firstConn, nil
	}
	// Simulate blocking forever on subsequent calls.
	select {}
}

// Close closes the listener.
func (l *fakeListener) Close() error { return nil }

// Addr returns the listener's address.
func (l *fakeListener) Addr() net.Addr { return &fakeAddr{} }

// blockingSshChannel is an ssh.Channel whose Read blocks until Close is called.
// It is used to simulate a peer that has not yet hung up (e.g. an SSH server
// holding a public-facing TCP connection open while waiting for the client to
// FIN). It also counts Close and CloseWrite invocations so tests can assert
// that the channel was fully torn down rather than only half-closed.
type blockingSshChannel struct {
	closed          chan struct{}
	closeOnce       sync.Once
	closeCalls      atomic.Int32
	closeWriteCalls atomic.Int32
}

func newBlockingSshChannel() *blockingSshChannel {
	return &blockingSshChannel{closed: make(chan struct{})}
}

func (b *blockingSshChannel) Read(p []byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *blockingSshChannel) Write(p []byte) (int, error) {
	select {
	case <-b.closed:
		return 0, io.ErrClosedPipe
	default:
		return len(p), nil
	}
}

func (b *blockingSshChannel) Close() error {
	b.closeCalls.Add(1)
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func (b *blockingSshChannel) CloseWrite() error {
	b.closeWriteCalls.Add(1)
	return nil
}

func (b *blockingSshChannel) SendRequest(string, bool, []byte) (bool, error) {
	return true, nil
}

func (b *blockingSshChannel) Stderr() io.ReadWriter { return &fakeReadWriter{} }

// fakeNewSshChannelWithAccept is a fake ssh.NewChannel whose Accept returns a
// caller-provided ssh.Channel, letting tests inject custom Read/Write/Close
// behavior on the channel returned to handleChannels.
type fakeNewSshChannelWithAccept struct {
	channelType string
	extraData   []byte
	channel     ssh.Channel
}

func (f *fakeNewSshChannelWithAccept) ChannelType() string { return f.channelType }
func (f *fakeNewSshChannelWithAccept) ExtraData() []byte   { return f.extraData }
func (f *fakeNewSshChannelWithAccept) Accept() (ssh.Channel, <-chan *ssh.Request, error) {
	requests := make(chan *ssh.Request)
	close(requests)
	return f.channel, requests, nil
}
func (f *fakeNewSshChannelWithAccept) Reject(_ ssh.RejectionReason, message string) error {
	return fmt.Errorf("channel rejected: %s", message)
}

// trackingNetConn is a net.Conn that EOFs on first Read and records Close
// calls, exposing a `closed` channel that fires the first time Close is
// invoked. Used as the "backend" connection in handleChannels tests where the
// backend has already torn down its TCP socket.
type trackingNetConn struct {
	readOnce   sync.Once
	closeOnce  sync.Once
	closed     chan struct{}
	closeCalls atomic.Int32
}

func newTrackingNetConn() *trackingNetConn {
	return &trackingNetConn{closed: make(chan struct{})}
}

func (t *trackingNetConn) Read(p []byte) (int, error) {
	var err error
	t.readOnce.Do(func() { err = io.EOF })
	if err != nil {
		return 0, err
	}
	<-t.closed
	return 0, io.EOF
}

func (t *trackingNetConn) Write(p []byte) (int, error) { return len(p), nil }

func (t *trackingNetConn) Close() error {
	t.closeCalls.Add(1)
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

func (t *trackingNetConn) LocalAddr() net.Addr              { return &fakeAddr{} }
func (t *trackingNetConn) RemoteAddr() net.Addr             { return &fakeAddr{} }
func (t *trackingNetConn) SetDeadline(time.Time) error      { return nil }
func (t *trackingNetConn) SetReadDeadline(time.Time) error  { return nil }
func (t *trackingNetConn) SetWriteDeadline(time.Time) error { return nil }
