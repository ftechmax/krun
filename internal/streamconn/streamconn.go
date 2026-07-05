// Package streamconn tracks the TCP connections bridged over a debug session
// stream, including half-close state for each direction. A connection is
// fully torn down once both its read side (local EOF observed) and write
// side (peer sent close-write) are closed, so protocols where one end
// half-closes and then waits for a response keep working.
package streamconn

import (
	"net"
	"strings"
	"sync"
)

type Registry struct {
	mu    sync.Mutex
	conns map[string]*entry
}

type entry struct {
	conn        net.Conn
	readClosed  bool
	writeClosed bool
}

func NewRegistry() *Registry {
	return &Registry{
		conns: map[string]*entry{},
	}
}

// Set registers a connection, closing any previous one under the same id.
func (r *Registry) Set(connectionID string, conn net.Conn) {
	r.mu.Lock()
	previous, ok := r.conns[connectionID]
	r.conns[connectionID] = &entry{conn: conn}
	r.mu.Unlock()
	if ok {
		_ = previous.conn.Close()
	}
}

func (r *Registry) Get(connectionID string) net.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.conns[connectionID]; ok {
		return e.conn
	}
	return nil
}

// MarkReadClosed records that the local read side hit EOF. It reports true
// when the write side was already closed, in which case the connection has
// been removed and closed and the caller should emit a final close envelope.
func (r *Registry) MarkReadClosed(connectionID string) bool {
	r.mu.Lock()
	e, ok := r.conns[connectionID]
	if !ok {
		r.mu.Unlock()
		return false
	}
	e.readClosed = true
	if !e.writeClosed {
		r.mu.Unlock()
		return false
	}
	delete(r.conns, connectionID)
	r.mu.Unlock()
	_ = e.conn.Close()
	return true
}

// CloseWriteHalf half-closes the write side after the peer sent close-write.
// It reports true when the read side was already closed, in which case the
// connection has been removed and closed and the caller should emit a final
// close envelope.
func (r *Registry) CloseWriteHalf(connectionID string) bool {
	r.mu.Lock()
	e, ok := r.conns[connectionID]
	if !ok {
		r.mu.Unlock()
		return false
	}
	e.writeClosed = true
	if e.readClosed {
		delete(r.conns, connectionID)
		r.mu.Unlock()
		_ = e.conn.Close()
		return true
	}
	conn := e.conn
	r.mu.Unlock()

	if closeWriter, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closeWriter.CloseWrite()
	}
	return false
}

func (r *Registry) CloseAndDelete(connectionID string) {
	if strings.TrimSpace(connectionID) == "" {
		return
	}
	r.mu.Lock()
	e, ok := r.conns[connectionID]
	if ok {
		delete(r.conns, connectionID)
	}
	r.mu.Unlock()
	if ok {
		_ = e.conn.Close()
	}
}

func (r *Registry) CloseAll() {
	r.mu.Lock()
	entries := make([]*entry, 0, len(r.conns))
	for _, e := range r.conns {
		entries = append(entries, e)
	}
	r.conns = map[string]*entry{}
	r.mu.Unlock()

	for _, e := range entries {
		_ = e.conn.Close()
	}
}
