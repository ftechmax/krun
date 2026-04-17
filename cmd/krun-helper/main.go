package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/krun-helper/hostfile"
	managerclient "github.com/ftechmax/krun/internal/krun-helper/manager-client"
	helperportforward "github.com/ftechmax/krun/internal/krun-helper/portforward"
	"github.com/ftechmax/krun/internal/krun-helper/service"
	"github.com/ftechmax/krun/internal/krun-helper/session"
	helperstream "github.com/ftechmax/krun/internal/krun-helper/stream"
	"github.com/spf13/cobra"
)

const (
	defaultHelperListenAddress       = "127.0.0.1:47831"
	managerAPIForwardSessionKey      = "__manager_api__"
	defaultManagerForwardAddress     = "127.0.0.1:47832"
	defaultManagerForwardNamespace   = "krun-system"
	defaultManagerForwardServiceName = "krun-traffic-manager"
	defaultManagerForwardRemotePort  = 8080
)

type sessionPortForwardRegistry interface {
	Upsert(sessionKey string, forwards []contracts.PortForward) error
	Remove(sessionKey string) error
	Clear() error
}

type sessionStreamRegistry interface {
	Upsert(sessionKey string, sessionID string, sessionToken string, interceptPort int) error
	Remove(sessionKey string) error
	Clear() error
}

type noopPortForwardRegistry struct{}

func (noopPortForwardRegistry) Upsert(_ string, _ []contracts.PortForward) error { return nil }
func (noopPortForwardRegistry) Remove(_ string) error                            { return nil }
func (noopPortForwardRegistry) Clear() error                                     { return nil }

type noopStreamRegistry struct{}

func (noopStreamRegistry) Upsert(_ string, _ string, _ string, _ int) error { return nil }
func (noopStreamRegistry) Remove(_ string) error                            { return nil }
func (noopStreamRegistry) Clear() error                                     { return nil }

var (
	hostfileUpdate                                             = hostfile.Update
	hostfileRemove                                             = hostfile.Remove
	hostsRegistry                                              = hostfile.NewSessionHostsRegistry()
	sessionsRegistry                                           = session.NewDebugSessionRegistry()
	managerSessionsRegistry                                    = session.NewManagerSessionRegistry()
	portForwardRegistry             sessionPortForwardRegistry = noopPortForwardRegistry{}
	streamRegistry                  sessionStreamRegistry      = noopStreamRegistry{}
	managerSessionClient            managerclient.SessionAPI   = managerclient.NoopSessionClient{}
	managerForwardBootstrapRequired bool
	newPortForwardRegistry          = newHelperPortForwardRegistry
	newManagerClient                = managerclient.NewSessionClient
	newStreamRegistry               = newHelperStreamRegistry
)

func newHelperPortForwardRegistry(kubeConfigPath string) (sessionPortForwardRegistry, error) {
	return helperportforward.NewSessionRegistry(kubeConfigPath)
}

func newHelperStreamRegistry(managerAddress string) sessionStreamRegistry {
	return helperstream.NewSessionRegistry(managerAddress)
}

type daemonOptions struct {
	externalShutdown <-chan struct{} // service stop signal (nil channel = not used)
	onReady          func()          // called when HTTP listener is bound
}

// startHelperDaemonForService adapts startHelperDaemon to the service.StartDaemonFunc
// signature so the service runner can call it without importing main.
func startHelperDaemonForService(listenAddress, kubeConfigPath string, opts service.DaemonOptions) error {
	return startHelperDaemon(listenAddress, kubeConfigPath, daemonOptions{
		externalShutdown: opts.ExternalShutdown,
		onReady:          opts.OnReady,
	})
}

func main() {
	var kubeConfigPath string
	var listenAddress string
	var serviceFlag bool

	rootCmd := &cobra.Command{
		Use:   "krun-helper",
		Short: "Elevated daemon helper for krun debug sessions",
		Run: func(cmd *cobra.Command, args []string) {
			if shouldRunAsService(serviceFlag) {
				if err := runAsService(listenAddress, kubeConfigPath); err != nil {
					fmt.Printf("service failed: %v\n", err)
					os.Exit(1)
				}
				return
			}
			if err := startHelperDaemon(listenAddress, kubeConfigPath, daemonOptions{}); err != nil {
				fmt.Printf("helper daemon failed: %v\n", err)
				os.Exit(1)
			}
		},
	}

	rootCmd.Flags().StringVar(&kubeConfigPath, "kubeconfig", "", "Path to kubeconfig file")
	rootCmd.Flags().StringVar(&listenAddress, "listen", "", "Daemon listen address")
	rootCmd.Flags().BoolVar(&serviceFlag, "service", false, "Run as system service (used by systemd ExecStart)")

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func startHelperDaemon(listenAddress string, kubeConfigPath string, opts daemonOptions) error {
	addr, err := resolveHelperListenAddress(listenAddress)
	if err != nil {
		return err
	}

	if err := initializeHelperDependencies(kubeConfigPath); err != nil {
		return err
	}

	return startHelperServer(addr, opts)
}

func resolveHelperListenAddress(listenAddress string) (string, error) {
	addr := strings.TrimSpace(listenAddress)
	if addr == "" {
		addr = defaultHelperListenAddress
	}
	if err := validateLoopbackAddress(addr); err != nil {
		return "", err
	}
	return addr, nil
}

func initializeHelperDependencies(kubeConfigPath string) error {
	registry, err := newPortForwardRegistry(kubeConfigPath)
	if err != nil {
		return fmt.Errorf("failed to initialize port-forward registry: %w", err)
	}
	portForwardRegistry = registry

	managerForwardPort, err := parsePortFromAddress(defaultManagerForwardAddress)
	if err != nil {
		return fmt.Errorf("invalid manager forward address %q: %w", defaultManagerForwardAddress, err)
	}

	managerForwardBootstrapRequired = false
	if err := ensureManagerAPIForward(managerForwardPort); err != nil {
		managerForwardBootstrapRequired = true
		fmt.Printf("warning: manager api port-forward is not ready yet: %v\n", err)
	}

	streamRegistry = newStreamRegistry("http://" + defaultManagerForwardAddress)

	managerClient, err := newManagerClient(kubeConfigPath)
	if err != nil {
		return fmt.Errorf("failed to initialize manager session client: %w", err)
	}
	managerSessionClient = managerClient
	return nil
}

func startHelperServer(addr string, opts daemonOptions) error {
	shutdownCh := make(chan struct{}, 1)
	server := &http.Server{
		Handler:           newHandler(shutdownCh),
		ReadHeaderTimeout: 5 * time.Second,
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	fmt.Printf("krun-helper listening on %s\n", addr)

	if opts.onReady != nil {
		opts.onReady()
	}

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- server.Serve(listener)
	}()

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signalCh)

	return waitForHelperShutdown(server, serverErrCh, signalCh, shutdownCh, opts.externalShutdown)
}

func waitForHelperShutdown(
	server *http.Server,
	serverErrCh <-chan error,
	signalCh <-chan os.Signal,
	shutdownCh <-chan struct{},
	externalShutdown <-chan struct{},
) error {
	select {
	case err := <-serverErrCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case sig := <-signalCh:
		return shutdownHelperServer(server, serverErrCh, fmt.Sprintf("received %s", sig))
	case <-shutdownCh:
		return shutdownHelperServer(server, serverErrCh, "shutdown requested via API")
	case <-externalShutdown:
		return shutdownHelperServer(server, serverErrCh, "service stop requested")
	}
}

func shutdownHelperServer(server *http.Server, serverErrCh <-chan error, reason string) error {
	fmt.Printf("%s, shutting down krun-helper\n", reason)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown helper server: %w", err)
	}
	if err := <-serverErrCh; err != nil && err != http.ErrServerClosed {
		return err
	}
	return cleanupHelperState()
}

func newHandler(shutdownCh chan<- struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/v1/debug/sessions", handleDebugSessionsList)
	mux.HandleFunc("/v1/debug/enable", handleDebugEnable)
	mux.HandleFunc("/v1/debug/disable", handleDebugDisable)
	mux.HandleFunc("/v1/shutdown", handleShutdown(shutdownCh))
	return mux
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, contracts.HelperResponse{
			Success: false,
			Message: "method not allowed",
		})
		return
	}
	writeJSON(w, http.StatusOK, contracts.HelperResponse{
		Success: true,
		Message: "ok",
	})
}

func handleShutdown(shutdownCh chan<- struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, contracts.HelperResponse{
				Success: false,
				Message: "method not allowed",
			})
			return
		}
		writeJSON(w, http.StatusOK, contracts.HelperResponse{
			Success: true,
			Message: "shutting down",
		})
		select {
		case shutdownCh <- struct{}{}:
		default:
		}
	}
}

func handleDebugEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, contracts.HelperResponse{
			Success: false,
			Message: "method not allowed",
		})
		return
	}

	req, sessionKey, err := parseDebugCommandRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	// Each step appends a rollback function. On failure, all rollbacks
	// run in reverse order so every completed step is undone cleanly.
	var rollbacks []func() error

	rollbackAll := func() error {
		var failures []string
		for i := len(rollbacks) - 1; i >= 0; i-- {
			if err := rollbacks[i](); err != nil {
				failures = append(failures, err.Error())
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("rollback failed: %s", strings.Join(failures, "; "))
		}
		return nil
	}

	fail := func(w http.ResponseWriter, step string, err error) {
		if rollbackErr := rollbackAll(); rollbackErr != nil {
			writeJSON(w, http.StatusInternalServerError, contracts.HelperResponse{
				Success: false,
				Message: fmt.Sprintf("%s: %v (%v)", step, err, rollbackErr),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, contracts.HelperResponse{
			Success: false,
			Message: fmt.Sprintf("%s: %v", step, err),
		})
	}

	// 1. update hostfile
	entries := buildDebugHostEntries(req.Context)
	mergedEntries := hostsRegistry.Upsert(sessionKey, entries)
	if err := hostfileUpdate(mergedEntries); err != nil {
		fail(w, "hostfile update failed", err)
		return
	}
	rollbacks = append(rollbacks, func() error {
		restored := hostsRegistry.Remove(sessionKey)
		return hostfileUpdate(restored)
	})

	// 2. set up port-forwards
	forwards := buildDebugPortForwards(req.Context)
	if err := portForwardRegistry.Upsert(sessionKey, forwards); err != nil {
		fail(w, "port-forward update failed", err)
		return
	}
	rollbacks = append(rollbacks, func() error {
		return portForwardRegistry.Remove(sessionKey)
	})

	if managerForwardBootstrapRequired {
		managerForwardPort, portErr := parsePortFromAddress(defaultManagerForwardAddress)
		if portErr != nil {
			fail(w, "manager api port-forward failed", fmt.Errorf("invalid manager forward address %q: %w", defaultManagerForwardAddress, portErr))
			return
		}
		if err := ensureManagerAPIForward(managerForwardPort); err != nil {
			fail(w, "manager api port-forward failed", err)
			return
		}
		managerForwardBootstrapRequired = false
	}

	// 3. clean up previous manager session if one exists for this key
	if err := cleanupPreviousManagerSession(sessionKey); err != nil {
		fail(w, "previous manager session cleanup failed", err)
		return
	}

	// 4. create manager session
	managerSession, err := managerSessionClient.CreateSession(req.Context)
	if err != nil {
		fail(w, "manager session create failed", err)
		return
	}
	rollbacks = append(rollbacks, func() error {
		return managerSessionClient.DeleteSession(managerSession.SessionID)
	})

	// 5. attach traffic stream
	if err := streamRegistry.Upsert(sessionKey, managerSession.SessionID, managerSession.SessionToken, req.Context.InterceptPort); err != nil {
		fail(w, "manager stream attach failed", err)
		return
	}

	// register the session.
	managerSessionsRegistry.Upsert(sessionKey, managerSession.SessionID)
	sessionsRegistry.Upsert(sessionKey, req.Context)

	writeJSON(w, http.StatusOK, contracts.HelperResponse{
		Success: true,
		Message: "debug enable applied",
	})
}

func handleDebugDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, contracts.HelperResponse{
			Success: false,
			Message: "method not allowed",
		})
		return
	}

	req, sessionKey, err := parseDebugCommandRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}
	if strings.TrimSpace(sessionKey) == "" {
		writeJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid payload: session key or context.service_name is required",
		})
		return
	}

	if !sessionsRegistry.Has(sessionKey) {
		writeJSON(w, http.StatusOK, contracts.HelperResponse{
			Success: true,
			Message: "no active session",
		})
		return
	}

	var failures []string
	managerDeleteFailed := false

	if err := streamRegistry.Remove(sessionKey); err != nil {
		failures = append(failures, fmt.Sprintf("stream detach failed: %v", err))
	}

	managerSessionID, ok := managerSessionsRegistry.Get(sessionKey)
	if !ok {
		resolvedManagerSessionID, resolveErr := resolveManagerSessionIDForDisable(req.Context)
		if resolveErr != nil {
			failures = append(failures, fmt.Sprintf("manager session lookup failed: %v", resolveErr))
		} else if strings.TrimSpace(resolvedManagerSessionID) != "" {
			managerSessionID = resolvedManagerSessionID
			ok = true
		}
	}

	if ok {
		if err := managerSessionClient.DeleteSession(managerSessionID); err != nil {
			failures = append(failures, fmt.Sprintf("manager session delete failed: %v", err))
			managerDeleteFailed = true
		}
	}

	if err := portForwardRegistry.Remove(sessionKey); err != nil {
		failures = append(failures, fmt.Sprintf("port-forward remove failed: %v", err))
	}

	mergedEntries := hostsRegistry.Remove(sessionKey)
	if err := hostfileUpdate(mergedEntries); err != nil {
		failures = append(failures, fmt.Sprintf("hostfile remove failed: %v", err))
	}

	if len(failures) > 0 {
		writeJSON(w, http.StatusInternalServerError, contracts.HelperResponse{
			Success: false,
			Message: strings.Join(failures, "; "),
		})
		return
	}

	sessionsRegistry.Remove(sessionKey)
	if !managerDeleteFailed {
		managerSessionsRegistry.Remove(sessionKey)
	}

	writeJSON(w, http.StatusOK, contracts.HelperResponse{
		Success: true,
		Message: "debug disable applied",
	})
}

func handleDebugSessionsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, contracts.HelperResponse{
			Success: false,
			Message: "method not allowed",
		})
		return
	}

	writeJSONAny(w, http.StatusOK, contracts.HelperDebugSessionsResponse{
		Sessions: sessionsRegistry.List(),
	})
}

func validateLoopbackAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}

	switch strings.TrimSpace(host) {
	case "127.0.0.1", "localhost":
		return nil
	default:
		return fmt.Errorf("listen address must be loopback (localhost or 127.0.0.1): %s", addr)
	}
}

func writeJSON(w http.ResponseWriter, code int, response contracts.HelperResponse) {
	writeJSONAny(w, code, response)
}

func writeJSONAny(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		fmt.Printf("warning: failed to write helper response: %v\n", err)
	}
}

func cleanupPreviousManagerSession(sessionKey string) error {
	previousManagerSessionID, ok := managerSessionsRegistry.Get(sessionKey)
	if !ok {
		return nil
	}
	if err := streamRegistry.Remove(sessionKey); err != nil {
		return fmt.Errorf("detach previous stream: %w", err)
	}
	if err := managerSessionClient.DeleteSession(previousManagerSessionID); err != nil {
		return fmt.Errorf("delete previous manager session: %w", err)
	}
	managerSessionsRegistry.Remove(sessionKey)
	return nil
}

func cleanupHelperState() error {
	if err := streamRegistry.Clear(); err != nil {
		return fmt.Errorf("clear streams: %w", err)
	}
	if err := portForwardRegistry.Clear(); err != nil {
		return fmt.Errorf("clear port-forwards: %w", err)
	}
	hostsRegistry.Clear()
	sessionsRegistry.Clear()
	managerSessionsRegistry.Clear()
	if err := hostfileRemove(); err != nil {
		return fmt.Errorf("remove hosts entries: %w", err)
	}
	return nil
}

func resolveManagerSessionIDForDisable(ctx contracts.DebugServiceContext) (string, error) {
	sessions, err := managerSessionClient.ListSessions()
	if err != nil {
		return "", err
	}

	targetService := strings.TrimSpace(ctx.ServiceName)
	targetNamespace := managerclient.NormalizeNamespace(ctx.Namespace)
	if targetService == "" {
		return "", nil
	}

	var matched contracts.DebugSession
	for _, managerSession := range sessions {
		if strings.TrimSpace(managerSession.ServiceName) != targetService {
			continue
		}
		if managerclient.NormalizeNamespace(managerSession.Namespace) != targetNamespace {
			continue
		}
		if strings.TrimSpace(managerSession.ClientID) != managerclient.ManagerClientID {
			continue
		}
		if strings.Compare(strings.TrimSpace(managerSession.CreatedAt), strings.TrimSpace(matched.CreatedAt)) > 0 {
			matched = managerSession
		}
	}

	return strings.TrimSpace(matched.SessionID), nil
}

func parsePortFromAddress(address string) (int, error) {
	host, portValue, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(host) == "" {
		return 0, fmt.Errorf("host is required")
	}
	port, err := strconv.Atoi(strings.TrimSpace(portValue))
	if err != nil {
		return 0, err
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("port out of range")
	}
	return port, nil
}

func ensureManagerAPIForward(managerForwardPort int) error {
	return portForwardRegistry.Upsert(managerAPIForwardSessionKey, []contracts.PortForward{
		{
			Namespace:  defaultManagerForwardNamespace,
			Service:    defaultManagerForwardServiceName,
			LocalPort:  managerForwardPort,
			RemotePort: defaultManagerForwardRemotePort,
		},
	})
}

func parseDebugCommandRequest(r *http.Request) (contracts.DebugSessionCommandRequest, string, error) {
	var req contracts.DebugSessionCommandRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return contracts.DebugSessionCommandRequest{}, "", fmt.Errorf("invalid payload: %w", err)
	}

	sessionKey := resolveDebugSessionKey(req.SessionKey, req.Context.Project, req.Context.ServiceName)
	if strings.TrimSpace(req.SessionKey) == "" && strings.TrimSpace(req.Context.ServiceName) == "" {
		return contracts.DebugSessionCommandRequest{}, "", fmt.Errorf("invalid payload: session key or context.service_name is required")
	}
	return req, sessionKey, nil
}

func resolveDebugSessionKey(sessionKey string, project string, serviceName string) string {
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

func buildDebugHostEntries(ctx contracts.DebugServiceContext) []contracts.HostsEntry {
	entries := make([]contracts.HostsEntry, 0, len(ctx.ServiceDependencies))
	indexByHost := map[string]int{}

	addHost := func(host string) {
		host = strings.TrimSpace(host)
		if host == "" {
			return
		}
		entry := contracts.HostsEntry{
			IP:       "127.0.0.1",
			Hostname: host,
		}
		if idx, ok := indexByHost[host]; ok {
			entries[idx] = entry
			return
		}
		indexByHost[host] = len(entries)
		entries = append(entries, entry)
	}

	for _, dependency := range ctx.ServiceDependencies {
		addHost(dependency.Host)
		for _, alias := range dependency.Aliases {
			addHost(alias)
		}
	}
	return entries
}

func buildDebugPortForwards(ctx contracts.DebugServiceContext) []contracts.PortForward {
	forwards := make([]contracts.PortForward, 0, len(ctx.ServiceDependencies))
	seen := map[string]bool{}

	appendForward := func(forward contracts.PortForward) {
		namespace := strings.TrimSpace(forward.Namespace)
		serviceName := strings.TrimSpace(forward.Service)
		if namespace == "" {
			namespace = "default"
		}
		if serviceName == "" || forward.LocalPort <= 0 || forward.RemotePort <= 0 {
			return
		}

		normalized := contracts.PortForward{
			Namespace:  namespace,
			Service:    serviceName,
			LocalPort:  forward.LocalPort,
			RemotePort: forward.RemotePort,
		}
		key := fmt.Sprintf("%s|%s|%d|%d", normalized.Namespace, normalized.Service, normalized.LocalPort, normalized.RemotePort)
		if seen[key] {
			return
		}
		seen[key] = true
		forwards = append(forwards, normalized)
	}

	for _, dependency := range ctx.ServiceDependencies {
		serviceName, namespace := dependencyServiceTarget(dependency)
		appendForward(contracts.PortForward{
			Namespace:  namespace,
			Service:    serviceName,
			LocalPort:  dependency.Port,
			RemotePort: dependency.Port,
		})
	}

	return forwards
}

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
		namespace = "default"
	}

	return serviceName, namespace
}
