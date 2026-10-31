package stream

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/streamlink"
	"github.com/gorilla/websocket"
)

const (
	peerSendQueueSize = 1024
	sendGraceTimeout  = 5 * time.Second
)

type SessionRelayRegistry struct {
	mu       sync.Mutex
	sessions map[string]*sessionRelay
}

type sessionRelay struct {
	client         *relayPeer
	agents         map[*relayPeer]struct{}
	connectionPeer map[string]*relayPeer
}

type relayPeer struct {
	role      string
	sessionID string
	conn      *websocket.Conn
	sendCh    chan contracts.StreamEnvelope
}

func (p *relayPeer) closeConn() {
	if p.conn != nil {
		_ = p.conn.Close()
	}
}

func NewSessionRelayRegistry() *SessionRelayRegistry {
	return &SessionRelayRegistry{
		sessions: map[string]*sessionRelay{},
	}
}

// ServePeer relays one attached peer until it disconnects. It blocks for the
// lifetime of the websocket connection.
func (h *SessionRelayRegistry) ServePeer(role string, sessionID string, conn *websocket.Conn) {
	peer := &relayPeer{
		role:      strings.TrimSpace(role),
		sessionID: strings.TrimSpace(sessionID),
		conn:      conn,
		sendCh:    make(chan contracts.StreamEnvelope, peerSendQueueSize),
	}

	h.register(peer)
	defer h.unregister(peer)

	_ = streamlink.Serve(context.Background(), peer.conn, peer.sendCh, func(envelope contracts.StreamEnvelope) {
		h.routeFromPeer(peer, envelope)
	})
}

func (h *SessionRelayRegistry) register(peer *relayPeer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	session := h.ensureSessionLocked(peer.sessionID)
	switch peer.role {
	case contracts.StreamRoleClient:
		if session.client != nil {
			session.client.closeConn()
		}
		session.client = peer
	default:
		session.agents[peer] = struct{}{}
	}
}

func (h *SessionRelayRegistry) unregister(peer *relayPeer) {
	h.mu.Lock()
	session := h.sessions[peer.sessionID]
	if session == nil || (peer.role == contracts.StreamRoleClient && session.client != peer) {
		h.mu.Unlock()
		return
	}

	message := "agent stream disconnected"
	if peer.role == contracts.StreamRoleClient {
		session.client = nil
		message = "helper stream disconnected"
	} else {
		delete(session.agents, peer)
	}

	notify := map[string]*relayPeer{}
	for connectionID, owner := range session.connectionPeer {
		if peer.role != contracts.StreamRoleClient && owner != peer {
			continue
		}
		delete(session.connectionPeer, connectionID)
		target := session.client
		if peer.role == contracts.StreamRoleClient {
			target = owner
		}
		if target != nil {
			notify[connectionID] = target
		}
	}

	if session.client == nil && len(session.agents) == 0 {
		delete(h.sessions, peer.sessionID)
	}
	h.mu.Unlock()

	for connectionID, target := range notify {
		sendEnvelope(target, contracts.StreamEnvelope{
			Type:         contracts.StreamTypeError,
			ConnectionID: connectionID,
			Message:      message,
		})
	}
}

func (h *SessionRelayRegistry) routeFromPeer(peer *relayPeer, envelope contracts.StreamEnvelope) {
	connectionID := strings.TrimSpace(envelope.ConnectionID)
	envelope.ConnectionID = connectionID

	switch peer.role {
	case contracts.StreamRoleClient:
		h.routeFromClient(peer, envelope)
	default:
		h.routeFromAgent(peer, envelope)
	}
}

func (h *SessionRelayRegistry) routeFromAgent(peer *relayPeer, envelope contracts.StreamEnvelope) {
	h.mu.Lock()
	session := h.sessions[peer.sessionID]
	target := session.client
	if envelope.Type == contracts.StreamTypeOpen && envelope.ConnectionID != "" && target != nil {
		session.connectionPeer[envelope.ConnectionID] = peer
	}
	if envelope.Type == contracts.StreamTypeClose || envelope.Type == contracts.StreamTypeError {
		delete(session.connectionPeer, envelope.ConnectionID)
	}
	h.mu.Unlock()

	if target != nil {
		sendEnvelope(target, envelope)
	} else if envelope.Type == contracts.StreamTypeOpen {
		sendEnvelope(peer, contracts.StreamEnvelope{
			Type:         contracts.StreamTypeError,
			ConnectionID: envelope.ConnectionID,
			Message:      "no helper client stream attached",
		})
	}
}

func (h *SessionRelayRegistry) routeFromClient(peer *relayPeer, envelope contracts.StreamEnvelope) {
	if envelope.ConnectionID == "" {
		return
	}

	var target *relayPeer
	h.mu.Lock()
	session := h.ensureSessionLocked(peer.sessionID)
	target = session.connectionPeer[envelope.ConnectionID]
	if envelope.Type == contracts.StreamTypeClose || envelope.Type == contracts.StreamTypeError {
		delete(session.connectionPeer, envelope.ConnectionID)
	}
	h.mu.Unlock()

	if target == nil {
		return
	}
	sendEnvelope(target, envelope)
}

func (h *SessionRelayRegistry) ensureSessionLocked(sessionID string) *sessionRelay {
	session, ok := h.sessions[sessionID]
	if ok {
		return session
	}
	session = &sessionRelay{
		agents:         map[*relayPeer]struct{}{},
		connectionPeer: map[string]*relayPeer{},
	}
	h.sessions[sessionID] = session
	return session
}

func sendEnvelope(peer *relayPeer, envelope contracts.StreamEnvelope) {
	select {
	case peer.sendCh <- envelope:
		return
	default:
	}

	// Queue is full: give the peer a bounded grace period to drain before
	// declaring it dead, instead of tearing down the shared stream instantly.
	timer := time.NewTimer(sendGraceTimeout)
	defer timer.Stop()
	select {
	case peer.sendCh <- envelope:
	case <-timer.C:
		peer.closeConn()
	}
}
