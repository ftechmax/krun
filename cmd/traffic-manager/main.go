package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/kube"
	"github.com/ftechmax/krun/internal/managerauth"
	"github.com/ftechmax/krun/internal/traffic-manager/agent"
	sessionregistry "github.com/ftechmax/krun/internal/traffic-manager/session"
	streamrelay "github.com/ftechmax/krun/internal/traffic-manager/stream"
	"github.com/gorilla/websocket"
	"k8s.io/client-go/kubernetes"
)

var (
	version = "debug" // will be set by the build system

	streamUpgrader = websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}
	errStreamSessionNotFound = errors.New("session not found")
	errStreamUnauthorized    = errors.New("invalid session token")
)

type server struct {
	sessions      *sessionregistry.DebugSessionRegistry
	injector      agent.Injector
	relay         *streamrelay.SessionRelayRegistry
	authTokenHash []byte
}

func newServer(injector agent.Injector, authTokenHash []byte) *server {
	return &server{
		sessions:      sessionregistry.NewDebugSessionRegistry(),
		injector:      injector,
		relay:         streamrelay.NewSessionRelayRegistry(),
		authTokenHash: append([]byte(nil), authTokenHash...),
	}
}

const (
	defaultListenAddress = ":8080"

	envAgentContainerName    = "KRUN_AGENT_CONTAINER_NAME"
	envAgentImage            = "KRUN_AGENT_IMAGE"
	envAgentImagePullPolicy  = "KRUN_AGENT_IMAGE_PULL_POLICY"
	envAgentProbePort        = "KRUN_AGENT_PROBE_PORT"
	envManagerAddress        = "KRUN_MANAGER_ADDRESS"
	streamSessionIDQuery     = "session_id"
	streamSessionTokenQuery  = "session_token"
	streamSessionIDHeader    = "X-Krun-Session-ID"
	streamSessionTokenHeader = "X-Krun-Session-Token"
	streamAgentVersionHeader = "X-Krun-Agent-Version"

	// The custom header avoids collision with Authorization handling by
	// Kubernetes and HTTP intermediaries.
	authTokenHeader = "X-Krun-Auth-Token"
)

func main() {
	if err := run(); err != nil {
		log.Printf("traffic-manager failed: %v", err)
		os.Exit(1)
	}
}

func run() error {
	client, err := kube.NewInClusterClient()
	if err != nil {
		return fmt.Errorf("initialize kubernetes client: %w", err)
	}

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStartup()

	authTokenHash, err := configuredAuthTokenHash()
	if err != nil {
		return fmt.Errorf("configure manager API authentication: %w", err)
	}

	manager := newServer(newInjector(client.Clientset), authTokenHash)

	log.Printf("cleaning up dangling traffic-agent sidecars")
	if err := manager.cleanupDanglingAgents(startupCtx); err != nil {
		return err
	}

	server := &http.Server{
		Addr:              defaultListenAddress,
		Handler:           newHandler(manager),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("krun traffic-manager listening on %s", defaultListenAddress)
	log.Printf("version: %s", version)

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- server.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErrCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Printf("received shutdown signal, shutting down traffic-manager")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
		if err := <-serverErrCh; err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}

func configuredAuthTokenHash() ([]byte, error) {
	return managerauth.ParseDigest(os.Getenv(managerauth.TokenHashEnv))
}

func newHandler(s *server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/v1/sessions", s.requireAuthToken(s.handleSessions))
	mux.HandleFunc("/v1/sessions/", s.requireAuthToken(s.handleSessionByID))
	mux.HandleFunc("/v1/stream/agent", func(w http.ResponseWriter, r *http.Request) {
		s.handleStreamAttach(w, r, contracts.StreamRoleAgent)
	})
	mux.HandleFunc("/v1/stream/client", func(w http.ResponseWriter, r *http.Request) {
		s.handleStreamAttach(w, r, contracts.StreamRoleClient)
	})
	return mux
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
}

func (s *server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleCreateSession(w, r)
	case http.MethodGet:
		writeJSON(w, http.StatusOK, contracts.ListDebugSessionsResponse{
			Sessions: s.sessions.List(),
		})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *server) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id, err := parseSessionID(r.URL.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "session id not found")
		return
	}

	debugSession, ok := s.sessions.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	if err := s.injector.Remove(r.Context(), debugSession); err != nil && !errors.Is(err, agent.ErrWorkloadNotFound) {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to remove traffic-agent sidecar: %v", err))
		return
	}

	s.sessions.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var request contracts.CreateDebugSessionRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid payload")
		return
	}

	session, err := s.sessions.Create(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.injector.Inject(r.Context(), session); err != nil {
		s.sessions.Delete(session.SessionID)
		statusCode := http.StatusInternalServerError
		if errors.Is(err, agent.ErrWorkloadNotFound) {
			statusCode = http.StatusNotFound
		}
		writeError(w, statusCode, fmt.Sprintf("failed to inject traffic-agent sidecar: %v", err))
		return
	}

	writeJSON(w, http.StatusCreated, session)
}

func (s *server) handleStreamAttach(w http.ResponseWriter, r *http.Request, role string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	debugSession, err := s.resolveStreamSession(r)
	if err != nil {
		statusCode := http.StatusBadRequest
		if errors.Is(err, errStreamSessionNotFound) {
			statusCode = http.StatusNotFound
		}
		if errors.Is(err, errStreamUnauthorized) {
			statusCode = http.StatusUnauthorized
		}
		writeError(w, statusCode, err.Error())
		return
	}

	conn, err := streamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("stream upgrade failed (role=%s session_id=%s): %v", role, debugSession.SessionID, err)
		return
	}

	if role == contracts.StreamRoleAgent {
		// A stale agent image (a re-pulled :latest, a workload never rolled)
		// speaks the same protocol and looks healthy, so surface the skew
		// rather than letting it pass as a working session.
		if agentVersion := strings.TrimSpace(r.Header.Get(streamAgentVersionHeader)); agentVersion != "" && agentVersion != version {
			log.Printf("WARNING: version skew - traffic-agent is %q but traffic-manager is %q (session_id=%s)", agentVersion, version, debugSession.SessionID)
		}
	}
	log.Printf("stream attached (role=%s session_id=%s)", role, debugSession.SessionID)
	s.relay.ServePeer(role, debugSession.SessionID, conn)
}

func parseSessionID(path string) (string, error) {
	id := strings.TrimPrefix(path, "/v1/sessions/")
	if id == "" || strings.Contains(id, "/") {
		return "", errors.New("missing id")
	}
	unescaped, err := url.PathUnescape(id)
	if err != nil {
		return "", errors.New("invalid id")
	}
	trimmed := strings.TrimSpace(unescaped)
	if trimmed == "" {
		return "", errors.New("invalid id")
	}
	return trimmed, nil
}

func (s *server) resolveStreamSession(r *http.Request) (contracts.DebugSession, error) {
	sessionID := strings.TrimSpace(r.URL.Query().Get(streamSessionIDQuery))
	if sessionID == "" {
		sessionID = strings.TrimSpace(r.Header.Get(streamSessionIDHeader))
	}
	if sessionID == "" {
		return contracts.DebugSession{}, errors.New("session id is required")
	}

	debugSession, ok := s.sessions.Get(sessionID)
	if !ok {
		return contracts.DebugSession{}, errStreamSessionNotFound
	}

	sessionToken := strings.TrimSpace(r.URL.Query().Get(streamSessionTokenQuery))
	if sessionToken == "" {
		sessionToken = strings.TrimSpace(r.Header.Get(streamSessionTokenHeader))
	}
	if strings.TrimSpace(debugSession.SessionToken) != "" && !constantTimeTokenEqual(sessionToken, debugSession.SessionToken) {
		return contracts.DebugSession{}, errStreamUnauthorized
	}

	return debugSession, nil
}

func writeError(w http.ResponseWriter, code int, message string) {
	log.Printf("http error status=%d message=%q", code, message)
	writeJSON(w, code, map[string]string{
		"message": message,
	})
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

func newInjector(clientset kubernetes.Interface) agent.Injector {
	probePort, _ := strconv.Atoi(strings.TrimSpace(os.Getenv(envAgentProbePort)))

	return agent.NewWorkloadInjector(clientset, agent.Options{
		ContainerName:   strings.TrimSpace(os.Getenv(envAgentContainerName)),
		Image:           strings.TrimSpace(os.Getenv(envAgentImage)),
		ImagePullPolicy: strings.TrimSpace(os.Getenv(envAgentImagePullPolicy)),
		ManagerAddress:  strings.TrimSpace(os.Getenv(envManagerAddress)),
		ProbePort:       probePort,
	})
}

func (s *server) requireAuthToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(r.Header.Get(authTokenHeader))
		digest := sha256.Sum256([]byte(token))
		if len(s.authTokenHash) != sha256.Size || subtle.ConstantTimeCompare(digest[:], s.authTokenHash) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid or missing auth token")
			return
		}
		next(w, r)
	}
}

func constantTimeTokenEqual(actual string, expected string) bool {
	actualDigest := sha256.Sum256([]byte(strings.TrimSpace(actual)))
	expectedDigest := sha256.Sum256([]byte(strings.TrimSpace(expected)))
	return subtle.ConstantTimeCompare(actualDigest[:], expectedDigest[:]) == 1
}

// cleanupDanglingAgents removes sidecars left behind by a previous manager
// instance, whose sessions did not survive the restart.
func (s *server) cleanupDanglingAgents(ctx context.Context) error {
	if err := s.injector.Cleanup(ctx); err != nil {
		return fmt.Errorf("cleanup dangling traffic-agent sidecars: %w", err)
	}
	return nil
}
