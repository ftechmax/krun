package contracts

type ListResponse struct {
	Services []string `json:"services"`
	Projects []string `json:"projects"`
}

type HelperResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type HelperDebugSession struct {
	SessionKey string              `json:"session_key"`
	Context    DebugServiceContext `json:"context"`
}

type BuildRequest struct {
	KubeConfig string `json:"kube_config,omitempty"`
	Target     string `json:"target"`
	SkipWeb    bool   `json:"skip_web,omitempty"`
	Force      bool   `json:"force,omitempty"`
	Flush      bool   `json:"flush,omitempty"`
}

type DeployRequest struct {
	KubeConfig        string `json:"kube_config,omitempty"`
	Target            string `json:"target"`
	UseRemoteRegistry bool   `json:"use_remote_registry,omitempty"`
	NoRestart         bool   `json:"no_restart,omitempty"`
}

type DeleteRequest struct {
	KubeConfig string `json:"kube_config,omitempty"`
	Target     string `json:"target"`
}

type DebugEnableRequest struct {
	KubeConfig    string              `json:"kube_config,omitempty"`
	Context       DebugServiceContext `json:"context"`
	ContainerName string              `json:"container_name,omitempty"`
}

type DebugDisableRequest struct {
	KubeConfig string              `json:"kube_config,omitempty"`
	Context    DebugServiceContext `json:"context"`
}
