package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/kube"
	"github.com/ftechmax/krun/internal/sessions"
	"github.com/ftechmax/krun/internal/traffic-manager/injector"
	"github.com/ftechmax/krun/internal/traffic-manager/relay"
	"github.com/google/uuid"
)

var (
	version         = "debug"
	sessionStore    = sessions.NewStore()
	relayRegistry   = relay.NewRegistry()
	sidecarInjector *injector.Injector
)

const injectTimeout = 30 * time.Second

const (
	defaultListenAddress     = ":8080"
	streamSessionIDQuery     = "session_id"
	streamSessionTokenQuery  = "session_token"
	streamSessionIDHeader    = "X-Krun-Session-ID"
	streamSessionTokenHeader = "X-Krun-Session-Token" //nolint:gosec
)

func main() {
	slog.Info("starting", "version", version)

	client, err := kube.NewInClusterClient()
	if err != nil {
		slog.Error("kube client init failed", "err", err)
		os.Exit(1)
	}

	sidecarInjector = injector.NewFromEnv(client.Clientset, version)
	slog.Info("sidecar injector ready",
		"image", sidecarInjector.AgentImage,
		"manager", sidecarInjector.ManagerAddress,
		"listen_port", sidecarInjector.AgentListenPort)

	server := &http.Server{
		Addr:              defaultListenAddress,
		Handler:           newHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	slog.Info("listening", "addr", defaultListenAddress)

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- server.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErrCh:
		if err == http.ErrServerClosed {
			slog.Info("server closed")
			return
		}
		slog.Error("server error", "err", err)
		os.Exit(1)
	case <-ctx.Done():
		slog.Info("received shutdown signal, shutting down traffic-manager")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("failed to shutdown server", "err", err)
			os.Exit(1)
		}
		if err := <-serverErrCh; err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
		slog.Info("traffic-manager shutdown complete")
	}
}

func newHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("POST /v1/sessions", handleCreateSession)
	mux.HandleFunc("GET /v1/sessions", handleListSessions)
	mux.HandleFunc("DELETE /v1/sessions/{sessionID}", handleDeleteSession)
	mux.HandleFunc("/v1/stream/agent", func(w http.ResponseWriter, r *http.Request) {
		handleStreamAttach(w, r, relay.RoleAgent)
	})
	mux.HandleFunc("/v1/stream/client", func(w http.ResponseWriter, r *http.Request) {
		handleStreamAttach(w, r, relay.RoleClient)
	})
	return mux
}

func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var request contracts.CreateDebugSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	if strings.TrimSpace(request.ServiceName) == "" {
		writeError(w, http.StatusBadRequest, "service_name is required")
		return
	}
	if request.ServicePort <= 0 {
		writeError(w, http.StatusBadRequest, "service_port must be greater than 0")
		return
	}
	if request.LocalPort <= 0 {
		writeError(w, http.StatusBadRequest, "local_port must be greater than 0")
		return
	}

	namespace := strings.TrimSpace(request.Namespace)
	if namespace == "" {
		namespace = "default"
	}
	workload := strings.TrimSpace(request.Workload)
	if workload == "" {
		workload = request.ServiceName
	}

	session := contracts.DebugSession{
		SessionID:    uuid.NewString(),
		SessionToken: uuid.NewString(),
		Namespace:    namespace,
		ServiceName:  request.ServiceName,
		Workload:     workload,
		ServicePort:  request.ServicePort,
		LocalPort:    request.LocalPort,
		ClientID:     request.ClientID,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	sessionStore.Put(session.SessionID, session)

	injectCtx, cancel := context.WithTimeout(r.Context(), injectTimeout)
	defer cancel()
	if err := sidecarInjector.Inject(injectCtx, session); err != nil {
		sessionStore.Delete(session.SessionID)
		slog.Error("sidecar injection failed", "session_id", session.SessionID, "err", err)
		writeError(w, http.StatusInternalServerError, "sidecar injection failed")
		return
	}
	slog.Info("sidecar injected",
		"session_id", session.SessionID,
		"namespace", session.Namespace,
		"workload", session.Workload)

	slog.Info("session created",
		"session_id", session.SessionID,
		"namespace", session.Namespace,
		"service", session.ServiceName)
	writeJSON(w, http.StatusCreated, session)
}

func handleListSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, contracts.ListDebugSessionsResponse{Sessions: sessionStore.List()})
}

func handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id is required")
		return
	}
	stored, ok := sessionStore.Get(sessionID)
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	removeCtx, cancel := context.WithTimeout(r.Context(), injectTimeout)
	defer cancel()
	if err := sidecarInjector.Remove(removeCtx, stored); err != nil {
		slog.Error("sidecar removal failed", "session_id", stored.SessionID, "err", err)
	}
	sessionStore.Delete(sessionID)
	slog.Info("session deleted", "session_id", stored.SessionID)
	w.WriteHeader(http.StatusNoContent)
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleStreamAttach(w http.ResponseWriter, r *http.Request, role string) {
	sessionID := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get(streamSessionIDQuery), r.Header.Get(streamSessionIDHeader)))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id is required")
		return
	}
	stored, ok := sessionStore.Get(sessionID)
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	token := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get(streamSessionTokenQuery), r.Header.Get(streamSessionTokenHeader)))
	if stored.SessionToken != "" && token != "" && token != stored.SessionToken {
		writeError(w, http.StatusUnauthorized, "invalid session token")
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		slog.Error("stream upgrade failed", "role", role, "session_id", stored.SessionID, "err", err)
		return
	}

	slog.Info("stream attached", "role", role, "session_id", stored.SessionID)
	if relayRegistry.ServePeer(role, stored.SessionID, websocket.NetConn(context.Background(), ws, websocket.MessageBinary)) {
		slog.Info("bridge ended", "session_id", stored.SessionID)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func writeError(w http.ResponseWriter, code int, message string) {
	slog.Warn("http error", "status", code, "message", message)
	writeJSON(w, code, map[string]string{
		"message": message,
	})
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("failed to write json response", "err", err)
	}
}
