package deploy

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	cfg "github.com/ftechmax/krun/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var config cfg.Config

func Deploy(projectName string, cfg cfg.Config) {
	config = cfg

	fmt.Printf("Deploying %s (remote registry: %v)\n", projectName, config.Registry == config.RemoteRegistry)

	servicePath := fmt.Sprintf("%s/%s", config.KrunSourceConfig.Path, projectName)
	k8sPath := fmt.Sprintf("%s/k8s", servicePath)
	k8sCloudPath := fmt.Sprintf("%s/cloud", k8sPath)
	k8sEdgePath := fmt.Sprintf("%s/edge", k8sPath)

	// Check if k8sCloudPath directory exists
	var kustomize string
	if _, err := os.Stat(k8sCloudPath); err == nil {
		kustomizePath := fmt.Sprintf("%s/overlays/local", k8sCloudPath)
		kustomize = apply(kustomizePath)

		kustomizePath = fmt.Sprintf("%s/overlays/local", k8sEdgePath)
		kustomize += apply(kustomizePath)
	} else {
		kustomizePath := fmt.Sprintf("%s/overlays/local", k8sPath)
		kustomize = apply(kustomizePath)
	}

	// Rollout restart
	restart(config.KubeConfig, kustomize)
}

func restart(kubeConfig string, kustomize string) {
	manifests := strings.SplitSeq(string(kustomize), "---")
	for manifest := range manifests {
		manifest = strings.TrimSpace(manifest)
		if manifest == "" {
			continue
		}
		obj := &unstructured.Unstructured{}
		decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
		if err := decoder.Decode(obj); err != nil {
			fmt.Fprintf(os.Stderr, "decode error: %v\n", err)
			continue
		}
		gvk := obj.GroupVersionKind()
		if gvk.Empty() {
			fmt.Fprintf(os.Stderr, "empty GVK for manifest: %s\n", manifest)
			continue
		}
		if gvk.Kind == "Deployment" || gvk.Kind == "StatefulSet" || gvk.Kind == "DaemonSet" {
			name := obj.GetName()
			namespace := obj.GetNamespace()
			if namespace == "" {
				namespace = "default"
			}
			fmt.Printf("Restarting %s/%s\n", namespace, name)
			cmd := exec.Command("kubectl", "--kubeconfig", kubeConfig, "rollout", "restart", gvk.Kind, name, "-n", namespace)
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "error restarting %s/%s: %v\n", namespace, name, err)
			}
		}
	}
}

func apply(kustomizePath string) string {
	// Create kustomize output
	kustomizeCmd := exec.Command("kubectl", "--kubeconfig", config.KubeConfig, "kustomize", kustomizePath)
	kustomizeOut, err := kustomizeCmd.Output()
	if err != nil {
		fmt.Printf("Error running kubectl kustomize: %v\n", err)
		return ""
	}

	// Replace localRegistry with registry in the output
	kustomize := string(kustomizeOut)
	kustomize = strings.Replace(kustomize, config.LocalRegistry, config.Registry, -1)

	// Deploy
	kustomizeCmd = exec.Command("kubectl", "--kubeconfig", config.KubeConfig, "apply", "-f", "-")
	kustomizeCmd.Stdin = strings.NewReader(kustomize)
	_, err = kustomizeCmd.Output()
	if err != nil {
		fmt.Printf("Error running kubectl apply: %v\n", err)
		return ""
	}

	return kustomize
}
