package runtime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	semver "github.com/blang/semver/v4"
	cfg "github.com/ftechmax/krun/internal/config"
	deploy "github.com/ftechmax/krun/internal/krun-helper/deploy"
	"github.com/ftechmax/krun/internal/kube"
	"github.com/ftechmax/krun/internal/utils"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func RuntimeInstall(config cfg.Config, version string) {
	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to create Kubernetes client: %s", err), utils.Red))
		return
	}

	objs, err := loadManifestObjects(version)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to load manifests: %s", err), utils.Red))
		return
	}

	if err := deploy.ApplyObjects(context.Background(), client, objs); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to apply manifests: %s", err), utils.Red))
		return
	}

	fmt.Println("Waiting for traffic manager to become ready...")

	timeout := 2 * time.Minute
	interval := 2 * time.Second
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		dep, err := client.Clientset.AppsV1().Deployments("krun-system").Get(context.Background(), "krun-traffic-manager", metav1.GetOptions{})
		if err == nil {
			desired := int32(1)
			if dep.Spec.Replicas != nil {
				desired = *dep.Spec.Replicas
			}
			if dep.Status.ReadyReplicas >= desired {
				fmt.Println(utils.Colorize("traffic manager installed", utils.Green))
				return
			}
		}
		time.Sleep(interval)
	}

	fmt.Println(utils.Colorize("Traffic manager was applied but did not become ready", utils.Yellow))
}

func RuntimeStatus(config cfg.Config) {
	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("error: failed to create kubernetes client (%v)", err), utils.Red))
		return
	}

	dep, err := client.Clientset.AppsV1().Deployments("krun-system").Get(context.Background(), "krun-traffic-manager", metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			printStatusValue("service:", "not installed", utils.Red)
			return
		}
		fmt.Println(utils.Colorize(fmt.Sprintf("error: failed to get traffic-manager deployment (%v)", err), utils.Red))
		return
	}

	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	ready := dep.Status.ReadyReplicas

	version := "unknown"
	if len(dep.Spec.Template.Spec.Containers) > 0 {
		image := dep.Spec.Template.Spec.Containers[0].Image
		if i := strings.LastIndex(image, ":"); i != -1 {
			version = image[i+1:]
		}
	}

	printStatusValue("service:", "installed", utils.Green)

	if ready == desired {
		printStatusValue("health:", "ok", utils.Green)
	} else {
		printStatusValue("health:", fmt.Sprintf("not ready (%d/%d pods ready)", ready, desired), utils.Red)
	}

	fmt.Printf("version: %s\n", version)
}

func RuntimeUninstall(config cfg.Config, version string) {
	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to create Kubernetes client: %s", err), utils.Red))
		return
	}

	_, err = client.Clientset.AppsV1().Deployments("krun-system").Get(context.Background(), "krun-traffic-manager", metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			fmt.Println(utils.Colorize("traffic manager is not installed", utils.Yellow))
			return
		}
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to check traffic manager: %s", err), utils.Red))
		return
	}

	objs, err := loadManifestObjects(version)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to load manifests: %s", err), utils.Red))
		return
	}

	if err := deploy.DeleteObjects(context.Background(), client, objs); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to delete manifests: %s", err), utils.Red))
		return
	}

	fmt.Println(utils.Colorize("traffic manager uninstalled", utils.Green))
}

func loadManifestObjects(version string) ([]*unstructured.Unstructured, error) {
	if version == "debug" && os.Getenv("KRUN_MANIFEST_URL") == "" {
		return deploy.RenderKustomizeObjects("deploy/runtime/overlays/local")
	}
	return fetchRemoteManifestObjects(version)
}

func fetchRemoteManifestObjects(version string) ([]*unstructured.Unstructured, error) {
	manifestURL := os.Getenv("KRUN_MANIFEST_URL")
	if manifestURL == "" {
		var err error
		manifestURL, err = releaseManifestURL(version)
		if err != nil {
			return nil, err
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(manifestURL) //nolint:gosec // URL is a trusted release endpoint or explicit user override.
	if err != nil {
		return nil, fmt.Errorf("fetch manifest from %s: %w", manifestURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch manifest from %s: status %d", manifestURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read manifest response: %w", err)
	}

	return deploy.DecodeManifestObjects(body)
}

func releaseManifestURL(version string) (string, error) {
	if version == "" {
		return "", fmt.Errorf("release version is empty")
	}
	if version != strings.TrimSpace(version) {
		return "", fmt.Errorf("invalid release version %q", version)
	}
	if _, err := semver.Parse(version); err != nil {
		return "", fmt.Errorf("invalid release version %q", version)
	}

	escapedVersion := url.PathEscape(version)
	return "https://github.com/ftechmax/krun/releases/download/" + escapedVersion + "/krun-traffic-manager.yaml", nil
}

func printStatusValue(label string, value string, color utils.Color) {
	fmt.Printf("%s %s\n", label, utils.Colorize(value, color))
}
