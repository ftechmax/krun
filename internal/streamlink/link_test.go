package streamlink

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/gorilla/websocket"
)

// wsPeer is a websocket server that hands its accepted connection back to the
// test, so the test can drive both ends of a Serve loop.
func wsPeer(t *testing.T) (clientConn *websocket.Conn, serverConn *websocket.Conn) {
	t.Helper()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	accepted := make(chan *websocket.Conn, 1)
	shutdown := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		select {
		case accepted <- conn:
		case <-shutdown:
			conn.Close()
			return
		}
		<-shutdown
		conn.Close()
	}))
	t.Cleanup(func() {
		close(shutdown)
		server.Close()
	})

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { clientConn.Close() })

	select {
	case serverConn = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the test websocket server connection")
	}
	return clientConn, serverConn
}

func TestServeDispatchesInboundEnvelopes(t *testing.T) {
	clientConn, serverConn := wsPeer(t)

	received := make(chan contracts.StreamEnvelope, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- Serve(ctx, clientConn, make(chan contracts.StreamEnvelope), func(envelope contracts.StreamEnvelope) {
			received <- envelope
		})
	}()

	inbound := contracts.StreamEnvelope{
		Type:         contracts.StreamTypeData,
		ConnectionID: "conn-1",
		Data:         []byte("hello"),
	}
	if err := serverConn.WriteJSON(inbound); err != nil {
		t.Fatalf("write inbound envelope: %v", err)
	}

	select {
	case got := <-received:
		if got.Type != inbound.Type || got.ConnectionID != inbound.ConnectionID || string(got.Data) != string(inbound.Data) {
			t.Fatalf("unexpected received envelope: got=%+v want=%+v", got, inbound)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the inbound envelope callback")
	}

	cancel()
	select {
	case err := <-serveErrCh:
		if err != nil {
			t.Fatalf("Serve returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Serve to exit")
	}
}

// A receive handler routinely answers an envelope by sending one back (a
// close-write reply, a per-connection error). Serve must keep draining the
// outbound queue while that handler runs, or a full queue deadlocks the whole
// stream against itself.
func TestServeLetsReceiveHandlerSendIntoAFullQueue(t *testing.T) {
	clientConn, serverConn := wsPeer(t)

	const replies = 5
	sendCh := make(chan contracts.StreamEnvelope, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go Serve(ctx, clientConn, sendCh, func(contracts.StreamEnvelope) {
		for i := 0; i < replies; i++ {
			// Blocking sends: with only one queue slot, all but the first
			// depend on the writer draining concurrently.
			sendCh <- contracts.StreamEnvelope{
				Type:         contracts.StreamTypeClose,
				ConnectionID: "conn-1",
			}
		}
	})

	if err := serverConn.WriteJSON(contracts.StreamEnvelope{
		Type:         contracts.StreamTypeCloseWrite,
		ConnectionID: "conn-1",
	}); err != nil {
		t.Fatalf("write inbound envelope: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		for i := 0; i < replies; i++ {
			var envelope contracts.StreamEnvelope
			if err := serverConn.ReadJSON(&envelope); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reading replies failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %d replies: the receive handler deadlocked against a full queue", replies)
	}
}

func TestServeReturnsWhenPeerDisconnects(t *testing.T) {
	clientConn, serverConn := wsPeer(t)

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- Serve(context.Background(), clientConn, make(chan contracts.StreamEnvelope), nil)
	}()

	serverConn.Close()

	select {
	case err := <-serveErrCh:
		if err == nil {
			t.Fatal("expected an error when the peer disconnects")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Serve to notice the disconnect")
	}
}

func TestServeRejectsOversizedMessages(t *testing.T) {
	clientConn, serverConn := wsPeer(t)

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- Serve(context.Background(), clientConn, make(chan contracts.StreamEnvelope), nil)
	}()

	payload := []byte(`{"type":"data","data":"` + strings.Repeat("A", maxMessageSize) + `"}`)
	if err := serverConn.WriteMessage(websocket.TextMessage, payload); err != nil {
		// The reader may enforce the limit and reset the connection while the
		// oversized frame is still being written.
		t.Logf("oversized write ended after peer rejection: %v", err)
	}

	select {
	case err := <-serveErrCh:
		if err == nil {
			t.Fatal("Serve accepted a websocket message larger than its read limit")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Serve to reject oversized message")
	}
}

func TestNextBackoffSaturatesAtMax(t *testing.T) {
	delay := initialBackoff
	for i := 0; i < 10; i++ {
		delay = nextBackoff(delay)
	}
	if delay != maxBackoff {
		t.Fatalf("expected backoff to saturate at %v, got %v", maxBackoff, delay)
	}
}

func TestLinkReconnectsAfterPeerDisconnect(t *testing.T) {
	var connections atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connections.Add(1)
		if err := conn.Close(); err != nil {
			t.Logf("close test peer: %v", err)
		}
	}))
	defer server.Close()

	link := New(context.Background(), Config{
		URL:       "ws" + strings.TrimPrefix(server.URL, "http"),
		QueueSize: 1,
	})
	link.Start(nil, nil)

	deadline := time.Now().Add(2 * time.Second)
	for connections.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := connections.Load(); got < 2 {
		link.Close()
		t.Fatalf("connections = %d, want at least 2", got)
	}

	closed := make(chan struct{})
	go func() {
		link.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Link.Close did not cancel the reconnect loop")
	}
}
