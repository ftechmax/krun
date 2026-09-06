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
	"github.com/ftechmax/krun/internal/helperipc"
	deploy "github.com/ftechmax/krun/internal/krun/deploy"
	"github.com/ftechmax/krun/internal/kube"
	"github.com/ftechmax/krun/internal/managerauth"
	"github.com/ftechmax/krun/internal/utils"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	// healthTimeout bounds the liveness probe; listTimeout the cheap reads.
	// commandTimeout covers enable/disable, whose helper-side work includes
	// a 10s ready-verify per dependency port-forward, a 10s manager session
	// call, and the sidecar injection — well beyond a generic 5s budget.
	healthTimeout       = 2 * time.Second
	listTimeout         = 5 * time.Second
	commandTimeout      = 60 * time.Second
	managerTokenFileEnv = "KRUN_MANAGER_TOKEN_FILE"
)

func List(config cfg.Config) {
	if err := ensureHelperStarted(config); err != nil {
		utils.Errf("helper unreachable: %v", err)
		return
	}

	sessions, err := helperListSessions()
	if err != nil {
		utils.Errf("cannot list debug sessions via helper: %v", err)
		return
	}

	if len(sessions) == 0 {
		utils.Warnf("No active debug sessions")
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
		if session.State == contracts.HelperSessionStateCleanupPending {
			utils.Warnf("State: cleanup pending")
		}
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
		utils.Errf("cannot start helper: %v", err)
		return
	}

	request := contracts.DebugSessionCommandRequest{
		Context: buildDebugServiceContext(service),
	}
	response, err := helperCall[contracts.HelperResponse](http.MethodPost, "/v1/debug/enable", request, commandTimeout)
	if err != nil {
		utils.Errf("cannot apply debug enable via helper: %v", err)
		return
	}
	if !response.Success {
		utils.Errf("helper refused debug enable: %s", response.Message)
		return
	}

	utils.Okf("Session created")

	if err := deploy.CreateEnvFile(service, config, containerName); err != nil {
		utils.Warnf("warning: could not create env file: %v", err)
	}
}

func Disable(service cfg.Service, config cfg.Config) {
	if err := ensureHelperStarted(config); err != nil {
		utils.Errf("cannot start helper: %v", err)
		return
	}

	response, err := helperCall[contracts.HelperResponse](http.MethodPost, "/v1/debug/disable", contracts.DebugSessionCommandRequest{
		Context: buildDebugServiceContext(service),
	}, commandTimeout)
	if err != nil {
		utils.Errf("cannot apply debug disable via helper: %v", err)
		return
	}
	if !response.Success {
		utils.Errf("helper refused debug disable: %s", response.Message)
		return
	}

	if response.Message == "no active session" {
		utils.Warnf("No active debug session found")
	} else {
		utils.Okf("Session removed")
		if err := deploy.RemoveEnvFile(service, config); err != nil {
			utils.Warnf("warning: could not remove env file: %v", err)
		}
	}
}

// HelperStatus reports the helper's state without side effects: unlike the
// other commands it never starts the helper.
func HelperStatus(config cfg.Config) {
	helperVersion, err := helperCheckHealth()
	if err != nil {
		utils.Warnf("helper is not running")
		fmt.Printf("endpoint: %s\n", helperipc.DefaultEndpoint())
		return
	}

	utils.Okf("helper is running")
	fmt.Printf("endpoint: %s\n", helperipc.DefaultEndpoint())
	fmt.Printf("kubeconfig: %s\n", config.KubeConfig)
	if strings.TrimSpace(helperVersion) != "" {
		fmt.Printf("helper version: %s\n", helperVersion)
	}
	warnOnHelperSkew(config.Version, helperVersion)

	sessions, err := helperListSessions()
	if err != nil {
		utils.Warnf("cannot list debug sessions: %v", err)
		return
	}
	if len(sessions) == 0 {
		fmt.Println("Active debug sessions: none")
		return
	}
	fmt.Println("Active debug sessions:")
	for _, session := range sessions {
		suffix := ""
		if session.State == contracts.HelperSessionStateCleanupPending {
			suffix = " [cleanup pending]"
		}
		fmt.Printf("  - %s (intercept port %d)%s\n", session.Context.ServiceName, session.Context.InterceptPort, suffix)
	}
}

func HelperStop() {
	if _, err := helperCheckHealth(); err != nil {
		utils.Warnf("helper is not running")
		return
	}

	response, err := helperCall[contracts.HelperResponse](http.MethodPost, "/v1/shutdown", nil, listTimeout)
	if err != nil {
		utils.Errf("failed to stop helper: %v", err)
		return
	}
	if !response.Success {
		utils.Errf("helper refused shutdown: %s", response.Message)
		return
	}

	utils.Okf("helper is shutting down")
}

func RuntimeInstall(config cfg.Config, version string) {
	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		utils.Errf("Failed to create Kubernetes client: %s", err)
		return
	}

	objs, err := loadManifestObjects(version)
	if err != nil {
		utils.Errf("Failed to load manifests: %s", err)
		return
	}

	managerTokenPath, err := resolveManagerTokenPath()
	if err != nil {
		utils.Errf("Failed to resolve manager token path: %s", err)
		return
	}
	managerToken, err := managerauth.EnsureLocalToken(managerTokenPath)
	if err != nil {
		utils.Errf("Failed to initialize manager token: %s", err)
		return
	}
	if err := configureManagerAuth(objs, managerauth.Digest(managerToken)); err != nil {
		utils.Errf("Failed to configure manager authentication: %s", err)
		return
	}

	if err := deploy.ApplyObjects(context.Background(), client, objs); err != nil {
		utils.Errf("Failed to apply manifests: %s", err)
		return
	}

	fmt.Println("Waiting for traffic manager to become ready...")

	err = wait.PollUntilContextTimeout(context.Background(), 2*time.Second, 2*time.Minute, false,
		func(ctx context.Context) (bool, error) {
			dep, err := client.Clientset.AppsV1().Deployments("krun-system").Get(ctx, "krun-traffic-manager", metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			desired := int32(1)
			if dep.Spec.Replicas != nil {
				desired = *dep.Spec.Replicas
			}
			return dep.Status.ObservedGeneration >= dep.Generation &&
				dep.Status.UpdatedReplicas >= desired &&
				dep.Status.ReadyReplicas >= desired &&
				dep.Status.AvailableReplicas >= desired, nil
		})
	if err != nil {
		utils.Warnf("Traffic manager was applied but did not become ready")
		return
	}

	utils.Okf("Traffic manager installed successfully")
	fmt.Printf("Manager token: %s\n", managerTokenPath)
}

func RuntimeStatus(config cfg.Config) {
	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		utils.Errf("Failed to create Kubernetes client: %s", err)
		return
	}

	dep, err := client.Clientset.AppsV1().Deployments("krun-system").Get(context.Background(), "krun-traffic-manager", metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			utils.Warnf("Traffic manager is not installed")
			return
		}
		utils.Errf("Failed to get deployment: %s", err)
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
		utils.Okf("Traffic manager: healthy")
	} else {
		utils.Warnf("Traffic manager: not ready (%d/%d pods ready)", ready, desired)
	}

	fmt.Printf("Version: %s\n", version)
}

func RuntimeUninstall(config cfg.Config, version string) {
	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		utils.Errf("Failed to create Kubernetes client: %s", err)
		return
	}

	objs, err := loadManifestObjects(version)
	if err != nil {
		utils.Errf("Failed to load manifests: %s", err)
		return
	}

	if err := deploy.DeleteObjects(context.Background(), client, objs); err != nil {
		utils.Errf("Failed to delete manifests: %s", err)
		return
	}

	utils.Okf("Traffic manager uninstalled successfully")
	if managerTokenPath, pathErr := resolveManagerTokenPath(); pathErr == nil {
		if _, statErr := os.Stat(managerTokenPath); statErr == nil {
			fmt.Printf("Local manager token retained at %s\n", managerTokenPath)
		} else if os.IsNotExist(statErr) {
			fmt.Println("No local manager token existed to retain")
		}
	}
}

func resolveManagerTokenPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(managerTokenFileEnv)); override != "" {
		return filepath.Abs(override)
	}
	return managerauth.DefaultTokenPath()
}

func configureManagerAuth(objects []*unstructured.Unstructured, digest string) error {
	if _, err := managerauth.ParseDigest(digest); err != nil {
		return err
	}

	for _, object := range objects {
		if object.GetAPIVersion() != "apps/v1" || object.GetKind() != "Deployment" ||
			object.GetNamespace() != "krun-system" || object.GetName() != "krun-traffic-manager" {
			continue
		}

		containers, found, err := unstructured.NestedSlice(object.Object, "spec", "template", "spec", "containers")
		if err != nil || !found {
			return errors.New("traffic-manager Deployment has no containers")
		}
		for index, item := range containers {
			container, ok := item.(map[string]any)
			if !ok || container["name"] != "manager" {
				continue
			}

			env, _, err := unstructured.NestedSlice(container, "env")
			if err != nil {
				return fmt.Errorf("read traffic-manager environment: %w", err)
			}
			filtered := make([]any, 0, len(env)+1)
			for _, value := range env {
				entry, ok := value.(map[string]any)
				if ok && entry["name"] == managerauth.TokenHashEnv {
					continue
				}
				filtered = append(filtered, value)
			}
			filtered = append(filtered, map[string]any{
				"name":  managerauth.TokenHashEnv,
				"value": digest,
			})
			container["env"] = filtered
			containers[index] = container
			if err := unstructured.SetNestedSlice(object.Object, containers, "spec", "template", "spec", "containers"); err != nil {
				return fmt.Errorf("configure traffic-manager environment: %w", err)
			}
			return nil
		}
		return errors.New("traffic-manager Deployment has no manager container")
	}
	return errors.New("traffic-manager Deployment not found in runtime manifests")
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
	if helperVersion, err := helperCheckHealth(); err == nil {
		warnOnHelperSkew(config.Version, helperVersion)
		return nil
	}

	if err := startHelperProcess(config); err != nil {
		// Handle races where another process started the helper concurrently.
		if helperVersion, checkErr := helperCheckHealth(); checkErr == nil {
			warnOnHelperSkew(config.Version, helperVersion)
			return nil
		}
		return err
	}

	pollErr := wait.PollUntilContextTimeout(context.Background(), 200*time.Millisecond, 5*time.Second, false,
		func(context.Context) (bool, error) {
			helperVersion, err := helperCheckHealth()
			if err != nil {
				return false, nil
			}
			warnOnHelperSkew(config.Version, helperVersion)
			return true, nil
		})
	if pollErr != nil {
		return fmt.Errorf("helper failed to become healthy on %s", helperipc.DefaultEndpoint())
	}
	return nil
}

func startHelperProcess(config cfg.Config) error {
	helperBinaryPath, err := resolveHelperBinaryPath()
	if err != nil {
		return err
	}

	managerTokenPath, err := resolveManagerTokenPath()
	if err != nil {
		return fmt.Errorf("resolve manager token path: %w", err)
	}
	if _, err := managerauth.ReadLocalToken(managerTokenPath); err != nil {
		return fmt.Errorf("load manager token (run `krun debug runtime install` first): %w", err)
	}

	args := make([]string, 0, 4)
	args = append(args, "--manager-token-file", managerTokenPath)
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

// helperCheckHealth probes the running helper and reports the version it is
// built from. A helper older than this contract reports an empty version.
func helperCheckHealth() (string, error) {
	response, err := helperCall[contracts.HelperResponse](http.MethodGet, "/healthz", nil, healthTimeout)
	if err != nil {
		return "", err
	}
	return response.Version, nil
}

// warnOnHelperSkew reports mixed CLI/helper builds without rejecting them.
func warnOnHelperSkew(cliVersion string, helperVersion string) {
	if helperVersion == cliVersion {
		return
	}

	reported := helperVersion
	if strings.TrimSpace(reported) == "" {
		reported = "unknown (predates version reporting)"
	}
	utils.Warnf("WARNING: version skew - krun is %q but the running helper is %s", cliVersion, reported)
	utils.Warnf("         a different krun build started the helper; restart it with: krun debug helper stop")
}

// helperCall sends one request to the helper over the local IPC endpoint and
// decodes its JSON reply. A 4xx/5xx is returned as an error carrying the
// helper's own message, which is what the CLI shows the user.
func helperCall[T any](method string, path string, payload any, timeout time.Duration) (T, error) {
	var result T

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return result, err
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequest(method, helperipc.BaseURL+path, body)
	if err != nil {
		return result, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := helperipc.NewClient(timeout).Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return result, err
	}
	if len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, &result); err != nil {
			return result, fmt.Errorf("invalid helper response: %w", err)
		}
	}

	if response.StatusCode >= 400 {
		var helperResponse contracts.HelperResponse
		_ = json.Unmarshal(responseBody, &helperResponse)
		if helperResponse.Message != "" {
			return result, errors.New(helperResponse.Message)
		}
		return result, fmt.Errorf("helper request failed with status %d", response.StatusCode)
	}
	return result, nil
}

func helperListSessions() ([]contracts.HelperDebugSession, error) {
	response, err := helperCall[contracts.HelperDebugSessionsResponse](http.MethodGet, "/v1/debug/sessions", nil, listTimeout)
	return response.Sessions, err
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
