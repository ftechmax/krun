package build

import (
	"fmt"
	"path/filepath"
	"time"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/utils"
)

var config cfg.Config

const workspacePath = "/var/workspace"
const sftpWorkspacePath = "workspace"
const buildPodName = "docker-build"

func Build(projectName string, servicesToBuild []cfg.Service, skipWeb bool, force bool, flush bool, cfg cfg.Config) {
	config = cfg
	
	fmt.Printf("Building project %s\n", projectName)

	// If flush requested, delete existing build pod to clear caches
	if flush {
		exists, err := buildPodExists(config.KubeConfig)
		if err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("Failed to check existing build pod for flush: %s", err.Error()), utils.Red))
		} else if exists {
			fmt.Println(utils.Colorize("Flushing existing build pod (clearing build cache)", utils.Yellow))
			if err := deleteBuildPod(config.KubeConfig); err != nil {
				fmt.Println(utils.Colorize(fmt.Sprintf("Failed to delete existing build pod: %s", err.Error()), utils.Red))
			} else {
				fmt.Println(utils.Colorize("Build pod deleted", utils.Green))
			}
		}
	}

	password, err := startBuildContainer(config.KubeConfig)
	if err != nil {
		panic(fmt.Sprintf("Failed to start build container: %s", err.Error()))
	}

	needsBuild, err := copySource(config.KubeConfig, projectName, skipWeb, password)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to copy source: %s", err.Error()), utils.Red))
		return
	}
	if needsBuild || force {

		for _, s := range servicesToBuild {
			if skipWeb && filepath.Base(s.Path) == "web" {
				continue
			}
			buildAndPushImagesBuildah(s, config.Registry, config.KubeConfig)
		}
	} else {
		fmt.Printf("No changes detected in project %s, skipping build\n", projectName)
	}
}

func startBuildContainer(kubeConfig string) (string, error) {
	password, err := createBuildPod(kubeConfig)
	if err != nil {
		return "", err
	}

	// Wait for build pod to be up
	for range 30 {
		exists, err := buildPodExists(kubeConfig)
		if err != nil {
			return "", fmt.Errorf("error checking build pod state: %w", err)
		}
		if exists {
			return password, nil
		}
		fmt.Println("Waiting for build pod to become ready...")
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("build pod did not become ready in time")
}

func buildAndPushImagesBuildah(service cfg.Service, registry string, kubeConfig string) {
	contextPath := filepath.ToSlash(filepath.Join(workspacePath, service.Project, service.Context))
	dockerfilePath := filepath.ToSlash(filepath.Join(workspacePath, service.Project, service.Path, service.Dockerfile, "Dockerfile"))

    cmd := fmt.Sprintf(
		"buildah bud -t %s/%s -f %s %s && buildah push %s/%s:latest docker://%s/%s:latest",
		registry, service.Name, dockerfilePath, contextPath, registry, service.Name, registry, service.Name,
    )
    utils.RunCmd("kubectl", "--kubeconfig="+kubeConfig, "exec", "pod/"+buildPodName, "--", "/bin/sh", "-c", cmd)
}
