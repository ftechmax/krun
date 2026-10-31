// Package session owns debug sessions and their local streams and dependency forwards.
package session

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
)

// Stream is a session's live traffic attachment.
type Stream interface {
	Close()
}

// StreamAttacher opens the traffic stream for a session.
type StreamAttacher func(sessionID string, sessionToken string, interceptPort int) (Stream, error)

// PortForwarder owns the dependency port-forwards, keyed by session.
type PortForwarder interface {
	Upsert(sessionKey string, forwards []contracts.PortForward) error
	Remove(sessionKey string)
	Clear()
}

// ManagerAPI is the traffic-manager's session REST surface.
type ManagerAPI interface {
	CreateSession(serviceContext config.Service) (contracts.DebugSession, error)
	DeleteSession(sessionID string) error
}

// Session is one active debug session.
type Session struct {
	Key       string
	Context   config.Service
	ManagerID string
	State     string

	stream Stream
}

func (s *Session) closeStream() {
	if s.stream != nil {
		s.stream.Close()
		s.stream = nil
	}
}

type Options struct {
	Forwards PortForwarder
	API      ManagerAPI
	Attach   StreamAttacher
}

type Manager struct {
	// ops serializes lifecycle mutations. mu protects only the sessions map;
	// when both are needed the order is ops then mu. Slow external operations
	// must never run while mu is held, so List remains responsive during them.
	ops      sync.Mutex
	mu       sync.RWMutex
	sessions map[string]*Session
	// hostsRevision changes with every change to sessions, under mu, so a
	// snapshot and its revision always match.
	hostsRevision contracts.HostsRevision

	forwards PortForwarder
	api      ManagerAPI
	attach   StreamAttacher
}

func NewManager(options Options) *Manager {
	return &Manager{
		sessions:      map[string]*Session{},
		hostsRevision: contracts.HostsRevision{Epoch: rand.Text()},
		forwards:      options.Forwards,
		api:           options.API,
		attach:        options.Attach,
	}
}

// Enable brings up a debug session and rolls completed steps back on failure.
// If cleanup fails, the session remains visible so Disable can retry it.
func (m *Manager) Enable(serviceContext config.Service) (err error) {
	key := serviceContext.Project + "/" + serviceContext.Name

	m.ops.Lock()
	defer m.ops.Unlock()

	previous, replacing := m.session(key)
	if replacing {
		if err := m.removeForReplacement(previous); err != nil {
			return err
		}
	}

	session := &Session{
		Key:     key,
		Context: serviceContext,
		State:   contracts.HelperSessionStateActive,
	}

	defer func() {
		if err == nil {
			return
		}
		if cleanupErr := m.teardown(session); cleanupErr != nil {
			session.State = contracts.HelperSessionStateCleanupPending
			m.storeSession(session)
			err = fmt.Errorf("%w (rollback failed: %v)", err, cleanupErr)
		}
	}()

	if err := m.forwards.Upsert(key, portForwardsFor(serviceContext)); err != nil {
		return fmt.Errorf("port-forward update failed: %w", err)
	}

	managerSession, err := m.api.CreateSession(serviceContext)
	if err != nil {
		return fmt.Errorf("manager session create failed: %w", err)
	}
	session.ManagerID = managerSession.SessionID

	stream, err := m.attach(managerSession.SessionID, managerSession.SessionToken, serviceContext.InterceptPort)
	if err != nil {
		return fmt.Errorf("manager stream attach failed: %w", err)
	}
	session.stream = stream

	m.storeSession(session)
	return nil
}

// Disable tears a session down. The bool reports whether there was a session
// to remove, so the caller can tell "removed" from "nothing to do".
func (m *Manager) Disable(serviceContext config.Service) (bool, error) {
	key := serviceContext.Project + "/" + serviceContext.Name

	m.ops.Lock()
	defer m.ops.Unlock()

	session, ok := m.takeSession(key)
	if !ok {
		return false, nil
	}

	if err := m.teardown(session); err != nil {
		session.State = contracts.HelperSessionStateCleanupPending
		m.storeSession(session)
		return true, err
	}
	return true, nil
}

func (m *Manager) List() []contracts.HelperDebugSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]contracts.HelperDebugSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		list = append(list, contracts.HelperDebugSession{
			SessionKey: session.Key,
			Context:    session.Context,
			State:      session.State,
		})
	}
	slices.SortFunc(list, func(a, b contracts.HelperDebugSession) int {
		return strings.Compare(a.SessionKey, b.SessionKey)
	})
	return list
}

// Close releases local resources. Manager-side sessions remain until a later
// enable replaces them or manager startup cleanup removes their sidecars.
func (m *Manager) Close() {
	m.ops.Lock()
	defer m.ops.Unlock()

	m.mu.Lock()
	sessions := m.sessions
	m.sessions = map[string]*Session{}
	m.hostsRevision.Seq++
	m.mu.Unlock()

	for _, session := range sessions {
		session.closeStream()
	}
	m.forwards.Clear()
}

func (m *Manager) removeForReplacement(previous *Session) error {
	// Delete the manager session before touching its stream or local state. If
	// deletion succeeds, replacement is destructive even if later cleanup fails.
	if previous.ManagerID != "" {
		if err := m.api.DeleteSession(previous.ManagerID); err != nil {
			return fmt.Errorf("replace previous manager session: %w", err)
		}
	}

	// Once the manager deletion succeeds, replacement is destructive. Remove
	// the old value from List before closing its stream or doing more network
	// work; reporting it as active from this point would be stale.
	m.takeSession(previous.Key)
	previous.ManagerID = ""
	previous.closeStream()
	return nil
}

func (m *Manager) teardown(session *Session) error {
	session.closeStream()
	defer m.forwards.Remove(session.Key)
	if session.ManagerID != "" {
		if err := m.api.DeleteSession(session.ManagerID); err != nil {
			return fmt.Errorf("delete manager session: %w", err)
		}
		session.ManagerID = ""
	}
	return nil
}

// HostsSnapshot returns the full hosts block for all stored sessions and the
// revision it belongs to, read under one lock.
func (m *Manager) HostsSnapshot() contracts.HostsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return contracts.HostsSnapshot{Entries: mergeHostEntries(m.sessions), Revision: m.hostsRevision}
}

func (m *Manager) session(key string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, ok := m.sessions[key]
	return session, ok
}

func (m *Manager) storeSession(session *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.Key] = session
	m.hostsRevision.Seq++
}

func (m *Manager) takeSession(key string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[key]
	if ok {
		delete(m.sessions, key)
		m.hostsRevision.Seq++
	}
	return session, ok
}
