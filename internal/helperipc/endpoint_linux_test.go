//go:build linux

package helperipc

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListenServeAndDial(t *testing.T) {
	endpoint := filepath.Join(t.TempDir(), "helper.sock")

	listener, err := Listen(endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	client := NewClientForEndpoint(endpoint, 2*time.Second)
	resp, err := client.Get(BaseURL + "/ping")
	if err != nil {
		t.Fatalf("request over unix socket: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "pong" {
		t.Fatalf("unexpected response body %q", body)
	}

	info, err := os.Stat(endpoint)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode = %o, want 0600", perm)
	}
}

func TestListenRemovesStaleSocket(t *testing.T) {
	endpoint := filepath.Join(t.TempDir(), "helper.sock")

	// A leftover socket file with no listener behind it must be cleaned up.
	staleListener, err := Listen(endpoint)
	if err != nil {
		t.Fatalf("first listen: %v", err)
	}
	_ = staleListener.Close()
	if _, err := os.Stat(endpoint); err != nil {
		// Close removed the file (Go unlinks unix sockets on close);
		// recreate a bare file to simulate a crash leftover.
		if err := os.WriteFile(endpoint, nil, 0o600); err != nil {
			t.Fatalf("create stale file: %v", err)
		}
	}

	listener, err := Listen(endpoint)
	if err != nil {
		t.Fatalf("listen over stale socket: %v", err)
	}
	_ = listener.Close()
}

func TestListenRefusesLiveSocket(t *testing.T) {
	endpoint := filepath.Join(t.TempDir(), "helper.sock")

	listener, err := Listen(endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	if _, err := Listen(endpoint); err == nil {
		t.Fatal("expected error when a live helper already listens")
	}
}

func TestInvokingUser(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(key string) string { return vars[key] }
	}

	uid, gid, ok := invokingUser(env(map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1001"}))
	if !ok || uid != 1000 || gid != 1001 {
		t.Fatalf("sudo case = (%d, %d, %v), want (1000, 1001, true)", uid, gid, ok)
	}

	uid, gid, ok = invokingUser(env(map[string]string{"PKEXEC_UID": "1000"}))
	if !ok || uid != 1000 || gid != -1 {
		t.Fatalf("pkexec case = (%d, %d, %v), want (1000, -1, true)", uid, gid, ok)
	}

	if _, _, ok := invokingUser(env(map[string]string{})); ok {
		t.Fatal("no elevation env must report not-found")
	}

	if _, _, ok := invokingUser(env(map[string]string{"SUDO_UID": "not-a-number"})); ok {
		t.Fatal("garbage uid must report not-found")
	}
}
