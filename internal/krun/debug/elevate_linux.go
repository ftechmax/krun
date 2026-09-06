//go:build linux

package debug

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// runElevatedWithInput runs a command as root in the foreground, feeding it
// stdin, and waits for it to finish.
//
// It is deliberately foreground: sudo reads its password from the terminal, so
// this only works from the CLI, which has one. stdin carries the payload
// rather than the password, so passing it here does not interfere with the
// prompt.
func runElevatedWithInput(binaryPath string, args []string, input []byte) error {
	name, argv, err := elevationCommand(binaryPath, args, os.Geteuid())
	if err != nil {
		return err
	}

	var stderr bytes.Buffer
	cmd := exec.Command(name, argv...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = os.Stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if message := bytes.TrimSpace(stderr.Bytes()); len(message) > 0 {
			return fmt.Errorf("%w: %s", err, message)
		}
		return err
	}
	return nil
}

// elevationCommand picks how to become root. pkexec is not used: it prompts
// through a polkit agent that is absent on headless machines, and unlike sudo
// it does not forward stdin to the target program.
func elevationCommand(binaryPath string, args []string, euid int) (string, []string, error) {
	if euid == 0 {
		return binaryPath, append([]string(nil), args...), nil
	}

	sudoPath, err := exec.LookPath("sudo")
	if err != nil {
		return "", nil, fmt.Errorf("sudo not found; run krun as root or install sudo")
	}
	return sudoPath, append([]string{"--", binaryPath}, args...), nil
}

// startBackgroundProcess launches the helper daemon as the current user. The
// daemon needs no privilege of its own; the one privileged step, writing the
// hosts file, is a separate elevated one-shot driven by the CLI.
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
