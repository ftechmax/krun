package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/traffic-manager/agent"
	sessionregistry "github.com/ftechmax/krun/internal/traffic-manager/session"
	streamrelay "github.com/ftechmax/krun/internal/traffic-manager/stream"
)

func TestSessionsLifecycle(t *testing.T) {
	resetSessionState(t)
	fake := &fakeInjector{}
	sidecarBridge = fake
	handler := newHandler()

	createPayload, _ := json.Marshal(contracts.CreateDebugSessionRequest{
		ServiceName: "orders-api",
		ServicePort: 8080,
		LocalPort:   5000,
		ClientID:    "dev-1",
	})
	createReq := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(createPayload))
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", createRec.Code)
	}
	var created contracts.DebugSession
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.SessionID == "" {
		t.Fatalf("expected session_id to be set")
	}
	if created.SessionToken == "" {
		t.Fatalf("expected session_token to be set")
	}
	if created.Namespace != "default" {
		t.Fatalf("expected default namespace, got %q", created.Namespace)
	}
	if created.Workload != "orders-api" {
		t.Fatalf("expected workload default to service_name, got %q", created.Workload)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d", listRec.Code)
	}
	var listResponse contracts.ListDebugSessionsResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResponse.Sessions) != 1 {
		t.Fatalf("expected one session, got %+v", listResponse.Sessions)
	}
	if listResponse.Sessions[0].SessionID != created.SessionID {
		t.Fatalf("expected listed session id %q, got %q", created.SessionID, listResponse.Sessions[0].SessionID)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/sessions/"+created.SessionID, nil)
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("expected delete status 204, got %d", deleteRec.Code)
	}
	if len(fake.removeCalls) != 1 || fake.removeCalls[0].SessionID != created.SessionID {
		t.Fatalf("expected sidecar remove call for deleted session, got %+v", fake.removeCalls)
	}

	listAfterDeleteReq := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	listAfterDeleteRec := httptest.NewRecorder()
	handler.ServeHTTP(listAfterDeleteRec, listAfterDeleteReq)
	if listAfterDeleteRec.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d", listAfterDeleteRec.Code)
	}
	var listAfterDelete contracts.ListDebugSessionsResponse
	if err := json.Unmarshal(listAfterDeleteRec.Body.Bytes(), &listAfterDelete); err != nil {
		t.Fatalf("decode list-after-delete response: %v", err)
	}
	if len(listAfterDelete.Sessions) != 0 {
		t.Fatalf("expected no sessions after delete, got %+v", listAfterDelete.Sessions)
	}
}

func TestCreateSessionValidation(t *testing.T) {
	resetSessionState(t)
	sidecarBridge = &fakeInjector{}
	handler := newHandler()

	payload := []byte(`{"service_name":"orders-api","service_port":8080}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestSessionMethodsAndNotFound(t *testing.T) {
	resetSessionState(t)
	sidecarBridge = &fakeInjector{}
	handler := newHandler()

	methodReq := httptest.NewRequest(http.MethodPut, "/v1/sessions", nil)
	methodRec := httptest.NewRecorder()
	handler.ServeHTTP(methodRec, methodReq)
	if methodRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", methodRec.Code)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/sessions/does-not-exist", nil)
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", deleteRec.Code)
	}
}

func TestCreateSessionRollsBackWhenInjectFails(t *testing.T) {
	resetSessionState(t)
	sidecarBridge = &fakeInjector{
		injectErr: errors.New("inject failed"),
	}
	handler := newHandler()

	createPayload, _ := json.Marshal(contracts.CreateDebugSessionRequest{
		ServiceName: "orders-api",
		ServicePort: 8080,
		LocalPort:   5000,
		ClientID:    "dev-1",
	})
	createReq := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(createPayload))
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", createRec.Code)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	var listResponse contracts.ListDebugSessionsResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResponse.Sessions) != 0 {
		t.Fatalf("expected no sessions after failed inject, got %+v", listResponse.Sessions)
	}
}

func TestDeleteSessionReturnsServerErrorWhenRemoveFails(t *testing.T) {
	resetSessionState(t)
	fake := &fakeInjector{
		removeErr: errors.New("remove failed"),
	}
	sidecarBridge = fake
	handler := newHandler()

	createPayload, _ := json.Marshal(contracts.CreateDebugSessionRequest{
		ServiceName: "orders-api",
		ServicePort: 8080,
		LocalPort:   5000,
		ClientID:    "dev-1",
	})
	createReq := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(createPayload))
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", createRec.Code)
	}
	var created contracts.DebugSession
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/sessions/"+created.SessionID, nil)
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", deleteRec.Code)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	var listResponse contracts.ListDebugSessionsResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResponse.Sessions) != 1 {
		t.Fatalf("expected session to remain when remove fails, got %+v", listResponse.Sessions)
	}
}

func TestDeleteSessionIgnoresMissingWorkloadOnRemove(t *testing.T) {
	resetSessionState(t)
	sidecarBridge = &fakeInjector{
		removeErr: agent.ErrWorkloadNotFound,
	}
	handler := newHandler()

	createPayload, _ := json.Marshal(contracts.CreateDebugSessionRequest{
		ServiceName: "orders-api",
		ServicePort: 8080,
		LocalPort:   5000,
		ClientID:    "dev-1",
	})
	createReq := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(createPayload))
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", createRec.Code)
	}
	var created contracts.DebugSession
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/sessions/"+created.SessionID, nil)
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", deleteRec.Code)
	}
}

func TestStreamAttachMethodNotAllowed(t *testing.T) {
	resetSessionState(t)
	handler := newHandler()

	req := httptest.NewRequest(http.MethodPost, "/v1/stream/agent", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", rec.Code)
	}
}

func TestStreamAttachRequiresValidSessionAndToken(t *testing.T) {
	resetSessionState(t)

	created, err := sessionRegistry.Create(contracts.CreateDebugSessionRequest{
		ServiceName: "orders-api",
		ServicePort: 8080,
		LocalPort:   5000,
		ClientID:    "dev-1",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	missingToken := httptest.NewRequest(http.MethodGet, "/v1/stream/agent?session_id="+created.SessionID, nil)
	missingTokenRec := httptest.NewRecorder()
	handleStreamAttach(missingTokenRec, missingToken, contracts.StreamRoleAgent)
	if missingTokenRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", missingTokenRec.Code)
	}

	invalidSession := httptest.NewRequest(http.MethodGet, "/v1/stream/agent?session_id=unknown&session_token=abc", nil)
	invalidSessionRec := httptest.NewRecorder()
	handleStreamAttach(invalidSessionRec, invalidSession, contracts.StreamRoleAgent)
	if invalidSessionRec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", invalidSessionRec.Code)
	}
}

func TestCleanupDanglingAgents(t *testing.T) {
	resetSessionState(t)
	fake := &fakeInjector{}
	sidecarBridge = fake

	if err := cleanupDanglingAgents(context.Background()); err != nil {
		t.Fatalf("cleanup dangling agents: %v", err)
	}
	if fake.cleanupCalls != 1 {
		t.Fatalf("expected cleanup to be called once, got %d", fake.cleanupCalls)
	}
}

func TestCleanupDanglingAgentsReturnsError(t *testing.T) {
	resetSessionState(t)
	cleanupErr := errors.New("cleanup failed")
	fake := &fakeInjector{
		cleanupErr: cleanupErr,
	}
	sidecarBridge = fake

	err := cleanupDanglingAgents(context.Background())
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("expected cleanup error %v, got %v", cleanupErr, err)
	}
}

func resetSessionState(t *testing.T) {
	t.Helper()
	sessionRegistry = sessionregistry.NewDebugSessionRegistry()
	sidecarBridge = agent.NoopInjector{}
	relayRegistry = streamrelay.NewSessionRelayRegistry()
}

type fakeInjector struct {
	injectErr    error
	removeErr    error
	cleanupErr   error
	injectCalls  []contracts.DebugSession
	removeCalls  []contracts.DebugSession
	cleanupCalls int
}

func (f *fakeInjector) Inject(_ context.Context, session contracts.DebugSession) error {
	f.injectCalls = append(f.injectCalls, session)
	return f.injectErr
}

func (f *fakeInjector) Remove(_ context.Context, session contracts.DebugSession) error {
	f.removeCalls = append(f.removeCalls, session)
	return f.removeErr
}

func (f *fakeInjector) Cleanup(_ context.Context) error {
	f.cleanupCalls++
	return f.cleanupErr
}
