//go:build !windows

package main

func runAsService(listenAddress, krunConfigPath string) error {
	return startHelperServer(listenAddress, krunConfigPath, daemonOptions{
		externalShutdown: make(chan struct{}),
	})
}
