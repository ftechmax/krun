package streamlink

import (
	"fmt"
	"net/url"
	"strings"
)

// Stream endpoints on the traffic-manager, one per peer role.
const (
	AgentPath  = "/v1/stream/agent"
	ClientPath = "/v1/stream/client"
)

// URL builds a websocket endpoint from the manager's HTTP address.
// Authentication stays in request headers.
func URL(managerAddress string, path string, sessionID string) (string, error) {
	if managerAddress != "" && !strings.Contains(managerAddress, "://") {
		managerAddress = "http://" + managerAddress
	}
	parsed, err := url.Parse(managerAddress)
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

	parsed.Path = path
	parsed.RawQuery = url.Values{"session_id": {sessionID}}.Encode()
	return parsed.String(), nil
}
