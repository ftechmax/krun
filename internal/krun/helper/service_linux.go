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
ExecStart=%s --kubeconfig %s%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`

func installHelperService(binaryPath, kubeConfigPath, ownerName string) error {
	// If already installed, stop and remove first for a clean update.
	if HelperServiceInstalled() {
		if err := runSystemctl("stop", unitName); err != nil {
			fmt.Printf("warning: failed to stop existing helper service: %v\n", err)
		}
	}

	content := buildSystemdUnit(binaryPath, kubeConfigPath, ownerName)
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

func buildSystemdUnit(binaryPath, kubeConfigPath, ownerName string) string {
	ownerArg := ""
	if trimmedOwnerName := strings.TrimSpace(ownerName); trimmedOwnerName != "" {
		ownerArg = fmt.Sprintf(" --owner %s", trimmedOwnerName)
	}
	return fmt.Sprintf(unitTemplate, binaryPath, kubeConfigPath, ownerArg)
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

func HelperServiceInstalled() bool {
	_, err := os.Stat(unitPath)
	return err == nil
}

func startHelperService() error {
	return runSystemctl("start", unitName)
}

func helperServiceStatus() (running bool, installed bool) {
	installed = HelperServiceInstalled()
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
