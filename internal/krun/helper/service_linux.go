//go:build linux

package helper

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	unitName = "krun-helper"
	unitPath = "/etc/systemd/system/krun-helper.service"
)

var unitTemplate = `[Unit]
Description=krun debug helper daemon
After=network.target

[Service]
Type=notify
%s
ExecStart=%s --kubeconfig %s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`

func installHelperService(binaryPath, kubeConfigPath, userHome string) error {
	// If already installed, stop and remove first for a clean update.
	if isHelperServiceInstalled() {
		if err := runSystemctl("stop", unitName); err != nil {
			fmt.Printf("warning: failed to stop existing helper service: %v\n", err)
		}
	}

	environmentLine := ""
	if trimmedUserHome := strings.TrimSpace(userHome); trimmedUserHome != "" {
		escapedUserHome := strings.ReplaceAll(trimmedUserHome, "\\", "\\\\")
		escapedUserHome = strings.ReplaceAll(escapedUserHome, "\"", "\\\"")
		environmentLine = fmt.Sprintf("Environment=\"KRUN_HOME=%s\"\n", escapedUserHome)
	}

	content := fmt.Sprintf(unitTemplate, environmentLine, binaryPath, kubeConfigPath)
	if err := os.WriteFile(unitPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	if err := runSystemctl("daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if err := runSystemctl("enable", "--now", unitName); err != nil {
		return fmt.Errorf("enable service: %w", err)
	}
	return nil
}

func uninstallHelperService() error {
	if err := runSystemctl("stop", unitName); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	if err := runSystemctl("disable", unitName); err != nil {
		return fmt.Errorf("disable service: %w", err)
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w", err)
	}
	if err := runSystemctl("daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	return nil
}

func isHelperServiceInstalled() bool {
	_, err := os.Stat(unitPath)
	return err == nil
}

func startHelperService() error {
	return runSystemctl("start", unitName)
}

func helperServiceStatus() (running bool, installed bool) {
	installed = isHelperServiceInstalled()
	if !installed {
		return false, false
	}
	out, err := exec.Command("systemctl", "is-active", unitName).Output() //nolint:gosec // Fixed binary and subcommand.
	if err != nil {
		return false, true
	}
	return strings.TrimSpace(string(out)) == "active", true
}

func runSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...) //nolint:gosec // Fixed binary; caller controls only known systemctl args.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
