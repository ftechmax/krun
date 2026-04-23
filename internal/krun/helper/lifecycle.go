package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/utils"
)

var BaseURL = "http://127.0.0.1:47831"

func EnsureStarted(config cfg.Config) error {
	return ensureHelperStarted(config)
}

func HelperStatus(config cfg.Config) {
	running, installed := helperServiceStatus()
	if !installed {
		printStatusValue("service:", "not installed", utils.Red)
		fmt.Printf("kubeconfig: %s\n", config.KubeConfig)
		return
	}

	if running {
		printStatusValue("service:", "installed", utils.Green)
	} else {
		printStatusValue("service:", "installed", utils.Yellow)
	}

	if err := helperCheckHealth(); err != nil {
		printStatusValue("health:", fmt.Sprintf("unreachable (%v)", err), utils.Red)
	} else {
		printStatusValue("health:", "ok", utils.Green)
	}
	fmt.Printf("kubeconfig: %s\n", config.KubeConfig)
}

func HelperStop() {
	if err := helperCheckHealth(); err != nil {
		fmt.Println(utils.Colorize("helper is not running", utils.Yellow))
		return
	}

	response, err := helperShutdown()
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("failed to stop helper: %v", err), utils.Red))
		return
	}
	if !response.Success {
		fmt.Println(utils.Colorize(fmt.Sprintf("helper refused shutdown: %s", response.Message), utils.Red))
		return
	}

	fmt.Println(utils.Colorize("helper is shutting down", utils.Green))
}

func HelperInstall(config cfg.Config) {
	absKubeConfig, err := filepath.Abs(config.KubeConfig)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot resolve kubeconfig path: %v", err), utils.Red))
		return
	}

	if needsElevationForHelperService() {
		if err := rerunHelperServiceCommandElevated("install", absKubeConfig); err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("failed to install helper service: %v", err), utils.Red))
			return
		}
	} else {
		if err := HelperInstallService(config); err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("failed to install helper service: %v", err), utils.Red))
			return
		}
	}

	fmt.Println("Waiting for helper service to become ready...")
	if err := waitForHelperHealth(); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("helper service installed but did not become ready: %v", err), utils.Yellow))
		return
	}

	fmt.Println(utils.Colorize("helper service installed", utils.Green))
}

// HelperInstallService installs the platform service. The caller must already
// be running with elevated privileges. Returns an error so the hidden
// __helper-install command can signal failure with a non-zero exit code.
func HelperInstallService(config cfg.Config) error {
	binaryPath, err := resolveHelperBinaryPath()
	if err != nil {
		return fmt.Errorf("cannot find krun-helper binary: %w", err)
	}

	absKubeConfig, err := filepath.Abs(config.KubeConfig)
	if err != nil {
		return fmt.Errorf("cannot resolve kubeconfig path: %w", err)
	}

	return installHelperService(binaryPath, absKubeConfig, resolveInstallerHomeDir())
}

func HelperUninstall() {
	if !isHelperServiceInstalled() {
		fmt.Println(utils.Colorize("helper service is not installed", utils.Yellow))
		return
	}

	if needsElevationForHelperService() {
		if err := rerunHelperServiceCommandElevated("uninstall", ""); err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("failed to uninstall helper service: %v", err), utils.Red))
			return
		}
	} else {
		if err := HelperUninstallService(); err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("failed to uninstall helper service: %v", err), utils.Red))
			return
		}
	}
	fmt.Println(utils.Colorize("helper service uninstalled", utils.Green))
}

// HelperUninstallService removes the platform service. The caller must already
// be running with elevated privileges.
func HelperUninstallService() error {
	return uninstallHelperService()
}

func needsElevationForHelperService() bool {
	return !isProcessElevated()
}

func rerunHelperServiceCommandElevated(action string, kubeConfigPath string) error {
	krunBinaryPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve krun executable: %w", err)
	}

	args := []string{"__helper-" + action}
	if trimmedKubeConfig := strings.TrimSpace(kubeConfigPath); trimmedKubeConfig != "" {
		args = append(args, "--kubeconfig", trimmedKubeConfig)
	}

	return runElevatedCommand(krunBinaryPath, args)
}

func ensureHelperStarted(config cfg.Config) error {
	// 1. Already running?
	if err := helperCheckHealth(); err == nil {
		return nil
	}

	// 2. Service installed?
	running, installed := helperServiceStatus()
	if installed {
		if running {
			// Service claims to be running but health check failed.
			return fmt.Errorf("helper service is running but not healthy on %s", BaseURL)
		}
		if err := startHelperService(); err != nil {
			return fmt.Errorf("failed to start helper service: %w", err)
		}
		return waitForHelperHealth()
	}

	// 3. No service — fall back to ad-hoc elevation.
	if err := startHelperProcess(config); err != nil {
		// Handle races where another process started the helper concurrently.
		if checkErr := helperCheckHealth(); checkErr == nil {
			return nil
		}
		return err
	}

	return waitForHelperHealth()
}

func waitForHelperHealth() error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := helperCheckHealth(); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("helper failed to become healthy on %s", BaseURL)
}

func startHelperProcess(config cfg.Config) error {
	helperBinaryPath, err := resolveHelperBinaryPath()
	if err != nil {
		return err
	}

	args := make([]string, 0, 2)
	if trimmedKubeConfig := strings.TrimSpace(config.KubeConfig); trimmedKubeConfig != "" {
		args = append(args, "--kubeconfig", trimmedKubeConfig)
	}

	if err := startElevatedProcess(helperBinaryPath, args); err != nil {
		return fmt.Errorf("failed to start helper: %w", err)
	}
	return nil
}

func resolveHelperBinaryPath() (string, error) {
	helperBinaryName := "krun-helper"
	if runtime.GOOS == "windows" {
		helperBinaryName = "krun-helper.exe"
	}

	krunBinaryPath, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(krunBinaryPath), helperBinaryName)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}

	workingDirectoryPath, err := os.Getwd()
	if err == nil {
		candidate := filepath.Join(workingDirectoryPath, helperBinaryName)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}

	helperBinaryPath, err := exec.LookPath(helperBinaryName)
	if err == nil {
		return helperBinaryPath, nil
	}

	return "", fmt.Errorf("%s not found next to krun executable, in current directory, or in PATH", helperBinaryName)
}

func helperCheckHealth() error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(BaseURL + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func helperShutdown() (contracts.HelperResponse, error) {
	request, err := http.NewRequest(http.MethodPost, BaseURL+"/v1/shutdown", nil)
	if err != nil {
		return contracts.HelperResponse{}, err
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return contracts.HelperResponse{}, err
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return contracts.HelperResponse{}, err
	}
	if response.StatusCode >= http.StatusBadRequest {
		return contracts.HelperResponse{}, parseHelperError(response.StatusCode, responseBody)
	}

	var helperResponse contracts.HelperResponse
	if len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, &helperResponse); err != nil {
			return contracts.HelperResponse{}, fmt.Errorf("invalid helper response: %w", err)
		}
	}

	return helperResponse, nil
}

func parseHelperError(statusCode int, body []byte) error {
	var helperResponse contracts.HelperResponse
	if len(body) > 0 {
		if err := json.Unmarshal(body, &helperResponse); err == nil {
			if message := strings.TrimSpace(helperResponse.Message); message != "" {
				return errors.New(message)
			}
		}
	}
	return fmt.Errorf("helper request failed with status %d", statusCode)
}

func printStatusValue(label string, value string, color utils.Color) {
	fmt.Printf("%s %s\n", label, utils.Colorize(value, color))
}

func resolveInstallerHomeDir() string {
	if home := strings.TrimSpace(os.Getenv("KRUN_HOME")); home != "" {
		return home
	}
	if home := lookupHomeByUsername(strings.TrimSpace(os.Getenv("SUDO_USER"))); home != "" {
		return home
	}
	if home := lookupHomeByUID(strings.TrimSpace(os.Getenv("PKEXEC_UID"))); home != "" {
		return home
	}
	if home := lookupHomeByUID(strings.TrimSpace(os.Getenv("SUDO_UID"))); home != "" {
		return home
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(home)
}

func lookupHomeByUsername(username string) string {
	if username == "" {
		return ""
	}
	record, err := user.Lookup(username)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(record.HomeDir)
}

func lookupHomeByUID(uid string) string {
	if uid == "" {
		return ""
	}
	record, err := user.LookupId(uid)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(record.HomeDir)
}
