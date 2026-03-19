package debug

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	deploy "github.com/ftechmax/krun/internal/krun/deploy"
	"github.com/ftechmax/krun/internal/kube"
	"github.com/ftechmax/krun/internal/utils"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const helperBaseURL = "http://127.0.0.1:47831"

func List(config cfg.Config) {
	if err := ensureHelperStarted(config); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("helper unreachable: %v", err), utils.Red))
		return
	}

	sessions, err := helperListSessions()
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot list debug sessions via helper: %v", err), utils.Red))
		return
	}

	if len(sessions) == 0 {
		fmt.Println(utils.Colorize("No active debug sessions", utils.Yellow))
		return
	}

	fmt.Println("Active debug sessions")
	fmt.Println("---------------------")
	for _, session := range sessions {
		serviceName := strings.TrimSpace(session.Context.ServiceName)
		namespace := strings.TrimSpace(session.Context.Namespace)
		if namespace == "" {
			namespace = "default"
		}

		fmt.Printf("Service: %s (namespace: %s)\n", serviceName, namespace)
		fmt.Printf("Intercept port: %d\n", session.Context.InterceptPort)
		fmt.Println("Service dependencies:")

		if len(session.Context.ServiceDependencies) == 0 {
			fmt.Println("  (none)")
		} else {
			for _, dependency := range session.Context.ServiceDependencies {
				host := strings.TrimSpace(dependency.Host)
				if host == "" {
					service := strings.TrimSpace(dependency.Service)
					dependencyNamespace := strings.TrimSpace(dependency.Namespace)
					if dependencyNamespace == "" {
						dependencyNamespace = "default"
					}
					if service == "" {
						host = "(unknown)"
					} else {
						host = fmt.Sprintf("%s.%s.svc", service, dependencyNamespace)
					}
				}
				fmt.Printf("  - %s:%d\n", host, dependency.Port)
			}
		}
		fmt.Println("")
	}
}

func Enable(service cfg.Service, config cfg.Config, containerName string) {
	if err := ensureHelperStarted(config); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot start helper: %v", err), utils.Red))
		return
	}

	request := contracts.DebugSessionCommandRequest{
		Context: buildDebugServiceContext(service),
	}
	response, err := helperRequest(http.MethodPost, "/v1/debug/enable", request)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot apply debug enable via helper: %v", err), utils.Red))
		return
	}
	if !response.Success {
		fmt.Println(utils.Colorize(fmt.Sprintf("helper refused debug enable: %s", response.Message), utils.Red))
		return
	}

	fmt.Println(utils.Colorize("Session created", utils.Green))

	if err := deploy.CreateEnvFile(service, config, containerName); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("warning: could not create env file: %v", err), utils.Yellow))
	}
}

func Disable(service cfg.Service, config cfg.Config) {
	if err := ensureHelperStarted(config); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot start helper: %v", err), utils.Red))
		return
	}

	response, err := helperRequest(http.MethodPost, "/v1/debug/disable", contracts.DebugSessionCommandRequest{
		Context: buildDebugServiceContext(service),
	})
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot apply debug disable via helper: %v", err), utils.Red))
		return
	}
	if !response.Success {
		fmt.Println(utils.Colorize(fmt.Sprintf("helper refused debug disable: %s", response.Message), utils.Red))
		return
	}

	if response.Message == "no active session" {
		fmt.Println(utils.Colorize("No active debug session found", utils.Yellow))
	} else {
		fmt.Println(utils.Colorize("Session removed", utils.Green))
		if err := deploy.RemoveEnvFile(service, config); err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("warning: could not remove env file: %v", err), utils.Yellow))
		}
	}
}

func HelperStatus(config cfg.Config) {
	if err := ensureHelperStarted(config); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot start helper: %v", err), utils.Red))
		return
	}

	if err := helperCheckHealth(); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("helper unreachable: %v", err), utils.Red))
		return
	}
	fmt.Println(utils.Colorize("helper is running", utils.Green))
	fmt.Printf("kubeconfig: %s\n", config.KubeConfig)
}

func HelperStop() {
	if err := helperCheckHealth(); err != nil {
		fmt.Println(utils.Colorize("helper is not running", utils.Yellow))
		return
	}

	response, err := helperRequest(http.MethodPost, "/v1/shutdown", nil)
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

func RuntimeInstall(config cfg.Config, version string) {
	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to create Kubernetes client: %s", err), utils.Red))
		return
	}

	objs, err := loadManifestObjects(version)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to load manifests: %s", err), utils.Red))
		return
	}

	if err := deploy.ApplyObjects(context.Background(), client, objs); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to apply manifests: %s", err), utils.Red))
		return
	}

	fmt.Println("Waiting for traffic manager to become ready...")

	timeout := 2 * time.Minute
	interval := 2 * time.Second
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		dep, err := client.Clientset.AppsV1().Deployments("krun-system").Get(context.Background(), "krun-traffic-manager", metav1.GetOptions{})
		if err == nil {
			desired := int32(1)
			if dep.Spec.Replicas != nil {
				desired = *dep.Spec.Replicas
			}
			if dep.Status.ReadyReplicas >= desired {
				fmt.Println(utils.Colorize("Traffic manager installed successfully", utils.Green))
				return
			}
		}
		time.Sleep(interval)
	}

	fmt.Println(utils.Colorize("Traffic manager was applied but did not become ready", utils.Yellow))
}

func RuntimeStatus(config cfg.Config) {
	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to create Kubernetes client: %s", err), utils.Red))
		return
	}

	dep, err := client.Clientset.AppsV1().Deployments("krun-system").Get(context.Background(), "krun-traffic-manager", metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			fmt.Println(utils.Colorize("Traffic manager is not installed", utils.Yellow))
			return
		}
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to get deployment: %s", err), utils.Red))
		return
	}

	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	ready := dep.Status.ReadyReplicas

	version := "unknown"
	if len(dep.Spec.Template.Spec.Containers) > 0 {
		image := dep.Spec.Template.Spec.Containers[0].Image
		if i := strings.LastIndex(image, ":"); i != -1 {
			version = image[i+1:]
		}
	}

	if ready == desired {
		fmt.Println(utils.Colorize("Traffic manager: healthy", utils.Green))
	} else {
		fmt.Println(utils.Colorize(fmt.Sprintf("Traffic manager: not ready (%d/%d pods ready)", ready, desired), utils.Yellow))
	}

	fmt.Printf("Version: %s\n", version)
}

func RuntimeUninstall(config cfg.Config, version string) {
	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to create Kubernetes client: %s", err), utils.Red))
		return
	}

	objs, err := loadManifestObjects(version)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to load manifests: %s", err), utils.Red))
		return
	}

	if err := deploy.DeleteObjects(context.Background(), client, objs); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to delete manifests: %s", err), utils.Red))
		return
	}

	fmt.Println(utils.Colorize("Traffic manager uninstalled successfully", utils.Green))
}

func loadManifestObjects(version string) ([]*unstructured.Unstructured, error) {
	if version == "debug" && os.Getenv("KRUN_MANIFEST_URL") == "" {
		return deploy.RenderKustomizeObjects("deploy/runtime/overlays/local")
	}
	return fetchRemoteManifestObjects(version)
}

func fetchRemoteManifestObjects(version string) ([]*unstructured.Unstructured, error) {
	url := fmt.Sprintf("https://github.com/ftechmax/krun/releases/download/%s/krun-traffic-manager.yaml", version)
	if override := os.Getenv("KRUN_MANIFEST_URL"); override != "" {
		url = override
	}
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch manifest from %s: status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read manifest response: %w", err)
	}

	return deploy.DecodeManifestObjects(body)
}

func ensureHelperStarted(config cfg.Config) error {
	if err := helperCheckHealth(); err == nil {
		return nil
	}

	if err := startHelperProcess(config); err != nil {
		// Handle races where another process started the helper concurrently.
		if checkErr := helperCheckHealth(); checkErr == nil {
			return nil
		}
		return err
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := helperCheckHealth(); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	return fmt.Errorf("helper failed to become healthy on %s", helperBaseURL)
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
	resp, err := client.Get(helperBaseURL + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func helperRequest(method string, path string, payload any) (contracts.HelperResponse, error) {
	var body io.Reader
	if payload != nil {
		marshaledBody, err := json.Marshal(payload)
		if err != nil {
			return contracts.HelperResponse{}, err
		}
		body = bytes.NewReader(marshaledBody)
	}

	request, err := http.NewRequest(method, helperBaseURL+path, body)
	if err != nil {
		return contracts.HelperResponse{}, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return contracts.HelperResponse{}, err
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return contracts.HelperResponse{}, err
	}

	var helperResponse contracts.HelperResponse
	if len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, &helperResponse); err != nil {
			return contracts.HelperResponse{}, fmt.Errorf("invalid helper response: %w", err)
		}
	}
	if response.StatusCode >= 400 {
		if helperResponse.Message != "" {
			return helperResponse, errors.New(helperResponse.Message)
		}
		return helperResponse, fmt.Errorf("helper request failed with status %d", response.StatusCode)
	}

	return helperResponse, nil
}

func helperListSessions() ([]contracts.HelperDebugSession, error) {
	request, err := http.NewRequest(http.MethodGet, helperBaseURL+"/v1/debug/sessions", nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	if response.StatusCode >= 400 {
		var helperResponse contracts.HelperResponse
		if len(responseBody) > 0 {
			_ = json.Unmarshal(responseBody, &helperResponse)
		}
		if helperResponse.Message != "" {
			return nil, errors.New(helperResponse.Message)
		}
		return nil, fmt.Errorf("helper request failed with status %d", response.StatusCode)
	}

	var listResponse contracts.HelperDebugSessionsResponse
	if len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, &listResponse); err != nil {
			return nil, fmt.Errorf("invalid helper response: %w", err)
		}
	}

	return listResponse.Sessions, nil
}

func buildDebugServiceContext(service cfg.Service) contracts.DebugServiceContext {
	dependencies := make([]contracts.DebugServiceDependencyContext, 0, len(service.ServiceDependencies))
	for _, dependency := range service.ServiceDependencies {
		dependencies = append(dependencies, contracts.DebugServiceDependencyContext{
			Host:      dependency.Host,
			Namespace: dependency.Namespace,
			Service:   dependency.Service,
			Port:      dependency.Port,
			Aliases:   dependency.Aliases,
		})
	}

	return contracts.DebugServiceContext{
		Project:             service.Project,
		ServiceName:         service.Name,
		Namespace:           service.Namespace,
		ContainerPort:       service.ContainerPort,
		InterceptPort:       service.InterceptPort,
		ServiceDependencies: dependencies,
	}
}
