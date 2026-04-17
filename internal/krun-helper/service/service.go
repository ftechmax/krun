package service

// DaemonOptions controls lifecycle hooks for the helper daemon when running
// under a service manager.
type DaemonOptions struct {
	ExternalShutdown <-chan struct{} // service stop signal (nil = not used)
	OnReady          func()         // called when HTTP listener is bound
}

// StartDaemonFunc is the signature that main.startHelperDaemon exposes so the
// service runner can call it without importing the main package.
type StartDaemonFunc func(listenAddress, kubeConfigPath string, opts DaemonOptions) error
