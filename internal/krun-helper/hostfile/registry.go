package hostfile

import (
	"strings"
	"sync"

	"github.com/ftechmax/krun/internal/contracts"
)

type Registry struct {
	mu       sync.Mutex
	sessions map[string][]contracts.HostsEntry
}

func NewRegistry() *Registry {
	return &Registry{
		sessions: map[string][]contracts.HostsEntry{},
	}
}

func (r *Registry) Upsert(sessionKey string, entries []contracts.HostsEntry) []contracts.HostsEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	if sessionKey == "" || len(entries) == 0 {
		delete(r.sessions, sessionKey)
	} else {
		r.sessions[sessionKey] = cloneEntries(entries)
	}

	return r.mergedLocked()
}

func (r *Registry) Remove(sessionKey string) []contracts.HostsEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.sessions, sessionKey)
	return r.mergedLocked()
}

func (r *Registry) mergedLocked() []contracts.HostsEntry {
	merged := make([]contracts.HostsEntry, 0)
	indexByHost := map[string]int{}
	for _, entries := range r.sessions {
		for _, entry := range entries {
			host := strings.TrimSpace(entry.Hostname)
			if host == "" {
				continue
			}
			if idx, ok := indexByHost[host]; ok {
				merged[idx] = entry
				continue
			}
			indexByHost[host] = len(merged)
			merged = append(merged, entry)
		}
	}
	return merged
}

func cloneEntries(entries []contracts.HostsEntry) []contracts.HostsEntry {
	out := make([]contracts.HostsEntry, len(entries))
	copy(out, entries)
	return out
}
