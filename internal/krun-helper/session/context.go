package session

import (
	"slices"
	"strings"

	"github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
)

// mergeHostEntries collects every dependency hostname across the given
// sessions into one sorted, deduplicated block pointing at localhost.
func mergeHostEntries(contexts []config.Service) []contracts.HostsEntry {
	entries := make([]contracts.HostsEntry, 0)
	seen := map[string]bool{}

	addHost := func(host string) {
		host = strings.TrimSpace(host)
		if host == "" || seen[host] {
			return
		}
		seen[host] = true
		entries = append(entries, contracts.HostsEntry{IP: "127.0.0.1", Hostname: host})
	}

	for _, serviceContext := range contexts {
		for _, dependency := range serviceContext.ServiceDependencies {
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
		serviceName, namespace := dependencyServiceTarget(dependency)
		forward := contracts.PortForward{
			Namespace:  namespace,
			Service:    serviceName,
			LocalPort:  dependency.Port,
			RemotePort: dependency.Port,
		}
		forwards = append(forwards, forward)
	}

	return forwards
}

// dependencyServiceTarget resolves the Kubernetes service and namespace to
// forward, preferring explicit fields and otherwise reading them out of a
// cluster DNS name such as "rabbitmq.default.svc".
func dependencyServiceTarget(dependency config.ServiceDependency) (string, string) {
	serviceName := strings.TrimSpace(dependency.Service)
	namespace := strings.TrimSpace(dependency.Namespace)
	host := strings.TrimSpace(dependency.Host)

	hostParts := strings.Split(host, ".")
	if serviceName == "" {
		serviceName = strings.TrimSpace(hostParts[0])
	}
	if namespace == "" && len(hostParts) >= 3 && strings.TrimSpace(hostParts[2]) == "svc" {
		namespace = strings.TrimSpace(hostParts[1])
	}
	if namespace == "" {
		namespace = "default"
	}

	return serviceName, namespace
}
