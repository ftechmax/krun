//go:build linux

package service

import (
	"net"
	"testing"
	"time"
)

func TestNormalizeNotifySocketAddress(t *testing.T) {
	if got := normalizeNotifySocketAddress("/run/systemd/notify"); got != "/run/systemd/notify" {
		t.Fatalf("expected filesystem socket path to remain unchanged, got %q", got)
	}

	got := normalizeNotifySocketAddress("@systemd-notify")
	want := "\x00systemd-notify"
	if got != want {
		t.Fatalf("expected abstract socket conversion, want %q got %q", want, got)
	}
}

func TestSDNotifyWritesPayload(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "@systemd-notify")

	var gotAddr net.UnixAddr
	fakeConn := &recordingConn{}
	originalDial := dialNotifySocket
	dialNotifySocket = func(addr *net.UnixAddr) (net.Conn, error) {
		gotAddr = *addr
		return fakeConn, nil
	}
	t.Cleanup(func() {
		dialNotifySocket = originalDial
	})

	if err := sdNotify("READY=1"); err != nil {
		t.Fatalf("sdNotify returned error: %v", err)
	}

	if gotAddr.Net != "unixgram" {
		t.Fatalf("unexpected socket net: %q", gotAddr.Net)
	}
	if gotAddr.Name != "\x00systemd-notify" {
		t.Fatalf("unexpected socket name: %q", gotAddr.Name)
	}
	if got := string(fakeConn.written); got != "READY=1" {
		t.Fatalf("unexpected notify payload: %q", got)
	}
	if !fakeConn.closed {
		t.Fatalf("expected sdNotify to close the connection")
	}
}

type recordingConn struct {
	written []byte
	closed  bool
}

func (c *recordingConn) Read(_ []byte) (int, error) {
	return 0, nil
}

func (c *recordingConn) Write(p []byte) (int, error) {
	c.written = append([]byte(nil), p...)
	return len(p), nil
}

func (c *recordingConn) Close() error {
	c.closed = true
	return nil
}

func (c *recordingConn) LocalAddr() net.Addr {
	return nil
}

func (c *recordingConn) RemoteAddr() net.Addr {
	return nil
}

func (c *recordingConn) SetDeadline(_ time.Time) error {
	return nil
}

func (c *recordingConn) SetReadDeadline(_ time.Time) error {
	return nil
}

func (c *recordingConn) SetWriteDeadline(_ time.Time) error {
	return nil
}
