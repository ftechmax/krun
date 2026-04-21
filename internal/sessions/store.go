package sessions

import (
	"sync"

	"github.com/ftechmax/krun/internal/contracts"
)

type Store struct {
	mu    sync.Mutex
	items map[string]contracts.DebugSession
}

func NewStore() *Store {
	return &Store{items: map[string]contracts.DebugSession{}}
}

func (s *Store) Put(key string, session contracts.DebugSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = session
}

func (s *Store) Get(key string) (contracts.DebugSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.items[key]
	return session, ok
}

func (s *Store) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[key]; !ok {
		return false
	}
	delete(s.items, key)
	return true
}

func (s *Store) List() []contracts.DebugSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contracts.DebugSession, 0, len(s.items))
	for _, session := range s.items {
		out = append(out, session)
	}
	return out
}
