package debug

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

var (
	shell32          = syscall.NewLazyDLL("shell32.dll")
	procShellExecute = shell32.NewProc("ShellExecuteW")
)

// runElevatedWithInput runs a command elevated and waits for it.
//
// ShellExecuteW "runas" cannot pipe stdin to the elevated child, so the
// payload goes through a temporary file that the child is pointed at. The file
// is created with the user's own permissions and removed afterwards; it holds
// hostnames, not secrets.
func runElevatedWithInput(binaryPath string, args []string, input []byte) error {
	if isElevated() {
		var stderr bytes.Buffer
		cmd := exec.Command(binaryPath, args...)
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

	payloadFile, err := os.CreateTemp("", "krun-hosts-*.json")
	if err != nil {
		return fmt.Errorf("create hosts payload file: %w", err)
	}
	payloadPath := payloadFile.Name()
	defer os.Remove(payloadPath)
	if _, err := payloadFile.Write(input); err != nil {
		payloadFile.Close()
		return fmt.Errorf("write hosts payload file: %w", err)
	}
	if err := payloadFile.Close(); err != nil {
		return fmt.Errorf("close hosts payload file: %w", err)
	}

	elevatedArgs := append(append([]string(nil), args...), "--input", payloadPath)
	return shellExecuteRunAsAndWait(binaryPath, elevatedArgs)
}

// startBackgroundProcess launches the helper daemon as the current user, with
// no elevation: it needs none.
func startBackgroundProcess(binaryPath string, args []string) error {
	cmd := exec.Command(binaryPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func isElevated() bool {
	var token syscall.Token
	process, err := syscall.GetCurrentProcess()
	if err != nil {
		return false
	}
	if err := syscall.OpenProcessToken(process, syscall.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()

	var elevated uint32
	var size uint32
	// TokenElevation = 20
	if err := syscall.GetTokenInformation(token, 20, (*byte)(unsafe.Pointer(&elevated)), uint32(unsafe.Sizeof(elevated)), &size); err != nil {
		return false
	}
	return elevated != 0
}

func shellExecuteRunAsAndWait(binaryPath string, args []string) error {
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(binaryPath)
	params, _ := syscall.UTF16PtrFromString(strings.Join(quoteArgs(args), " "))

	ret, _, _ := procShellExecute.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		0,
		0, // SW_HIDE
	)
	if ret <= 32 {
		return fmt.Errorf("ShellExecute failed with code %d", ret)
	}
	return nil
}

func quoteArgs(args []string) []string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.ContainsAny(arg, ` "`) {
			quoted = append(quoted, `"`+strings.ReplaceAll(arg, `"`, `\"`)+`"`)
			continue
		}
		quoted = append(quoted, arg)
	}
	return quoted
}
