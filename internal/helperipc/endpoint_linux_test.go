//go:build linux

package helperipc

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

	client := newClientForEndpoint(endpoint, 2*time.Second)
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

func TestCheckPrivateDir(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := checkPrivateDir(private); err != nil {
		t.Fatalf("0700 dir owned by us must pass: %v", err)
	}

	loose := filepath.Join(dir, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := checkPrivateDir(loose); err == nil {
		t.Fatal("a group/other-accessible socket directory must be refused")
	}

	file := filepath.Join(dir, "notadir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := checkPrivateDir(file); err == nil {
		t.Fatal("a non-directory must be refused")
	}
}

func TestDefaultEndpointUsesRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/4242")
	if got, want := DefaultEndpoint(), "/run/user/4242/krun/krun-helper.sock"; got != want {
		t.Fatalf("DefaultEndpoint() = %q, want %q", got, want)
	}

	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := DefaultEndpoint(); !strings.HasPrefix(got, os.TempDir()) || !strings.Contains(got, fmt.Sprintf("krun-%d", os.Getuid())) {
		t.Fatalf("fallback endpoint = %q, want a uid-scoped path under %s", got, os.TempDir())
	}
}
