package stream

import (
	"testing"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
)

func newTestPeer(role string, sessionID string) *relayPeer {
	return &relayPeer{
		role:      role,
		sessionID: sessionID,
		sendCh:    make(chan contracts.StreamEnvelope, 8),
	}
}

func drainOne(t *testing.T, peer *relayPeer) contracts.StreamEnvelope {
	t.Helper()
	select {
	case envelope := <-peer.sendCh:
		return envelope
	case <-time.After(time.Second):
		t.Fatal("expected envelope was not delivered")
		return contracts.StreamEnvelope{}
	}
}

func assertNoEnvelope(t *testing.T, peer *relayPeer) {
	t.Helper()
	select {
	case envelope := <-peer.sendCh:
		t.Fatalf("unexpected envelope: %+v", envelope)
	default:
	}
}

func TestUnregisterReplacedClientKeepsActiveClientState(t *testing.T) {
	registry := NewSessionRelayRegistry()
	agent := newTestPeer(contracts.StreamRoleAgent, "sess_a")
	oldClient := newTestPeer(contracts.StreamRoleClient, "sess_a")
	newClient := newTestPeer(contracts.StreamRoleClient, "sess_a")

	registry.register(agent)
	registry.register(oldClient)
	registry.register(newClient)

	registry.routeFromPeer(agent, contracts.StreamEnvelope{
		Type:         contracts.StreamTypeOpen,
		ConnectionID: "conn-1",
	})
	if envelope := drainOne(t, newClient); envelope.Type != contracts.StreamTypeOpen {
		t.Fatalf("expected open on new client, got %+v", envelope)
	}

	// The replaced client unregisters after being displaced; it must not
	// tear down routing state that now belongs to the new client.
	registry.unregister(oldClient)

	assertNoEnvelope(t, agent)

	registry.routeFromPeer(newClient, contracts.StreamEnvelope{
		Type:         contracts.StreamTypeData,
		ConnectionID: "conn-1",
		Data:         []byte("response"),
	})
	if envelope := drainOne(t, agent); envelope.Type != contracts.StreamTypeData {
		t.Fatalf("expected data routed to agent, got %+v", envelope)
	}
}

func TestUnregisterActiveClientNotifiesAgents(t *testing.T) {
	registry := NewSessionRelayRegistry()
	agent := newTestPeer(contracts.StreamRoleAgent, "sess_b")
	client := newTestPeer(contracts.StreamRoleClient, "sess_b")

	registry.register(agent)
	registry.register(client)

	registry.routeFromPeer(agent, contracts.StreamEnvelope{
		Type:         contracts.StreamTypeOpen,
		ConnectionID: "conn-1",
	})
	drainOne(t, client)

	registry.unregister(client)

	if envelope := drainOne(t, agent); envelope.Type != contracts.StreamTypeError {
		t.Fatalf("expected error envelope, got %+v", envelope)
	}
	if envelope := drainOne(t, agent); envelope.Type != contracts.StreamTypeClose {
		t.Fatalf("expected close envelope, got %+v", envelope)
	}
}

func TestSendEnvelopeWaitsForQueueDrain(t *testing.T) {
	peer := &relayPeer{sendCh: make(chan contracts.StreamEnvelope, 1)}
	peer.sendCh <- contracts.StreamEnvelope{ConnectionID: "first"}

	done := make(chan struct{})
	go func() {
		sendEnvelope(peer, contracts.StreamEnvelope{ConnectionID: "second"})
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	if envelope := <-peer.sendCh; envelope.ConnectionID != "first" {
		t.Fatalf("expected first envelope, got %+v", envelope)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sendEnvelope did not complete after queue drained")
	}
	if envelope := <-peer.sendCh; envelope.ConnectionID != "second" {
		t.Fatalf("expected second envelope, got %+v", envelope)
	}
}
