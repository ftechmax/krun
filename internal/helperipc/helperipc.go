// Package helperipc provides the local IPC transport between the krun CLI
// and the elevated krun-helper daemon: HTTP served over a unix domain
// socket on Linux and a named pipe on Windows. OS-level permissions on the
// endpoint (socket file mode / pipe DACL) replace the open localhost TCP
// port the helper used to listen on.
package helperipc

import (
	"context"
	"net"
	"net/http"
	"time"
)

// BaseURL is the dummy host used in request URLs; the transport dials the
// IPC endpoint regardless of the URL host.
const BaseURL = "http://krun-helper"

// NewClient returns an HTTP client that connects to the default helper
// endpoint. The timeout bounds the whole request, so callers pick it per
// operation (short for health probes, generous for enable/disable).
func NewClient(timeout time.Duration) *http.Client {
	return NewClientForEndpoint(DefaultEndpoint, timeout)
}

func NewClientForEndpoint(endpoint string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
				return dial(ctx, endpoint)
			},
		},
	}
}
