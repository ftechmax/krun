// Package session owns the krun-helper's view of active debug sessions. One
// Session holds every piece of state krun set up for one service — hosts
// entries, dependency port-forwards, the manager-side session, and the traffic
// stream — so enable and disable move a single object instead of keeping four
// registries in step.
package session

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/ftechmax/krun/internal/contracts"
)

// Stream is a session's live traffic attachment.
type Stream interface {
	Close()
}

// StreamAttacher opens the traffic stream for a session.
type StreamAttacher func(sessionID string, sessionToken string, interceptPort int) (Stream, error)

// HostsFile is the machine's hosts file. Write replaces krun's block wholesale
// with the union across all sessions; Remove strips the block entirely.
type HostsFile interface {
	Write(entries []contracts.HostsEntry) error
	Remove() error
}

// PortForwarder owns the dependency port-forwards, keyed by session.
type PortForwarder interface {
	Upsert(sessionKey string, forwards []contracts.PortForward) error
	Remove(sessionKey string) error
	Clear() error
}

// ManagerAPI is the traffic-manager's session REST surface.
type ManagerAPI interface {
	CreateSession(serviceContext contracts.DebugServiceContext) (contracts.DebugSession, error)
	DeleteSession(sessionID string) error
}

// Session is one active debug session.
type Session struct {
	Key       string
	Context   contracts.DebugServiceContext
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
	Hosts    HostsFile
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

	hosts    HostsFile
	forwards PortForwarder
	api      ManagerAPI
	attach   StreamAttacher
}

func NewManager(options Options) *Manager {
	return &Manager{
		sessions: map[string]*Session{},
		hosts:    options.Hosts,
		forwards: options.Forwards,
		api:      options.API,
		attach:   options.Attach,
	}
}

// Enable brings up a debug session and rolls completed steps back on failure.
// If cleanup fails, the session remains visible so Disable can retry it.
func (m *Manager) Enable(sessionKey string, serviceContext contracts.DebugServiceContext) error {
	key := resolveKey(sessionKey, serviceContext.Project, serviceContext.ServiceName)
	if key == "" {
		return errors.New("session key or context.service_name is required")
	}

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

	type rollback struct {
		step string
		run  func() error
	}
	var undo []rollback
	if replacing {
		// The previous manager session and stream are gone, but its hosts and
		// forwards deliberately remain so Upsert can reuse live handles. Own
		// their cleanup before the first fallible replacement write.
		undo = append(undo, rollback{
			step: "remove preserved replacement local state",
			run: func() error {
				var failures []error
				if err := m.forwards.Remove(key); err != nil {
					failures = append(failures, fmt.Errorf("remove port-forwards: %w", err))
				}
				if err := m.hosts.Write(m.mergedHosts(nil)); err != nil {
					failures = append(failures, fmt.Errorf("restore hosts file: %w", err))
				}
				return errors.Join(failures...)
			},
		})
	}
	fail := func(step string, err error) error {
		var rollbackFailures []string
		for i := len(undo) - 1; i >= 0; i-- {
			if rollbackErr := undo[i].run(); rollbackErr != nil {
				rollbackFailures = append(rollbackFailures, fmt.Sprintf("%s: %v", undo[i].step, rollbackErr))
			}
		}
		if len(rollbackFailures) > 0 {
			session.State = contracts.HelperSessionStateCleanupPending
			m.storeSession(session)
			return fmt.Errorf("%s: %w (rollback failed: %s)", step, err, strings.Join(rollbackFailures, "; "))
		}
		return fmt.Errorf("%s: %w", step, err)
	}

	if err := m.hosts.Write(m.mergedHosts(session)); err != nil {
		return fail("hostfile update failed", err)
	}
	if !replacing {
		undo = append(undo, rollback{
			step: "restore hosts file",
			run:  func() error { return m.hosts.Write(m.mergedHosts(nil)) },
		})
	}

	if err := m.forwards.Upsert(key, portForwardsFor(serviceContext)); err != nil {
		return fail("port-forward update failed", err)
	}
	if !replacing {
		undo = append(undo, rollback{
			step: "remove port-forwards",
			run:  func() error { return m.forwards.Remove(key) },
		})
	}

	managerSession, err := m.api.CreateSession(serviceContext)
	if err != nil {
		return fail("manager session create failed", err)
	}
	session.ManagerID = managerSession.SessionID
	undo = append(undo, rollback{
		step: "delete manager session",
		run: func() error {
			if err := m.api.DeleteSession(managerSession.SessionID); err != nil {
				return err
			}
			session.ManagerID = ""
			return nil
		},
	})

	stream, err := m.attach(managerSession.SessionID, managerSession.SessionToken, serviceContext.InterceptPort)
	if err != nil {
		return fail("manager stream attach failed", err)
	}
	session.stream = stream

	m.storeSession(session)
	return nil
}

// Disable tears a session down. The bool reports whether there was a session
// to remove, so the caller can tell "removed" from "nothing to do".
func (m *Manager) Disable(sessionKey string, serviceContext contracts.DebugServiceContext) (bool, error) {
	key := resolveKey(sessionKey, serviceContext.Project, serviceContext.ServiceName)
	if key == "" {
		return false, errors.New("session key or context.service_name is required")
	}

	m.ops.Lock()
	defer m.ops.Unlock()

	session, ok := m.takeSession(key)
	if !ok {
		return false, nil
	}

	if failures := m.teardown(session); len(failures) > 0 {
		session.State = contracts.HelperSessionStateCleanupPending
		m.storeSession(session)
		return true, errors.New(strings.Join(failures, "; "))
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
func (m *Manager) Close() error {
	m.ops.Lock()
	defer m.ops.Unlock()

	m.mu.Lock()
	sessions := m.sessions
	m.sessions = map[string]*Session{}
	m.mu.Unlock()

	for _, session := range sessions {
		session.closeStream()
	}
	var failures []error
	if err := m.forwards.Clear(); err != nil {
		failures = append(failures, fmt.Errorf("clear port-forwards: %w", err))
	}
	if err := m.hosts.Remove(); err != nil {
		failures = append(failures, fmt.Errorf("remove hosts entries: %w", err))
	}
	return errors.Join(failures...)
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

func (m *Manager) teardown(session *Session) []string {
	var failures []string

	session.closeStream()

	if session.ManagerID != "" {
		if err := m.api.DeleteSession(session.ManagerID); err != nil {
			failures = append(failures, fmt.Sprintf("manager session delete failed: %v", err))
		} else {
			session.ManagerID = ""
		}
	}
	if err := m.forwards.Remove(session.Key); err != nil {
		failures = append(failures, fmt.Sprintf("port-forward remove failed: %v", err))
	}
	if err := m.hosts.Write(m.mergedHosts(nil)); err != nil {
		failures = append(failures, fmt.Sprintf("hostfile remove failed: %v", err))
	}
	return failures
}

// mergedHosts snapshots the union of hosts entries across every stored
// session plus pending, if given. krun keeps one block in the hosts file for
// all sessions, so every change rewrites the whole union.
func (m *Manager) mergedHosts(pending *Session) []contracts.HostsEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	contexts := make([]contracts.DebugServiceContext, 0, len(m.sessions)+1)
	for _, session := range m.sessions {
		contexts = append(contexts, session.Context)
	}
	if pending != nil {
		contexts = append(contexts, pending.Context)
	}
	return mergeHostEntries(contexts)
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
}

func (m *Manager) takeSession(key string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[key]
	if ok {
		delete(m.sessions, key)
	}
	return session, ok
}
