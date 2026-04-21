package contracts

type HostsEntry struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
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
	Path                string                          `json:"path,omitempty"`
	ServiceName         string                          `json:"service_name"`
	Namespace           string                          `json:"namespace,omitempty"`
	ContainerPort       int                             `json:"container_port"`
	InterceptPort       int                             `json:"intercept_port"`
	ServiceDependencies []DebugServiceDependencyContext `json:"service_dependencies,omitempty"`
}

// type DebugSessionUser struct {
// 	UID string `json:"uid,omitempty"`
// 	GID string `json:"gid,omitempty"`
// }

type StreamLogEvent struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

type StreamDoneEvent struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}
