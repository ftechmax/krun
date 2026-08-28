package managerclient

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/kube"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
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

	created, err := client.CreateSession(contracts.DebugServiceContext{
		Namespace:     " dev ",
		ServiceName:   " orders-api ",
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
		t.Fatalf("expected normalized namespace, got %q", createReq.Namespace)
	}
	if createReq.ServiceName != "orders-api" {
		t.Fatalf("expected trimmed service name, got %q", createReq.ServiceName)
	}
	if createReq.Workload != "orders-api" {
		t.Fatalf("expected workload to match service name, got %q", createReq.Workload)
	}
	if createReq.ServicePort != 8080 || createReq.LocalPort != 5000 {
		t.Fatalf("unexpected ports in create request: %+v", createReq)
	}
	if createReq.ClientID != ManagerClientID {
		t.Fatalf("expected client_id %q, got %q", ManagerClientID, createReq.ClientID)
	}
}

func TestManagerClientCreateSessionValidation(t *testing.T) {
	client, closeFn := newTestManagerClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("expected no request to be sent, got %s %s", r.Method, r.URL.Path)
	})
	defer closeFn()

	if _, err := client.CreateSession(contracts.DebugServiceContext{
		ContainerPort: 8080,
		InterceptPort: 5000,
	}); err == nil {
		t.Fatalf("expected error for empty service_name")
	}
	if _, err := client.CreateSession(contracts.DebugServiceContext{
		ServiceName:   "orders-api",
		ContainerPort: 0,
		InterceptPort: 5000,
	}); err == nil {
		t.Fatalf("expected error for invalid service_port")
	}
	if _, err := client.CreateSession(contracts.DebugServiceContext{
		ServiceName:   "orders-api",
		ContainerPort: 8080,
		InterceptPort: 0,
	}); err == nil {
		t.Fatalf("expected error for invalid local_port")
	}
}

func TestManagerClientCreateSessionRejectsEmptySessionID(t *testing.T) {
	client, closeFn := newTestManagerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusCreated, contracts.DebugSession{ServiceName: "orders-api"})
	})
	defer closeFn()

	if _, err := client.CreateSession(contracts.DebugServiceContext{
		ServiceName:   "orders-api",
		ContainerPort: 8080,
		InterceptPort: 5000,
	}); err == nil {
		t.Fatal("expected an error when the manager returns no session id")
	}
}

func TestManagerClientCreateSessionSurfacesManagerError(t *testing.T) {
	client, closeFn := newTestManagerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusNotFound, map[string]string{
			"message": "failed to inject traffic-agent sidecar",
		})
	})
	defer closeFn()

	_, err := client.CreateSession(contracts.DebugServiceContext{
		ServiceName:   "orders-api",
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

func TestManagerClientDeleteSessionIgnoresBlankID(t *testing.T) {
	client, closeFn := newTestManagerClient(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Fatalf("expected no request for a blank session id, got %s %s", r.Method, r.URL.Path)
	})
	defer closeFn()

	if err := client.DeleteSession("  "); err != nil {
		t.Fatalf("expected blank session id to be a no-op, got %v", err)
	}
}

func TestNormalizeNamespace(t *testing.T) {
	if got := NormalizeNamespace(""); got != "default" {
		t.Fatalf("expected default namespace, got %q", got)
	}
	if got := NormalizeNamespace("  dev  "); got != "dev" {
		t.Fatalf("expected trimmed namespace, got %q", got)
	}
}

const testAuthToken = "test-auth-token"

// newTestManagerClient serves both roles from one httptest server: the kube
// API paths that hand out the auth Secret, and the manager's own session API
// reached over the port-forward.
func newTestManagerClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/secrets/"+authSecretName) {
			writeJSONResponse(t, w, http.StatusOK, map[string]any{
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata": map[string]any{
					"name":      authSecretName,
					"namespace": ManagerNamespace,
				},
				"data": map[string]string{
					authSecretKey: base64.StdEncoding.EncodeToString([]byte(testAuthToken)),
				},
			})
			return
		}
		if got := r.Header.Get(authTokenHeader); got != testAuthToken {
			t.Errorf("manager request missing auth token header, got %q", got)
		}
		handler(w, r)
	}))

	groupVersion := schema.GroupVersion{Version: "v1"}
	restConfig := &rest.Config{
		Host:    server.URL,
		APIPath: "/api",
		ContentConfig: rest.ContentConfig{
			GroupVersion:         &groupVersion,
			NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		},
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		server.Close()
		t.Fatalf("create kubernetes clientset: %v", err)
	}

	return &Client{
		baseURL: server.URL,
		kube:    &kube.Client{RestConfig: restConfig, Clientset: clientset},
		http:    server.Client(),
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
