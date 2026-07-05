//go:build windows

package helperipc

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

const DefaultEndpoint = `\\.\pipe\krun-helper`

// The helper runs UAC-elevated, and an elevated process's default pipe DACL
// does not admit the non-elevated interactive user running the krun CLI.
// Grant SYSTEM and Administrators full access, interactive users
// read/write.
const pipeSecurityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"

func dial(ctx context.Context, endpoint string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, endpoint)
}

func Listen(endpoint string) (net.Listener, error) {
	return winio.ListenPipe(endpoint, &winio.PipeConfig{
		SecurityDescriptor: pipeSecurityDescriptor,
	})
}

// Cleanup is a no-op: named pipes disappear with their last handle.
func Cleanup(_ string) {}
