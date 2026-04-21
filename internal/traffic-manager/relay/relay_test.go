package relay

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestBridgePairsAndCopiesBothDirections(t *testing.T) {
	r := NewRegistry()
	agentInner, agentOuter := net.Pipe()
	clientInner, clientOuter := net.Pipe()

	go r.ServePeer(RoleAgent, "s1", agentInner)
	go r.ServePeer(RoleClient, "s1", clientInner)

	go func() { _, _ = agentOuter.Write([]byte("hello")) }()
	if got := mustRead(t, clientOuter, 5); !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("agent->client: got %q", got)
	}

	go func() { _, _ = clientOuter.Write([]byte("world")) }()
	if got := mustRead(t, agentOuter, 5); !bytes.Equal(got, []byte("world")) {
		t.Fatalf("client->agent: got %q", got)
	}

	_ = agentOuter.Close()
	_ = clientOuter.Close()
}

func TestReattachReplacesPreviousPeer(t *testing.T) {
	r := NewRegistry()
	stale, staleOuter := net.Pipe()
	go r.ServePeer(RoleAgent, "s1", stale)
	waitForParkedConn(t, r, "s1", RoleAgent, stale)

	fresh, freshOuter := net.Pipe()
	go r.ServePeer(RoleAgent, "s1", fresh)
	waitForParkedConn(t, r, "s1", RoleAgent, fresh)

	_ = staleOuter.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := staleOuter.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected stale peer closed")
	}

	clientInner, clientOuter := net.Pipe()
	go r.ServePeer(RoleClient, "s1", clientInner)

	go func() { _, _ = freshOuter.Write([]byte("ping")) }()
	if got := mustRead(t, clientOuter, 4); string(got) != "ping" {
		t.Fatalf("got %q", got)
	}

	_ = freshOuter.Close()
	_ = clientOuter.Close()
}

func waitForParkedConn(t *testing.T, r *Registry, id, role string, want net.Conn) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		s := r.sessions[id]
		r.mu.Unlock()
		if s != nil {
			s.mu.Lock()
			cur := s.peers[role]
			s.mu.Unlock()
			if cur == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("conn not parked for role=%s", role)
}

func TestSessionRemovedWhenIdle(t *testing.T) {
	r := NewRegistry()
	a, aOut := net.Pipe()
	c, cOut := net.Pipe()
	go r.ServePeer(RoleAgent, "s1", a)
	go r.ServePeer(RoleClient, "s1", c)

	_ = aOut.Close()
	_ = cOut.Close()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		empty := len(r.sessions) == 0
		r.mu.Unlock()
		if empty {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session not cleaned up")
}

func mustRead(t *testing.T, r net.Conn, n int) []byte {
	t.Helper()
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf
}
