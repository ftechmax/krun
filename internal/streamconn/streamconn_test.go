package streamconn

import (
	"net"
	"testing"
)

func newPipeConn(t *testing.T) net.Conn {
	t.Helper()
	local, remote := net.Pipe()
	t.Cleanup(func() {
		_ = local.Close()
		_ = remote.Close()
	})
	return local
}

func TestHalfCloseReadThenWrite(t *testing.T) {
	registry := NewRegistry()
	registry.Set("c1", newPipeConn(t))

	if registry.MarkReadClosed("c1") {
		t.Fatal("read close alone must not fully close the connection")
	}
	if registry.Get("c1") == nil {
		t.Fatal("connection must stay registered after read close")
	}
	if !registry.CloseWriteHalf("c1") {
		t.Fatal("write close after read close must fully close the connection")
	}
	if registry.Get("c1") != nil {
		t.Fatal("connection must be removed once both directions are closed")
	}
}

func TestHalfCloseWriteThenRead(t *testing.T) {
	registry := NewRegistry()
	registry.Set("c1", newPipeConn(t))

	if registry.CloseWriteHalf("c1") {
		t.Fatal("write close alone must not fully close the connection")
	}
	if registry.Get("c1") == nil {
		t.Fatal("connection must stay registered after write close")
	}
	if !registry.MarkReadClosed("c1") {
		t.Fatal("read close after write close must fully close the connection")
	}
	if registry.Get("c1") != nil {
		t.Fatal("connection must be removed once both directions are closed")
	}
}

func TestUnknownConnectionIsNoop(t *testing.T) {
	registry := NewRegistry()
	if registry.MarkReadClosed("missing") || registry.CloseWriteHalf("missing") {
		t.Fatal("unknown connection ids must report not-fully-closed")
	}
	registry.CloseAndDelete("missing")
}

func TestSetReplacesPreviousConnection(t *testing.T) {
	registry := NewRegistry()
	first := newPipeConn(t)
	second := newPipeConn(t)

	registry.Set("c1", first)
	registry.Set("c1", second)

	if registry.Get("c1") != second {
		t.Fatal("expected replacement connection to be registered")
	}
	if _, err := first.Write([]byte("x")); err == nil {
		t.Fatal("expected previous connection to be closed")
	}
}
