package session

import (
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/ftechmax/krun/internal/contracts"
)

type DebugSessionRegistry struct {
	mu       sync.RWMutex
	sessions map[string]contracts.DebugSession
}

func NewDebugSessionRegistry() *DebugSessionRegistry {
	return &DebugSessionRegistry{
		sessions: map[string]contracts.DebugSession{},
	}
}

func (s *DebugSessionRegistry) Create(req contracts.CreateDebugSessionRequest) (contracts.DebugSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	serviceName := strings.TrimSpace(req.ServiceName)
	if serviceName == "" {
		return contracts.DebugSession{}, errors.New("invalid payload: service_name is required")
	}
	if req.ServicePort <= 0 {
		return contracts.DebugSession{}, errors.New("invalid payload: service_port must be greater than 0")
	}
	namespace := strings.TrimSpace(req.Namespace)
	if namespace == "" {
		namespace = "default"
	}
	// Supersede any earlier session for the same workload: the new sidecar
	// injection overwrites the old one in place, so the old registry entry
	// can only go stale (e.g. after a helper restart skipped the delete).
	for sessionID, existing := range s.sessions {
		if existing.Namespace == namespace && existing.ServiceName == serviceName {
			delete(s.sessions, sessionID)
		}
	}

	session := contracts.DebugSession{
		SessionID:    "sess_" + rand.Text(),
		SessionToken: rand.Text(),
		Namespace:    namespace,
		ServiceName:  serviceName,
		ServicePort:  req.ServicePort,
	}

	s.sessions[session.SessionID] = session
	return session, nil
}

func (s *DebugSessionRegistry) List() []contracts.DebugSession {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]contracts.DebugSession, 0, len(s.sessions))
	for _, debugSession := range s.sessions {
		list = append(list, debugSession)
	}
	slices.SortFunc(list, func(a, b contracts.DebugSession) int {
		return strings.Compare(a.SessionID, b.SessionID)
	})
	return list
}

func (s *DebugSessionRegistry) Get(sessionID string) (contracts.DebugSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	debugSession, ok := s.sessions[sessionID]
	return debugSession, ok
}

func (s *DebugSessionRegistry) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}
