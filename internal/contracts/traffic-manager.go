package contracts

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
