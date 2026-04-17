//go:build linux

package debug

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

const pkexecDetachScript = `trap "" HUP; "$@" </dev/null >/dev/null 2>&1 &`

type linuxLaunchSpec struct {
	path     string
	args     []string
	detached bool
}

func startElevatedProcess(binaryPath string, args []string) error {
	sudoPath, err := exec.LookPath("sudo")
	if err != nil {
		sudoPath = ""
	}
	pkexecPath, err := exec.LookPath("pkexec")
	if err != nil {
		pkexecPath = ""
	}

	spec, err := buildLinuxLaunchSpec(binaryPath, args, os.Geteuid(), sudoPath, pkexecPath)
	if err != nil {
		return err
	}

	if spec.detached {
		return startDetachedProcess(spec.path, spec.args)
	}

	return runForegroundCommand(spec.path, spec.args)
}

func runElevatedCommand(binaryPath string, args []string) error {
	sudoPath, err := exec.LookPath("sudo")
	if err != nil {
		sudoPath = ""
	}
	pkexecPath, err := exec.LookPath("pkexec")
	if err != nil {
		pkexecPath = ""
	}

	spec, err := buildLinuxRunSpec(binaryPath, args, os.Geteuid(), sudoPath, pkexecPath)
	if err != nil {
		return err
	}

	if spec.detached {
		return startDetachedProcess(spec.path, spec.args)
	}

	return runForegroundCommand(spec.path, spec.args)
}

func buildLinuxLaunchSpec(binaryPath string, args []string, euid int, sudoPath string, pkexecPath string) (linuxLaunchSpec, error) {
	return buildLinuxElevationSpec(binaryPath, args, euid, sudoPath, pkexecPath, true)
}

func buildLinuxRunSpec(binaryPath string, args []string, euid int, sudoPath string, pkexecPath string) (linuxLaunchSpec, error) {
	return buildLinuxElevationSpec(binaryPath, args, euid, sudoPath, pkexecPath, false)
}

func buildLinuxElevationSpec(binaryPath string, args []string, euid int, sudoPath string, pkexecPath string, detached bool) (linuxLaunchSpec, error) {
	if euid == 0 {
		return linuxLaunchSpec{
			path:     binaryPath,
			args:     append([]string(nil), args...),
			detached: detached,
		}, nil
	}

	if sudoPath != "" {
		sudoArgs := append([]string{"--", binaryPath}, args...)
		if detached {
			sudoArgs = append([]string{"-b", "--", binaryPath}, args...)
		}
		return linuxLaunchSpec{
			path: sudoPath,
			args: sudoArgs,
		}, nil
	}

	if pkexecPath != "" {
		if detached {
			pkexecArgs := []string{"/bin/sh", "-c", pkexecDetachScript, "sh", binaryPath}
			pkexecArgs = append(pkexecArgs, args...)
			return linuxLaunchSpec{
				path: pkexecPath,
				args: pkexecArgs,
			}, nil
		}

		pkexecArgs := append([]string{binaryPath}, args...)
		return linuxLaunchSpec{
			path: pkexecPath,
			args: pkexecArgs,
		}, nil
	}

	return linuxLaunchSpec{}, fmt.Errorf("no supported Linux elevation command found; install sudo or pkexec, or run krun as root")
}

func startDetachedProcess(binaryPath string, args []string) error {
	cmd := exec.Command(binaryPath, args...) //nolint:gosec // binaryPath and args are prepared by trusted launch-spec selection.

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

func runForegroundCommand(binaryPath string, args []string) error {
	cmd := exec.Command(binaryPath, args...) //nolint:gosec // binaryPath and args are prepared by trusted launch-spec selection.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func isProcessElevated() bool {
	return os.Geteuid() == 0
}
