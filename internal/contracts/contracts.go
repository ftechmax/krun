package contracts

type PipeCommand struct {
	Command     string `json:"command"`
	KubeConfig  string `json:"kubeconfig"`
	ServiceName string `json:"service_name,omitempty"`
	ServicePath string `json:"service_path,omitempty"`
	ServicePort int    `json:"service_port,omitempty"`
}

type PipeResponse struct {
	Message string `json:"message"`
}