package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/krun-helper/session"
)

// These cover the daemon's HTTP surface — routing, verbs, payload validation
// and status codes. Session behaviour itself is tested in the session package.

func TestEnableReturnsOKAndRegistersSession(t *testing.T) {
	handler, _ := newTestDaemon(t, nil)

	rec := post(handler, "/v1/debug/enable", commandBody("proj-a", "svc-a"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if response := decodeHelperResponse(t, rec); !response.Success {
		t.Fatalf("expected a successful response, got %+v", response)
	}

	sessions := listSessions(t, handler)
	if len(sessions) != 1 || sessions[0].SessionKey != "proj-a/svc-a" {
		t.Fatalf("unexpected session list: %+v", sessions)
	}
}

func TestEnableReturnsServerErrorWhenSetupFails(t *testing.T) {
	handler, _ := newTestDaemon(t, errors.New("manager unavailable"))

	rec := post(handler, "/v1/debug/enable", commandBody("proj-a", "svc-a"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}
	response := decodeHelperResponse(t, rec)
	if response.Success {
		t.Fatal("expected an unsuccessful response")
	}
	if !strings.Contains(response.Message, "manager unavailable") {
		t.Fatalf("expected the cause to reach the CLI, got %q", response.Message)
	}
}

func TestDisableReportsNoActiveSession(t *testing.T) {
	handler, _ := newTestDaemon(t, nil)

	rec := post(handler, "/v1/debug/disable", commandBody("proj-a", "svc-a"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	// The CLI keys its "no active debug session found" message off this text.
	if response := decodeHelperResponse(t, rec); response.Message != "no active session" {
		t.Fatalf("unexpected message %q", response.Message)
	}
}

func TestDisableRemovesSession(t *testing.T) {
	handler, _ := newTestDaemon(t, nil)

	if rec := post(handler, "/v1/debug/enable", commandBody("proj-a", "svc-a")); rec.Code != http.StatusOK {
		t.Fatalf("enable failed with status %d", rec.Code)
	}

	rec := post(handler, "/v1/debug/disable", commandBody("proj-a", "svc-a"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if sessions := listSessions(t, handler); len(sessions) != 0 {
		t.Fatalf("expected no sessions after disable, got %+v", sessions)
	}
}

func TestRejectsPayloadWithoutSessionIdentity(t *testing.T) {
	handler, _ := newTestDaemon(t, nil)

	for _, path := range []string{"/v1/debug/enable", "/v1/debug/disable"} {
		body, _ := json.Marshal(contracts.DebugSessionCommandRequest{})
		if rec := post(handler, path, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected status 400, got %d", path, rec.Code)
		}
	}
}

func TestRejectsUnknownPayloadFields(t *testing.T) {
	handler, _ := newTestDaemon(t, nil)

	rec := post(handler, "/v1/debug/enable", []byte(`{"service_name":"svc-a"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 for an unknown field, got %d", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	handler, _ := newTestDaemon(t, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
}

func TestShutdownSignalsTheDaemon(t *testing.T) {
	handler, shutdownCh := newTestDaemon(t, nil)

	rec := post(handler, "/v1/shutdown", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	select {
	case <-shutdownCh:
	default:
		t.Fatal("expected a shutdown signal")
	}
}

func TestMethodGuard(t *testing.T) {
	handler, _ := newTestDaemon(t, nil)

	cases := []struct{ method, path string }{
		{http.MethodPost, "/healthz"},
		{http.MethodPost, "/v1/debug/sessions"},
		{http.MethodGet, "/v1/debug/enable"},
		{http.MethodGet, "/v1/debug/disable"},
		{http.MethodGet, "/v1/shutdown"},
	}
	for _, testCase := range cases {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(testCase.method, testCase.path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: expected status 405, got %d", testCase.method, testCase.path, rec.Code)
		}
	}
}

func newTestDaemon(t *testing.T, createErr error) (http.Handler, chan struct{}) {
	t.Helper()

	shutdownCh := make(chan struct{}, 1)
	sessions := session.NewManager(session.Options{
		Hosts:    stubHosts{},
		Forwards: stubForwards{},
		API:      stubAPI{createErr: createErr},
		Attach: func(string, string, int) (session.Stream, error) {
			return stubStream{}, nil
		},
	})
	return newHandler(&daemon{sessions: sessions, shutdownCh: shutdownCh}), shutdownCh
}

func commandBody(project string, service string) []byte {
	body, _ := json.Marshal(contracts.DebugSessionCommandRequest{
		Context: contracts.DebugServiceContext{
			Project:       project,
			ServiceName:   service,
			ContainerPort: 8080,
			InterceptPort: 5001,
		},
	})
	return body
}

func post(handler http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	return rec
}

func decodeHelperResponse(t *testing.T, rec *httptest.ResponseRecorder) contracts.HelperResponse {
	t.Helper()
	var response contracts.HelperResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode helper response: %v", err)
	}
	return response
}

func listSessions(t *testing.T, handler http.Handler) []contracts.HelperDebugSession {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/debug/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d", rec.Code)
	}
	var response contracts.HelperDebugSessionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	return response.Sessions
}

type stubHosts struct{}

func (stubHosts) Write([]contracts.HostsEntry) error { return nil }
func (stubHosts) Remove() error                      { return nil }

type stubForwards struct{}

func (stubForwards) Upsert(string, []contracts.PortForward) error { return nil }
func (stubForwards) Remove(string) error                          { return nil }
func (stubForwards) Clear() error                                 { return nil }

type stubAPI struct{ createErr error }

func (s stubAPI) CreateSession(contracts.DebugServiceContext) (contracts.DebugSession, error) {
	if s.createErr != nil {
		return contracts.DebugSession{}, s.createErr
	}
	return contracts.DebugSession{SessionID: "mgr-session-1", SessionToken: "token"}, nil
}

func (stubAPI) DeleteSession(string) error { return nil }

type stubStream struct{}

func (stubStream) Close() {}
