package managerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		if !strings.HasSuffix(r.URL.Path, "/proxy/v1/sessions") {
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

func TestManagerClientListSessions(t *testing.T) {
	client, closeFn := newTestManagerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/proxy/v1/sessions") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeJSONResponse(t, w, http.StatusOK, contracts.ListDebugSessionsResponse{
			Sessions: []contracts.DebugSession{
				{SessionID: "mgr-1", ServiceName: "orders-api"},
				{SessionID: "mgr-2", ServiceName: "payments-api"},
			},
		})
	})
	defer closeFn()

	sessions, err := client.ListSessions()
	if err != nil {
		t.Fatalf("list sessions failed: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %+v", sessions)
	}
	if sessions[0].SessionID != "mgr-1" || sessions[1].SessionID != "mgr-2" {
		t.Fatalf("unexpected sessions list: %+v", sessions)
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
	if !strings.HasSuffix(deletePath, "/proxy/v1/sessions/mgr-delete-1") {
		t.Fatalf("unexpected delete path: %s", deletePath)
	}
}

func TestManagerClientDeleteSessionNotFoundIsIgnored(t *testing.T) {
	client, closeFn := newTestManagerClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(t, w, http.StatusNotFound, metav1.Status{
			Status:  metav1.StatusFailure,
			Reason:  metav1.StatusReasonNotFound,
			Message: "session not found",
			Code:    http.StatusNotFound,
		})
	})
	defer closeFn()

	if err := client.DeleteSession("mgr-missing"); err != nil {
		t.Fatalf("expected 404 to be ignored, got %v", err)
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

func newTestManagerClient(t *testing.T, handler http.HandlerFunc) (*kubeManagerSessionClient, func()) {
	t.Helper()

	server := httptest.NewServer(handler)

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

	return &kubeManagerSessionClient{
		client: &kube.Client{
			RestConfig: restConfig,
			Clientset:  clientset,
		},
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
