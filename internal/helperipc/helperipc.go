// Package helperipc provides HTTP over local sockets or named pipes for the
// unprivileged helper and privileged hosts writer. Endpoint permissions
// restrict access to the local processes.
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
	return newClientForEndpoint(DefaultEndpoint(), timeout)
}

func newClientForEndpoint(endpoint string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
				return dial(ctx, endpoint)
			},
		},
	}
}

// NewClientForHosts connects to the privileged hosts writer.
func NewClientForHosts(timeout time.Duration) *http.Client {
	return newClientForEndpoint(HostsEndpoint, timeout)
}
