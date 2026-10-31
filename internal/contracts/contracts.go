package contracts

import "github.com/ftechmax/krun/internal/config"

type DebugSession struct {
	SessionID    string `json:"session_id"`
	SessionToken string `json:"session_token,omitempty"`
	Namespace    string `json:"namespace"`
	ServiceName  string `json:"service_name"`
	ServicePort  int    `json:"service_port"`
}

type CreateDebugSessionRequest struct {
	Namespace   string `json:"namespace,omitempty"`
	ServiceName string `json:"service_name"`
	ServicePort int    `json:"service_port"`
}

type ListDebugSessionsResponse struct {
	Sessions []DebugSession `json:"sessions"`
}

type HostsEntry struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
}

type PortForward struct {
	Namespace  string `json:"namespace,omitempty"`
	Service    string `json:"service"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
}

type HelperDebugSession struct {
	SessionKey string         `json:"session_key"`
	Context    config.Service `json:"context"`
	State      string         `json:"state,omitempty"`
}

const AgentContainerName = "krun-traffic-agent"

const (
	HelperSessionStateActive         = "active"
	HelperSessionStateCleanupPending = "cleanup-pending"
)

type HelperDebugSessionsResponse struct {
	Sessions []HelperDebugSession `json:"sessions"`
}

type HelperResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	// Version is the helper's build version, reported on /healthz so the CLI
	// can detect a helper started by a different krun build.
	Version string `json:"version,omitempty"`
}

const (
	StreamTypeOpen = "open"
	StreamTypeData = "data"
	// StreamTypeCloseWrite half-closes a connection: the sender saw EOF on
	// its read side but the reverse direction may still carry data. The
	// relay routes it like data; only close/error tear the connection down.
	StreamTypeCloseWrite = "close-write"
	StreamTypeClose      = "close"
	StreamTypeError      = "error"
)

const (
	StreamRoleAgent  = "agent"
	StreamRoleClient = "client"
)

type StreamEnvelope struct {
	Type         string `json:"type"`
	ConnectionID string `json:"connection_id,omitempty"`
	Data         []byte `json:"data,omitempty"`
	Message      string `json:"message,omitempty"`
}
