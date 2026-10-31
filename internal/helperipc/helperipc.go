// Package helperipc provides HTTP over local sockets or named pipes for the
// unprivileged helper and privileged hosts writer. Endpoint permissions
// restrict access to the local processes.
package helperipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Call sends one request over a local IPC endpoint and decodes its JSON
// reply. A 4xx/5xx is returned as an error carrying the daemon's own message,
// which is what the CLI shows the user.
func Call[T any](client *http.Client, method string, path string, payload any) (T, error) {
	var result T

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return result, err
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequest(method, BaseURL+path, body)
	if err != nil {
		return result, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()

	if response.StatusCode >= 400 {
		var helperResponse struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(response.Body).Decode(&helperResponse); err == nil && helperResponse.Message != "" {
			return result, errors.New(helperResponse.Message)
		}
		return result, fmt.Errorf("IPC request failed with status %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return result, fmt.Errorf("invalid IPC response: %w", err)
	}
	return result, nil
}
