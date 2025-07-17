package utils

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	colorReset   = "\033[0m"
	colorRed     = "\033[31m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorMagenta = "\033[35m"
	colorCyan    = "\033[36m"
	colorGray    = "\033[37m"
	colorWhite   = "\033[97m"
)

type Color int

const (
	ColorNone Color = iota
	Red
	Green
	Yellow
	Blue
	Magenta
	Cyan
	Gray
)

func Colorize(text string, color Color) string {
	switch color {
	case Red:
		return colorRed + text + colorReset
	case Green:
		return colorGreen + text + colorReset
	case Yellow:
		return colorYellow + text + colorReset
	case Blue:
		return colorBlue + text + colorReset
	case Magenta:
		return colorMagenta + text + colorReset
	case Cyan:
		return colorCyan + text + colorReset
	case Gray:
		return colorGray + text + colorReset
	default:
		return text
	}
}

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
	var stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = log.Writer()
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Printf("command failed\n%v", Colorize(stderr.String(), Red))
		return err
	}

	return nil
}

func RunCmdStdin(stdin string, name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = log.Writer()
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Printf("command failed\n%v", Colorize(stderr.String(), Red))
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