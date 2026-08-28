package streamlink

import (
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
)

const connReadBuffer = 32 * 1024

// PumpConn streams a bridged connection's bytes onto the link as data
// envelopes until EOF or error, applying the half-close protocol. It does not
// emit `open`: whichever side originates a connection sends that itself before
// handing the connection over.
func PumpConn(link *Link, conns *Conns, connectionID string, conn net.Conn) {
	buffer := make([]byte, connReadBuffer)
	for {
		readBytes, readErr := conn.Read(buffer)
		if readBytes > 0 {
			envelope := link.Envelope(connectionID, contracts.StreamTypeData)
			envelope.Data = append([]byte(nil), buffer[:readBytes]...)
			if err := link.Send(envelope); err != nil {
				conns.CloseAndDelete(connectionID)
				return
			}
		}

		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			// Half-close: this end finished sending, but the reverse
			// direction may still carry the response.
			_ = link.Send(link.Envelope(connectionID, contracts.StreamTypeCloseWrite))
			if conns.MarkReadClosed(connectionID) {
				_ = link.Send(link.Envelope(connectionID, contracts.StreamTypeClose))
			}
			return
		}
		if errors.Is(readErr, net.ErrClosed) {
			// Torn down locally (session stopping, or the peer's close
			// already removed it); the peer needs no further notice.
			return
		}

		SendErrorClose(link, connectionID, readErr)
		conns.CloseAndDelete(connectionID)
		return
	}
}

// ApplyInbound handles the envelope types both ends of the bridge share. It
// reports whether the envelope was consumed, so a caller can layer on the
// types specific to its side (`open` and `ping` on the helper).
func ApplyInbound(link *Link, conns *Conns, envelope contracts.StreamEnvelope) bool {
	connectionID := strings.TrimSpace(envelope.ConnectionID)
	if connectionID == "" {
		return false
	}

	switch envelope.Type {
	case contracts.StreamTypeData:
		if len(envelope.Data) == 0 {
			return true
		}
		conn := conns.Get(connectionID)
		if conn == nil {
			return true
		}
		// ApplyInbound is synchronous, so bound how long one blocked connection
		// can delay the other connections sharing this stream.
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := conn.Write(envelope.Data); err != nil {
			conns.CloseAndDelete(connectionID)
			SendErrorClose(link, connectionID, err)
		}
		return true
	case contracts.StreamTypeCloseWrite:
		if conns.CloseWriteHalf(connectionID) {
			_ = link.Send(link.Envelope(connectionID, contracts.StreamTypeClose))
		}
		return true
	case contracts.StreamTypeClose, contracts.StreamTypeError:
		conns.CloseAndDelete(connectionID)
		return true
	}
	return false
}

// SendErrorClose reports a per-connection failure to the peer and tears the
// connection down: an error envelope carrying the cause, then a close.
func SendErrorClose(link *Link, connectionID string, cause error) {
	envelope := link.Envelope(connectionID, contracts.StreamTypeError)
	envelope.Message = cause.Error()
	_ = link.Send(envelope)
	_ = link.Send(link.Envelope(connectionID, contracts.StreamTypeClose))
}
