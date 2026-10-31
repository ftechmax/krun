package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/krun-helper/hostfile"
)

func newHostsTestHandler(t *testing.T) (http.Handler, string) {
	t.Helper()
	hostsPath := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatalf("seed hosts: %v", err)
	}
	hostfile.SetPathForTest(t, hostsPath)
	return newHostsHandler(&hostsWriter{}, make(chan struct{}, 1)), hostsPath
}

func postHosts(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/hosts", strings.NewReader(body)))
	return rec
}

func TestHostsDaemonWritesAndStripsBlock(t *testing.T) {
	handler, hostsPath := newHostsTestHandler(t)

	rec := postHosts(t, handler, `{"entries":[{"ip":"127.0.0.1","hostname":"redis.default.svc"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("write status = %d, body %s", rec.Code, rec.Body)
	}
	content, _ := os.ReadFile(hostsPath)
	if !strings.Contains(string(content), "redis.default.svc") {
		t.Fatalf("entry not written:\n%s", content)
	}
	if !strings.Contains(string(content), "127.0.0.1 localhost") {
		t.Fatalf("pre-existing content lost:\n%s", content)
	}

	rec = postHosts(t, handler, `{"entries":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("strip status = %d, body %s", rec.Code, rec.Body)
	}
	content, _ = os.ReadFile(hostsPath)
	if strings.Contains(string(content), "redis.default.svc") {
		t.Fatalf("block not stripped:\n%s", content)
	}
}

// The writer runs as root and takes requests off a socket, so a request must
// not be able to point a hostname anywhere but the local machine.
func TestHostsDaemonRefusesNonLoopbackAddress(t *testing.T) {
	handler, hostsPath := newHostsTestHandler(t)

	rec := postHosts(t, handler, `{"entries":[{"ip":"10.0.0.5","hostname":"github.com"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-loopback address, got %d", rec.Code)
	}
	content, _ := os.ReadFile(hostsPath)
	if strings.Contains(string(content), "github.com") {
		t.Fatalf("a refused entry must not be written:\n%s", content)
	}
}

func TestHostsDaemonRefusesMalformedHostname(t *testing.T) {
	handler, _ := newHostsTestHandler(t)

	for _, body := range []string{
		`{"entries":[{"ip":"127.0.0.1","hostname":"bad host\nevil.com"}]}`,
		`{"entries":[{"ip":"127.0.0.1","hostname":""}]}`,
		`{"entries":[{"ip":"not-an-ip","hostname":"redis"}]}`,
	} {
		if rec := postHosts(t, handler, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", body, rec.Code)
		}
	}
}

func TestHostsDaemonRejectsUnknownFields(t *testing.T) {
	handler, _ := newHostsTestHandler(t)
	if rec := postHosts(t, handler, `{"entries":[],"extra":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown field, got %d", rec.Code)
	}
}

func TestHostsDaemonHealthzReportsVersion(t *testing.T) {
	handler, _ := newHostsTestHandler(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
	var response contracts.HelperResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !response.Success || response.Version != version {
		t.Fatalf("unexpected healthz response %+v", response)
	}
}

func TestHostsDaemonShutdownSignals(t *testing.T) {
	hostsPath := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hostsPath, nil, 0o644); err != nil {
		t.Fatalf("seed hosts: %v", err)
	}
	hostfile.SetPathForTest(t, hostsPath)

	shutdownCh := make(chan struct{}, 1)
	handler := newHostsHandler(&hostsWriter{}, shutdownCh)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/shutdown", bytes.NewReader(nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("shutdown status = %d", rec.Code)
	}
	select {
	case <-shutdownCh:
	default:
		t.Fatal("shutdown was not signalled")
	}
}

// Concurrent CLI commands can deliver snapshots out of order. An older
// snapshot from the same helper must not undo a newer one.
func TestHostsDaemonIgnoresStaleSnapshot(t *testing.T) {
	handler, hostsPath := newHostsTestHandler(t)

	newer := `{"entries":[{"ip":"127.0.0.1","hostname":"a.default.svc"},{"ip":"127.0.0.1","hostname":"b.default.svc"}],"revision":{"epoch":"e1","seq":2}}`
	older := `{"entries":[{"ip":"127.0.0.1","hostname":"a.default.svc"}],"revision":{"epoch":"e1","seq":1}}`

	if rec := postHosts(t, handler, newer); rec.Code != http.StatusOK {
		t.Fatalf("newer status = %d, body %s", rec.Code, rec.Body)
	}
	rec := postHosts(t, handler, older)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "stale") {
		t.Fatalf("stale snapshot should be acknowledged and ignored, got %d %s", rec.Code, rec.Body)
	}
	content, _ := os.ReadFile(hostsPath)
	if !strings.Contains(string(content), "b.default.svc") {
		t.Fatalf("stale snapshot removed a newer entry:\n%s", content)
	}

	// A restarted helper starts a new epoch with a low sequence number.
	restarted := `{"entries":[{"ip":"127.0.0.1","hostname":"c.default.svc"}],"revision":{"epoch":"e2","seq":1}}`
	if rec := postHosts(t, handler, restarted); rec.Code != http.StatusOK {
		t.Fatalf("new epoch status = %d, body %s", rec.Code, rec.Body)
	}
	content, _ = os.ReadFile(hostsPath)
	if !strings.Contains(string(content), "c.default.svc") || strings.Contains(string(content), "b.default.svc") {
		t.Fatalf("snapshot from a new epoch was not applied:\n%s", content)
	}
}

// A request already in flight when shutdown starts must not write the block
// back after shutdown strips it.
func TestHostsDaemonShutdownDrainsBeforeStripping(t *testing.T) {
	hostsPath := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatalf("seed hosts: %v", err)
	}
	hostfile.SetPathForTest(t, hostsPath)

	writer := &hostsWriter{}
	inner := newHostsHandler(writer, make(chan struct{}, 1))
	entered := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		inner.ServeHTTP(w, r)
	})}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serverErrCh := make(chan error, 1)
	go func() { serverErrCh <- server.Serve(listener) }()

	// Hold the writer so the request stays in flight inside the handler.
	writer.mu.Lock()
	statusCh := make(chan int, 1)
	go func() {
		body := `{"entries":[{"ip":"127.0.0.1","hostname":"redis.default.svc"}],"revision":{"epoch":"e1","seq":1}}`
		resp, err := http.Post("http://"+listener.Addr().String()+"/v1/hosts", "application/json", strings.NewReader(body))
		if err != nil {
			statusCh <- -1
			return
		}
		resp.Body.Close()
		statusCh <- resp.StatusCode
	}()
	<-entered

	doneCh := make(chan error, 1)
	go func() { doneCh <- shutdownHostsDaemon(server, serverErrCh, writer) }()

	// Shutdown closes the listener first; once dials fail it is draining.
	for {
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			break
		}
		conn.Close()
	}
	writer.mu.Unlock()

	if err := <-doneCh; err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
	if status := <-statusCh; status != http.StatusOK {
		t.Fatalf("in-flight request should complete before shutdown, got status %d", status)
	}
	content, _ := os.ReadFile(hostsPath)
	if strings.Contains(string(content), "redis.default.svc") {
		t.Fatalf("in-flight request left the block behind after shutdown:\n%s", content)
	}
	if !strings.Contains(string(content), "127.0.0.1 localhost") {
		t.Fatalf("pre-existing content lost:\n%s", content)
	}

	if err := writer.apply([]contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "late.default.svc"}}, contracts.HostsRevision{}); !errors.Is(err, errHostsWriterClosed) {
		t.Fatalf("writes after shutdown must be refused, got %v", err)
	}
}
