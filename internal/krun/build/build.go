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

func Build(projectName string, services []cfg.Service, force bool, flush bool, config cfg.Config) error {
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

	localRoot := filepath.Join(config.Path, config.Projects[projectName].Path)

	needsBuild, err := copySource(client, projectName, localRoot, dockerfilePaths(services))
	if err != nil {
		return fmt.Errorf("copy source: %w", err)
	}
	if !needsBuild && !force {
		fmt.Printf("No changes detected in project %s, skipping build\n", projectName)
		return nil
	}

	var failed []string
	for _, s := range services {
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

// dockerfilePaths returns each service's Dockerfile relative to the project root.
func dockerfilePaths(services []cfg.Service) []string {
	paths := make([]string, 0, len(services))
	for _, s := range services {
		paths = append(paths, path.Join(serviceDockerfileDir(s), "Dockerfile"))
	}
	return paths
}

func serviceDockerfileDir(service cfg.Service) string {
	return path.Join(filepath.ToSlash(service.Path), filepath.ToSlash(service.Dockerfile))
}

func buildctlArgs(service cfg.Service, registry string) []string {
	contextPath := path.Join(workspacePath, service.Project, filepath.ToSlash(service.Context))
	dockerfileDir := path.Join(workspacePath, service.Project, serviceDockerfileDir(service))
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
