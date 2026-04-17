package contracts

import cfg "github.com/ftechmax/krun/internal/config"

// krun-helper contracts
type DebugSession struct {
	SessionID       string `json:"session_id"`
	SessionToken    string `json:"session_token,omitempty"`
	Namespace       string `json:"namespace"`
	ServiceName     string `json:"service_name"`
	Workload        string `json:"workload"`
	ServicePort     int    `json:"service_port"`
	LocalPort       int    `json:"local_port"`
	ClientID        string `json:"client_id"`
	CreatedAt       string `json:"created_at"`
	ClientConnected bool   `json:"client_connected,omitempty"`
}

type CreateDebugSessionRequest struct {
	Namespace   string `json:"namespace,omitempty"`
	ServiceName string `json:"service_name"`
	Workload    string `json:"workload,omitempty"`
	ServicePort int    `json:"service_port"`
	LocalPort   int    `json:"local_port"`
	ClientID    string `json:"client_id,omitempty"`
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

type DebugServiceDependencyContext struct {
	Host      string   `json:"host"`
	Namespace string   `json:"namespace,omitempty"`
	Service   string   `json:"service,omitempty"`
	Port      int      `json:"port"`
	Aliases   []string `json:"aliases,omitempty"`
}

type DebugServiceContext struct {
	Project             string                          `json:"project,omitempty"`
	ServiceName         string                          `json:"service_name"`
	Namespace           string                          `json:"namespace,omitempty"`
	ContainerPort       int                             `json:"container_port"`
	InterceptPort       int                             `json:"intercept_port"`
	ServiceDependencies []DebugServiceDependencyContext `json:"service_dependencies,omitempty"`
}

type DebugSessionCommandRequest struct {
	SessionKey string              `json:"session_key,omitempty"`
	Context    DebugServiceContext `json:"context"`
}

type HelperDebugSession struct {
	SessionKey string              `json:"session_key"`
	Context    DebugServiceContext `json:"context"`
}

type HelperDebugSessionsResponse struct {
	Sessions []HelperDebugSession `json:"sessions"`
}

type HelperResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type ServiceListResponse struct {
	Services []cfg.Service `json:"services"`
	Projects []string      `json:"projects"`
}

type BuildRequest struct {
	Target  string `json:"target"`
	SkipWeb bool   `json:"skip_web,omitempty"`
	Force   bool   `json:"force,omitempty"`
	Flush   bool   `json:"flush,omitempty"`
}

type DeployRequest struct {
	Target            string `json:"target"`
	UseRemoteRegistry bool   `json:"use_remote_registry,omitempty"`
	NoRestart         bool   `json:"no_restart,omitempty"`
}

type DeleteRequest struct {
	Target string `json:"target"`
}

type StreamLogEvent struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

type StreamDoneEvent struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

const (
	StreamTypeOpen  = "open"
	StreamTypeData  = "data"
	StreamTypeClose = "close"
	StreamTypeError = "error"
	StreamTypePing  = "ping"
)

const (
	StreamRoleAgent  = "agent"
	StreamRoleClient = "client"
)

type StreamEnvelope struct {
	Type         string            `json:"type"`
	SessionID    string            `json:"session_id"`
	SessionToken string            `json:"session_token,omitempty"`
	ConnectionID string            `json:"connection_id,omitempty"`
	Data         []byte            `json:"data,omitempty"`
	Message      string            `json:"message,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}
