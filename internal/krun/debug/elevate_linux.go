//go:build linux

package debug

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// startElevatedProcess launches a long-lived privileged process. krun uses it
// once, for the hosts writer: the developer answers one prompt and the writer
// then stays up for the rest of the session.
//
// It must run from the CLI. sudo reads its password from the terminal, and the
// unprivileged helper daemon is detached without one.
func startElevatedProcess(binaryPath string, args []string) error {
	sudoPath, err := exec.LookPath("sudo")
	if err != nil {
		return fmt.Errorf("sudo is required on Linux to start the hosts writer; install sudo: %w", err)
	}

	// sudo authenticates in the foreground, then backgrounds the hosts writer.
	cmd := exec.Command(sudoPath, append([]string{"-b", "--", binaryPath}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// startBackgroundProcess launches a process as the current user, detached so
// it outlives the CLI. Used for the unprivileged helper daemon, which needs no
// elevation at all.
func startBackgroundProcess(binaryPath string, args []string) error {
	cmd := exec.Command(binaryPath, args...)

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
