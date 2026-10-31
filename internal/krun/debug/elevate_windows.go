package debug

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// DETACHED_PROCESS is not exported by the syscall package.
const detachedProcess = 0x00000008

var (
	shell32          = syscall.NewLazyDLL("shell32.dll")
	procShellExecute = shell32.NewProc("ShellExecuteW")
)

// startElevatedProcess launches a long-lived privileged process through the
// UAC "runas" verb. krun uses it once, for the hosts writer, so the developer
// sees a single UAC prompt per session rather than one per debug command.
func startElevatedProcess(binaryPath string, args []string) error {
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(binaryPath)
	params, _ := syscall.UTF16PtrFromString(strings.Join(args, " "))

	ret, _, _ := procShellExecute.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		0,
		0, // SW_HIDE
	)

	// ShellExecuteW returns a value > 32 on success.
	if ret <= 32 {
		return fmt.Errorf("ShellExecute failed with code %d", ret)
	}
	return nil
}

// startBackgroundProcess launches a process as the current user, with no
// elevation. Used for the unprivileged helper daemon.
func startBackgroundProcess(binaryPath string, args []string) error {
	cmd := exec.Command(binaryPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		// Detach from the CLI console so closing it or pressing Ctrl+C there
		// does not take the helper down.
		CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
