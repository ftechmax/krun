// Package streamlink carries a debug session's intercepted traffic over a
// websocket to the traffic-manager. Both ends of the bridge use it: the
// traffic-agent in the cluster and krun-helper on the developer machine.
package streamlink

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/gorilla/websocket"
)

const (
	defaultQueueSize = 2048
	inboundQueueSize = 128
	dialTimeout      = 5 * time.Second
	initialBackoff   = time.Second
	maxBackoff       = 10 * time.Second
	maxMessageSize   = 1 << 20
)

// Serve runs one websocket connection's read and write pumps until ctx is
// cancelled, the peer disconnects, or a write fails. Inbound envelopes are
// dispatched to onReceive on the calling goroutine; outbound envelopes are
// drained from sendCh on a dedicated goroutine. Keeping the writer separate is
// what lets onReceive enqueue a reply without deadlocking against a full
// queue, which a single combined pump cannot do.
//
// The connection is always closed before returning: that is the only thing
// that unblocks the reader goroutine.
func Serve(
	ctx context.Context,
	conn *websocket.Conn,
	sendCh <-chan contracts.StreamEnvelope,
	onReceive func(contracts.StreamEnvelope),
) error {
	conn.SetReadLimit(maxMessageSize)
	configureKeepalive(conn)

	stopped := make(chan struct{})
	// LIFO: signal the pumps first, then close the connection under them.
	defer conn.Close()
	defer close(stopped)

	go pingLoop(conn, stopped)

	writeErrCh := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stopped:
				return
			case envelope := <-sendCh:
				_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
				if err := conn.WriteJSON(envelope); err != nil {
					select {
					case writeErrCh <- err:
					default:
					}
					return
				}
			}
		}
	}()

	readCh := make(chan contracts.StreamEnvelope, inboundQueueSize)
	readErrCh := make(chan error, 1)
	go func() {
		for {
			var envelope contracts.StreamEnvelope
			if err := conn.ReadJSON(&envelope); err != nil {
				readErrCh <- err
				return
			}
			select {
			case readCh <- envelope:
			case <-stopped:
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case envelope := <-readCh:
			if onReceive != nil {
				onReceive(envelope)
			}
		case err := <-readErrCh:
			return err
		case err := <-writeErrCh:
			return err
		}
	}
}

// Config describes one peer's attachment to a session stream.
type Config struct {
	// URL is the websocket endpoint, normally built with URL().
	URL     string
	Headers http.Header
	// SessionID is stamped onto every outbound envelope.
	SessionID string
	// LogPrefix identifies this peer in connect/disconnect logs.
	LogPrefix string
	// QueueSize bounds the outbound envelope queue; zero picks the default.
	QueueSize int
}

// Link is a session stream attachment that re-dials with bounded backoff for
// as long as it is open. Both ends of the bridge use it: the traffic-agent
// attaches as the session's agent, krun-helper as its client.
type Link struct {
	cfg    Config
	sendCh chan contracts.StreamEnvelope

	onReceive    func(contracts.StreamEnvelope)
	onDisconnect func()

	ctx       context.Context
	cancel    context.CancelFunc
	doneCh    chan struct{}
	closeOnce sync.Once
}

// New builds a Link. Start must be called exactly once to run it.
func New(parent context.Context, cfg Config) *Link {
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	ctx, cancel := context.WithCancel(parent)
	return &Link{
		cfg:    cfg,
		sendCh: make(chan contracts.StreamEnvelope, queueSize),
		ctx:    ctx,
		cancel: cancel,
		doneCh: make(chan struct{}),
	}
}

// Start launches the dial/serve loop. It is separate from New so the caller
// can wire callbacks that reference the Link itself.
//
// onDisconnect fires after every lost connection: the manager drops all
// routing for a session when a peer detaches, so connections from the
// previous epoch are dead and must be closed rather than streamed into.
func (l *Link) Start(onReceive func(contracts.StreamEnvelope), onDisconnect func()) {
	l.onReceive = onReceive
	l.onDisconnect = onDisconnect
	go l.run()
}

// Send queues an envelope for the writer goroutine. It blocks while the queue
// is full, which applies backpressure to the caller until queue space becomes
// available or the link's context is cancelled.
func (l *Link) Send(envelope contracts.StreamEnvelope) error {
	select {
	case <-l.ctx.Done():
		return l.ctx.Err()
	case l.sendCh <- envelope:
		return nil
	}
}

// Envelope builds an outbound envelope carrying this link's session identity.
func (l *Link) Envelope(connectionID string, envelopeType string) contracts.StreamEnvelope {
	return contracts.StreamEnvelope{
		Type:         envelopeType,
		SessionID:    l.cfg.SessionID,
		ConnectionID: connectionID,
	}
}

// Close stops the loop and waits for it to exit.
func (l *Link) Close() {
	l.closeOnce.Do(func() {
		l.cancel()
		<-l.doneCh
	})
}

func (l *Link) run() {
	defer close(l.doneCh)

	backoff := initialBackoff
	for {
		if l.ctx.Err() != nil {
			return
		}

		dialCtx, cancel := context.WithTimeout(l.ctx, dialTimeout)
		conn, _, err := websocket.DefaultDialer.DialContext(dialCtx, l.cfg.URL, l.cfg.Headers)
		cancel()
		if err != nil {
			log.Printf("%s connect failed: %v", l.cfg.LogPrefix, err)
			if !sleepWithContext(l.ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		log.Printf("%s connected", l.cfg.LogPrefix)
		backoff = initialBackoff
		if err := Serve(l.ctx, conn, l.sendCh, l.onReceive); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("%s disconnected: %v", l.cfg.LogPrefix, err)
		}
		if l.onDisconnect != nil {
			l.onDisconnect()
		}
	}
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
	if next := current * 2; next < maxBackoff {
		return next
	}
	return maxBackoff
}
