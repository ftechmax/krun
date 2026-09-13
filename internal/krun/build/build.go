package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/kube"
	"github.com/ftechmax/krun/internal/utils"
	"k8s.io/apimachinery/pkg/util/wait"
)

const workspacePath = "/var/workspace"
const buildPodName = "docker-build"

func Build(projectName string, servicesToBuild []cfg.Service, skipWeb bool, force bool, flush bool, config cfg.Config) {
	fmt.Printf("Building project %s\n", projectName)

	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		utils.Errf("Failed to create Kubernetes client: %s", err.Error())
		return
	}

	if flush {
		exists, err := buildPodExists(client)
		if err != nil {
			utils.Errf("Failed to check existing build pod for flush: %s", err.Error())
		} else if exists {
			utils.Warnf("Deleting existing build pod")
			if err := deleteBuildPod(client); err != nil {
				utils.Errf("Failed to delete existing build pod: %s", err.Error())
			} else {
				fmt.Print(utils.Colorize("Waiting for previous build pod to terminate", utils.Cyan))
				if err := waitForBuildPodDeletion(client, 45*time.Second); err != nil {
					utils.Warnf(" (timed out)")
					utils.Warnf("Previous build pod is still terminating; proceeding to recreate")
				} else {
					utils.Okf(" (done)")
					utils.Okf("Previous build pod fully removed.")
				}
			}
		}
	}

	if err := startBuildContainer(client); err != nil {
		panic(fmt.Sprintf("Failed to start build container: %s", err.Error()))
	}

	projectPath := config.ProjectPaths[projectName]
	if projectPath == "" {
		projectPath = projectName
	}
	localRoot := filepath.Join(config.KrunSourceConfig.Path, filepath.FromSlash(projectPath))
	needsBuild, err := copySource(client, projectName, localRoot, skipWeb)
	if err != nil {
		utils.Errf("Failed to copy source: %s", err.Error())
		return
	}
	if needsBuild || force {

		for _, s := range servicesToBuild {
			if skipWeb && filepath.Base(s.Path) == "web" {
				continue
			}
			buildAndPushImagesBuildah(client, s, config.Registry)
		}
	} else {
		fmt.Printf("No changes detected in project %s, skipping build\n", projectName)
	}
}

func startBuildContainer(client *kube.Client) error {
	if err := createBuildPod(client); err != nil {
		return err
	}

	err := wait.PollUntilContextTimeout(context.Background(), 500*time.Millisecond, 30*time.Second, true,
		func(ctx context.Context) (bool, error) {
			exists, err := buildPodExists(client)
			if err != nil {
				return false, fmt.Errorf("error checking build pod state: %w", err)
			}
			if !exists {
				fmt.Println("Waiting for build pod to become ready...")
			}
			return exists, nil
		})
	if wait.Interrupted(err) {
		return fmt.Errorf("build pod did not become ready in time")
	}
	return err
}

func buildAndPushImagesBuildah(client *kube.Client, service cfg.Service, registry string) {
	contextPath := filepath.ToSlash(filepath.Join(workspacePath, service.Project, service.Context))
	dockerfilePath := filepath.ToSlash(filepath.Join(workspacePath, service.Project, service.Path, service.Dockerfile, "Dockerfile"))

	cmd := fmt.Sprintf(
		"buildah bud -t %s/%s -f %s %s && buildah push %s/%s:latest docker://%s/%s:latest",
		registry, service.Name, dockerfilePath, contextPath, registry, service.Name, registry, service.Name,
	)

	err := execInBuildPodStream(
		context.Background(),
		client,
		[]string{"/bin/sh", "-c", cmd},
		nil,
		os.Stdout,
		os.Stderr,
	)
	if err != nil {
		utils.Errf("Buildah command failed for %s: %s", service.Name, err.Error())
	}
}

func waitForBuildPodDeletion(client *kube.Client, timeout time.Duration) error {
	err := wait.PollUntilContextTimeout(context.Background(), 750*time.Millisecond, timeout, true,
		func(ctx context.Context) (bool, error) {
			exists, err := buildPodExists(client)
			if err != nil {
				return false, err
			}
			if exists {
				fmt.Print(".")
			}
			return !exists, nil
		})
	if wait.Interrupted(err) {
		return fmt.Errorf("timeout waiting for build pod deletion")
	}
	return err
}
