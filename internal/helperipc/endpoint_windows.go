//go:build windows

package helperipc

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// DefaultEndpoint is the named pipe the helper listens on. The helper runs as
// the invoking user, so the pipe's default security descriptor already admits
// the krun CLI; no custom DACL is needed.
func DefaultEndpoint() string { return `\\.\pipe\krun-helper` }

func dial(ctx context.Context, endpoint string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, endpoint)
}

func Listen(endpoint string) (net.Listener, error) {
	return winio.ListenPipe(endpoint, &winio.PipeConfig{})
}

// Cleanup is a no-op: named pipes disappear with their last handle.
func Cleanup(_ string) {}
