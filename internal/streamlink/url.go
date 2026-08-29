package streamlink

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Stream endpoints on the traffic-manager, one per peer role.
const (
	AgentPath  = "/v1/stream/agent"
	ClientPath = "/v1/stream/client"
)

// URL turns a manager address into a websocket stream URL for one session.
// The address may omit the scheme (http is assumed) and may carry a base path,
// which the stream path is appended to. Authentication belongs in request
// headers and is never placed in the URL.
func URL(managerAddress string, path string, sessionID string) (string, error) {
	rawAddress := strings.TrimSpace(managerAddress)
	if rawAddress == "" {
		return "", errors.New("manager address is required")
	}
	if !strings.Contains(rawAddress, "://") {
		rawAddress = "http://" + rawAddress
	}

	parsed, err := url.Parse(rawAddress)
	if err != nil {
		return "", fmt.Errorf("invalid manager address %q: %w", managerAddress, err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid manager address %q: host is required", managerAddress)
	}

	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported manager address scheme %q", parsed.Scheme)
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	query := parsed.Query()
	query.Set("session_id", strings.TrimSpace(sessionID))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
