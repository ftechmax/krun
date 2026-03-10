package stream

import (
	"strings"
	"sync"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/sessionkey"
	"github.com/gorilla/websocket"
)

const (
	peerSendQueueSize = 1024
	writeTimeout      = 10 * time.Second
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

func NewSessionRelayRegistry() *SessionRelayRegistry {
	return &SessionRelayRegistry{
		sessions: map[string]*sessionRelay{},
	}
}

func (h *SessionRelayRegistry) ServePeer(role string, sessionID string, conn *websocket.Conn) {
	peer := &relayPeer{
		role:      sessionkey.Trim(role),
		sessionID: sessionkey.Trim(sessionID),
		conn:      conn,
		sendCh:    make(chan contracts.StreamEnvelope, peerSendQueueSize),
	}

	h.register(peer)
	defer h.unregister(peer)
	defer peer.conn.Close()

	readErrCh := make(chan error, 1)
	envelopeCh := make(chan contracts.StreamEnvelope, 128)
	go func() {
		for {
			var envelope contracts.StreamEnvelope
			if err := peer.conn.ReadJSON(&envelope); err != nil {
				readErrCh <- err
				return
			}
			envelopeCh <- envelope
		}
	}()

	for {
		select {
		case envelope := <-envelopeCh:
			h.routeFromPeer(peer, envelope)
		case outbound := <-peer.sendCh:
			_ = peer.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := peer.conn.WriteJSON(outbound); err != nil {
				return
			}
		case <-readErrCh:
			return
		}
	}
}

func (h *SessionRelayRegistry) register(peer *relayPeer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	session := h.ensureSessionLocked(peer.sessionID)
	switch peer.role {
	case contracts.StreamRoleClient:
		if session.client != nil {
			_ = session.client.conn.Close()
		}
		session.client = peer
	default:
		session.agents[peer] = struct{}{}
	}
}

func (h *SessionRelayRegistry) unregister(peer *relayPeer) {
	var toClient []contracts.StreamEnvelope
	var toAgent map[*relayPeer][]contracts.StreamEnvelope
	var targetClient *relayPeer

	h.mu.Lock()
	session, ok := h.sessions[peer.sessionID]
	if !ok {
		h.mu.Unlock()
		return
	}

	switch peer.role {
	case contracts.StreamRoleClient:
		if session.client == peer {
			session.client = nil
		}
		if len(session.connectionPeer) > 0 {
			toAgent = map[*relayPeer][]contracts.StreamEnvelope{}
			for connectionID, owner := range session.connectionPeer {
				delete(session.connectionPeer, connectionID)
				if owner == nil {
					continue
				}
				toAgent[owner] = append(toAgent[owner],
					contracts.StreamEnvelope{
						Type:         contracts.StreamTypeError,
						SessionID:    peer.sessionID,
						ConnectionID: connectionID,
						Message:      "helper stream disconnected",
					},
					contracts.StreamEnvelope{
						Type:         contracts.StreamTypeClose,
						SessionID:    peer.sessionID,
						ConnectionID: connectionID,
					},
				)
			}
		}
	default:
		delete(session.agents, peer)
		if len(session.connectionPeer) > 0 {
			for connectionID, owner := range session.connectionPeer {
				if owner != peer {
					continue
				}
				delete(session.connectionPeer, connectionID)
				if session.client != nil {
					toClient = append(toClient,
						contracts.StreamEnvelope{
							Type:         contracts.StreamTypeError,
							SessionID:    peer.sessionID,
							ConnectionID: connectionID,
							Message:      "agent stream disconnected",
						},
						contracts.StreamEnvelope{
							Type:         contracts.StreamTypeClose,
							SessionID:    peer.sessionID,
							ConnectionID: connectionID,
						},
					)
				}
			}
		}
	}
	targetClient = session.client

	if session.client == nil && len(session.agents) == 0 && len(session.connectionPeer) == 0 {
		delete(h.sessions, peer.sessionID)
	}
	h.mu.Unlock()

	if len(toClient) > 0 && targetClient != nil {
		for _, envelope := range toClient {
			sendEnvelope(targetClient, envelope)
		}
	}
	for agentPeer, envelopes := range toAgent {
		for _, envelope := range envelopes {
			sendEnvelope(agentPeer, envelope)
		}
	}
}

func (h *SessionRelayRegistry) routeFromPeer(peer *relayPeer, envelope contracts.StreamEnvelope) {
	envelope.SessionID = peer.sessionID
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
	var target *relayPeer
	var toAgent []contracts.StreamEnvelope

	h.mu.Lock()
	session := h.ensureSessionLocked(peer.sessionID)
	if envelope.Type == contracts.StreamTypeOpen && envelope.ConnectionID != "" {
		session.connectionPeer[envelope.ConnectionID] = peer
	}
	if envelope.Type == contracts.StreamTypeClose || envelope.Type == contracts.StreamTypeError {
		delete(session.connectionPeer, envelope.ConnectionID)
	}

	target = session.client
	if target == nil && envelope.Type == contracts.StreamTypeOpen && envelope.ConnectionID != "" {
		delete(session.connectionPeer, envelope.ConnectionID)
		toAgent = []contracts.StreamEnvelope{
			{
				Type:         contracts.StreamTypeError,
				SessionID:    peer.sessionID,
				ConnectionID: envelope.ConnectionID,
				Message:      "no helper client stream attached",
			},
			{
				Type:         contracts.StreamTypeClose,
				SessionID:    peer.sessionID,
				ConnectionID: envelope.ConnectionID,
			},
		}
	}
	h.mu.Unlock()

	if target != nil {
		sendEnvelope(target, envelope)
		return
	}
	for _, response := range toAgent {
		sendEnvelope(peer, response)
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
	if peer == nil {
		return
	}
	select {
	case peer.sendCh <- envelope:
	default:
		_ = peer.conn.Close()
	}
}
