package build

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ftechmax/krun/internal/utils"
)

const manifest = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: docker-build-registries
  namespace: default
data:
  registries.conf: |
    [registries.search]
    registries = ['docker.io']

    [registries.insecure]
    registries = ['registry:5000']
---
apiVersion: v1
kind: Pod
metadata:
  labels:
    app: docker-build
  name: docker-build
  namespace: default
spec:
  securityContext:
    fsGroup: 1001
  containers:
  - image: quay.io/buildah/stable:latest
    name: docker-build
    securityContext:
      privileged: true
      runAsGroup: 1001
    command: ["sleep", "infinity"]
    volumeMounts:
      - mountPath: /var/workspace
        name: workspace
      - mountPath: /etc/containers/registries.conf
        name: docker-build-registries
        subPath: registries.conf
  - name: sftp
    image: atmoz/sftp:alpine
    args: ["user:##password##:1001:1001:workspace"]
    securityContext:
      privileged: false
      runAsGroup: 1001
    ports:
      - containerPort: 22
    volumeMounts:
      - mountPath: /home/user/workspace
        name: workspace
  volumes:
  - name: docker-build-lib
    emptyDir: {}
  - name: workspace
    emptyDir: {}
  - name: docker-build-registries
    configMap:
      name: docker-build-registries
      items:
        - key: registries.conf
          path: registries.conf
`

const password = "r6iq5N6Ji3Kn"

func buildPodExists(kubeConfig string) (bool, error) {
	cmd := exec.Command("kubectl", "--kubeconfig="+kubeConfig, "get", "pod", buildPodName, "-o", "jsonpath={.status.phase}")
	phaseOut, err := cmd.CombinedOutput()
	phase := strings.TrimSpace(string(phaseOut))

	if err != nil {
		// If the error indicates the pod is not found, treat as not existing, not an error
		errMsg := string(phaseOut) + err.Error()
		if strings.Contains(errMsg, "NotFound") {
			return false, nil
		}
		return false, err
	}

	switch phase {
	case "Running":
		return true, nil
	default:
		return false, nil
	}
}

func createBuildPod(kubeConfig string) (string, error) {
	exists, err := buildPodExists(kubeConfig)
	if err != nil {
		return "", fmt.Errorf("failed to check if build pod exists: %w", err)
	}
	if exists {
		return password, nil
	}

	fmt.Println("\033[32mCreating build container\033[0m")
	manifest := strings.ReplaceAll(manifest, "##password##", password)

	err = utils.RunCmdStdin(manifest, "kubectl", "--kubeconfig="+kubeConfig, "apply", "-f", "-")
	if err != nil {
		return "", fmt.Errorf("failed to create build pod: %w", err)
	}

	err = utils.RunCmd("kubectl", "--kubeconfig="+kubeConfig, "wait", "--for", "condition=Ready", "--timeout=90s", "pod", "-l", "app="+buildPodName)
	if err != nil {
		return "", fmt.Errorf("failed to wait for build pod to be ready: %w", err)
	}

	return password, nil
}

func generatePassword(length int) (string, error) {
	b := make([]byte, length)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b)[:length], nil
}
