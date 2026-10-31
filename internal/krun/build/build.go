package build

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/kube"
	"github.com/ftechmax/krun/internal/utils"
)

const workspacePath = "/var/workspace"
const buildPodName = "docker-build"

func Build(projectName string, servicesToBuild []cfg.Service, skipWeb bool, force bool, flush bool, config cfg.Config) error {
	fmt.Printf("Building project %s\n", projectName)

	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		return fmt.Errorf("create kube client: %w", err)
	}

	if flush {
		flushBuildPod(client)
	}

	if err := createBuildPod(client); err != nil {
		return fmt.Errorf("start build container: %w", err)
	}

	projectPath := config.ProjectPaths[projectName]
	if projectPath == "" {
		projectPath = projectName
	}
	localRoot := filepath.Join(config.KrunSourceConfig.Path, filepath.FromSlash(projectPath))
	needsBuild, err := copySource(client, projectName, localRoot, skipWeb)
	if err != nil {
		return fmt.Errorf("copy source: %w", err)
	}
	if !needsBuild && !force {
		fmt.Printf("No changes detected in project %s, skipping build\n", projectName)
		return nil
	}

	var failed []string
	for _, s := range servicesToBuild {
		if skipWeb && filepath.Base(s.Path) == "web" {
			continue
		}
		if err := buildAndPushImage(client, s, config.Registry); err != nil {
			utils.Errf("Build failed for %s: %s", s.Name, err.Error())
			failed = append(failed, s.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("build failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}

// flushBuildPod deletes the build pod to clear its caches. Failures are reported but not fatal;
// createBuildPod waits for a terminating pod before recreating it.
func flushBuildPod(client *kube.Client) {
	exists, err := buildPodExists(client)
	if err != nil {
		utils.Errf("Failed to check existing build pod for flush: %s", err.Error())
		return
	}
	if !exists {
		return
	}
	utils.Warnf("Deleting existing build pod")
	if err := deleteBuildPod(client); err != nil {
		utils.Errf("Failed to delete existing build pod: %s", err.Error())
	}
}

func buildctlArgs(service cfg.Service, registry string) []string {
	contextPath := path.Join(workspacePath, service.Project, filepath.ToSlash(service.Context))
	dockerfileDir := path.Join(workspacePath, service.Project, filepath.ToSlash(service.Path), filepath.ToSlash(service.Dockerfile))
	image := fmt.Sprintf("%s/%s:latest", registry, service.Name)

	return []string{
		"buildctl", "build",
		"--progress=plain",
		"--frontend=dockerfile.v0",
		"--local", "context=" + contextPath,
		"--local", "dockerfile=" + dockerfileDir,
		"--output", "type=image,name=" + image + ",push=true",
	}
}

func buildAndPushImage(client *kube.Client, service cfg.Service, registry string) error {
	return execInBuildPodStream(
		context.Background(),
		client,
		buildctlArgs(service, registry),
		nil,
		os.Stdout,
		os.Stderr,
	)
}
