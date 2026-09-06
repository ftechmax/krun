//go:build windows

package helperipc

import (
	"net"

	"github.com/Microsoft/go-winio"
)

// HostsEndpoint is the named pipe of the small privileged hosts writer.
const HostsEndpoint = `\\.\pipe\krun-hosts`

// An elevated process's default pipe DACL does not admit the non-elevated
// krun CLI, so grant SYSTEM and Administrators full access and interactive
// users read/write.
const hostsPipeSecurityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"

func ListenElevated(endpoint string) (net.Listener, error) {
	return winio.ListenPipe(endpoint, &winio.PipeConfig{
		SecurityDescriptor: hostsPipeSecurityDescriptor,
	})
}
