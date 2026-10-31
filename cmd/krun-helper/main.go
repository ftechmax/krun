package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/helperipc"
	managerclient "github.com/ftechmax/krun/internal/krun-helper/manager-client"
	helperportforward "github.com/ftechmax/krun/internal/krun-helper/portforward"
	"github.com/ftechmax/krun/internal/krun-helper/session"
	helperstream "github.com/ftechmax/krun/internal/krun-helper/stream"
	"github.com/ftechmax/krun/internal/managerauth"
	"github.com/spf13/cobra"
)

// The manager port-forward is not tied to any debug session, so it gets a
// reserved key of its own in the port-forward registry.
const managerForwardKey = "__manager_api__"

var version = "debug" // will be set by the build system

type daemon struct {
	sessions   *session.Manager
	shutdownCh chan<- struct{}
}

type serverControl interface {
	Shutdown(context.Context) error
	Close() error
}

// warnOnManagerSkew reports mixed helper/runtime builds without rejecting them.
func warnOnManagerSkew(api *managerclient.Client) {
	managerVersion, err := api.Health()
	if err != nil {
		fmt.Printf("warning: could not read traffic-manager version: %v\n", err)
		return
	}
	if managerVersion != version {
		fmt.Printf("WARNING: version skew - krun-helper is %q but traffic-manager is %q\n", version, managerVersion)
		fmt.Printf("         reinstall the cluster runtime with: krun debug runtime install\n")
		return
	}
	fmt.Printf("traffic-manager version %s\n", managerVersion)
}

func main() {
	var kubeConfigPath string
	var socketEndpoint string
	var managerTokenFile string

	rootCmd := &cobra.Command{
		Use:   "krun-helper",
		Short: "Local daemon for krun debug sessions (runs unprivileged)",
		Run: func(cmd *cobra.Command, args []string) {
			if err := startHelperDaemon(socketEndpoint, kubeConfigPath, managerTokenFile); err != nil {
				fmt.Printf("helper daemon failed: %v\n", err)
				os.Exit(1)
			}
		},
	}

	rootCmd.AddCommand(hostsDaemonCommand())

	rootCmd.Flags().StringVar(&kubeConfigPath, "kubeconfig", "", "Path to kubeconfig file")
	rootCmd.Flags().StringVar(&socketEndpoint, "socket", "", "IPC endpoint override (unix socket path / named pipe name)")
	rootCmd.Flags().StringVar(&managerTokenFile, "manager-token-file", "", "Path to the local traffic-manager bearer token")

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func startHelperDaemon(socketEndpoint string, kubeConfigPath string, managerTokenFile string) error {
	endpoint := strings.TrimSpace(socketEndpoint)
	if endpoint == "" {
		endpoint = helperipc.DefaultEndpoint()
	}
	authToken, err := managerauth.ReadLocalToken(managerTokenFile)
	if err != nil {
		return fmt.Errorf("load manager authentication: %w", err)
	}

	forwards, err := helperportforward.NewSessionRegistry(kubeConfigPath)
	if err != nil {
		return fmt.Errorf("failed to initialize port-forward registry: %w", err)
	}
	defer forwards.Clear()

	managerAddress, err := startManagerForward(forwards)
	if err != nil {
		return err
	}
	fmt.Printf("manager api forwarded to %s\n", managerAddress)

	api := managerclient.NewSessionClient(managerAddress, authToken)
	warnOnManagerSkew(api)

	sessions := session.NewManager(session.Options{
		Forwards: forwards,
		API:      api,
		Attach: func(sessionID string, sessionToken string, interceptPort int) (session.Stream, error) {
			return helperstream.Attach(managerAddress, sessionID, sessionToken, interceptPort)
		},
	})

	defer sessions.Close()

	shutdownCh := make(chan struct{}, 1)

	listener, err := helperipc.Listen(endpoint)
	if err != nil {
		return fmt.Errorf("failed to listen on helper endpoint: %w", err)
	}
	defer helperipc.Cleanup(endpoint)

	server := &http.Server{
		Handler:           newHandler(&daemon{sessions: sessions, shutdownCh: shutdownCh}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("krun-helper %s listening on %s\n", version, endpoint)

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- server.Serve(listener)
	}()

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signalCh)

	return runHelperServer(server, serverErrCh, signalCh, shutdownCh)
}

func runHelperServer(server serverControl, serverErrCh <-chan error, signalCh <-chan os.Signal, shutdownCh <-chan struct{}) error {
	doShutdown := func(reason string) error {
		fmt.Printf("%s, shutting down krun-helper\n", reason)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		var closeErr error
		if shutdownErr != nil {
			closeErr = server.Close()
		}
		serveErr := <-serverErrCh
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		if shutdownErr != nil {
			shutdownErr = fmt.Errorf("shutdown helper server: %w", shutdownErr)
		}
		return errors.Join(shutdownErr, closeErr, serveErr)
	}

	select {
	case err := <-serverErrCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case sig := <-signalCh:
		return doShutdown(fmt.Sprintf("received %s", sig))
	case <-shutdownCh:
		return doShutdown("shutdown requested via API")
	}
}

// startManagerForward brings up the port-forward that carries both the session
// REST calls and the traffic streams, and reports its local address. Local
// port 0 lets the OS pick a free port, so a busy port can never block helper
// startup; the bound port is queried back afterwards.
func startManagerForward(forwards *helperportforward.SessionRegistry) (string, error) {
	forward := contracts.PortForward{
		Namespace:  managerclient.ManagerNamespace,
		Service:    managerclient.ManagerServiceName,
		LocalPort:  0,
		RemotePort: managerclient.ManagerServicePort,
	}
	if err := forwards.Upsert(managerForwardKey, []contracts.PortForward{forward}); err != nil {
		return "", fmt.Errorf("failed to initialize manager api port-forward: %w", err)
	}

	boundPort, ok := forwards.BoundLocalPort(managerForwardKey, forward)
	if !ok || boundPort <= 0 {
		return "", fmt.Errorf("manager api port-forward reported no bound local port")
	}
	return fmt.Sprintf("http://127.0.0.1:%d", boundPort), nil
}

func newHandler(d *daemon) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", d.handleHealthz)
	mux.HandleFunc("GET /v1/debug/sessions", d.handleSessionsList)
	mux.HandleFunc("POST /v1/debug/enable", d.handleEnable)
	mux.HandleFunc("POST /v1/debug/disable", d.handleDisable)
	mux.HandleFunc("POST /v1/shutdown", d.handleShutdown)
	return mux
}

func (d *daemon) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, contracts.HelperResponse{Success: true, Message: "ok", Version: version})
}

func (d *daemon) handleShutdown(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, contracts.HelperResponse{Success: true, Message: "shutting down"})
	select {
	case d.shutdownCh <- struct{}{}:
	default:
	}
}

func (d *daemon) handleSessionsList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, contracts.HelperDebugSessionsResponse{Sessions: d.sessions.List()})
}

func (d *daemon) handleEnable(w http.ResponseWriter, r *http.Request) {
	request, err := parseDebugCommandRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, contracts.HelperResponse{Success: false, Message: err.Error()})
		return
	}

	if err := d.sessions.Enable(request); err != nil {
		writeJSON(w, http.StatusInternalServerError, contracts.HelperResponse{Success: false, Message: err.Error()})
		return
	}

	hosts := d.sessions.HostsSnapshot()
	writeJSON(w, http.StatusOK, contracts.HelperResponse{
		Success:       true,
		Message:       "debug enable applied",
		HostsEntries:  hosts.Entries,
		HostsRevision: hosts.Revision,
	})
}

func (d *daemon) handleDisable(w http.ResponseWriter, r *http.Request) {
	request, err := parseDebugCommandRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, contracts.HelperResponse{Success: false, Message: err.Error()})
		return
	}

	found, err := d.sessions.Disable(request)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, contracts.HelperResponse{Success: false, Message: err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, contracts.HelperResponse{Success: true, Message: "no active session"})
		return
	}

	hosts := d.sessions.HostsSnapshot()
	writeJSON(w, http.StatusOK, contracts.HelperResponse{
		Success:       true,
		Message:       "debug disable applied",
		HostsEntries:  hosts.Entries,
		HostsRevision: hosts.Revision,
	})
}

func parseDebugCommandRequest(r *http.Request) (config.Service, error) {
	defer r.Body.Close()

	var request config.Service
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return config.Service{}, fmt.Errorf("invalid payload: %w", err)
	}
	if request.Name == "" {
		return config.Service{}, fmt.Errorf("invalid payload: name is required")
	}
	return request, nil
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
