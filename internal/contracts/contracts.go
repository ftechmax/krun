package contracts

// krun-helper contracts
type DebugSession struct {
	SessionID       string `json:"session_id"`
	SessionToken    string `json:"session_token,omitempty"`
	Namespace       string `json:"namespace"`
	ServiceName     string `json:"service_name"`
	Workload        string `json:"workload"`
	TargetContainer string `json:"target_container,omitempty"`
	ServicePort     int    `json:"service_port"`
	LocalPort       int    `json:"local_port"`
	ClientID        string `json:"client_id"`
	CreatedAt       string `json:"created_at"`
	ClientConnected bool   `json:"client_connected,omitempty"`
}

type CreateDebugSessionRequest struct {
	Namespace       string `json:"namespace,omitempty"`
	ServiceName     string `json:"service_name"`
	Workload        string `json:"workload,omitempty"`
	TargetContainer string `json:"target_container,omitempty"`
	ServicePort     int    `json:"service_port"`
	LocalPort       int    `json:"local_port"`
	ClientID        string `json:"client_id,omitempty"`
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
	Host      string `json:"host"`
	Namespace string `json:"namespace,omitempty"`
	Service   string `json:"service,omitempty"`
	Port      int    `json:"port"`
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
