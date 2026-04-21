package build

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/kube"
)

var config cfg.Config

const workspacePath = "/var/workspace"
const buildPodName = "docker-build"

func Build(ctx context.Context, out io.Writer, projectName string, servicesToBuild []cfg.Service, skipWeb bool, force bool, flush bool, cfg cfg.Config) error {
	if out == nil {
		out = io.Discard
	}

	config = cfg

	fmt.Fprintf(out, "Building project %s\n", projectName)

	// If flush requested, delete existing build pod to clear caches
	if flush {
		flushBuildPodIfRequested(ctx, out, config.KubeConfig)
	}

	if err := startBuildContainer(ctx, out, config.KubeConfig); err != nil {
		return fmt.Errorf("failed to start build container: %w", err)
	}

	projectPath := ""
	if config.ProjectPaths != nil {
		projectPath = config.ProjectPaths[projectName]
	}
	needsBuild, err := copySource(ctx, out, config.KubeConfig, projectName, projectPath, skipWeb)
	if err != nil {
		return fmt.Errorf("failed to copy source: %w", err)
	}
	if !needsBuild && !force {
		fmt.Fprintf(out, "No changes detected in project %s, skipping build\n", projectName)
		return nil
	}

	return buildServices(ctx, out, servicesToBuild, skipWeb, config.Registry, config.KubeConfig)
}

func flushBuildPodIfRequested(ctx context.Context, out io.Writer, kubeConfig string) {
	exists, err := buildPodExists(ctx, kubeConfig)
	if err != nil {
		fmt.Fprintf(out, "Failed to check existing build pod for flush: %s\n", err.Error())
		return
	}
	if !exists {
		return
	}

	fmt.Fprintln(out, "Deleting existing build pod")
	if err := deleteBuildPod(ctx, kubeConfig); err != nil {
		fmt.Fprintf(out, "Failed to delete existing build pod: %s\n", err.Error())
		return
	}

	fmt.Fprint(out, "Waiting for previous build pod to terminate")
	if err := waitForBuildPodDeletion(ctx, out, kubeConfig, 45*time.Second); err != nil {
		fmt.Fprintln(out, " (timed out)")
		fmt.Fprintln(out, "Previous build pod is still terminating; proceeding to recreate")
		return
	}

	fmt.Fprintln(out, " (done)")
	fmt.Fprintln(out, "Previous build pod fully removed.")
}

func buildServices(ctx context.Context, out io.Writer, servicesToBuild []cfg.Service, skipWeb bool, registry string, kubeConfig string) error {
	for _, service := range servicesToBuild {
		if skipWeb && filepath.Base(service.Path) == "web" {
			continue
		}
		if err := buildAndPushImages(ctx, out, service, registry, kubeConfig); err != nil {
			return err
		}
	}
	return nil
}

func startBuildContainer(ctx context.Context, out io.Writer, kubeConfig string) error {
	if err := createBuildPod(ctx, out, kubeConfig); err != nil {
		return err
	}

	// Wait for build pod to be up
	for range 60 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		exists, err := buildPodExists(ctx, kubeConfig)
		if err != nil {
			return fmt.Errorf("error checking build pod state: %w", err)
		}
		if exists {
			return nil
		}
		fmt.Fprintln(out, "Waiting for build pod to become ready...")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("build pod did not become ready in time")
}

func buildAndPushImages(ctx context.Context, out io.Writer, service cfg.Service, registry string, kubeConfig string) error {
	contextPath := filepath.ToSlash(filepath.Join(workspacePath, service.Project, service.Context))
	dockerfilePath := filepath.ToSlash(filepath.Join(workspacePath, service.Project, service.Path, service.Dockerfile, "Dockerfile"))

	cmd := fmt.Sprintf(
		"buildah bud --layers=true -t %s/%s -f %s %s && buildah push %s/%s:latest docker://%s/%s:latest",
		registry, service.Name, dockerfilePath, contextPath, registry, service.Name, registry, service.Name,
	)

	client, err := kube.NewClient(kubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	err = execInBuildPodStream(
		ctx,
		client,
		[]string{"/bin/sh", "-c", cmd},
		nil,
		out,
		out,
	)
	if err != nil {
		return fmt.Errorf("buildah command failed for %s: %w", service.Name, err)
	}

	return nil
}

func waitForBuildPodDeletion(ctx context.Context, out io.Writer, kubeConfig string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		exists, err := buildPodExists(ctx, kubeConfig)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for build pod deletion")
		}
		fmt.Fprint(out, ".")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
