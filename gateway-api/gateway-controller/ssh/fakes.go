package ssh

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeAddr represents a fake network address.
type fakeAddr struct{}

// Network returns the network type.
func (a *fakeAddr) Network() string { return "tcp" }

// String returns the address as a string.
func (a *fakeAddr) String() string { return "" }

// fakeNetConn represents a fake network connection.
type fakeNetConn struct{}

// Close closes the connection.
func (f *fakeNetConn) Close() error { return nil }

// Read reads data from the connection.
func (f *fakeNetConn) Read([]byte) (int, error) { return 0, nil }

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

// fakeClient represents a fake SSH client.
type fakeClient struct{}

// Listen listens for incoming connections.
func (f *fakeClient) Listen(network, addr string) (net.Listener, error) { return &fakeListener{}, nil }

type sendRequestCalledWithParams struct {
	FakeClient *fakeClient
	Name       string
	WantReply  bool
	Payload    []byte
}

var fakeSendRequestCalledWith []sendRequestCalledWithParams

// SendRequest sends a request to the server.
func (f *fakeClient) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	fakeSendRequestCalledWith = append(fakeSendRequestCalledWith, sendRequestCalledWithParams{
		FakeClient: f,
		Name:       name,
		WantReply:  wantReply,
		Payload:    payload,
	})
	return true, nil, nil
}

// fakeNewSshChannel is a mock implementation of ssh.NewChannel.
type fakeNewSshChannel struct {
	channelType string
	extraData   []byte
}

type fakeSshChannel struct {
}

func (f *fakeSshChannel) Read(b []byte) (int, error) {
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
	return fakeConn, requests, nil
}

// Reject simulates rejecting the channel.
func (f *fakeNewSshChannel) Reject(reason ssh.RejectionReason, message string) error {
	return fmt.Errorf("channel rejected: %s", message)
}

// HandleChannelOpen simulates handling channel open requests and keeps producing fakeNewSshChannels.
func (f *fakeClient) HandleChannelOpen(channelType string) <-chan ssh.NewChannel {
	ch := make(chan ssh.NewChannel)
	go func() {
		for {
			ch <- &fakeNewSshChannel{channelType: channelType, extraData: ssh.Marshal(forwardedTCPPayload{
				Addr: "0.0.0.0",
				Port: 2222,
			})}
		}
	}()
	return ch
}

// Close closes the client.
func (f *fakeClient) Close() error { return nil }

// fakeListener represents a fake listener.
type fakeListener struct {
	acceptOnce sync.Once
	firstConn  net.Conn
}

// Accept accepts an incoming connection.
func (l *fakeListener) Accept() (net.Conn, error) {
	l.firstConn = nil
	l.acceptOnce.Do(func() {
		clientConn, serverConn := net.Pipe()
		l.firstConn = clientConn
		go func() {
			defer clientConn.Close()
			defer serverConn.Close()
			io.Copy(serverConn, clientConn)
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
