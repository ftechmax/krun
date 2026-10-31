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

// startElevatedProcess launches a long-lived privileged process. krun uses it
// once, for the hosts writer: the developer answers one prompt and the writer
// then stays up for the rest of the session.
//
// It must run from the CLI. sudo reads its password from the terminal, and the
// unprivileged helper daemon is detached without one.
func startElevatedProcess(binaryPath string, args []string) error {
	sudoPath, _ := exec.LookPath("sudo")
	pkexecPath, _ := exec.LookPath("pkexec")

	spec, err := buildLinuxLaunchSpec(binaryPath, args, os.Geteuid(), sudoPath, pkexecPath)
	if err != nil {
		return err
	}

	if spec.detached {
		return startBackgroundProcess(spec.path, spec.args)
	}
	return runForegroundCommand(spec.path, spec.args)
}

func buildLinuxLaunchSpec(binaryPath string, args []string, euid int, sudoPath string, pkexecPath string) (linuxLaunchSpec, error) {
	if euid == 0 {
		return linuxLaunchSpec{
			path:     binaryPath,
			args:     append([]string(nil), args...),
			detached: true,
		}, nil
	}

	// sudo authenticates in the foreground; the script then detaches the
	// daemon from the CLI terminal (no SIGHUP, no output into the shell).
	if sudoPath != "" {
		sudoArgs := []string{"--", "/bin/sh", "-c", pkexecDetachScript, "sh", binaryPath}
		sudoArgs = append(sudoArgs, args...)
		return linuxLaunchSpec{path: sudoPath, args: sudoArgs}, nil
	}

	if pkexecPath != "" {
		pkexecArgs := []string{"/bin/sh", "-c", pkexecDetachScript, "sh", binaryPath}
		pkexecArgs = append(pkexecArgs, args...)
		return linuxLaunchSpec{path: pkexecPath, args: pkexecArgs}, nil
	}

	return linuxLaunchSpec{}, fmt.Errorf("no supported Linux elevation command found; install sudo or pkexec, or run krun as root")
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

func runForegroundCommand(binaryPath string, args []string) error {
	cmd := exec.Command(binaryPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
