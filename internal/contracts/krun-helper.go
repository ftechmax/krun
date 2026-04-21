package contracts

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

type PortForward struct {
	Namespace  string `json:"namespace,omitempty"`
	Service    string `json:"service"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
}
