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

func TestURLRejectsBadAddresses(t *testing.T) {
	for _, address := range []string{"", "ftp://manager:8080"} {
		if _, err := URL(address, AgentPath, "session-1"); err == nil {
			t.Fatalf("expected invalid manager address %q to fail", address)
		}
	}
}

func TestURLDefaultsBareAddressToHTTP(t *testing.T) {
	got, err := URL("127.0.0.1:39481", AgentPath, "session-1")
	if err != nil {
		t.Fatalf("bare host:port: %v", err)
	}
	want := "ws://127.0.0.1:39481" + AgentPath + "?session_id=session-1"
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}
