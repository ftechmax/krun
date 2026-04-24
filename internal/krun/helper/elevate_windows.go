package helper

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const seeMaskNoCloseProcess = 0x00000040

var (
	shell32            = syscall.NewLazyDLL("shell32.dll")
	procShellExecute   = shell32.NewProc("ShellExecuteW")
	procShellExecuteEx = shell32.NewProc("ShellExecuteExW")
)

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstResult  uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     syscall.Handle
}

// startElevatedProcess launches the given binary with UAC elevation using ShellExecuteW "runas".
// It is fire-and-forget: the elevated process runs in the background.
func startElevatedProcess(binaryPath string, args []string) error {
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(binaryPath)
	params, _ := syscall.UTF16PtrFromString(joinWindowsCommandArgs(args))

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

// runElevatedCommand launches the given binary with UAC elevation and waits for
// it to exit. Returns an error if the UAC prompt is declined or the process
// exits with a non-zero code.
func runElevatedCommand(binaryPath string, args []string) error {
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(binaryPath)
	params, _ := syscall.UTF16PtrFromString(joinWindowsCommandArgs(args))

	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	ret, _, err := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return fmt.Errorf("ShellExecuteEx: %w", err)
	}
	defer syscall.CloseHandle(info.hProcess)

	event, err := syscall.WaitForSingleObject(info.hProcess, syscall.INFINITE)
	if event != 0 {
		return fmt.Errorf("waiting for elevated process: %w", err)
	}

	var exitCode uint32
	if err := syscall.GetExitCodeProcess(info.hProcess, &exitCode); err != nil {
		return fmt.Errorf("get exit code: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("elevated process exited with code %d", exitCode)
	}
	return nil
}

func joinWindowsCommandArgs(args []string) string {
	escaped := make([]string, 0, len(args))
	for _, arg := range args {
		escaped = append(escaped, syscall.EscapeArg(arg))
	}
	return strings.Join(escaped, " ")
}

func isProcessElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
