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

var config cfg.Config

const workspacePath = "/var/workspace"
const buildPodName = "docker-build"

func Build(projectName string, servicesToBuild []cfg.Service, skipWeb bool, force bool, flush bool, cfg cfg.Config) error {
	config = cfg

	fmt.Printf("Building project %s\n", projectName)

	if flush {
		flushBuildPod(config.KubeConfig)
	}

	if err := createBuildPod(config.KubeConfig); err != nil {
		return fmt.Errorf("start build container: %w", err)
	}

	projectPath := ""
	if config.ProjectPaths != nil {
		projectPath = config.ProjectPaths[projectName]
	}
	needsBuild, err := copySource(config.KubeConfig, projectName, projectPath, skipWeb)
	if err != nil {
		return fmt.Errorf("copy source: %w", err)
	}
	if !needsBuild && !force {
		fmt.Printf("No changes detected in project %s, skipping build\n", projectName)
		return nil
	}

	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		return fmt.Errorf("create kube client: %w", err)
	}

	var failed []string
	for _, s := range servicesToBuild {
		if skipWeb && filepath.Base(s.Path) == "web" {
			continue
		}
		if err := buildAndPushImage(client, s, config.Registry); err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("Build failed for %s: %s", s.Name, err.Error()), utils.Red))
			failed = append(failed, s.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("build failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}

// flushBuildPod deletes the build pod to clear its caches. Failures are reported but not fatal.
func flushBuildPod(kubeConfig string) {
	exists, err := buildPodExists(kubeConfig)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to check existing build pod for flush: %s", err.Error()), utils.Red))
		return
	}
	if !exists {
		return
	}
	fmt.Println(utils.Colorize("Deleting existing build pod", utils.Yellow))
	if err := deleteBuildPod(kubeConfig); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to delete existing build pod: %s", err.Error()), utils.Red))
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
