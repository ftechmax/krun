package deploy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	cfg "github.com/voortman-steel-machinery/krun/internal/config"
)

var config cfg.Config

func Deploy(projectName string, cfg cfg.Config) {
	config = cfg
	
	fmt.Printf("Deploying %s (use remote registry: %v)\n", projectName, config.Registry == config.RemoteRegistry)

	servicePath := fmt.Sprintf("%s/%s", config.KrunSourceConfig.Path, projectName)
	k8sPath := fmt.Sprintf("%s/k8s", servicePath)
	k8sCloudPath := fmt.Sprintf("%s/cloud", k8sPath)
	k8sEdgePath := fmt.Sprintf("%s/edge", k8sPath)

	// Check if k8sCloudPath directory exists
	if _, err := os.Stat(k8sCloudPath); err == nil {
		kustomizePath := fmt.Sprintf("%s/overlays/local", k8sCloudPath)
		apply(kustomizePath)

		kustomizePath = fmt.Sprintf("%s/overlays/local", k8sEdgePath)
		apply(kustomizePath)
	} else {
		kustomizePath := fmt.Sprintf("%s/overlays/local", k8sPath)
		apply(kustomizePath)
	}

	// Rollout restart
	restart(config.KubeConfig, k8sPath)
}

func restart(kubeConfig string, k8sPath string) {
	// Find all deployment.yaml files in the k8sPath directory
	var deploymentFiles []string
	err := filepath.Walk(k8sPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Name() == "deployment.yaml" {
			deploymentFiles = append(deploymentFiles, path)
		}
		return nil
	})
	if err != nil {
		fmt.Printf("Error finding deployment.yaml files: %v\n", err)
		return
	}

	for _, file := range deploymentFiles {
		content, err := os.ReadFile(file)
		
		if err != nil {
			fmt.Printf("Error reading %s: %v\n", file, err)
			continue
		}
		lines := strings.Split(string(content), "\n")
		var deploymentName string
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "name:") {
				parts := strings.Fields(line)
				if len(parts) == 2 {
					deploymentName = parts[1]
					break
				}
			}
		}
		if deploymentName != "" {
			fmt.Printf("Restarting deployment %s\n", deploymentName)
			cmd := exec.Command("kubectl", "--kubeconfig", kubeConfig, "rollout", "restart", "deploy", deploymentName)
			out, err := cmd.CombinedOutput()
			if err != nil {
				fmt.Printf("Error restarting deployment %s: %v\nOutput: %s\n", deploymentName, err, string(out))
			}
		}
	}
}

func apply(kustomizePath string) {
	// Create kustomize output
	kustomizeCmd := exec.Command("kubectl", "--kubeconfig", config.KubeConfig, "kustomize", kustomizePath)
	kustomizeOut, err := kustomizeCmd.Output()
	if err != nil {
		fmt.Printf("Error running kubectl kustomize: %v\n", err)
		return
	}

	// Replace localRegistry with registry in the output
	kustomize := string(kustomizeOut)
	kustomize = strings.Replace(kustomize, config.LocalRegistry, config.Registry, -1)

	// Deploy
	kustomizeCmd = exec.Command("kubectl", "--kubeconfig", config.KubeConfig, "apply", "-f", "-")
	kustomizeCmd.Stdin = strings.NewReader(kustomize)
	kustomizeOut, err = kustomizeCmd.Output()
	if err != nil {
		fmt.Printf("Error running kubectl apply: %v\n", err)
		return
	}
}