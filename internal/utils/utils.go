package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[37m"
)

type Color int

const (
	Red Color = iota + 1
	Green
	Yellow
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
		binaryName := "krun"
		if runtime.GOOS == "windows" {
			binaryName = "krun.exe"
		}
		exePath = filepath.Join(cwd, binaryName)
	}
	return exePath, nil
}
