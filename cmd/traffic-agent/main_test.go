package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/gorilla/websocket"
)

func TestResolveManagerAddressPrefersNewEnv(t *testing.T) {
	t.Setenv(managerAddressEnv, "http://new-manager:8080")

	got := resolveManagerAddress()
	if got != "http://new-manager:8080" {
		t.Fatalf("expected new manager address, got %q", got)
	}
}

func TestResolveManagerAddressEmptyWhenUnset(t *testing.T) {
	t.Setenv(managerAddressEnv, "")

	got := resolveManagerAddress()
	if got != "" {
		t.Fatalf("expected empty manager address, got %q", got)
	}
}

func TestLoadRuntimeConfig(t *testing.T) {
	t.Setenv(managerAddressEnv, "http://manager.default.svc:8080")
	t.Setenv(sessionIDEnv, "session-1")
	t.Setenv(sessionTokenEnv, "token-1")
	t.Setenv(targetPortEnv, "8080")

	cfg, err := loadRuntimeConfig()
	if err != nil {
		t.Fatalf("loadRuntimeConfig returned error: %v", err)
	}
	if cfg.SessionID != "session-1" {
		t.Fatalf("expected session id, got %q", cfg.SessionID)
	}
	if cfg.SessionToken != "token-1" {
		t.Fatalf("expected session token, got %q", cfg.SessionToken)
	}
	if cfg.TargetPort != 8080 {
		t.Fatalf("expected target port 8080, got %d", cfg.TargetPort)
	}
	if cfg.AgentListenPort != defaultAgentListenPort {
		t.Fatalf("expected default listen port %d, got %d", defaultAgentListenPort, cfg.AgentListenPort)
	}
	if cfg.StreamURL != "ws://manager.default.svc:8080/v1/stream/agent?session_id=session-1" {
		t.Fatalf("unexpected stream url %q", cfg.StreamURL)
	}
}

func TestLoadRuntimeConfigRequiresSessionID(t *testing.T) {
	t.Setenv(managerAddressEnv, "http://manager.default.svc:8080")
	t.Setenv(targetPortEnv, "8080")

	_, err := loadRuntimeConfig()
	if err == nil {
		t.Fatalf("expected error when session id is missing")
	}
	if !strings.Contains(err.Error(), sessionIDEnv) {
		t.Fatalf("expected error to mention %s, got %v", sessionIDEnv, err)
	}
}

func TestBuildStreamURL(t *testing.T) {
	got, err := buildStreamURL("http://manager.default.svc:8080", "session-1")
	if err != nil {
		t.Fatalf("buildStreamURL returned error: %v", err)
	}
	want := "ws://manager.default.svc:8080/v1/stream/agent?session_id=session-1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildStreamURLConvertsHTTPS(t *testing.T) {
	got, err := buildStreamURL("https://manager.default.svc:8443", "session-1")
	if err != nil {
		t.Fatalf("buildStreamURL returned error: %v", err)
	}
	want := "wss://manager.default.svc:8443/v1/stream/agent?session_id=session-1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildRedirectArgs(t *testing.T) {
	got := buildRedirectArgs(8080, 18081)
	want := []string{"-p", "tcp", "--dport", "8080", "-j", "REDIRECT", "--to-ports", "18081"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected redirect args: want=%v got=%v", want, got)
	}
}

func TestRedirectRuleInstallAddsRuleWhenMissing(t *testing.T) {
	fake := &fakeRunner{
		responses: map[string]error{
			"-t nat -C PREROUTING -p tcp --dport 8080 -j REDIRECT --to-ports 18081": errors.New("not found"),
		},
	}
	rule := redirectRule{
		TargetPort:  8080,
		ListenPort:  18081,
		RunIptables: fake.Run,
	}

	if err := rule.Install(context.Background()); err != nil {
		t.Fatalf("install returned error: %v", err)
	}

	expectedCalls := [][]string{
		{"-t", "nat", "-C", "PREROUTING", "-p", "tcp", "--dport", "8080", "-j", "REDIRECT", "--to-ports", "18081"},
		{"-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", "8080", "-j", "REDIRECT", "--to-ports", "18081"},
	}
	if !reflect.DeepEqual(fake.calls, expectedCalls) {
		t.Fatalf("unexpected iptables calls: want=%v got=%v", expectedCalls, fake.calls)
	}
}

func TestRedirectRuleRemoveIgnoresMissingRule(t *testing.T) {
	fake := &fakeRunner{
		responses: map[string]error{
			"-t nat -D PREROUTING -p tcp --dport 8080 -j REDIRECT --to-ports 18081": errors.New("Bad rule (does a matching rule exist in that chain?)"),
		},
	}
	rule := redirectRule{
		TargetPort:  8080,
		ListenPort:  18081,
		RunIptables: fake.Run,
	}

	if err := rule.Remove(context.Background()); err != nil {
		t.Fatalf("remove returned error for missing rule: %v", err)
	}
}

func TestPumpConnectionHandlesInboundWithoutPendingWrite(t *testing.T) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	serverConnCh := make(chan *websocket.Conn, 1)
	serverShutdown := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		select {
		case serverConnCh <- conn:
		case <-serverShutdown:
			return
		}
		<-serverShutdown
	}))
	defer server.Close()
	defer close(serverShutdown)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}

	var received contracts.StreamEnvelope
	receivedCh := make(chan struct{}, 1)
	client := &reconnectingStreamClient{
		sendCh: make(chan contracts.StreamEnvelope, 1),
		onReceive: func(envelope contracts.StreamEnvelope) {
			received = envelope
			select {
			case receivedCh <- struct{}{}:
			default:
			}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pumpErrCh := make(chan error, 1)
	go func() {
		pumpErrCh <- client.pumpConnection(ctx, conn)
	}()

	var serverConn *websocket.Conn
	select {
	case serverConn = <-serverConnCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for test websocket server connection")
	}

	inbound := contracts.StreamEnvelope{
		Type:         contracts.StreamTypeData,
		ConnectionID: "conn-1",
		Data:         []byte("hello"),
	}
	if err := serverConn.WriteJSON(inbound); err != nil {
		t.Fatalf("write inbound envelope: %v", err)
	}

	select {
	case <-receivedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for inbound envelope callback")
	}

	if received.Type != inbound.Type || received.ConnectionID != inbound.ConnectionID || string(received.Data) != string(inbound.Data) {
		t.Fatalf("unexpected received envelope: got=%+v want=%+v", received, inbound)
	}

	cancel()
	select {
	case err := <-pumpErrCh:
		if err != nil {
			t.Fatalf("pumpConnection returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pumpConnection to exit")
	}
}

type fakeRunner struct {
	calls     [][]string
	responses map[string]error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) error {
	copied := make([]string, len(args))
	copy(copied, args)
	f.calls = append(f.calls, copied)

	if f.responses == nil {
		return nil
	}
	if err, ok := f.responses[strings.Join(args, " ")]; ok {
		return err
	}
	return nil
}
