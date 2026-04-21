package envfile

import (
	"bytes"
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
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
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

type envVar struct {
	Key   string
	Value string
}

func CreateEnvFile(config cfg.KrunConfig, kubeConfig string, svcContext contracts.DebugServiceContext, containerName string) error {

	fmt.Println("Creating .env file with the following parameters:")
	fmt.Println(config)
	fmt.Println(svcContext)
	fmt.Println(containerName)

	dir := serviceDir(config, svcContext)

	namespace := svcContext.Namespace
	if strings.TrimSpace(namespace) == "" {
		namespace = "default"
	}

	client, err := kube.NewClient(kubeConfig)
	if err != nil {
		return fmt.Errorf("create kube client: %w", err)
	}

	container := strings.TrimSpace(containerName)
	if container == "" {
		container = svcContext.ServiceName
	}

	podName, err := findRunningPod(context.Background(), client, namespace, svcContext.ServiceName)
	if err != nil {
		return err
	}

	envOutput, err := execEnv(context.Background(), client, namespace, podName, container)
	if err != nil {
		return fmt.Errorf("read environment from pod %s: %w", podName, err)
	}

	vars := filterEnvVars(parseEnvVars(envOutput))
	return writeDotEnv(dir, vars)
}

func RemoveEnvFile(config cfg.KrunConfig, svcContext contracts.DebugServiceContext) error {
	dir := serviceDir(config, svcContext)

	if err := os.Remove(filepath.Join(dir, ".env")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove .env file: %w", err)
	}
	return nil
}

func parseEnvVars(raw string) []envVar {
	var vars []envVar
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		vars = append(vars, envVar{Key: line[:idx], Value: line[idx+1:]})
	}
	return vars
}

func filterEnvVars(vars []envVar) []envVar {
	var filtered []envVar
	for _, v := range vars {
		if excludedVars[v.Key] {
			continue
		}
		if strings.HasPrefix(v.Key, "KUBERNETES_") {
			continue
		}
		// Kubernetes service discovery: value is a tcp:// address
		if strings.HasPrefix(v.Value, "tcp://") {
			continue
		}
		// Kubernetes service discovery: *_SERVICE_HOST, *_SERVICE_PORT*
		if strings.HasSuffix(v.Key, "_SERVICE_HOST") || strings.Contains(v.Key, "_SERVICE_PORT") {
			continue
		}
		// Kubernetes service discovery: *_PORT_*_TCP* (e.g. SVC_PORT_8080_TCP_ADDR)
		if strings.Contains(v.Key, "_PORT_") && strings.Contains(v.Key, "_TCP") {
			continue
		}
		filtered = append(filtered, v)
	}
	return filtered
}

func writeDotEnv(dir string, vars []envVar) error {
	var buf strings.Builder
	for _, v := range vars {
		fmt.Fprintf(&buf, "%s=%s\n", v.Key, v.Value)
	}
	// TODO: set to local dir owner + 600 instead of 644
	return os.WriteFile(filepath.Join(dir, ".env"), []byte(buf.String()), 0o644)
}

func serviceDir(config cfg.KrunConfig, svcContext contracts.DebugServiceContext) string {
	dir := filepath.Join(config.Path, filepath.FromSlash(svcContext.Project), filepath.FromSlash(svcContext.Path))
	return dir
}

func findRunningPod(ctx context.Context, client *kube.Client, namespace, serviceName string) (string, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		pods, err := client.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app=%s", serviceName),
		})
		if err != nil {
			return "", fmt.Errorf("list pods for %s/%s: %w", namespace, serviceName, err)
		}

		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
				return pod.Name, nil
			}
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("no running pod found for %s/%s", namespace, serviceName)
		}
		time.Sleep(2 * time.Second)
	}
}

func execEnv(ctx context.Context, client *kube.Client, namespace, podName, containerName string) (string, error) {
	req := client.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   []string{"env"},
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(client.RestConfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("create exec stream: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return "", fmt.Errorf("exec env: %w (stderr: %s)", err, stderr.String())
	}

	return stdout.String(), nil
}
