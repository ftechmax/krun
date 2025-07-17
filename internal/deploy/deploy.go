package deploy

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/utils"
	"gopkg.in/yaml.v3"
)

func Deploy(projectName string, config cfg.Config) {
	fmt.Printf("Deploying %s (use remote registry: %v)\n", projectName, config.Registry == config.RemoteRegistry)
	kustomize := handle(config, projectName, false)
	restartAll(config.KubeConfig, kustomize)
}

func Delete(projectName string, config cfg.Config) {
	fmt.Printf("Deleting %s\n", projectName)
	_ = handle(config, projectName, true)
}

func handle(config cfg.Config, projectName string, delete bool) string {
	servicePath := fmt.Sprintf("%s/%s", config.KrunSourceConfig.Path, projectName)
	k8sPath := fmt.Sprintf("%s/k8s", servicePath)
	k8sCloudPath := fmt.Sprintf("%s/cloud", k8sPath)
	k8sEdgePath := fmt.Sprintf("%s/edge", k8sPath)

	// Check if k8sCloudPath directory exists
	var kustomize string
	if _, err := os.Stat(k8sCloudPath); err == nil {
		kustomizePath := fmt.Sprintf("%s/overlays/local", k8sCloudPath)
		kustomize = apply(config, kustomizePath, delete)

		kustomizePath = fmt.Sprintf("%s/overlays/local", k8sEdgePath)
		kustomize = apply(config, kustomizePath, delete)
	} else {
		kustomizePath := fmt.Sprintf("%s/overlays/local", k8sPath)
		kustomize = apply(config, kustomizePath, delete)
	}

	return kustomize
}

func restartAll(kubeConfig string, kustomize string) {
	type resource struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
	}

	type ResourceName struct {
		kind      string
		name      string
		namespace string
	}
	var names []ResourceName

	docs := strings.SplitSeq(kustomize, "---")
	for doc := range docs {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}
		var res resource
		err := yaml.Unmarshal([]byte(doc), &res)
		if err != nil {
			continue
		}
		switch res.Kind {
		case "Deployment", "StatefulSet", "DaemonSet":
			if res.Metadata.Name != "" {
				names = append(names, ResourceName{
					kind: res.Kind, 
					name: res.Metadata.Name, 
					namespace: res.Metadata.Namespace,
				})
			}
		}
	}

	var stderr bytes.Buffer
	for _, n := range names {
		args := []string{"--kubeconfig", kubeConfig, "rollout", "restart", strings.ToLower(n.kind), n.name}
		if n.namespace != "" {
			args = append(args, "-n", n.namespace)
		}
		cmd := exec.Command("kubectl", args...)
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			fmt.Printf("Error restarting %s %s: %v\nOutput: %s\n", n.kind, n.name, err, utils.Colorize(stderr.String(), utils.Red))
		}
	}
}

func apply(config cfg.Config, kustomizePath string, delete bool) string {
	var stdout, stderr bytes.Buffer

	// Create kustomize output
	kustomizeCmd := exec.Command("kubectl", "--kubeconfig", config.KubeConfig, "kustomize", kustomizePath)
	kustomizeCmd.Stdout = &stdout
	kustomizeCmd.Stderr = &stderr
	err := kustomizeCmd.Run()
	if err != nil {
		fmt.Printf("Error running kubectl kustomize\n%v\n", utils.Colorize(stderr.String(), utils.Red))
		return ""
	}

	// Replace localRegistry with registry in the output
	kustomize := stdout.String()
	kustomize = strings.ReplaceAll(kustomize, config.LocalRegistry, config.Registry)

	action := "apply"
	if delete {
		action = "delete"
	}

	// Apply the kustomize output
	kustomizeCmd = exec.Command("kubectl", "--kubeconfig", config.KubeConfig, action, "-f", "-")
	kustomizeCmd.Stdin = strings.NewReader(kustomize)
	kustomizeCmd.Stderr = &stderr
	kustomizeOut, err := kustomizeCmd.Output()
	if err != nil {
		fmt.Printf("Error running kubectl %v\n%v\n", action, utils.Colorize(stderr.String(), utils.Red))
		return ""
	}

	fmt.Println(string(kustomizeOut))

	return kustomize
}
