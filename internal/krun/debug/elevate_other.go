//go:build !windows && !linux

package debug

import (
	"fmt"
	"runtime"
)

func startElevatedProcess(_ string, _ []string) error {
	return fmt.Errorf("elevated helper launch is not supported on %s", runtime.GOOS)
}
