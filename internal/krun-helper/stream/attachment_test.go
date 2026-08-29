package stream

import "testing"

func TestStreamHeadersCarryTokenOutsideURL(t *testing.T) {
	headers := streamHeaders(" token-1 ")
	if got := headers.Get("X-Krun-Session-Token"); got != "token-1" {
		t.Fatalf("session token header = %q, want %q", got, "token-1")
	}
}
