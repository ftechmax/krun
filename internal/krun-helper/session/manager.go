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
	mu       sync.Mutex
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

	m.mu.Lock()
	defer m.mu.Unlock()

	if previous, ok := m.sessions[key]; ok {
		if err := m.removeForReplacementLocked(previous); err != nil {
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
	fail := func(step string, err error) error {
		var rollbackFailures []string
		for i := len(undo) - 1; i >= 0; i-- {
			if rollbackErr := undo[i].run(); rollbackErr != nil {
				rollbackFailures = append(rollbackFailures, fmt.Sprintf("%s: %v", undo[i].step, rollbackErr))
			}
		}
		if len(rollbackFailures) > 0 {
			session.State = contracts.HelperSessionStateCleanupPending
			m.sessions[key] = session
			return fmt.Errorf("%s: %w (rollback failed: %s)", step, err, strings.Join(rollbackFailures, "; "))
		}
		return fmt.Errorf("%s: %w", step, err)
	}

	if err := m.hosts.Write(m.mergedHostsLocked(session)); err != nil {
		return fail("hostfile update failed", err)
	}
	undo = append(undo, rollback{
		step: "restore hosts file",
		run:  func() error { return m.hosts.Write(m.mergedHostsLocked(nil)) },
	})

	if err := m.forwards.Upsert(key, portForwardsFor(serviceContext)); err != nil {
		return fail("port-forward update failed", err)
	}
	undo = append(undo, rollback{
		step: "remove port-forwards",
		run:  func() error { return m.forwards.Remove(key) },
	})

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

	m.sessions[key] = session
	return nil
}

// Disable tears a session down. The bool reports whether there was a session
// to remove, so the caller can tell "removed" from "nothing to do".
func (m *Manager) Disable(sessionKey string, serviceContext contracts.DebugServiceContext) (bool, error) {
	key := resolveKey(sessionKey, serviceContext.Project, serviceContext.ServiceName)
	if key == "" {
		return false, errors.New("session key or context.service_name is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[key]
	if !ok {
		return false, nil
	}
	delete(m.sessions, key)

	if failures := m.teardownLocked(session); len(failures) > 0 {
		session.State = contracts.HelperSessionStateCleanupPending
		m.sessions[key] = session
		return true, errors.New(strings.Join(failures, "; "))
	}
	return true, nil
}

func (m *Manager) List() []contracts.HelperDebugSession {
	m.mu.Lock()
	defer m.mu.Unlock()

	list := make([]contracts.HelperDebugSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		list = append(list, contracts.HelperDebugSession{
			SessionKey: session.Key,
			Context:    session.Context,
			State:      normalizedState(session.State),
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
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, session := range m.sessions {
		session.closeStream()
		delete(m.sessions, key)
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

func (m *Manager) removeForReplacementLocked(previous *Session) error {
	// Delete the manager session before touching its stream or local state. If
	// deletion succeeds, replacement is destructive even if later cleanup fails.
	if previous.ManagerID != "" {
		if err := m.api.DeleteSession(previous.ManagerID); err != nil {
			return fmt.Errorf("replace previous manager session: %w", err)
		}
		previous.ManagerID = ""
	}

	previous.closeStream()
	delete(m.sessions, previous.Key)

	var failures []string
	if err := m.forwards.Remove(previous.Key); err != nil {
		failures = append(failures, fmt.Sprintf("port-forward remove failed: %v", err))
	}
	if err := m.hosts.Write(m.mergedHostsLocked(nil)); err != nil {
		failures = append(failures, fmt.Sprintf("hostfile remove failed: %v", err))
	}
	if len(failures) == 0 {
		return nil
	}

	previous.State = contracts.HelperSessionStateCleanupPending
	m.sessions[previous.Key] = previous
	return fmt.Errorf("replace previous session cleanup failed: %s", strings.Join(failures, "; "))
}

func (m *Manager) teardownLocked(session *Session) []string {
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
	if err := m.hosts.Write(m.mergedHostsLocked(nil)); err != nil {
		failures = append(failures, fmt.Sprintf("hostfile remove failed: %v", err))
	}
	return failures
}

// mergedHostsLocked is the union of hosts entries across every stored session
// plus pending, if given. krun keeps one block in the hosts file for all
// sessions, so every change rewrites the whole union.
func (m *Manager) mergedHostsLocked(pending *Session) []contracts.HostsEntry {
	contexts := make([]contracts.DebugServiceContext, 0, len(m.sessions)+1)
	for _, session := range m.sessions {
		contexts = append(contexts, session.Context)
	}
	if pending != nil {
		contexts = append(contexts, pending.Context)
	}
	return mergeHostEntries(contexts)
}

func normalizedState(state string) string {
	if state == contracts.HelperSessionStateCleanupPending {
		return state
	}
	return contracts.HelperSessionStateActive
}
