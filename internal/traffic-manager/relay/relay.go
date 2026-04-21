package relay

import (
	"io"
	"net"
	"sync"
)

const (
	RoleAgent  = "agent"
	RoleClient = "client"
)

// Registry pairs agent and client peers attached to the same session and
// bridges raw bytes between them.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	mu    sync.Mutex
	cond  *sync.Cond
	peers map[string]net.Conn
}

func NewRegistry() *Registry {
	return &Registry{sessions: map[string]*session{}}
}

// ServePeer blocks until the partner attaches and the bridge ends. Reattaching
// the same role replaces the previous conn and ends its bridge. Returns true
// if this call ran the bridge, false if the conn was handed off to the partner
// goroutine (the conn stays alive under that goroutine).
func (r *Registry) ServePeer(role, sessionID string, conn net.Conn) bool {
	s := r.acquire(sessionID)
	defer r.release(sessionID, s)

	s.mu.Lock()
	if old := s.peers[role]; old != nil {
		_ = old.Close()
	}

	var partnerRole string
	var partner net.Conn
	for k, v := range s.peers {
		if k != role {
			partnerRole, partner = k, v
			break
		}
	}

	if partner != nil {
		delete(s.peers, partnerRole)
		s.cond.Broadcast()
		s.mu.Unlock()
		bridge(conn, partner)
		return true
	}

	s.peers[role] = conn
	for s.peers[role] == conn {
		s.cond.Wait()
	}
	s.mu.Unlock()
	return false
}

func (r *Registry) acquire(id string) *session {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		s = &session{peers: map[string]net.Conn{}}
		s.cond = sync.NewCond(&s.mu)
		r.sessions[id] = s
	}
	return s
}

func (r *Registry) release(id string, s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[id] != s {
		return
	}
	s.mu.Lock()
	empty := len(s.peers) == 0
	s.mu.Unlock()
	if empty {
		delete(r.sessions, id)
	}
}

func bridge(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}
