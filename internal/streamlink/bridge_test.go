package streamlink

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
)

func TestPumpConnDataThenEOF(t *testing.T) {
	link := testQueueLink(t)
	conns := NewConns()
	local, remote := net.Pipe()
	conns.Set("c1", local)
	t.Cleanup(func() { _ = remote.Close() })

	done := make(chan struct{})
	go func() {
		PumpConn(link, conns, "c1", local)
		close(done)
	}()
	if _, err := remote.Write([]byte("hello")); err != nil {
		t.Fatalf("write pipe: %v", err)
	}
	if err := remote.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	assertEnvelopeTypes(t, link.sendCh, contracts.StreamTypeData, contracts.StreamTypeCloseWrite)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PumpConn did not return after EOF")
	}
}

func TestPumpConnPreservesDataErrorCloseOrdering(t *testing.T) {
	link := testQueueLink(t)
	conns := NewConns()
	readErr := errors.New("read failed")
	conn := &recordingConn{readData: []byte("partial"), readErr: readErr}
	conns.Set("c1", conn)

	PumpConn(link, conns, "c1", conn)
	assertEnvelopeTypes(t, link.sendCh, contracts.StreamTypeData, contracts.StreamTypeError, contracts.StreamTypeClose)
	if conns.Get("c1") != nil {
		t.Fatal("failed connection remained registered")
	}
}

func TestApplyInboundDataAndLifecycle(t *testing.T) {
	link := testQueueLink(t)
	conns := NewConns()
	conn := &recordingConn{}
	conns.Set("c1", conn)

	if !ApplyInbound(link, conns, contracts.StreamEnvelope{
		Type: contracts.StreamTypeData, ConnectionID: "c1", Data: []byte("hello"),
	}) {
		t.Fatal("data envelope was not consumed")
	}
	if string(conn.written) != "hello" {
		t.Fatalf("written data = %q", conn.written)
	}
	if !ApplyInbound(link, conns, contracts.StreamEnvelope{
		Type: contracts.StreamTypeCloseWrite, ConnectionID: "c1",
	}) {
		t.Fatal("close-write envelope was not consumed")
	}
	if !conn.writeClosed {
		t.Fatal("close-write did not half-close the connection")
	}
	if !ApplyInbound(link, conns, contracts.StreamEnvelope{
		Type: contracts.StreamTypeClose, ConnectionID: "c1",
	}) {
		t.Fatal("close envelope was not consumed")
	}
	if !conn.closed || conns.Get("c1") != nil {
		t.Fatal("close did not remove and close the connection")
	}
}

func TestApplyInboundWriteErrorSendsErrorThenClose(t *testing.T) {
	link := testQueueLink(t)
	conns := NewConns()
	conn := &recordingConn{writeErr: errors.New("write failed")}
	conns.Set("c1", conn)

	ApplyInbound(link, conns, contracts.StreamEnvelope{
		Type: contracts.StreamTypeData, ConnectionID: "c1", Data: []byte("hello"),
	})
	assertEnvelopeTypes(t, link.sendCh, contracts.StreamTypeError, contracts.StreamTypeClose)
	if !conn.closed || conns.Get("c1") != nil {
		t.Fatal("write failure did not remove and close the connection")
	}
}

func TestApplyInboundErrorClosesConnection(t *testing.T) {
	link := testQueueLink(t)
	conns := NewConns()
	conn := &recordingConn{}
	conns.Set("c1", conn)

	if !ApplyInbound(link, conns, contracts.StreamEnvelope{
		Type: contracts.StreamTypeError, ConnectionID: "c1", Message: "peer failed",
	}) {
		t.Fatal("error envelope was not consumed")
	}
	if !conn.closed || conns.Get("c1") != nil {
		t.Fatal("error envelope did not close the connection")
	}
}

func testQueueLink(t *testing.T) *Link {
	t.Helper()
	link := New(context.Background(), Config{SessionID: "session", QueueSize: 8})
	t.Cleanup(link.cancel)
	return link
}

func assertEnvelopeTypes(t *testing.T, envelopes <-chan contracts.StreamEnvelope, want ...string) {
	t.Helper()
	for index, envelopeType := range want {
		select {
		case envelope := <-envelopes:
			if envelope.Type != envelopeType {
				t.Fatalf("envelope %d type = %q, want %q", index, envelope.Type, envelopeType)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for envelope %d (%s)", index, envelopeType)
		}
	}
}

type recordingConn struct {
	readData    []byte
	readErr     error
	written     []byte
	writeErr    error
	closed      bool
	writeClosed bool
}

func (c *recordingConn) Read(buffer []byte) (int, error) {
	if len(c.readData) == 0 {
		if c.readErr == nil {
			return 0, io.EOF
		}
		err := c.readErr
		c.readErr = io.EOF
		return 0, err
	}
	n := copy(buffer, c.readData)
	c.readData = c.readData[n:]
	err := c.readErr
	c.readErr = io.EOF
	return n, err
}

func (c *recordingConn) Write(data []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	c.written = append(c.written, data...)
	return len(data), nil
}

func (c *recordingConn) Close() error                     { c.closed = true; return nil }
func (c *recordingConn) CloseWrite() error                { c.writeClosed = true; return nil }
func (c *recordingConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *recordingConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *recordingConn) SetDeadline(time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(time.Time) error { return nil }

type testAddr string

func (a testAddr) Network() string { return "test" }
func (a testAddr) String() string  { return string(a) }
