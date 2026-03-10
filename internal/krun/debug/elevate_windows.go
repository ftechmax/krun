package debug

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

var (
	shell32          = syscall.NewLazyDLL("shell32.dll")
	procShellExecute = shell32.NewProc("ShellExecuteW")
)

// startElevatedProcess launches the given binary with UAC elevation using ShellExecuteW "runas".
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
