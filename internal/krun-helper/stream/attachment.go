// Package stream attaches a debug session to the traffic-manager's relay and
// serves the developer's local app to it: every connection the traffic-agent
// intercepts in the cluster is opened against 127.0.0.1:<intercept_port> here
// and proxied byte for byte.
package stream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/streamlink"
)

const localDialTimeout = 2 * time.Second

// Attachment is one session's live stream to the manager. It re-dials on its
// own for as long as it is open; Close tears it and every bridged connection
// down.
type Attachment struct {
	sessionID     string
	interceptAddr string
	link          *streamlink.Link
	conns         *streamlink.Conns
}

// Attach opens the session stream. It returns as soon as the attachment is
// running; connecting to the manager happens in the background with retries,
// so a manager that is briefly unreachable does not fail debug enable.
func Attach(managerAddress string, sessionID string, sessionToken string, interceptPort int) (*Attachment, error) {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return nil, errors.New("stream session id is required")
	}
	if interceptPort < 1 || interceptPort > 65535 {
		return nil, fmt.Errorf("invalid intercept port %d", interceptPort)
	}
	trimmedSessionToken := strings.TrimSpace(sessionToken)
	if trimmedSessionToken == "" {
		return nil, errors.New("stream session token is required")
	}

	streamURL, err := streamlink.URL(managerAddress, streamlink.ClientPath, trimmedSessionID)
	if err != nil {
		return nil, err
	}

	attachment := &Attachment{
		sessionID:     trimmedSessionID,
		interceptAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(interceptPort)),
		conns:         streamlink.NewConns(),
	}
	// Authenticate once during the websocket handshake. Keeping the token out
	// of the URL and data envelopes prevents it from reaching access logs.
	attachment.link = streamlink.New(context.Background(), streamlink.Config{
		URL:       streamURL,
		Headers:   streamHeaders(trimmedSessionToken),
		SessionID: trimmedSessionID,
		LogPrefix: fmt.Sprintf("helper stream (session_id=%s)", trimmedSessionID),
	})
	// The manager drops all connection routing for this session when the
	// client stream detaches, so any local conns from this epoch are dead.
	attachment.link.Start(attachment.handleInbound, attachment.conns.CloseAll)

	return attachment, nil
}

func streamHeaders(sessionToken string) http.Header {
	headers := make(http.Header)
	headers.Set("X-Krun-Session-Token", strings.TrimSpace(sessionToken))
	return headers
}

func (a *Attachment) Close() {
	a.link.Close()
	a.conns.CloseAll()
}

func (a *Attachment) handleInbound(envelope contracts.StreamEnvelope) {
	if streamlink.ApplyInbound(a.link, a.conns, envelope) {
		return
	}

	if envelope.Type == contracts.StreamTypeOpen {
		a.handleOpen(strings.TrimSpace(envelope.ConnectionID))
	}
}

// handleOpen answers an intercepted connection by dialling the local app. A
// refused dial (app not started, or listening elsewhere) is reported per
// connection rather than tearing down the session.
func (a *Attachment) handleOpen(connectionID string) {
	if connectionID == "" {
		return
	}

	localConn, err := net.DialTimeout("tcp", a.interceptAddr, localDialTimeout)
	if err != nil {
		streamlink.SendErrorClose(a.link, connectionID, err)
		return
	}

	a.conns.Set(connectionID, localConn)
	go streamlink.PumpConn(a.link, a.conns, connectionID, localConn)
}
