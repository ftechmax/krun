package managerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
)

func TestManagerClientCreateSession(t *testing.T) {
	var createReq contracts.CreateDebugSessionRequest
	client, closeFn := newTestManagerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/sessions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&createReq); err != nil {
			t.Fatalf("decode create request: %v", err)
		}
		writeJSONResponse(t, w, http.StatusCreated, contracts.DebugSession{
			SessionID:   "mgr-session-1",
			ServiceName: "orders-api",
		})
	})
	defer closeFn()

	created, err := client.CreateSession(config.Service{
		Namespace:     "dev",
		Name:          "orders-api",
		ContainerPort: 8080,
		InterceptPort: 5000,
	})
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}
	if created.SessionID != "mgr-session-1" {
		t.Fatalf("expected created session id, got %q", created.SessionID)
	}
	if createReq.Namespace != "dev" {
		t.Fatalf("expected service namespace, got %q", createReq.Namespace)
	}
	if createReq.ServiceName != "orders-api" {
		t.Fatalf("expected service name, got %q", createReq.ServiceName)
	}
	if createReq.ServicePort != 8080 {
		t.Fatalf("unexpected service port: %d", createReq.ServicePort)
	}
}

func TestManagerClientCreateSessionSurfacesManagerError(t *testing.T) {
	client, closeFn := newTestManagerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusNotFound, map[string]string{
			"message": "failed to inject traffic-agent sidecar",
		})
	})
	defer closeFn()

	_, err := client.CreateSession(config.Service{
		Name:          "orders-api",
		ContainerPort: 8080,
		InterceptPort: 5000,
	})
	if err == nil {
		t.Fatal("expected an error when the manager rejects the create")
	}
}

func TestManagerClientDeleteSession(t *testing.T) {
	var deletePath string
	client, closeFn := newTestManagerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		deletePath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	defer closeFn()

	if err := client.DeleteSession("mgr-delete-1"); err != nil {
		t.Fatalf("delete session failed: %v", err)
	}
	if deletePath != "/v1/sessions/mgr-delete-1" {
		t.Fatalf("unexpected delete path: %s", deletePath)
	}
}

func TestManagerClientDeleteSessionNotFoundIsIgnored(t *testing.T) {
	client, closeFn := newTestManagerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusNotFound, map[string]string{"message": "session not found"})
	})
	defer closeFn()

	if err := client.DeleteSession("mgr-missing"); err != nil {
		t.Fatalf("expected 404 to be ignored, got %v", err)
	}
}

func TestManagerClientHealthOmitsAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" || r.Header.Get(authTokenHeader) != "" {
			t.Errorf("unexpected health request: %s, auth present: %v", r.URL.Path, r.Header.Get(authTokenHeader) != "")
		}
		writeJSONResponse(t, w, http.StatusOK, map[string]string{"version": "test-version"})
	}))
	defer server.Close()
	client := NewSessionClient(server.URL, testAuthToken)
	version, err := client.Health()
	if err != nil || version != "test-version" {
		t.Fatalf("health version = %q, err = %v", version, err)
	}
}

const testAuthToken = "test-auth-token"

func newTestManagerClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(authTokenHeader); got != testAuthToken {
			t.Errorf("manager request missing auth token header, got %q", got)
		}
		handler(w, r)
	}))

	return &Client{
		baseURL:   server.URL,
		authToken: testAuthToken,
		http:      server.Client(),
	}, server.Close
}

func writeJSONResponse(t *testing.T, w http.ResponseWriter, code int, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
