package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/krun-helper/build"
	"github.com/ftechmax/krun/internal/krun-helper/deploy"
	"github.com/ftechmax/krun/internal/krun-helper/envfile"
	"github.com/ftechmax/krun/internal/krun-helper/hostfile"
	"github.com/ftechmax/krun/internal/krun-helper/network"
	"github.com/ftechmax/krun/internal/krun-helper/portforward"
	"github.com/ftechmax/krun/internal/krun-helper/stream"
	"github.com/ftechmax/krun/internal/krun-helper/trafficmanagerclient"
	"github.com/ftechmax/krun/internal/krun-helper/workspace"
	"github.com/ftechmax/krun/internal/sessions"
	"github.com/ftechmax/krun/internal/utils"
	"github.com/spf13/cobra"
)

var (
	version             = "debug" // will be set by the build system
	config              cfg.KrunConfig
	token               string
	debugSessions       = sessions.NewStore()
	portForwardRegistry = portforward.NewRegistry()
	hostsRegistry       = hostfile.NewRegistry()
	streamRegistry      = stream.NewRegistry("http://" + defaultManagerForwardAddress)
)

const (
	defaultHelperListenAddress       = "127.0.0.1:47831"
	defaultManagerForwardAddress     = "127.0.0.1:47832"
	defaultManagerForwardNamespace   = "krun-system"
	defaultManagerForwardServiceName = "krun-traffic-manager"
	defaultManagerForwardRemotePort  = 8080
)

type daemonOptions struct {
	externalShutdown <-chan struct{}
	onReady          func() // called when HTTP listener is bound
}

func main() {
	var krunConfigDir string
	var listenAddress string
	var serviceFlag bool

	rootCmd := &cobra.Command{
		Use:   "krun-helper",
		Short: "Elevated daemon for krun",
		Run: func(cmd *cobra.Command, args []string) {
			var err error
			if serviceFlag {
				err = runAsService(listenAddress, krunConfigDir)
			} else {
				err = startHelperServer(listenAddress, krunConfigDir, daemonOptions{
					externalShutdown: make(chan struct{}),
				})
			}
			if err != nil {
				fmt.Printf("krun-helper error: %v\n", err)
				os.Exit(1)
			}
		},
	}

	rootCmd.Flags().StringVar(&listenAddress, "listen", defaultHelperListenAddress, "Daemon listen address")

	rootCmd.Flags().StringVar(&krunConfigDir, "config-path", "", "Directory containing config.json and token.bin (e.g. ~/.krun)")
	if err := rootCmd.MarkFlagRequired("config-path"); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	rootCmd.Flags().BoolVar(&serviceFlag, "service", false, "Is it running as a service? Set by service unit")
	err := rootCmd.Flags().MarkHidden("service")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func startHelperServer(addr, krunConfigDir string, opts daemonOptions) error {

	loadedConfig, err := cfg.LoadKrunConfigDir(krunConfigDir)
	if err != nil {
		return fmt.Errorf("failed to load krun config: %w", err)
	}
	config = loadedConfig

	loadedToken, err := cfg.LoadTokenDir(krunConfigDir)
	if err != nil {
		return fmt.Errorf("failed to load auth token: %w", err)
	}
	token = loadedToken

	server := &http.Server{
		Handler:           newHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	fmt.Printf("version: %s\n", version)
	fmt.Printf("listening on %s\n", addr)

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

	return waitForHelperShutdown(server, serverErrCh, signalCh, opts.externalShutdown)
}

func waitForHelperShutdown(server *http.Server, serverErrCh <-chan error, signalCh <-chan os.Signal, externalShutdown <-chan struct{}) error {
	select {
	case err := <-serverErrCh:
		return fmt.Errorf("server error: %w", err)
	case sig := <-signalCh:
		fmt.Printf("received signal: %v, shutting down...\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	case <-externalShutdown:
		fmt.Println("received external shutdown signal, shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	}
}

func middlewareAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer "+token {
			network.WriteJSON(w, http.StatusUnauthorized, contracts.HelperResponse{
				Success: false,
				Message: "unauthorized",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newHandler() http.Handler {
	protected := http.NewServeMux()
	protected.HandleFunc("GET /v1/workspace/list", handleList)
	protected.HandleFunc("GET /v1/workspace/service/{serviceName}", handleGetService)
	protected.HandleFunc("POST /v1/workspace/build", handleBuild)
	protected.HandleFunc("POST /v1/workspace/deploy", handleDeploy)
	protected.HandleFunc("POST /v1/workspace/delete", handleDelete)
	protected.HandleFunc("GET /v1/debug/sessions", handleDebugSessionsList)
	protected.HandleFunc("POST /v1/debug/enable", handleDebugEnable)
	protected.HandleFunc("POST /v1/debug/disable", handleDebugDisable)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("/v1/", middlewareAuth(protected))
	return mux
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	network.WriteJSON(w, http.StatusOK, contracts.HelperResponse{
		Success: true,
		Message: "ok",
	})
}

func handleList(w http.ResponseWriter, r *http.Request) {
	serviceDefinitions, projectPaths, err := workspace.DiscoverServices(config.Path, config.SearchDepth)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Error discovering services: %s", err), utils.Red))
		os.Exit(0)
	}

	services := make([]string, 0, len(serviceDefinitions))
	for _, svc := range serviceDefinitions {
		services = append(services, svc.Name)
	}
	sort.Strings(services)

	projects := make([]string, 0, len(projectPaths))
	for project := range projectPaths {
		projects = append(projects, project)
	}
	sort.Strings(projects)

	network.WriteJSON(w, http.StatusOK, contracts.ListResponse{
		Services: services,
		Projects: projects,
	})
}

func handleGetService(w http.ResponseWriter, r *http.Request) {
	serviceName := r.PathValue("serviceName")
	if strings.TrimSpace(serviceName) == "" {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "serviceName is required",
		})
		return
	}

	services, _, err := workspace.DiscoverServices(config.Path, config.SearchDepth)
	if err != nil {
		network.WriteJSON(w, http.StatusInternalServerError, contracts.HelperResponse{
			Success: false,
			Message: fmt.Sprintf("discover services: %s", err),
		})
		return
	}

	for _, svc := range services {
		if svc.Name == serviceName {
			network.WriteJSON(w, http.StatusOK, svc)
			return
		}
	}

	network.WriteJSON(w, http.StatusNotFound, contracts.HelperResponse{
		Success: false,
		Message: fmt.Sprintf("service '%s' not found", serviceName),
	})
}

func handleBuild(w http.ResponseWriter, r *http.Request) {
	var request contracts.BuildRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid JSON payload",
		})
		return
	}

	if strings.TrimSpace(request.KubeConfig) == "" {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid payload: kube_config is required",
		})
		return
	}

	if strings.TrimSpace(request.Target) == "" {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid payload: target is required",
		})
		return
	}

	conf, services, err := buildConfig(request.KubeConfig)
	if err != nil {
		network.WriteJSON(w, http.StatusInternalServerError, contracts.HelperResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	serviceName, projectName, err := resolveTarget(request.Target, services)
	if err != nil {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}
	servicesToBuild := filterServicesForBuild(serviceName, projectName, services)

	network.StreamSSE(w, r, func(ctx context.Context, out io.Writer) error {
		return build.Build(ctx, out, projectName, servicesToBuild, request.SkipWeb, request.Force, request.Flush, conf)
	})
}

func handleDeploy(w http.ResponseWriter, r *http.Request) {
	var request contracts.DeployRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid JSON payload",
		})
		return
	}

	if strings.TrimSpace(request.KubeConfig) == "" {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid payload: kube_config is required",
		})
		return
	}

	if strings.TrimSpace(request.Target) == "" {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid payload: target is required",
		})
		return
	}

	conf, services, err := buildConfig(request.KubeConfig)
	if err != nil {
		network.WriteJSON(w, http.StatusInternalServerError, contracts.HelperResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	_, projectName, err := resolveTarget(request.Target, services)
	if err != nil {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	conf.Registry = conf.LocalRegistry
	if request.UseRemoteRegistry {
		conf.Registry = conf.RemoteRegistry
	}

	network.StreamSSE(w, r, func(ctx context.Context, out io.Writer) error {
		return deploy.Deploy(ctx, out, projectName, conf, !request.NoRestart)
	})
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	var request contracts.DeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid JSON payload",
		})
		return
	}

	if strings.TrimSpace(request.KubeConfig) == "" {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid payload: kube_config is required",
		})
		return
	}

	if strings.TrimSpace(request.Target) == "" {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid payload: target is required",
		})
		return
	}

	conf, services, err := buildConfig(request.KubeConfig)
	if err != nil {
		network.WriteJSON(w, http.StatusServiceUnavailable, contracts.HelperResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	_, projectName, err := resolveTarget(request.Target, services)
	if err != nil {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	network.StreamSSE(w, r, func(ctx context.Context, out io.Writer) error {
		return deploy.Delete(ctx, out, projectName, conf)
	})
}

func handleDebugSessionsList(w http.ResponseWriter, r *http.Request) {

	network.WriteJSON(w, http.StatusOK, contracts.HelperResponse{
		Success: true,
		Message: "ok",
	})
}

func handleDebugEnable(w http.ResponseWriter, r *http.Request) {
	var request contracts.DebugEnableRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid JSON payload",
		})
		return
	}

	// create debug session in traffic manager
	session, err := trafficmanagerclient.CreateSession(request.KubeConfig, request.Context)
	if err != nil {
		network.WriteJSON(w, http.StatusInternalServerError, contracts.HelperResponse{
			Success: false,
			Message: fmt.Sprintf("failed to create debug session: %s", err),
		})
		return
	}
	sessionKey := resolveDebugSessionKey(request.Context.Project, request.Context.ServiceName)
	debugSessions.Put(sessionKey, session)

	// set up port-forwards (manager relay + service dependencies)
	managerForward := managerPortForward()
	forwards := append([]contracts.PortForward{managerForward}, buildDebugPortForwards(request.Context)...)
	if err := portForwardRegistry.Upsert(session.SessionID, request.KubeConfig, forwards); err != nil {
		fmt.Printf("port-forward upsert failed: %v\n", err)
	}

	// wait for manager port-forward ready before stream attach
	if err := portForwardRegistry.WaitReady(managerForward, 10*time.Second); err != nil {
		fmt.Printf("manager port-forward not ready: %v\n", err)
	}

	// attach traffic stream
	if err := streamRegistry.Upsert(sessionKey, session.SessionID, session.SessionToken, request.Context.InterceptPort); err != nil {
		fmt.Printf("manager stream upsert failed: %v\n", err)
	}

	// write hosts file entries
	entries := buildDebugHostEntries(request.Context)
	mergedEntries := hostsRegistry.Upsert(sessionKey, entries)
	if err := hostfile.Update(mergedEntries); err != nil {
		fmt.Printf("failed to update hosts file: %v\n", err)
	}

	// write the service .env file
	if err := envfile.CreateEnvFile(config, request.KubeConfig, request.Context, request.ContainerName); err != nil {
		fmt.Printf("failed to create service .env file: %v\n", err)
	}

	network.WriteJSON(w, http.StatusOK, contracts.HelperResponse{
		Success: true,
		Message: "ok",
	})
}

func handleDebugDisable(w http.ResponseWriter, r *http.Request) {
	var request contracts.DebugDisableRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		network.WriteJSON(w, http.StatusBadRequest, contracts.HelperResponse{
			Success: false,
			Message: "invalid JSON payload",
		})
		return
	}

	// Find the session for the given context
	sessionKey := resolveDebugSessionKey(request.Context.Project, request.Context.ServiceName)
	session, ok := debugSessions.Get(sessionKey)
	if !ok {
		network.WriteJSON(w, http.StatusNotFound, contracts.HelperResponse{
			Success: false,
			Message: "debug session not found for the given context",
		})
		return
	}

	// delete debug session in traffic manager
	if err := trafficmanagerclient.DeleteSession(request.KubeConfig, session.SessionID); err != nil {
		fmt.Printf("failed to delete manager session: %v\n", err)
	}

	// clean up port-forwards
	if err := portForwardRegistry.Remove(session.SessionID); err != nil {
		fmt.Printf("failed to remove port-forward: %v\n", err)
	}

	// detach traffic stream
	if err := streamRegistry.Remove(sessionKey); err != nil {
		fmt.Printf("failed to remove manager stream: %v\n", err)
	}

	// clean up hosts file entries
	mergedEntries := hostsRegistry.Remove(sessionKey)
	if err := hostfile.Update(mergedEntries); err != nil {
		fmt.Printf("failed to update hosts file: %v\n", err)
	}

	// delete the service .env file
	if err := envfile.RemoveEnvFile(config, request.Context); err != nil {
		fmt.Printf("failed to remove service .env file: %v\n", err)
	}

	debugSessions.Delete(sessionKey)

	network.WriteJSON(w, http.StatusOK, contracts.HelperResponse{
		Success: true,
		Message: "ok",
	})
}

func resolveDebugSessionKey(project string, serviceName string) string {
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

func managerPortForward() contracts.PortForward {
	_, port, err := net.SplitHostPort(defaultManagerForwardAddress)
	localPort := 0
	if err == nil {
		if p, convErr := strconv.Atoi(port); convErr == nil {
			localPort = p
		}
	}
	return contracts.PortForward{
		Namespace:  defaultManagerForwardNamespace,
		Service:    defaultManagerForwardServiceName,
		LocalPort:  localPort,
		RemotePort: defaultManagerForwardRemotePort,
	}
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

func resolveTarget(name string, services []cfg.Service) (string, string, error) {
	serviceName := ""
	projectName := ""
	for _, s := range services {
		if s.Name == name {
			serviceName = s.Name
			projectName = s.Project
			break
		}
		if s.Project == name {
			projectName = s.Project
		}
	}

	if serviceName == "" && projectName == "" {
		return "", "", fmt.Errorf("service or project '%s' not found", name)
	}

	return serviceName, projectName, nil
}

func filterServicesForBuild(serviceName string, projectName string, services []cfg.Service) []cfg.Service {
	filtered := make([]cfg.Service, 0, len(services))
	for _, service := range services {
		if serviceName != "" {
			if service.Name == serviceName {
				filtered = append(filtered, service)
				break
			}
			continue
		}

		if service.Project == projectName {
			filtered = append(filtered, service)
		}
	}
	return filtered
}

func buildConfig(kubeConfig string) (cfg.Config, []cfg.Service, error) {

	services, projectPaths, err := workspace.DiscoverServices(config.Path, config.SearchDepth)
	if err != nil {
		return cfg.Config{}, nil, fmt.Errorf("discover services: %w", err)
	}

	return cfg.Config{
		KrunConfig:   config,
		KubeConfig:   kubeConfig,
		Registry:     config.LocalRegistry,
		ProjectPaths: projectPaths,
	}, services, nil
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
