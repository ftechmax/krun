package streamlink

import "testing"

func TestURLBuildsAgentStreamURL(t *testing.T) {
	got, err := URL("http://manager.default.svc:8080", AgentPath, "session-1")
	if err != nil {
		t.Fatalf("URL returned error: %v", err)
	}
	want := "ws://manager.default.svc:8080/v1/stream/agent?session_id=session-1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestURLConvertsHTTPS(t *testing.T) {
	got, err := URL("https://manager.default.svc:8443", AgentPath, "session-1")
	if err != nil {
		t.Fatalf("URL returned error: %v", err)
	}
	want := "wss://manager.default.svc:8443/v1/stream/agent?session_id=session-1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestURLAssumesHTTPWhenSchemeOmitted(t *testing.T) {
	got, err := URL("127.0.0.1:39481", ClientPath, "session-1")
	if err != nil {
		t.Fatalf("URL returned error: %v", err)
	}
	want := "ws://127.0.0.1:39481/v1/stream/client?session_id=session-1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestURLRejectsBadAddresses(t *testing.T) {
	if _, err := URL("", AgentPath, "session-1"); err == nil {
		t.Fatal("expected an error for a blank manager address")
	}
	if _, err := URL("ftp://manager:8080", AgentPath, "session-1"); err == nil {
		t.Fatal("expected an error for an unsupported scheme")
	}
}
