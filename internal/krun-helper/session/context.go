package session

import (
	"slices"
	"strings"

	"github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
)

// mergeHostEntries collects every dependency hostname across the given
// sessions into one sorted, deduplicated block pointing at localhost.
func mergeHostEntries(sessions map[string]*Session) []contracts.HostsEntry {
	entries := make([]contracts.HostsEntry, 0)
	seen := map[string]bool{}

	addHost := func(host string) {
		if host == "" || seen[host] {
			return
		}
		seen[host] = true
		entries = append(entries, contracts.HostsEntry{IP: "127.0.0.1", Hostname: host})
	}

	for _, session := range sessions {
		for _, dependency := range session.Context.ServiceDependencies {
			addHost(dependency.Host)
			for _, alias := range dependency.Aliases {
				addHost(alias)
			}
		}
	}

	slices.SortFunc(entries, func(a, b contracts.HostsEntry) int {
		return strings.Compare(a.Hostname, b.Hostname)
	})
	return entries
}

// portForwardsFor turns a service's declared dependencies into the local
// port-forwards the app needs while it runs outside the cluster. Local and
// remote ports match so connection strings work unchanged.
func portForwardsFor(serviceContext config.Service) []contracts.PortForward {
	forwards := make([]contracts.PortForward, 0, len(serviceContext.ServiceDependencies))

	for _, dependency := range serviceContext.ServiceDependencies {
		forward := contracts.PortForward{
			Namespace:  dependency.Namespace,
			Service:    dependency.Service,
			LocalPort:  dependency.Port,
			RemotePort: dependency.Port,
		}
		forwards = append(forwards, forward)
	}

	return forwards
}
