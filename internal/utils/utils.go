package utils

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func GetExecutablePath() (string, error) {
    exePath, err := os.Executable()
    if err != nil {
        return "", fmt.Errorf("failed to get executable path: %w", err)
    }

    // If running from a go-build temp directory, use the current working directory
    if strings.Contains(exePath, "go-build") {
        cwd, err := os.Getwd()
        if err != nil {
            return "", fmt.Errorf("failed to get current working directory: %w", err)
        }
        exePath = filepath.Join(cwd, "krun.exe")
    }
    return exePath, nil
}

func RunCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = log.Writer()
	cmd.Stderr = log.Writer()
	if err := cmd.Run(); err != nil {
		log.Printf("command failed: %v", err)
		return err
	}

	return nil
}

func RunCmdStdin(stdin string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = log.Writer()
	cmd.Stderr = log.Writer()
	if err := cmd.Run(); err != nil {
		log.Fatalf("command failed: %v", err)
		return err
	}

	return nil
}

func GetFreePort() (port int, err error) {
	var a *net.TCPAddr
	if a, err = net.ResolveTCPAddr("tcp", "localhost:0"); err == nil {
		var l *net.TCPListener
		if l, err = net.ListenTCP("tcp", a); err == nil {
			defer l.Close()
			return l.Addr().(*net.TCPAddr).Port, nil
		}
	}
	return
}