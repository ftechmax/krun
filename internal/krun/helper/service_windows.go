//go:build windows

package helper

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "krun-helper"

// openServiceForRead opens the SCM and service with read access only so
// status checks work without administrator privileges.
func openServiceForRead() (scm windows.Handle, service windows.Handle, err error) {
	scm, err = windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, 0, err
	}

	name, err := windows.UTF16PtrFromString(serviceName)
	if err != nil {
		windows.CloseServiceHandle(scm)
		return 0, 0, err
	}

	service, err = windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		windows.CloseServiceHandle(scm)
		return 0, 0, err
	}
	return scm, service, nil
}

func installHelperService(binaryPath, kubeConfigPath, ownerName string) error {
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
	}, helperServiceArgs(kubeConfigPath, ownerName)...)
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

func HelperServiceInstalled() bool {
	scm, service, err := openServiceForRead()
	if err != nil {
		return false
	}
	windows.CloseServiceHandle(service)
	windows.CloseServiceHandle(scm)
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
	scm, service, err := openServiceForRead()
	if err != nil {
		return false, false
	}
	defer windows.CloseServiceHandle(scm)
	defer windows.CloseServiceHandle(service)

	var status windows.SERVICE_STATUS_PROCESS
	var needed uint32
	err = windows.QueryServiceStatusEx(
		service,
		windows.SC_STATUS_PROCESS_INFO,
		(*byte)(unsafe.Pointer(&status)),
		uint32(unsafe.Sizeof(status)),
		&needed,
	)
	if err != nil {
		return false, true
	}
	return svc.State(status.CurrentState) == svc.Running, true
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
