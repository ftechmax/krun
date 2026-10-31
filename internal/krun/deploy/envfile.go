package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

var excludedVars = map[string]bool{
	"PATH":                                  true,
	"HOME":                                  true,
	"HOSTNAME":                              true,
	"PWD":                                   true,
	"SHLVL":                                 true,
	"TERM":                                  true,
	"APP_UID":                               true,
	"_":                                     true,
	"DOTNET_RUNNING_IN_CONTAINER":           true,
	"DOTNET_VERSION":                        true,
	"ASPNET_VERSION":                        true,
	"COMPlus_EnableDiagnostics":             true,
	"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT": true,
}

func CreateEnvFile(service cfg.Service, config cfg.Config, containerName string) error {
	dir, err := serviceDir(service, config)
	if err != nil {
		return err
	}

	namespace := service.Namespace

	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		return fmt.Errorf("create kube client: %w", err)
	}

	pod, err := findRunningPod(context.Background(), client, namespace, service.Name)
	if err != nil {
		return err
	}

	container, err := selectContainer(pod, service, containerName)
	if err != nil {
		return err
	}

	envOutput, err := execEnv(context.Background(), client, namespace, pod.Name, container)
	if err != nil {
		return fmt.Errorf("read environment from pod %s: %w", pod.Name, err)
	}

	return writeDotEnv(dir, filterEnvVars(envOutput))
}

func RemoveEnvFile(service cfg.Service, config cfg.Config) error {
	dir, err := serviceDir(service, config)
	if err != nil {
		return err
	}

	if err := os.Remove(filepath.Join(dir, ".env")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove .env file: %w", err)
	}
	return nil
}

// filterEnvVars keeps app settings as dotenv lines.
func filterEnvVars(raw string) string {
	var filtered strings.Builder
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" || excludedVars[key] {
			continue
		}
		if strings.HasPrefix(key, "KUBERNETES_") || strings.HasPrefix(value, "tcp://") {
			continue
		}
		if strings.HasSuffix(key, "_SERVICE_HOST") || strings.Contains(key, "_SERVICE_PORT") {
			continue
		}
		if strings.Contains(key, "_PORT_") && strings.Contains(key, "_TCP") {
			continue
		}
		filtered.WriteString(line)
		filtered.WriteByte('\n')
	}
	return filtered.String()
}

func writeDotEnv(dir, contents string) error {
	path := filepath.Join(dir, ".env")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("set %s permissions: %w", path, err)
	}
	if _, err := file.WriteString(contents); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return file.Close()
}

func serviceDir(service cfg.Service, config cfg.Config) (string, error) {
	baseDir := config.KrunConfig.Path
	if service.Project != "" && config.ProjectPaths != nil {
		if rel, ok := config.ProjectPaths[service.Project]; ok && rel != "" {
			baseDir = filepath.Join(baseDir, filepath.FromSlash(rel))
		}
	}
	if strings.TrimSpace(baseDir) == "" {
		return "", fmt.Errorf("project path not available for service %s", service.Name)
	}
	if strings.TrimSpace(service.Path) != "" {
		baseDir = filepath.Join(baseDir, filepath.FromSlash(service.Path))
	}
	return baseDir, nil
}

func findRunningPod(ctx context.Context, client *kube.Client, namespace, serviceName string) (*corev1.Pod, error) {
	var found *corev1.Pod
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 30*time.Second, true,
		func(ctx context.Context) (bool, error) {
			pods, err := client.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("app=%s", serviceName),
			})
			if err != nil {
				return false, fmt.Errorf("list pods for %s/%s: %w", namespace, serviceName, err)
			}
			for idx := range pods.Items {
				pod := &pods.Items[idx]
				if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
					found = pod
					return true, nil
				}
			}
			return false, nil
		})
	if wait.Interrupted(err) {
		return nil, fmt.Errorf("no running pod found for %s/%s", namespace, serviceName)
	}
	if err != nil {
		return nil, err
	}
	return found, nil
}

// Prefer an explicit override, then the service name, then its port.
// The injected agent is excluded from automatic selection.
func selectContainer(pod *corev1.Pod, service cfg.Service, override string) (string, error) {
	if name := strings.TrimSpace(override); name != "" {
		return name, nil
	}

	var names []string
	var portMatch string
	for _, container := range pod.Spec.Containers {
		if container.Name == contracts.AgentContainerName {
			continue
		}
		if container.Name == service.Name {
			return container.Name, nil
		}
		names = append(names, container.Name)
		if portMatch == "" && exposesPort(container, service.ContainerPort) {
			portMatch = container.Name
		}
	}
	if portMatch != "" {
		return portMatch, nil
	}
	if len(names) == 1 {
		return names[0], nil
	}
	return "", fmt.Errorf("cannot pick a container in pod %s (candidates: %s); pass --container", pod.Name, strings.Join(names, ", "))
}

func exposesPort(container corev1.Container, port int) bool {
	if port <= 0 {
		return false
	}
	for _, p := range container.Ports {
		if int(p.ContainerPort) == port {
			return true
		}
	}
	return false
}

func execEnv(ctx context.Context, client *kube.Client, namespace, podName, containerName string) (string, error) {
	stdout, stderr, err := kube.ExecCapture(ctx, client, namespace, podName, containerName, []string{"env"}, nil)
	if err != nil {
		return "", fmt.Errorf("exec env: %w (stderr: %s)", err, stderr)
	}
	return stdout, nil
}
