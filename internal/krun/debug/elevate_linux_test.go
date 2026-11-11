//go:build linux

package debug

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStartElevatedProcessUsesSudo(t *testing.T) {
	for _, exitCode := range []string{"0", "7"} {
		t.Run("sudo exit "+exitCode, func(t *testing.T) {
			dir := t.TempDir()
			capture := filepath.Join(dir, "args")
			stub := "#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$KRUN_TEST_SUDO_ARGS\"\nexit \"$KRUN_TEST_SUDO_EXIT\"\n"
			if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			t.Setenv("KRUN_TEST_SUDO_ARGS", capture)
			t.Setenv("KRUN_TEST_SUDO_EXIT", exitCode)

			err := startElevatedProcess("/tmp/krun helper;literal", []string{"hosts-daemon", "--socket", "/tmp/hosts socket;literal"})
			if exitCode == "0" {
				if err != nil {
					t.Fatalf("start hosts writer: %v", err)
				}
			} else {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
					t.Fatalf("expected sudo exit status 7, got %v", err)
				}
			}

			data, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
			want := []string{"-b", "--", "/tmp/krun helper;literal", "hosts-daemon", "--socket", "/tmp/hosts socket;literal"}
			if !slices.Equal(got, want) {
				t.Fatalf("sudo arguments = %q, want %q", got, want)
			}
		})
	}
}

func TestStartElevatedProcessRequiresSudo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := startElevatedProcess("/tmp/krun-helper", []string{"hosts-daemon"})
	if err == nil || !strings.Contains(err.Error(), "install sudo") {
		t.Fatalf("expected an instruction to install sudo, got %v", err)
	}
}
