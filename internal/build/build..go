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
const stfpWorkspacePath = "workspace"

func Build(projectName string, servicesToBuild []cfg.Service, skipWeb bool, force bool, cfg cfg.Config) {
	config = cfg

	fmt.Printf("Building project %s\n", projectName)

	sftpPort, password, _ := startBuildContainer(config.KubeConfig)

	needsBuild := copySource(projectName, skipWeb, sftpPort, password)
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

func startBuildContainer(kubeConfig string) (int32, string, error) {
	fmt.Println("\033[32mCreating build container\033[0m")
	sftpPort, password, err := createBuildPod(kubeConfig)
	time.Sleep(5 * time.Second) // Give docker daemon some time to spin up

	return sftpPort, password, err
}

func buildAndPushImagesBuildah(service cfg.Service, registry string, kubeConfig string) {
	contextPath := filepath.ToSlash(filepath.Join(workspacePath, service.Project, service.Context))
	dockerfilePath := filepath.ToSlash(filepath.Join(workspacePath, service.Project, service.Path, service.Dockerfile, "Dockerfile"))

	cmd := fmt.Sprintf(
		"buildah bud -t %s/%s -f %s %s && buildah push %s/%s:latest docker://%s/%s:latest",
		registry, service.Name, dockerfilePath, contextPath, registry, service.Name, registry, service.Name,
	)
	utils.RunCmd("kubectl", "--kubeconfig="+kubeConfig, "exec", "pod/docker-build", "--", "/bin/sh", "-c", cmd)
}
