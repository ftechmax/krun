package wskeepalive

import (
	"time"

	"github.com/gorilla/websocket"
)

const (
	readTimeout  = 60 * time.Second
	pingPeriod   = 30 * time.Second
	writeTimeout = 10 * time.Second
)

// Configure arms the connection's read deadline and extends it whenever
// ping/pong control traffic proves the peer is alive. Without this, a
// half-open connection (e.g. through a dropped kubectl port-forward) is
// never detected and the stream silently stops relaying.
func Configure(conn *websocket.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(readTimeout))
	})
	defaultPingHandler := conn.PingHandler()
	conn.SetPingHandler(func(message string) error {
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		return defaultPingHandler(message)
	})
}

// Ping sends websocket ping control frames until done is closed or a write
// fails. WriteControl is safe to call concurrently with WriteJSON, so this
// runs alongside the connection's pump loop.
func Ping(conn *websocket.Conn, done <-chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				return
			}
		}
	}
}
