package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
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
