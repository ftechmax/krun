package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/sessionkey"
	"github.com/gorilla/websocket"
)

const (
	defaultStreamPath    = "/v1/stream/client"
	connectTimeout       = 5 * time.Second
	writeTimeout         = 10 * time.Second
	initialBackoff       = time.Second
	maxBackoff           = 10 * time.Second
	sendQueueSize        = 2048
	localDialTimeout     = 2 * time.Second
	connectionReadBuffer = 32 * 1024
)

type SessionRegistry struct {
	mu             sync.Mutex
	managerAddress string
	attachments    map[string]*sessionAttachment
}

func NewSessionRegistry(managerAddress string) *SessionRegistry {
	return &SessionRegistry{
		managerAddress: strings.TrimSpace(managerAddress),
		attachments:    map[string]*sessionAttachment{},
	}
}

func (r *SessionRegistry) Upsert(sessionKey string, sessionID string, sessionToken string, interceptPort int) error {
	key := sessionkey.Normalize(sessionKey)
	attachment, err := newSessionAttachment(r.managerAddress, sessionID, sessionToken, interceptPort)
	if err != nil {
		return err
	}
	attachment.start()

	r.mu.Lock()
	previous := r.attachments[key]
	r.attachments[key] = attachment
	r.mu.Unlock()

	if previous != nil {
		previous.stop()
	}
	return nil
}

func (r *SessionRegistry) Remove(sessionKey string) error {
	key := sessionkey.Normalize(sessionKey)

	r.mu.Lock()
	attachment, ok := r.attachments[key]
	if ok {
		delete(r.attachments, key)
	}
	r.mu.Unlock()

	if ok {
		attachment.stop()
	}
	return nil
}

func (r *SessionRegistry) Clear() error {
	r.mu.Lock()
	attachments := make([]*sessionAttachment, 0, len(r.attachments))
	for _, attachment := range r.attachments {
		attachments = append(attachments, attachment)
	}
	r.attachments = map[string]*sessionAttachment{}
	r.mu.Unlock()

	for _, attachment := range attachments {
		attachment.stop()
	}
	return nil
}

type sessionAttachment struct {
	sessionID    string
	interceptURL string
	streamURL    string

	cancel context.CancelFunc
	doneCh chan struct{}
	sendCh chan contracts.StreamEnvelope

	connMu sync.Mutex
	conns  map[string]net.Conn
}

func newSessionAttachment(managerAddress string, sessionID string, sessionToken string, interceptPort int) (*sessionAttachment, error) {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return nil, errors.New("stream session id is required")
	}
	if interceptPort < 1 || interceptPort > 65535 {
		return nil, fmt.Errorf("invalid intercept port %d", interceptPort)
	}

	streamURL, err := buildStreamURL(managerAddress, trimmedSessionID, sessionToken)
	if err != nil {
		return nil, err
	}

	return &sessionAttachment{
		sessionID:    trimmedSessionID,
		interceptURL: net.JoinHostPort("127.0.0.1", strconv.Itoa(interceptPort)),
		streamURL:    streamURL,
		doneCh:       make(chan struct{}),
		sendCh:       make(chan contracts.StreamEnvelope, sendQueueSize),
		conns:        map[string]net.Conn{},
	}, nil
}

func (a *sessionAttachment) start() {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go a.run(ctx)
}

func (a *sessionAttachment) stop() {
	if a.cancel != nil {
		a.cancel()
	}
	<-a.doneCh
}

func (a *sessionAttachment) run(ctx context.Context) {
	defer close(a.doneCh)
	defer a.closeAllLocalConns()

	backoff := initialBackoff
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		conn, _, err := websocket.DefaultDialer.DialContext(dialCtx, a.streamURL, http.Header{})
		cancel()
		if err != nil {
			log.Printf("helper stream connect failed (session_id=%s): %v", a.sessionID, err)
			if !sleepWithContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		log.Printf("helper stream connected (session_id=%s)", a.sessionID)
		backoff = initialBackoff
		if err := a.pumpConnection(ctx, conn); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("helper stream disconnected (session_id=%s): %v", a.sessionID, err)
		}
	}
}

func (a *sessionAttachment) pumpConnection(ctx context.Context, conn *websocket.Conn) error {
	defer conn.Close()

	readErrCh := make(chan error, 1)
	envelopeCh := make(chan contracts.StreamEnvelope, 128)
	go func() {
		for {
			var envelope contracts.StreamEnvelope
			if err := conn.ReadJSON(&envelope); err != nil {
				readErrCh <- err
				return
			}
			envelopeCh <- envelope
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case readErr := <-readErrCh:
			return readErr
		case envelope := <-envelopeCh:
			a.handleInboundEnvelope(ctx, envelope)
		case outbound := <-a.sendCh:
			_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteJSON(outbound); err != nil {
				return err
			}
		}
	}
}

func (a *sessionAttachment) handleInboundEnvelope(ctx context.Context, envelope contracts.StreamEnvelope) {
	connectionID := strings.TrimSpace(envelope.ConnectionID)
	envelope.ConnectionID = connectionID
	envelope.SessionID = a.sessionID

	switch envelope.Type {
	case contracts.StreamTypeOpen:
		a.handleOpen(ctx, connectionID)
	case contracts.StreamTypeData:
		a.handleData(connectionID, envelope.Data)
	case contracts.StreamTypeClose, contracts.StreamTypeError:
		a.closeLocalConn(connectionID)
	case contracts.StreamTypePing:
		if err := a.enqueueOutbound(ctx, contracts.StreamEnvelope{
			Type:      contracts.StreamTypePing,
			SessionID: a.sessionID,
		}); err != nil {
			log.Printf("enqueue ping envelope failed (session_id=%s): %v", a.sessionID, err)
		}
	}
}

func (a *sessionAttachment) handleOpen(ctx context.Context, connectionID string) {
	if connectionID == "" {
		return
	}

	localConn, err := net.DialTimeout("tcp", a.interceptURL, localDialTimeout)
	if err != nil {
		if enqueueErr := a.enqueueOutbound(ctx, contracts.StreamEnvelope{
			Type:         contracts.StreamTypeError,
			SessionID:    a.sessionID,
			ConnectionID: connectionID,
			Message:      err.Error(),
		}); enqueueErr != nil {
			log.Printf("enqueue open-error envelope failed (session_id=%s connection_id=%s): %v", a.sessionID, connectionID, enqueueErr)
		}
		if enqueueErr := a.enqueueOutbound(ctx, contracts.StreamEnvelope{
			Type:         contracts.StreamTypeClose,
			SessionID:    a.sessionID,
			ConnectionID: connectionID,
		}); enqueueErr != nil {
			log.Printf("enqueue open-close envelope failed (session_id=%s connection_id=%s): %v", a.sessionID, connectionID, enqueueErr)
		}
		return
	}

	a.connMu.Lock()
	if existing, ok := a.conns[connectionID]; ok {
		_ = existing.Close()
	}
	a.conns[connectionID] = localConn
	a.connMu.Unlock()

	go a.pumpLocalConnection(ctx, connectionID, localConn)
}

func (a *sessionAttachment) handleData(connectionID string, payload []byte) {
	if connectionID == "" || len(payload) == 0 {
		return
	}
	localConn := a.getLocalConn(connectionID)
	if localConn == nil {
		return
	}
	if _, err := localConn.Write(payload); err != nil {
		_ = localConn.Close()
		a.closeLocalConn(connectionID)
		if enqueueErr := a.enqueueOutbound(context.Background(), contracts.StreamEnvelope{
			Type:         contracts.StreamTypeError,
			SessionID:    a.sessionID,
			ConnectionID: connectionID,
			Message:      err.Error(),
		}); enqueueErr != nil {
			log.Printf("enqueue write-error envelope failed (session_id=%s connection_id=%s): %v", a.sessionID, connectionID, enqueueErr)
		}
		if enqueueErr := a.enqueueOutbound(context.Background(), contracts.StreamEnvelope{
			Type:         contracts.StreamTypeClose,
			SessionID:    a.sessionID,
			ConnectionID: connectionID,
		}); enqueueErr != nil {
			log.Printf("enqueue write-close envelope failed (session_id=%s connection_id=%s): %v", a.sessionID, connectionID, enqueueErr)
		}
	}
}

func (a *sessionAttachment) pumpLocalConnection(ctx context.Context, connectionID string, localConn net.Conn) {
	defer a.closeLocalConn(connectionID)

	buffer := make([]byte, connectionReadBuffer)
	for {
		readBytes, readErr := localConn.Read(buffer)
		if readBytes > 0 {
			chunk := make([]byte, readBytes)
			copy(chunk, buffer[:readBytes])
			if err := a.enqueueOutbound(ctx, contracts.StreamEnvelope{
				Type:         contracts.StreamTypeData,
				SessionID:    a.sessionID,
				ConnectionID: connectionID,
				Data:         chunk,
			}); err != nil {
				return
			}
		}

		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			if err := a.enqueueOutbound(ctx, contracts.StreamEnvelope{
				Type:         contracts.StreamTypeClose,
				SessionID:    a.sessionID,
				ConnectionID: connectionID,
			}); err != nil {
				log.Printf("enqueue eof-close envelope failed (session_id=%s connection_id=%s): %v", a.sessionID, connectionID, err)
			}
			return
		}
		if errors.Is(readErr, net.ErrClosed) {
			return
		}

		if err := a.enqueueOutbound(ctx, contracts.StreamEnvelope{
			Type:         contracts.StreamTypeError,
			SessionID:    a.sessionID,
			ConnectionID: connectionID,
			Message:      readErr.Error(),
		}); err != nil {
			log.Printf("enqueue read-error envelope failed (session_id=%s connection_id=%s): %v", a.sessionID, connectionID, err)
		}
		if err := a.enqueueOutbound(ctx, contracts.StreamEnvelope{
			Type:         contracts.StreamTypeClose,
			SessionID:    a.sessionID,
			ConnectionID: connectionID,
		}); err != nil {
			log.Printf("enqueue read-close envelope failed (session_id=%s connection_id=%s): %v", a.sessionID, connectionID, err)
		}
		return
	}
}

func (a *sessionAttachment) enqueueOutbound(ctx context.Context, envelope contracts.StreamEnvelope) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.doneCh:
		return errors.New("session stream stopped")
	case a.sendCh <- envelope:
		return nil
	default:
		return errors.New("session stream outbound queue is full")
	}
}

func (a *sessionAttachment) getLocalConn(connectionID string) net.Conn {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	return a.conns[connectionID]
}

func (a *sessionAttachment) closeLocalConn(connectionID string) {
	if strings.TrimSpace(connectionID) == "" {
		return
	}
	a.connMu.Lock()
	localConn, ok := a.conns[connectionID]
	if ok {
		delete(a.conns, connectionID)
	}
	a.connMu.Unlock()
	if ok {
		_ = localConn.Close()
	}
}

func (a *sessionAttachment) closeAllLocalConns() {
	a.connMu.Lock()
	conns := make([]net.Conn, 0, len(a.conns))
	for _, localConn := range a.conns {
		conns = append(conns, localConn)
	}
	a.conns = map[string]net.Conn{}
	a.connMu.Unlock()
	for _, localConn := range conns {
		_ = localConn.Close()
	}
}

func buildStreamURL(managerAddress string, sessionID string, sessionToken string) (string, error) {
	rawAddress := strings.TrimSpace(managerAddress)
	if rawAddress == "" {
		return "", errors.New("manager address is required")
	}
	if !strings.Contains(rawAddress, "://") {
		rawAddress = "http://" + rawAddress
	}

	parsed, err := url.Parse(rawAddress)
	if err != nil {
		return "", fmt.Errorf("invalid manager address %q: %w", managerAddress, err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid manager address %q: host is required", managerAddress)
	}

	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported manager address scheme %q", parsed.Scheme)
	}

	basePath := strings.TrimRight(parsed.Path, "/")
	parsed.Path = basePath + defaultStreamPath
	query := parsed.Query()
	query.Set("session_id", strings.TrimSpace(sessionID))
	if strings.TrimSpace(sessionToken) != "" {
		query.Set("session_token", strings.TrimSpace(sessionToken))
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func sleepWithContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}
