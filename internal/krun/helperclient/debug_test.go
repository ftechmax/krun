package helperclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	osuser "os/user"
	"strings"
	"testing"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/krun/helper"
)

func TestHelperDebugEnableSendsRequestAndParsesResponse(t *testing.T) {
	originalLookupCurrentUser := lookupCurrentUser
	lookupCurrentUser = func() (*osuser.User, error) {
		return &osuser.User{
			Uid: "1000",
			Gid: "1001",
		}, nil
	}
	t.Cleanup(func() { lookupCurrentUser = originalLookupCurrentUser })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/v1/debug/enable":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST, got %s", r.Method)
			}

			var request contracts.DebugSessionCommandRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if request.Context.ServiceName != "svc-a" {
				t.Fatalf("unexpected service name %q", request.Context.ServiceName)
			}
			if request.ContainerName != "custom-container" {
				t.Fatalf("unexpected container name %q", request.ContainerName)
			}
			if request.User.UID != "1000" || request.User.GID != "1001" {
				t.Fatalf("unexpected uid/gid in request user: %+v", request.User)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"message":"debug enable applied"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalBaseURL := helper.BaseURL
	helper.BaseURL = server.URL
	t.Cleanup(func() { helper.BaseURL = originalBaseURL })

	response, err := helperDebugEnable(cfg.Config{}, contracts.DebugServiceContext{ServiceName: "svc-a"}, "custom-container")
	if err != nil {
		t.Fatalf("helperDebugEnable returned error: %v", err)
	}
	if !response.Success {
		t.Fatalf("expected success response, got %+v", response)
	}
}

func TestHelperDebugDisableReturnsErrorPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/v1/debug/disable":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"success":false,"message":"invalid session"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalBaseURL := helper.BaseURL
	helper.BaseURL = server.URL
	t.Cleanup(func() { helper.BaseURL = originalBaseURL })

	_, err := helperDebugDisable(cfg.Config{}, contracts.DebugServiceContext{ServiceName: "svc-a"})
	if err == nil {
		t.Fatalf("expected helperDebugDisable to return error")
	}
	if !strings.Contains(err.Error(), "invalid session") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHelperDebugSessionsListDecodesSessions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/v1/debug/sessions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sessions":[{"session_key":"proj/svc","context":{"service_name":"svc"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalBaseURL := helper.BaseURL
	helper.BaseURL = server.URL
	t.Cleanup(func() { helper.BaseURL = originalBaseURL })

	sessions, err := helperDebugSessionsList(cfg.Config{})
	if err != nil {
		t.Fatalf("helperDebugSessionsList returned error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Context.ServiceName != "svc" {
		t.Fatalf("unexpected sessions response: %+v", sessions)
	}
}
