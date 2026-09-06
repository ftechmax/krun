//go:build !windows && !linux

package debug

import (
	"fmt"
	"runtime"
)

func runElevatedWithInput(_ string, _ []string, _ []byte) error {
	return fmt.Errorf("elevated hosts-file update is not supported on %s", runtime.GOOS)
}

func startBackgroundProcess(_ string, _ []string) error {
	return fmt.Errorf("helper launch is not supported on %s", runtime.GOOS)
}
