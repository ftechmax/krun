//go:build windows

package main

import (
	"github.com/ftechmax/krun/internal/krun-helper/service"
	"golang.org/x/sys/windows/svc"
)

func shouldRunAsService(_ bool) bool {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return isService
}

func runAsService(listenAddress, kubeConfigPath string) error {
	return service.RunAsService(listenAddress, kubeConfigPath, startHelperDaemonForService)
}
