//go:build !windows

package managerauth

import (
	"fmt"
	"os"
)

func secureDirectory(path string) error {
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure manager token directory %s: %w", path, err)
	}
	return nil
}

func validateTokenPermissions(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect manager token %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("manager token %s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("manager token %s permissions are %04o; want 0600", path, info.Mode().Perm())
	}
	return nil
}
