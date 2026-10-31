// Package managerclient talks to the in-cluster traffic-manager's session
// REST API. Requests go over the same port-forward the session stream uses, so
// there is one path to the manager and one thing to diagnose when it breaks.
package managerclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
)

const (
	ManagerNamespace   = "krun-system"
	ManagerServiceName = "krun-traffic-manager"
	ManagerServicePort = 8080

	requestTimeout = 10 * time.Second

	// The custom header avoids collision with Authorization handling by
	// Kubernetes and HTTP intermediaries.
	authTokenHeader = "X-Krun-Auth-Token"
)

// Client reaches the manager at baseURL (the local end of the port-forward)
// and authenticates with the install-time token supplied by the helper.
type Client struct {
	baseURL   string
	authToken string
	http      *http.Client
}

func NewSessionClient(baseURL string, authToken string) *Client {
	return &Client{
		baseURL:   baseURL,
		authToken: authToken,
		http:      &http.Client{Timeout: requestTimeout},
	}
}

func (c *Client) CreateSession(serviceContext config.Service) (contracts.DebugSession, error) {
	request := contracts.CreateDebugSessionRequest{
		Namespace:   serviceContext.Namespace,
		ServiceName: serviceContext.Name,
		ServicePort: serviceContext.ContainerPort,
	}

	return do[contracts.DebugSession](c, http.MethodPost, "/v1/sessions", request)
}

func (c *Client) DeleteSession(sessionID string) error {
	_, err := do[struct{}](c, http.MethodDelete, "/v1/sessions/"+sessionID, nil)
	return err
}

func (c *Client) Health() (string, error) {
	health, err := do[struct {
		Version string `json:"version"`
	}](c, http.MethodGet, "/healthz", nil)
	return health.Version, err
}

func do[T any](c *Client, method string, path string, payload any) (T, error) {
	var result T

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return result, fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return result, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	if path != "/healthz" {
		request.Header.Set(authTokenHeader, c.authToken)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()

	// A missing session is already deleted; successful deletes have no body.
	if response.StatusCode == http.StatusNoContent || (method == http.MethodDelete && response.StatusCode == http.StatusNotFound) {
		return result, nil
	}
	if response.StatusCode >= 400 {
		var failure struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
			return result, fmt.Errorf("manager responded %s", response.Status)
		}
		return result, fmt.Errorf("manager responded %d: %s", response.StatusCode, failure.Message)
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return result, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}
