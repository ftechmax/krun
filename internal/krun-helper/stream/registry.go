package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

const (
	streamPath       = "/v1/stream/client"
	connectTimeout   = 5 * time.Second
	initialBackoff   = time.Second
	maxBackoff       = 10 * time.Second
	localDialTimeout = 2 * time.Second
)

type SessionRegistry struct {
	mu             sync.Mutex
	managerAddress string
	attachments    map[string]*attachment
}

func NewRegistry(managerAddress string) *SessionRegistry {
	return &SessionRegistry{
		managerAddress: strings.TrimSpace(managerAddress),
		attachments:    map[string]*attachment{},
	}
}

func (r *SessionRegistry) Upsert(sessionKey, sessionID, sessionToken string, interceptPort int) error {
	a, err := newAttachment(r.managerAddress, sessionID, sessionToken, interceptPort)
	if err != nil {
		return err
	}
	a.start()

	r.mu.Lock()
	previous := r.attachments[sessionKey]
	r.attachments[sessionKey] = a
	r.mu.Unlock()

	if previous != nil {
		previous.stop()
	}
	return nil
}

func (r *SessionRegistry) Remove(sessionKey string) error {
	r.mu.Lock()
	a, ok := r.attachments[sessionKey]
	if ok {
		delete(r.attachments, sessionKey)
	}
	r.mu.Unlock()

	if ok {
		a.stop()
	}
	return nil
}

func (r *SessionRegistry) Clear() error {
	r.mu.Lock()
	all := make([]*attachment, 0, len(r.attachments))
	for _, a := range r.attachments {
		all = append(all, a)
	}
	r.attachments = map[string]*attachment{}
	r.mu.Unlock()

	for _, a := range all {
		a.stop()
	}
	return nil
}

type attachment struct {
	sessionID    string
	streamURL    string
	interceptURL string

	cancel context.CancelFunc
	doneCh chan struct{}
}

func newAttachment(managerAddress, sessionID, sessionToken string, interceptPort int) (*attachment, error) {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return nil, errors.New("stream session id is required")
	}
	if interceptPort < 1 || interceptPort > 65535 {
		return nil, fmt.Errorf("invalid intercept port %d", interceptPort)
	}
	streamURL, err := buildStreamURL(managerAddress, id, sessionToken)
	if err != nil {
		return nil, err
	}
	return &attachment{
		sessionID:    id,
		streamURL:    streamURL,
		interceptURL: net.JoinHostPort("127.0.0.1", strconv.Itoa(interceptPort)),
		doneCh:       make(chan struct{}),
	}, nil
}

func (a *attachment) start() {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go a.run(ctx)
}

func (a *attachment) stop() {
	if a.cancel != nil {
		a.cancel()
	}
	<-a.doneCh
}

func (a *attachment) run(ctx context.Context) {
	defer close(a.doneCh)

	backoff := initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}

		dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		ws, _, err := websocket.Dial(dialCtx, a.streamURL, nil)
		cancel()
		if err != nil {
			slog.Error("helper stream connect failed", "session_id", a.sessionID, "err", err)
			if !sleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		mux, err := yamux.Server(websocket.NetConn(ctx, ws, websocket.MessageBinary), yamuxConfig())
		if err != nil {
			slog.Error("yamux server init failed", "session_id", a.sessionID, "err", err)
			_ = ws.Close(websocket.StatusInternalError, "")
			if !sleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		slog.Info("helper stream connected", "session_id", a.sessionID)
		backoff = initialBackoff
		a.serve(ctx, mux)
		slog.Info("helper stream disconnected", "session_id", a.sessionID)
	}
}

func (a *attachment) serve(ctx context.Context, mux *yamux.Session) {
	defer mux.Close()
	go func() {
		<-ctx.Done()
		_ = mux.Close()
	}()

	for {
		stream, err := mux.AcceptStream()
		if err != nil {
			return
		}
		go a.handleStream(stream)
	}
}

func (a *attachment) handleStream(stream net.Conn) {
	defer stream.Close()
	local, err := net.DialTimeout("tcp", a.interceptURL, localDialTimeout)
	if err != nil {
		slog.Error("dial intercept failed", "url", a.interceptURL, "session_id", a.sessionID, "err", err)
		return
	}
	defer local.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(local, stream); done <- struct{}{} }()
	go func() { _, _ = io.Copy(stream, local); done <- struct{}{} }()
	<-done
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	return cfg
}

func buildStreamURL(managerAddress, sessionID, sessionToken string) (string, error) {
	raw := strings.TrimSpace(managerAddress)
	if raw == "" {
		return "", errors.New("manager address is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid manager address %q", managerAddress)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported manager address scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + streamPath
	q := u.Query()
	q.Set("session_id", sessionID)
	if strings.TrimSpace(sessionToken) != "" {
		q.Set("session_token", strings.TrimSpace(sessionToken))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
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
