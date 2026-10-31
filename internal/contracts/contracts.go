package contracts

type DebugSession struct {
	SessionID    string `json:"session_id"`
	SessionToken string `json:"session_token,omitempty"`
	Namespace    string `json:"namespace"`
	ServiceName  string `json:"service_name"`
	Workload     string `json:"workload"`
	ServicePort  int    `json:"service_port"`
	LocalPort    int    `json:"local_port"`
	ClientID     string `json:"client_id"`
	CreatedAt    string `json:"created_at"`
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

// ApplyHostsRequest is what the CLI sends the privileged hosts writer.
type ApplyHostsRequest struct {
	Entries  []HostsEntry  `json:"entries"`
	Revision HostsRevision `json:"revision"`
}

// HostsRevision orders the hosts snapshots of one helper instance. The CLI
// applies a snapshot after the helper has returned it, so two concurrent
// commands can reach the hosts writer out of order; the writer uses the
// revision to drop the older one.
type HostsRevision struct {
	// Epoch identifies the helper instance. A restarted helper starts a new
	// epoch, and its snapshots are never treated as older than the last one's.
	Epoch string `json:"epoch"`
	Seq   uint64 `json:"seq"`
}

// OlderThan reports whether r was taken before other by the same helper.
func (r HostsRevision) OlderThan(other HostsRevision) bool {
	return r.Epoch == other.Epoch && r.Seq < other.Seq
}

// HostsSnapshot is the hosts block for every stored session together with
// the revision it was taken at.
type HostsSnapshot struct {
	Entries  []HostsEntry
	Revision HostsRevision
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
	State      string              `json:"state,omitempty"`
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
	// HostsEntries is the hosts block the machine should now have, covering
	// every stored session. The helper computes it; the CLI applies it
	// through the privileged hosts daemon.
	HostsEntries []HostsEntry `json:"hosts_entries,omitempty"`
	// HostsRevision is the revision HostsEntries was taken at; the CLI passes
	// it on so the hosts writer can drop snapshots that arrive out of order.
	HostsRevision HostsRevision `json:"hosts_revision,omitzero"`
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
	Type         string            `json:"type"`
	SessionID    string            `json:"session_id"`
	ConnectionID string            `json:"connection_id,omitempty"`
	Data         []byte            `json:"data,omitempty"`
	Message      string            `json:"message,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}
