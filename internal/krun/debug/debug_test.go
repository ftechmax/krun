package debug

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveHelperBinaryPathFallsBackToWorkingDirectory(t *testing.T) {
	helperName := "krun-helper"
	if runtime.GOOS == "windows" {
		helperName = "krun-helper.exe"
	}

	tempDirectory := t.TempDir()
	helperPath := filepath.Join(tempDirectory, helperName)
	if err := os.WriteFile(helperPath, []byte("helper"), 0o700); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(tempDirectory); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalDirectory)
	})

	resolvedPath, err := resolveHelperBinaryPath()
	if err != nil {
		t.Fatalf("resolve helper binary path: %v", err)
	}
	if resolvedPath != helperPath {
		t.Fatalf("expected %q, got %q", helperPath, resolvedPath)
	}
}
