package helperclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/krun/helper"
)

func TestWorkspaceListFetchesServices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/v1/services":
			if r.Method != http.MethodGet {
				t.Fatalf("expected GET, got %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"services":[{"name":"api","project":"proj"}],"projects":["proj"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalBaseURL := helper.BaseURL
	helper.BaseURL = server.URL
	t.Cleanup(func() { helper.BaseURL = originalBaseURL })

	list, err := WorkspaceList(cfg.Config{})
	if err != nil {
		t.Fatalf("WorkspaceList returned error: %v", err)
	}
	if len(list.Services) != 1 || list.Services[0].Name != "api" {
		t.Fatalf("unexpected services response: %+v", list.Services)
	}
	if len(list.Projects) != 1 || list.Projects[0] != "proj" {
		t.Fatalf("unexpected projects response: %+v", list.Projects)
	}
}

func TestWorkspaceBuildStreamsLogEventsToOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/v1/build":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST, got %s", r.Method)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatalf("response writer does not implement Flusher")
			}

			_, _ = fmt.Fprint(w, "event: log\n")
			_, _ = fmt.Fprint(w, "data: {\"stream\":\"stdout\",\"text\":\"hello \"}\n\n")
			flusher.Flush()
			_, _ = fmt.Fprint(w, "event: log\n")
			_, _ = fmt.Fprint(w, "data: {\"stream\":\"stdout\",\"text\":\"world\\n\"}\n\n")
			flusher.Flush()
			_, _ = fmt.Fprint(w, "event: done\n")
			_, _ = fmt.Fprint(w, "data: {\"ok\":true}\n\n")
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalBaseURL := helper.BaseURL
	helper.BaseURL = server.URL
	t.Cleanup(func() { helper.BaseURL = originalBaseURL })

	var output bytes.Buffer
	err := WorkspaceBuild(cfg.Config{}, contracts.BuildRequest{Target: "proj"}, &output)
	if err != nil {
		t.Fatalf("WorkspaceBuild returned error: %v", err)
	}
	if output.String() != "hello world\n" {
		t.Fatalf("unexpected streamed output %q", output.String())
	}
}

func TestWorkspaceDeployReturnsErrorWhenDoneNotOk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/v1/deploy":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST, got %s", r.Method)
			}
			var req contracts.DeployRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if req.Target != "proj" {
				t.Fatalf("unexpected target %q", req.Target)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatalf("response writer does not implement Flusher")
			}
			_, _ = fmt.Fprint(w, "event: done\n")
			_, _ = fmt.Fprint(w, "data: {\"ok\":false,\"error\":\"deploy failed\"}\n\n")
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalBaseURL := helper.BaseURL
	helper.BaseURL = server.URL
	t.Cleanup(func() { helper.BaseURL = originalBaseURL })

	err := WorkspaceDeploy(cfg.Config{}, contracts.DeployRequest{Target: "proj"}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected WorkspaceDeploy to return error")
	}
	if !strings.Contains(err.Error(), "deploy failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
