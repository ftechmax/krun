package hostfile

import (
	"slices"
	"strings"
	"sync"

	"github.com/ftechmax/krun/internal/contracts"
)

type SessionHostsRegistry struct {
	mu       sync.Mutex
	sessions map[string][]contracts.HostsEntry
}

func NewSessionHostsRegistry() *SessionHostsRegistry {
	return &SessionHostsRegistry{
		sessions: map[string][]contracts.HostsEntry{},
	}
}

func (r *SessionHostsRegistry) Upsert(sessionKey string, entries []contracts.HostsEntry) []contracts.HostsEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := normalizeSessionKey(sessionKey)
	normalized := normalizeEntries(entries)
	if len(normalized) == 0 {
		delete(r.sessions, key)
	} else {
		r.sessions[key] = normalized
	}
	return mergeEntries(r.sessions)
}

func (r *SessionHostsRegistry) Remove(sessionKey string) []contracts.HostsEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	if strings.TrimSpace(sessionKey) == "" {
		r.sessions = map[string][]contracts.HostsEntry{}
		return nil
	}
	delete(r.sessions, normalizeSessionKey(sessionKey))
	return mergeEntries(r.sessions)
}

func (r *SessionHostsRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = map[string][]contracts.HostsEntry{}
}

func normalizeSessionKey(sessionKey string) string {
	trimmed := strings.TrimSpace(sessionKey)
	if trimmed == "" {
		return "__default__"
	}
	return trimmed
}

func normalizeEntries(entries []contracts.HostsEntry) []contracts.HostsEntry {
	result := make([]contracts.HostsEntry, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		ip := strings.TrimSpace(entry.IP)
		host := strings.TrimSpace(entry.Hostname)
		if ip == "" || host == "" {
			continue
		}
		key := ip + "|" + host
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, contracts.HostsEntry{
			IP:       ip,
			Hostname: host,
		})
	}
	slices.SortFunc(result, func(a, b contracts.HostsEntry) int {
		if a.Hostname == b.Hostname {
			return strings.Compare(a.IP, b.IP)
		}
		return strings.Compare(a.Hostname, b.Hostname)
	})
	return result
}

func mergeEntries(sessions map[string][]contracts.HostsEntry) []contracts.HostsEntry {
	merged := make([]contracts.HostsEntry, 0)
	seen := map[string]bool{}
	for _, entries := range sessions {
		for _, entry := range entries {
			key := entry.IP + "|" + entry.Hostname
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, entry)
		}
	}
	slices.SortFunc(merged, func(a, b contracts.HostsEntry) int {
		if a.Hostname == b.Hostname {
			return strings.Compare(a.IP, b.IP)
		}
		return strings.Compare(a.Hostname, b.Hostname)
	})
	return merged
}
