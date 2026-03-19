package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/sessionkey"
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
	if req.LocalPort <= 0 {
		return contracts.DebugSession{}, errors.New("invalid payload: local_port must be greater than 0")
	}

	namespace := strings.TrimSpace(req.Namespace)
	if namespace == "" {
		namespace = "default"
	}
	workload := strings.TrimSpace(req.Workload)
	if workload == "" {
		workload = serviceName
	}

	session := contracts.DebugSession{
		SessionID:    "sess_" + randomHex(8),
		SessionToken: randomHex(16),
		Namespace:    namespace,
		ServiceName:  serviceName,
		Workload:     workload,
		ServicePort:  req.ServicePort,
		LocalPort:    req.LocalPort,
		ClientID:     strings.TrimSpace(req.ClientID),
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if session.ClientID == "" {
		session.ClientID = "unknown"
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
		if cmp := strings.Compare(a.CreatedAt, b.CreatedAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.SessionID, b.SessionID)
	})
	return list
}

func (s *DebugSessionRegistry) Get(sessionID string) (contracts.DebugSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := sessionkey.Trim(sessionID)
	if key == "" {
		return contracts.DebugSession{}, false
	}
	debugSession, ok := s.sessions[key]
	if !ok {
		return contracts.DebugSession{}, false
	}
	return debugSession, true
}

func (s *DebugSessionRegistry) Delete(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := sessionkey.Trim(sessionID)
	if key == "" {
		return false
	}
	if _, ok := s.sessions[key]; !ok {
		return false
	}
	delete(s.sessions, key)
	return true
}

func (s *DebugSessionRegistry) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = map[string]contracts.DebugSession{}
}

func randomHex(length int) string {
	buffer := make([]byte, length)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}
