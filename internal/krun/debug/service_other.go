//go:build !linux && !windows

package debug

import (
	"fmt"
	"runtime"
)

func installHelperService(_, _ string) error {
	return fmt.Errorf("service install is not supported on %s", runtime.GOOS)
}

func uninstallHelperService() error {
	return fmt.Errorf("service uninstall is not supported on %s", runtime.GOOS)
}

func isHelperServiceInstalled() bool {
	return false
}

func startHelperService() error {
	return fmt.Errorf("service start is not supported on %s", runtime.GOOS)
}

func helperServiceStatus() (running bool, installed bool) {
	return false, false
}
