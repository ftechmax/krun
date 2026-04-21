//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows/svc"
)

const windowsServiceName = "krun-helper"

type helperService struct {
	listenAddress  string
	krunConfigPath string
}

func (h *helperService) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	status <- svc.Status{State: svc.StartPending}

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		errCh <- startHelperServer(h.listenAddress, h.krunConfigPath, daemonOptions{
			externalShutdown: stopCh,
			onReady:          func() { close(readyCh) },
		})
	}()

	select {
	case <-readyCh:
		status <- svc.Status{State: svc.Running, Accepts: accepted}
	case err := <-errCh:
		fmt.Printf("krun-helper service start error: %v\n", err)
		return false, 1
	}

	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				close(stopCh)
				<-errCh
				status <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-errCh:
			if err != nil {
				fmt.Printf("krun-helper server exited: %v\n", err)
			}
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func runAsService(listenAddress, krunConfigPath string) error {
	return svc.Run(windowsServiceName, &helperService{listenAddress: listenAddress, krunConfigPath: krunConfigPath})
}
