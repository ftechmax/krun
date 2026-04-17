package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	workspacebuild "github.com/ftechmax/krun/internal/krun-helper/build"
	workspacedeploy "github.com/ftechmax/krun/internal/krun-helper/deploy"
	"github.com/ftechmax/krun/internal/krun-helper/hostfile"
	managerclient "github.com/ftechmax/krun/internal/krun-helper/manager-client"
	"github.com/ftechmax/krun/internal/krun-helper/session"
)

func TestDebugEnableHandlerAppliesHostsAndPortForwards(t *testing.T) {
	resetHelperGlobals(t)

	fakeRegistry := &fakePortForwardRegistry{}
	portForwardRegistry = fakeRegistry
	fakeManager := &fakeManagerSessionClient{createSessionID: "mgr-session-1"}
	managerSessionClient = fakeManager
	fakeStreams := &fakeStreamRegistry{}
	streamRegistry = fakeStreams

	originalUpdate := hostfileUpdate
	var updatedEntries []contracts.HostsEntry
	hostfileUpdate = func(entries []contracts.HostsEntry) error {
		updatedEntries = append([]contracts.HostsEntry(nil), entries...)
		return nil
	}
	t.Cleanup(func() { hostfileUpdate = originalUpdate })

	handler := newHandler(make(chan struct{}, 1))
	body, _ := json.Marshal(contracts.DebugSessionCommandRequest{
		Context: contracts.DebugServiceContext{
			Project:       "awesome-app3",
			ServiceName:   "awesome-app3-worker",
			ContainerPort: 8080,
			InterceptPort: 5001,
			ServiceDependencies: []contracts.DebugServiceDependencyContext{
				{Host: "rabbitmq.default.svc", Port: 5672},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/debug/enable", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if len(updatedEntries) != 1 || updatedEntries[0].Hostname != "rabbitmq.default.svc" {
		t.Fatalf("unexpected hosts entries: %+v", updatedEntries)
	}
	if fakeRegistry.upsertCalls != 1 {
		t.Fatalf("expected one Upsert call, got %d", fakeRegistry.upsertCalls)
	}
	if fakeManager.createCalls != 1 {
		t.Fatalf("expected one manager create call, got %d", fakeManager.createCalls)
	}
	if fakeRegistry.lastSessionKey != "awesome-app3/awesome-app3-worker" {
		t.Fatalf("unexpected session key %q", fakeRegistry.lastSessionKey)
	}
	if len(fakeRegistry.lastForwards) != 1 {
		t.Fatalf("expected 1 forward, got %+v", fakeRegistry.lastForwards)
	}
	if fakeStreams.upsertCalls != 1 {
		t.Fatalf("expected one stream Upsert call, got %d", fakeStreams.upsertCalls)
	}
	if fakeStreams.lastSessionID != "mgr-session-1" {
		t.Fatalf("unexpected stream session id %q", fakeStreams.lastSessionID)
	}
	if fakeStreams.lastInterceptPort != 5001 {
		t.Fatalf("unexpected stream intercept port %d", fakeStreams.lastInterceptPort)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/debug/sessions", nil)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d", listRec.Code)
	}
	var listResponse contracts.HelperDebugSessionsResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResponse.Sessions) != 1 {
		t.Fatalf("expected one debug session, got %+v", listResponse.Sessions)
	}
	if listResponse.Sessions[0].Context.ServiceName != "awesome-app3-worker" {
		t.Fatalf("unexpected session service: %+v", listResponse.Sessions[0])
	}
	if listResponse.Sessions[0].Context.InterceptPort != 5001 {
		t.Fatalf("unexpected intercept port: %+v", listResponse.Sessions[0])
	}
	if len(listResponse.Sessions[0].Context.ServiceDependencies) != 1 {
		t.Fatalf("unexpected service dependencies: %+v", listResponse.Sessions[0].Context.ServiceDependencies)
	}

	managerSessionID, ok := managerSessionsRegistry.Get("awesome-app3/awesome-app3-worker")
	if !ok || managerSessionID != "mgr-session-1" {
		t.Fatalf("unexpected manager session mapping: %q %v", managerSessionID, ok)
	}
}

func TestDebugDisableHandlerRemovesScopedState(t *testing.T) {
	resetHelperGlobals(t)

	fakeRegistry := &fakePortForwardRegistry{}
	portForwardRegistry = fakeRegistry
	fakeManager := &fakeManagerSessionClient{}
	managerSessionClient = fakeManager
	fakeStreams := &fakeStreamRegistry{}
	streamRegistry = fakeStreams

	hostsRegistry.Upsert("proj-a/svc-a", []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "rabbitmq.default.svc"},
	})
	hostsRegistry.Upsert("proj-b/svc-b", []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "redis.default.svc"},
	})
	managerSessionsRegistry.Upsert("proj-a/svc-a", "mgr-session-2")
	sessionsRegistry.Upsert("proj-a/svc-a", contracts.DebugServiceContext{
		Project:     "proj-a",
		ServiceName: "svc-a",
	})

	originalUpdate := hostfileUpdate
	var updatedEntries []contracts.HostsEntry
	hostfileUpdate = func(entries []contracts.HostsEntry) error {
		updatedEntries = append([]contracts.HostsEntry(nil), entries...)
		return nil
	}
	t.Cleanup(func() { hostfileUpdate = originalUpdate })

	handler := newHandler(make(chan struct{}, 1))
	body, _ := json.Marshal(contracts.DebugSessionCommandRequest{
		Context: contracts.DebugServiceContext{
			Project:     "proj-a",
			ServiceName: "svc-a",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/debug/disable", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if fakeRegistry.removeCalls != 1 {
		t.Fatalf("expected one Remove call, got %d", fakeRegistry.removeCalls)
	}
	if fakeStreams.removeCalls != 1 {
		t.Fatalf("expected one stream Remove call, got %d", fakeStreams.removeCalls)
	}
	if fakeManager.deleteCalls != 1 {
		t.Fatalf("expected one manager delete call, got %d", fakeManager.deleteCalls)
	}
	if fakeManager.lastDeletedSessionID != "mgr-session-2" {
		t.Fatalf("unexpected manager session id %q", fakeManager.lastDeletedSessionID)
	}
	if fakeRegistry.lastSessionKey != "proj-a/svc-a" {
		t.Fatalf("unexpected session key %q", fakeRegistry.lastSessionKey)
	}
	want := []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "redis.default.svc"},
	}
	if !slices.Equal(updatedEntries, want) {
		t.Fatalf("unexpected hosts entries after disable: %+v", updatedEntries)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/debug/sessions", nil)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d", listRec.Code)
	}
	var listResponse contracts.HelperDebugSessionsResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResponse.Sessions) != 0 {
		t.Fatalf("expected no active sessions after disable, got %+v", listResponse.Sessions)
	}
	if _, ok := managerSessionsRegistry.Get("proj-a/svc-a"); ok {
		t.Fatalf("expected manager session mapping to be removed")
	}
}

func TestDebugDisableHandlerFallsBackToManagerLookup(t *testing.T) {
	resetHelperGlobals(t)

	fakeRegistry := &fakePortForwardRegistry{}
	portForwardRegistry = fakeRegistry
	fakeManager := &fakeManagerSessionClient{
		listSessions: []contracts.DebugSession{
			{SessionID: "other", Namespace: "default", ServiceName: "other-svc", ClientID: managerclient.ManagerClientID, CreatedAt: "2026-01-01T00:00:00Z"},
			{SessionID: "target-1", Namespace: "default", ServiceName: "svc-a", ClientID: managerclient.ManagerClientID, CreatedAt: "2026-01-01T00:00:01Z"},
			{SessionID: "target-2", Namespace: "default", ServiceName: "svc-a", ClientID: managerclient.ManagerClientID, CreatedAt: "2026-01-01T00:00:02Z"},
		},
	}
	managerSessionClient = fakeManager
	streamRegistry = &fakeStreamRegistry{}

	hostsRegistry.Upsert("proj-a/svc-a", []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "rabbitmq.default.svc"},
	})
	sessionsRegistry.Upsert("proj-a/svc-a", contracts.DebugServiceContext{
		Project:     "proj-a",
		ServiceName: "svc-a",
	})

	originalUpdate := hostfileUpdate
	hostfileUpdate = func(entries []contracts.HostsEntry) error { return nil }
	t.Cleanup(func() { hostfileUpdate = originalUpdate })

	handler := newHandler(make(chan struct{}, 1))
	body, _ := json.Marshal(contracts.DebugSessionCommandRequest{
		Context: contracts.DebugServiceContext{
			Project:     "proj-a",
			ServiceName: "svc-a",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/debug/disable", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if fakeManager.listCalls != 1 {
		t.Fatalf("expected one manager list call, got %d", fakeManager.listCalls)
	}
	if fakeManager.deleteCalls != 1 {
		t.Fatalf("expected one manager delete call, got %d", fakeManager.deleteCalls)
	}
	if fakeManager.lastDeletedSessionID != "target-2" {
		t.Fatalf("expected latest matching manager session to be deleted, got %q", fakeManager.lastDeletedSessionID)
	}
}

func TestDebugEnableHandlerRollsBackWhenManagerCreateFails(t *testing.T) {
	resetHelperGlobals(t)

	fakeRegistry := &fakePortForwardRegistry{}
	portForwardRegistry = fakeRegistry
	managerSessionClient = &fakeManagerSessionClient{
		createErr: errors.New("manager unavailable"),
	}
	streamRegistry = &fakeStreamRegistry{}

	originalUpdate := hostfileUpdate
	var lastUpdated []contracts.HostsEntry
	hostfileUpdate = func(entries []contracts.HostsEntry) error {
		lastUpdated = append([]contracts.HostsEntry(nil), entries...)
		return nil
	}
	t.Cleanup(func() { hostfileUpdate = originalUpdate })

	handler := newHandler(make(chan struct{}, 1))
	body, _ := json.Marshal(contracts.DebugSessionCommandRequest{
		Context: contracts.DebugServiceContext{
			Project:       "proj-a",
			ServiceName:   "svc-a",
			ContainerPort: 8080,
			InterceptPort: 5001,
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/debug/enable", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}
	if fakeRegistry.removeCalls != 1 {
		t.Fatalf("expected rollback remove call, got %d", fakeRegistry.removeCalls)
	}
	if len(lastUpdated) != 0 {
		t.Fatalf("expected hosts rollback to empty entries, got %+v", lastUpdated)
	}
	if len(sessionsRegistry.List()) != 0 {
		t.Fatalf("expected no active sessions after rollback, got %+v", sessionsRegistry.List())
	}
}

func TestDebugEnableHandlerCleansUpPreviousSession(t *testing.T) {
	resetHelperGlobals(t)

	fakeRegistry := &fakePortForwardRegistry{}
	portForwardRegistry = fakeRegistry
	fakeManager := &fakeManagerSessionClient{createSessionIDs: []string{"mgr-session-1", "mgr-session-2"}}
	managerSessionClient = fakeManager
	fakeStreams := &fakeStreamRegistry{}
	streamRegistry = fakeStreams

	hostfileUpdate = func(entries []contracts.HostsEntry) error { return nil }
	t.Cleanup(func() { hostfileUpdate = hostfile.Update })

	handler := newHandler(make(chan struct{}, 1))
	makeBody := func() *bytes.Reader {
		body, _ := json.Marshal(contracts.DebugSessionCommandRequest{
			Context: contracts.DebugServiceContext{
				Project:       "proj-a",
				ServiceName:   "svc-a",
				ContainerPort: 8080,
				InterceptPort: 5001,
			},
		})
		return bytes.NewReader(body)
	}

	// First enable
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/debug/enable", makeBody()))
	if rec.Code != http.StatusOK {
		t.Fatalf("first enable: expected 200, got %d", rec.Code)
	}
	if fakeManager.createCalls != 1 {
		t.Fatalf("expected 1 create call after first enable, got %d", fakeManager.createCalls)
	}
	if fakeManager.deleteCalls != 0 {
		t.Fatalf("expected 0 delete calls after first enable, got %d", fakeManager.deleteCalls)
	}

	// Second enable for the same service — should clean up the previous session
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/debug/enable", makeBody()))
	if rec.Code != http.StatusOK {
		t.Fatalf("second enable: expected 200, got %d", rec.Code)
	}
	if fakeManager.createCalls != 2 {
		t.Fatalf("expected 2 create calls after second enable, got %d", fakeManager.createCalls)
	}
	if fakeManager.deleteCalls != 1 {
		t.Fatalf("expected 1 delete call (cleanup of old session), got %d", fakeManager.deleteCalls)
	}
	if fakeManager.lastDeletedSessionID != "mgr-session-1" {
		t.Fatalf("expected old session mgr-session-1 to be deleted, got %q", fakeManager.lastDeletedSessionID)
	}
	// Stream should have been removed for cleanup + upserted for new session
	if fakeStreams.removeCalls != 1 {
		t.Fatalf("expected 1 stream remove (cleanup), got %d", fakeStreams.removeCalls)
	}
	if fakeStreams.upsertCalls != 2 {
		t.Fatalf("expected 2 stream upserts, got %d", fakeStreams.upsertCalls)
	}

	// Verify only one session is registered
	sessions := sessionsRegistry.List()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 active session, got %d", len(sessions))
	}
	managerSessionID, ok := managerSessionsRegistry.Get("proj-a/svc-a")
	if !ok || managerSessionID != "mgr-session-2" {
		t.Fatalf("expected manager session mgr-session-2, got %q (ok=%v)", managerSessionID, ok)
	}
}

func TestDebugEnableHandlerEnsuresManagerForwardWhenBootstrapPending(t *testing.T) {
	resetHelperGlobals(t)

	managerForwardBootstrapRequired = true

	fakeRegistry := &fakePortForwardRegistry{}
	portForwardRegistry = fakeRegistry
	fakeManager := &fakeManagerSessionClient{createSessionID: "mgr-session-1"}
	managerSessionClient = fakeManager
	fakeStreams := &fakeStreamRegistry{}
	streamRegistry = fakeStreams

	hostfileUpdate = func(entries []contracts.HostsEntry) error { return nil }
	t.Cleanup(func() { hostfileUpdate = hostfile.Update })

	handler := newHandler(make(chan struct{}, 1))
	body, _ := json.Marshal(contracts.DebugSessionCommandRequest{
		Context: contracts.DebugServiceContext{
			Project:       "proj-a",
			ServiceName:   "svc-a",
			ContainerPort: 8080,
			InterceptPort: 5001,
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/debug/enable", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if fakeRegistry.upsertCalls != 2 {
		t.Fatalf("expected 2 upsert calls (service + manager api), got %d", fakeRegistry.upsertCalls)
	}
	if len(fakeRegistry.upsertSessionKeys) != 2 {
		t.Fatalf("expected 2 upsert keys, got %+v", fakeRegistry.upsertSessionKeys)
	}
	if fakeRegistry.upsertSessionKeys[1] != managerAPIForwardSessionKey {
		t.Fatalf("expected second upsert key to be %q, got %q", managerAPIForwardSessionKey, fakeRegistry.upsertSessionKeys[1])
	}
	if managerForwardBootstrapRequired {
		t.Fatalf("expected manager forward bootstrap flag to clear after successful ensure")
	}
}

func TestDebugEnableHandlerFailsWhenManagerForwardEnsureFails(t *testing.T) {
	resetHelperGlobals(t)

	managerForwardBootstrapRequired = true

	fakeRegistry := &fakePortForwardRegistry{
		upsertErrByKey: map[string]error{
			managerAPIForwardSessionKey: errors.New("manager unreachable"),
		},
	}
	portForwardRegistry = fakeRegistry
	fakeManager := &fakeManagerSessionClient{createSessionID: "mgr-session-1"}
	managerSessionClient = fakeManager
	streamRegistry = &fakeStreamRegistry{}

	hostfileUpdate = func(entries []contracts.HostsEntry) error { return nil }
	t.Cleanup(func() { hostfileUpdate = hostfile.Update })

	handler := newHandler(make(chan struct{}, 1))
	body, _ := json.Marshal(contracts.DebugSessionCommandRequest{
		Context: contracts.DebugServiceContext{
			Project:       "proj-a",
			ServiceName:   "svc-a",
			ContainerPort: 8080,
			InterceptPort: 5001,
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/debug/enable", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}
	if fakeManager.createCalls != 0 {
		t.Fatalf("expected manager create not to run when manager forward ensure fails, got %d calls", fakeManager.createCalls)
	}
	if fakeRegistry.removeCalls != 1 {
		t.Fatalf("expected rollback remove call, got %d", fakeRegistry.removeCalls)
	}
	if !managerForwardBootstrapRequired {
		t.Fatalf("expected manager forward bootstrap flag to remain set")
	}
}

func TestDebugSessionsListMethodNotAllowed(t *testing.T) {
	resetHelperGlobals(t)
	handler := newHandler(make(chan struct{}, 1))

	req := httptest.NewRequest(http.MethodPost, "/v1/debug/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", rec.Code)
	}
}

func resetHelperGlobals(t *testing.T) {
	t.Helper()
	hostsRegistry = hostfile.NewSessionHostsRegistry()
	sessionsRegistry = session.NewDebugSessionRegistry()
	managerSessionsRegistry = session.NewManagerSessionRegistry()
	portForwardRegistry = noopPortForwardRegistry{}
	streamRegistry = noopStreamRegistry{}
	managerSessionClient = managerclient.NoopSessionClient{}
	managerForwardBootstrapRequired = false
	discoverServices = cfg.DiscoverServices
	runWorkspaceBuild = workspacebuild.Build
	runWorkspaceDeploy = workspacedeploy.Deploy
	runWorkspaceDelete = workspacedeploy.Delete
	helperKrunConfig = cfg.KrunConfig{}
	helperKrunConfigLoaded = false
	helperKubeConfigPath = ""
	helperWorkspacePath = ""
}

type fakePortForwardRegistry struct {
	upsertCalls       int
	removeCalls       int
	clearCalls        int
	lastSessionKey    string
	lastForwards      []contracts.PortForward
	upsertSessionKeys []string
	upsertErrByKey    map[string]error
}

func (f *fakePortForwardRegistry) Upsert(sessionKey string, forwards []contracts.PortForward) error {
	f.upsertCalls++
	f.lastSessionKey = sessionKey
	f.lastForwards = append([]contracts.PortForward(nil), forwards...)
	f.upsertSessionKeys = append(f.upsertSessionKeys, sessionKey)
	if err, ok := f.upsertErrByKey[sessionKey]; ok {
		return err
	}
	return nil
}

func (f *fakePortForwardRegistry) Remove(sessionKey string) error {
	f.removeCalls++
	f.lastSessionKey = sessionKey
	return nil
}

func (f *fakePortForwardRegistry) Clear() error {
	f.clearCalls++
	return nil
}

type fakeManagerSessionClient struct {
	createCalls          int
	listCalls            int
	deleteCalls          int
	createSessionID      string
	createSessionIDs     []string
	lastDeletedSessionID string
	createErr            error
	listErr              error
	deleteErr            error
	listSessions         []contracts.DebugSession
}

func (f *fakeManagerSessionClient) CreateSession(ctx contracts.DebugServiceContext) (contracts.DebugSession, error) {
	f.createCalls++
	if f.createErr != nil {
		return contracts.DebugSession{}, f.createErr
	}

	var sessionID string
	if len(f.createSessionIDs) > 0 {
		sessionID = f.createSessionIDs[0]
		f.createSessionIDs = f.createSessionIDs[1:]
	} else if f.createSessionID != "" {
		sessionID = f.createSessionID
	} else {
		sessionID = "mgr-default"
	}
	return contracts.DebugSession{
		SessionID:   sessionID,
		ServiceName: ctx.ServiceName,
	}, nil
}

func (f *fakeManagerSessionClient) DeleteSession(sessionID string) error {
	f.deleteCalls++
	f.lastDeletedSessionID = sessionID
	return f.deleteErr
}

func (f *fakeManagerSessionClient) ListSessions() ([]contracts.DebugSession, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]contracts.DebugSession(nil), f.listSessions...), nil
}

type fakeStreamRegistry struct {
	upsertCalls       int
	removeCalls       int
	clearCalls        int
	lastSessionKey    string
	lastSessionID     string
	lastSessionToken  string
	lastInterceptPort int
}

func (f *fakeStreamRegistry) Upsert(sessionKey string, sessionID string, sessionToken string, interceptPort int) error {
	f.upsertCalls++
	f.lastSessionKey = sessionKey
	f.lastSessionID = sessionID
	f.lastSessionToken = sessionToken
	f.lastInterceptPort = interceptPort
	return nil
}

func (f *fakeStreamRegistry) Remove(sessionKey string) error {
	f.removeCalls++
	f.lastSessionKey = sessionKey
	return nil
}

func (f *fakeStreamRegistry) Clear() error {
	f.clearCalls++
	return nil
}
