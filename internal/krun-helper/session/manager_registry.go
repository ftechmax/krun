package session

import (
	"strings"
	"sync"
)

type ManagerSessionRegistry struct {
	mu         sync.Mutex
	sessionIDs map[string]string
}

func NewManagerSessionRegistry() *ManagerSessionRegistry {
	return &ManagerSessionRegistry{
		sessionIDs: map[string]string{},
	}
}

func (r *ManagerSessionRegistry) Upsert(sessionKey string, managerSessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := strings.TrimSpace(sessionKey)
	id := strings.TrimSpace(managerSessionID)
	if key == "" || id == "" {
		return
	}
	r.sessionIDs[key] = id
}

func (r *ManagerSessionRegistry) Get(sessionKey string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := strings.TrimSpace(sessionKey)
	if key == "" {
		return "", false
	}
	value, ok := r.sessionIDs[key]
	return value, ok
}

func (r *ManagerSessionRegistry) Remove(sessionKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := strings.TrimSpace(sessionKey)
	if key == "" {
		r.sessionIDs = map[string]string{}
		return
	}
	delete(r.sessionIDs, key)
}

func (r *ManagerSessionRegistry) Clear() {
	r.Remove("")
}
