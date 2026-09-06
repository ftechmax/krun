//go:build !windows && !linux

package debug

import (
	"fmt"
	"runtime"
)

func startElevatedProcess(_ string, _ []string) error {
	return fmt.Errorf("the privileged hosts writer is not supported on %s", runtime.GOOS)
}

func startBackgroundProcess(_ string, _ []string) error {
	return fmt.Errorf("helper launch is not supported on %s", runtime.GOOS)
}
