package session

import (
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/ftechmax/krun/internal/contracts"
)

const defaultNamespace = "default"

// resolveKey names a session. An explicit key wins; otherwise the key is
// derived from the project and service so two projects can debug services of
// the same name without colliding.
func resolveKey(sessionKey string, project string, serviceName string) string {
	if trimmed := strings.TrimSpace(sessionKey); trimmed != "" {
		return trimmed
	}

	trimmedProject := strings.TrimSpace(project)
	trimmedService := strings.TrimSpace(serviceName)
	switch {
	case trimmedProject == "" && trimmedService == "":
		return ""
	case trimmedProject == "":
		return trimmedService
	case trimmedService == "":
		return trimmedProject
	default:
		return trimmedProject + "/" + trimmedService
	}
}

// mergeHostEntries collects every dependency hostname across the given
// sessions into one sorted, deduplicated block pointing at localhost.
func mergeHostEntries(contexts []contracts.DebugServiceContext) []contracts.HostsEntry {
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
		if a.Hostname == b.Hostname {
			return strings.Compare(a.IP, b.IP)
		}
		return strings.Compare(a.Hostname, b.Hostname)
	})
	return entries
}

// portForwardsFor turns a service's declared dependencies into the local
// port-forwards the app needs while it runs outside the cluster. Local and
// remote ports match so connection strings work unchanged.
func portForwardsFor(serviceContext contracts.DebugServiceContext) []contracts.PortForward {
	forwards := make([]contracts.PortForward, 0, len(serviceContext.ServiceDependencies))
	seen := map[string]bool{}

	for _, dependency := range serviceContext.ServiceDependencies {
		serviceName, namespace := dependencyServiceTarget(dependency)
		if serviceName == "" || dependency.Port <= 0 {
			continue
		}

		forward := contracts.PortForward{
			Namespace:  namespace,
			Service:    serviceName,
			LocalPort:  dependency.Port,
			RemotePort: dependency.Port,
		}
		key := fmt.Sprintf("%s|%s|%d|%d", forward.Namespace, forward.Service, forward.LocalPort, forward.RemotePort)
		if seen[key] {
			continue
		}
		seen[key] = true
		forwards = append(forwards, forward)
	}

	return forwards
}

// dependencyServiceTarget resolves the Kubernetes service and namespace to
// forward, preferring explicit fields and otherwise reading them out of a
// cluster DNS name such as "rabbitmq.default.svc".
func dependencyServiceTarget(dependency contracts.DebugServiceDependencyContext) (string, string) {
	serviceName := strings.TrimSpace(dependency.Service)
	namespace := strings.TrimSpace(dependency.Namespace)
	host := strings.TrimSpace(dependency.Host)

	if host != "" {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			host = parsedHost
		}
	}

	hostParts := strings.Split(host, ".")
	if serviceName == "" && len(hostParts) > 0 {
		serviceName = strings.TrimSpace(hostParts[0])
	}
	if namespace == "" && len(hostParts) >= 3 && strings.TrimSpace(hostParts[2]) == "svc" {
		namespace = strings.TrimSpace(hostParts[1])
	}
	if namespace == "" {
		namespace = defaultNamespace
	}

	return serviceName, namespace
}
