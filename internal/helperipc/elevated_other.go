//go:build !linux && !windows

package helperipc

import (
	"fmt"
	"net"
	"runtime"
)

const HostsEndpoint = ""

func ListenElevated(_ string) (net.Listener, error) {
	return nil, fmt.Errorf("privileged hosts writer is not supported on %s", runtime.GOOS)
}
