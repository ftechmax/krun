package session

import (
	"slices"
	"strings"
	"sync"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/sessionkey"
)

type DebugSessionRegistry struct {
	mu       sync.RWMutex
	sessions map[string]contracts.DebugServiceContext
}

func NewDebugSessionRegistry() *DebugSessionRegistry {
	return &DebugSessionRegistry{
		sessions: map[string]contracts.DebugServiceContext{},
	}
}

func (r *DebugSessionRegistry) Upsert(sessionKey string, context contracts.DebugServiceContext) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := sessionkey.Trim(sessionKey)
	if key == "" {
		return
	}
	r.sessions[key] = context
}

func (r *DebugSessionRegistry) Has(sessionKey string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := sessionkey.Trim(sessionKey)
	_, ok := r.sessions[key]
	return ok
}

func (r *DebugSessionRegistry) Remove(sessionKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := sessionkey.Trim(sessionKey)
	if key == "" {
		r.sessions = map[string]contracts.DebugServiceContext{}
		return
	}
	delete(r.sessions, key)
}

func (r *DebugSessionRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = map[string]contracts.DebugServiceContext{}
}

func (r *DebugSessionRegistry) List() []contracts.HelperDebugSession {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]contracts.HelperDebugSession, 0, len(r.sessions))
	for sessionKey, context := range r.sessions {
		result = append(result, contracts.HelperDebugSession{
			SessionKey: sessionKey,
			Context:    context,
		})
	}
	slices.SortFunc(result, func(a, b contracts.HelperDebugSession) int {
		return strings.Compare(a.SessionKey, b.SessionKey)
	})
	return result
}
