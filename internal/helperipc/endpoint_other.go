//go:build !linux && !windows

package helperipc

import (
	"context"
	"fmt"
	"net"
	"runtime"
)

const DefaultEndpoint = ""

func dial(_ context.Context, _ string) (net.Conn, error) {
	return nil, fmt.Errorf("helper IPC is not supported on %s", runtime.GOOS)
}

func Listen(_ string) (net.Listener, error) {
	return nil, fmt.Errorf("helper IPC is not supported on %s", runtime.GOOS)
}

func Cleanup(_ string) {}
