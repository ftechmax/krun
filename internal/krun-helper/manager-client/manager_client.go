// Package managerclient talks to the in-cluster traffic-manager's session
// REST API. Requests go over the same port-forward the session stream uses, so
// there is one path to the manager and one thing to diagnose when it breaks.
package managerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
)

const (
	ManagerNamespace   = "krun-system"
	ManagerServiceName = "krun-traffic-manager"
	ManagerServicePort = 8080
	ManagerClientID    = "krun-helper"

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

func NewSessionClient(baseURL string, authToken string) (*Client, error) {
	trimmedBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmedBaseURL == "" {
		return nil, errors.New("manager base URL is required")
	}
	trimmedToken := strings.TrimSpace(authToken)
	if trimmedToken == "" {
		return nil, errors.New("manager auth token is required")
	}
	return &Client{
		baseURL:   trimmedBaseURL,
		authToken: trimmedToken,
		http:      &http.Client{Timeout: requestTimeout},
	}, nil
}

func (c *Client) CreateSession(serviceContext contracts.DebugServiceContext) (contracts.DebugSession, error) {
	serviceName := strings.TrimSpace(serviceContext.ServiceName)
	if serviceName == "" {
		return contracts.DebugSession{}, errors.New("create manager session: service_name is required")
	}
	if serviceContext.ContainerPort <= 0 {
		return contracts.DebugSession{}, errors.New("create manager session: service_port must be greater than 0")
	}
	if serviceContext.InterceptPort <= 0 {
		return contracts.DebugSession{}, errors.New("create manager session: local_port must be greater than 0")
	}

	request := contracts.CreateDebugSessionRequest{
		Namespace:   NormalizeNamespace(serviceContext.Namespace),
		ServiceName: serviceName,
		Workload:    serviceName,
		ServicePort: serviceContext.ContainerPort,
		LocalPort:   serviceContext.InterceptPort,
		ClientID:    ManagerClientID,
	}

	session, err := do[contracts.DebugSession](c, http.MethodPost, "/v1/sessions", request)
	if err != nil {
		return contracts.DebugSession{}, fmt.Errorf("create manager session: %w", err)
	}
	if strings.TrimSpace(session.SessionID) == "" {
		return contracts.DebugSession{}, errors.New("create manager session: empty session id")
	}
	return session, nil
}

func (c *Client) DeleteSession(sessionID string) error {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return nil
	}

	_, err := do[struct{}](c, http.MethodDelete, "/v1/sessions/"+trimmedSessionID, nil)
	// A session the manager no longer knows about is already in the state we
	// are asking for.
	if err == nil || errors.Is(err, errNotFound) {
		return nil
	}
	return fmt.Errorf("delete manager session %q: %w", trimmedSessionID, err)
}

// Health reports the manager's build version. It deliberately skips the
// shared auth token: /healthz is intentionally unauthenticated.
func (c *Client) Health() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return "", err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("read health response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manager health responded %d: %s", response.StatusCode, managerErrorMessage(body))
	}

	var health struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		return "", fmt.Errorf("decode health response: %w", err)
	}
	if strings.TrimSpace(health.Version) == "" {
		return "", errors.New("manager reported no version")
	}
	return health.Version, nil
}

var errNotFound = errors.New("not found")

func do[T any](c *Client, method string, path string, payload any) (T, error) {
	var result T

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return result, fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return result, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	request.Header.Set(authTokenHeader, c.authToken)

	response, err := c.http.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return result, fmt.Errorf("read response: %w", err)
	}

	if response.StatusCode == http.StatusNotFound {
		return result, errNotFound
	}
	if response.StatusCode >= 400 {
		return result, fmt.Errorf("manager responded %d: %s", response.StatusCode, managerErrorMessage(responseBody))
	}

	if len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, &result); err != nil {
			return result, fmt.Errorf("decode response: %w", err)
		}
	}
	return result, nil
}

func managerErrorMessage(body []byte) string {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Message) != "" {
		return payload.Message
	}
	return strings.TrimSpace(string(body))
}

func NormalizeNamespace(namespace string) string {
	if trimmed := strings.TrimSpace(namespace); trimmed != "" {
		return trimmed
	}
	return "default"
}
