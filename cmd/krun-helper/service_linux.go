//go:build linux

package main

import "github.com/ftechmax/krun/internal/krun-helper/service"

func shouldRunAsService(serviceFlag bool) bool {
	return serviceFlag
}

func runAsService(listenAddress, kubeConfigPath string) error {
	return service.RunAsService(listenAddress, kubeConfigPath, startHelperDaemonForService)
}
