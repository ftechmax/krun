//go:build windows

package service

import (
	"fmt"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

const serviceName = "krun-helper"

type helperService struct {
	listenAddress  string
	kubeConfigPath string
	startDaemon    StartDaemonFunc
}

func (s *helperService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	shutdownCh := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		errCh <- s.startDaemon(s.listenAddress, s.kubeConfigPath, DaemonOptions{
			ExternalShutdown: shutdownCh,
			OnReady: func() {
				changes <- svc.Status{
					State:   svc.Running,
					Accepts: svc.AcceptStop | svc.AcceptShutdown,
				}
			},
		})
	}()

	for {
		select {
		case err := <-errCh:
			if err != nil {
				elog, logErr := eventlog.Open(serviceName)
				if logErr == nil {
					_ = elog.Error(1, fmt.Sprintf("daemon error: %v", err))
					elog.Close()
				}
				changes <- svc.Status{State: svc.StopPending}
				return false, 1
			}
			changes <- svc.Status{State: svc.StopPending}
			return false, 0
		case cr := <-r:
			switch cr.Cmd {
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				close(shutdownCh)
				<-errCh // wait for daemon to finish
				return false, 0
			case svc.Interrogate:
				changes <- cr.CurrentStatus
			}
		}
	}
}

// ShouldRunAsService reports whether the helper was launched by the Windows
// Service Control Manager. The serviceFlag argument is ignored on Windows:
// the SCM always sets the process up so that svc.IsWindowsService is true.
func ShouldRunAsService(_ bool) bool {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return isService
}

// RunAsService starts the helper daemon as a Windows service.
func RunAsService(listenAddress, kubeConfigPath string, startDaemon StartDaemonFunc) error {
	elog, err := eventlog.Open(serviceName)
	if err == nil {
		_ = elog.Info(1, "starting krun-helper service")
		elog.Close()
	}

	return svc.Run(serviceName, &helperService{
		listenAddress:  listenAddress,
		kubeConfigPath: kubeConfigPath,
		startDaemon:    startDaemon,
	})
}
