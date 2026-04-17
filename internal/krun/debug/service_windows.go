//go:build windows

package debug

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "krun-helper"

func installHelperService(binaryPath, kubeConfigPath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()

	// If already installed, stop and remove for a clean update.
	if existing, openErr := m.OpenService(serviceName); openErr == nil {
		_ = stopWindowsService(existing)
		_ = existing.Delete()
		existing.Close()
	}

	s, err := m.CreateService(serviceName, binaryPath, mgr.Config{
		DisplayName: "krun Debug Helper",
		StartType:   mgr.StartAutomatic,
	}, "--kubeconfig", kubeConfigPath)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	// Set up event log source.
	_ = eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info)

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

func uninstallHelperService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()

	_ = stopWindowsService(s)

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	_ = eventlog.Remove(serviceName)
	return nil
}

func isHelperServiceInstalled() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

func startHelperService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()

	return s.Start()
}

func helperServiceStatus() (running bool, installed bool) {
	m, err := mgr.Connect()
	if err != nil {
		return false, false
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return false, false
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return false, true
	}
	return status.State == svc.Running, true
}

func stopWindowsService(s *mgr.Service) error {
	status, err := s.Control(svc.Stop)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for status.State != svc.Stopped && time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			return err
		}
	}
	return nil
}
