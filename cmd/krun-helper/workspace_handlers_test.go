package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
)

func TestHandleServicesListReturnsDiscoveredServicesAndProjects(t *testing.T) {
	resetHelperGlobals(t)

	helperKrunConfigLoaded = true
	helperKrunConfig = cfg.KrunConfig{
		KrunSourceConfig: cfg.KrunSourceConfig{Path: "/workspace", SearchDepth: 1},
		LocalRegistry:    "registry:5000",
		RemoteRegistry:   "registry:5001",
	}
	discoverServices = func(_ string, _ int) ([]cfg.Service, map[string]string, error) {
		return []cfg.Service{
			{Name: "api", Project: "alpha"},
			{Name: "worker", Project: "alpha"},
			{Name: "web", Project: "beta"},
		}, map[string]string{"alpha": "alpha", "beta": "beta"}, nil
	}

	handler := newHandler(make(chan struct{}, 1))
	req := httptest.NewRequest(http.MethodGet, "/v1/services", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var response contracts.ServiceListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Services) != 3 {
		t.Fatalf("expected 3 services, got %d", len(response.Services))
	}
	if len(response.Projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(response.Projects))
	}
	if response.Projects[0] != "alpha" || response.Projects[1] != "beta" {
		t.Fatalf("unexpected project list: %#v", response.Projects)
	}
}

func TestHandleBuildReturnsBadRequestWhenTargetMissing(t *testing.T) {
	resetHelperGlobals(t)
	helperKrunConfigLoaded = true
	helperKrunConfig = cfg.KrunConfig{KrunSourceConfig: cfg.KrunSourceConfig{Path: "/workspace", SearchDepth: 1}}
	discoverServices = func(_ string, _ int) ([]cfg.Service, map[string]string, error) {
		return []cfg.Service{{Name: "api", Project: "proj"}}, map[string]string{"proj": "proj"}, nil
	}

	handler := newHandler(make(chan struct{}, 1))
	req := httptest.NewRequest(http.MethodPost, "/v1/build", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleBuildReturnsBadRequestWhenTargetUnknown(t *testing.T) {
	resetHelperGlobals(t)
	helperKrunConfigLoaded = true
	helperKrunConfig = cfg.KrunConfig{KrunSourceConfig: cfg.KrunSourceConfig{Path: "/workspace", SearchDepth: 1}}
	discoverServices = func(_ string, _ int) ([]cfg.Service, map[string]string, error) {
		return []cfg.Service{{Name: "api", Project: "proj"}}, map[string]string{"proj": "proj"}, nil
	}

	handler := newHandler(make(chan struct{}, 1))
	req := httptest.NewRequest(http.MethodPost, "/v1/build", bytes.NewReader([]byte(`{"target":"missing"}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("expected not-found response, got %q", rec.Body.String())
	}
}

func TestHandleBuildReturnsServiceUnavailableWhenConfigNotLoaded(t *testing.T) {
	resetHelperGlobals(t)

	handler := newHandler(make(chan struct{}, 1))
	req := httptest.NewRequest(http.MethodPost, "/v1/build", bytes.NewReader([]byte(`{"target":"proj"}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rec.Code)
	}
}

func TestHandleBuildStreamsSSEAndDoneEvent(t *testing.T) {
	resetHelperGlobals(t)

	helperKrunConfigLoaded = true
	helperKrunConfig = cfg.KrunConfig{
		KrunSourceConfig: cfg.KrunSourceConfig{Path: "/workspace", SearchDepth: 1},
		LocalRegistry:    "registry:5000",
		RemoteRegistry:   "registry:5001",
	}
	helperKubeConfigPath = "/tmp/kubeconfig"
	discoverServices = func(_ string, _ int) ([]cfg.Service, map[string]string, error) {
		return []cfg.Service{
			{Name: "api", Project: "proj"},
			{Name: "worker", Project: "proj"},
		}, map[string]string{"proj": "apps/proj"}, nil
	}

	called := false
	runWorkspaceBuild = func(ctx context.Context, out io.Writer, projectName string, servicesToBuild []cfg.Service, skipWeb bool, force bool, flush bool, conf cfg.Config) error {
		called = true
		if projectName != "proj" {
			t.Fatalf("unexpected project name %q", projectName)
		}
		if len(servicesToBuild) != 2 {
			t.Fatalf("expected 2 services, got %d", len(servicesToBuild))
		}
		if !skipWeb {
			t.Fatalf("expected skipWeb=true")
		}
		if !force {
			t.Fatalf("expected force=true")
		}
		if flush {
			t.Fatalf("expected flush=false")
		}
		if conf.Registry != "registry:5000" {
			t.Fatalf("unexpected registry %q", conf.Registry)
		}

		_, _ = io.WriteString(out, "line one\nline two\nline three")
		return nil
	}

	handler := newHandler(make(chan struct{}, 1))
	req := httptest.NewRequest(http.MethodPost, "/v1/build", bytes.NewReader([]byte(`{"target":"proj","skip_web":true,"force":true}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !called {
		t.Fatalf("expected workspace build to be called")
	}

	body := rec.Body.String()
	if strings.Count(body, "event: log") != 3 {
		t.Fatalf("expected 3 log events, got body: %q", body)
	}
	if !strings.Contains(body, `event: done`) || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("expected done success event, got body: %q", body)
	}
}

func TestStreamSSEEmitsDoneOnFailure(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/build", nil)
	rec := httptest.NewRecorder()

	streamSSE(rec, req, func(_ context.Context, out io.Writer) error {
		_, _ = io.WriteString(out, "before fail\n")
		return errors.New("boom")
	})

	body := rec.Body.String()
	if !strings.Contains(body, "event: log") {
		t.Fatalf("expected log event, got %q", body)
	}
	if !strings.Contains(body, `"ok":false`) {
		t.Fatalf("expected done failure event, got %q", body)
	}
	if !strings.Contains(body, `"error":"boom"`) {
		t.Fatalf("expected done error payload, got %q", body)
	}
}

func TestStreamSSERespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/build", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	runStarted := make(chan struct{})
	runStopped := make(chan struct{})
	done := make(chan struct{})

	go func() {
		streamSSE(rec, req, func(ctx context.Context, out io.Writer) error {
			close(runStarted)
			_, _ = io.WriteString(out, "running\n")
			<-ctx.Done()
			close(runStopped)
			return ctx.Err()
		})
		close(done)
	}()

	<-runStarted
	cancel()

	select {
	case <-runStopped:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for run function to observe cancellation")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for streamSSE to return")
	}

	if !strings.Contains(rec.Body.String(), `"text":"running\n"`) {
		t.Fatalf("expected at least one log event before cancellation, got %q", rec.Body.String())
	}
}
