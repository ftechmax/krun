package build

import (
	"context"
	"fmt"
	"time"

	deploy "github.com/ftechmax/krun/internal/krun/deploy"
	"github.com/ftechmax/krun/internal/kube"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"
)

const buildkitImage = "moby/buildkit:v0.25.1-rootless"

// Rootless BuildKit needs seccomp/AppArmor unconfined for its user namespace.
const manifest = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: docker-build-buildkitd
  namespace: default
data:
  buildkitd.toml: |
    [registry."registry:5000"]
      http = true
      insecure = true
---
apiVersion: v1
kind: Pod
metadata:
  labels:
    app: docker-build
  name: docker-build
  namespace: default
spec:
  automountServiceAccountToken: false
  securityContext:
    fsGroup: 1000
  containers:
  - image: ` + buildkitImage + `
    name: docker-build
    args: ["--oci-worker-no-process-sandbox"]
    securityContext:
      runAsUser: 1000
      runAsGroup: 1000
      seccompProfile:
        type: Unconfined
      appArmorProfile:
        type: Unconfined
    readinessProbe:
      exec:
        command: ["buildctl", "debug", "workers"]
      initialDelaySeconds: 2
      periodSeconds: 3
    resources:
      requests:
        cpu: 100m
        memory: 256Mi
        ephemeral-storage: 1Gi
    volumeMounts:
      - mountPath: /var/workspace
        name: workspace
      - mountPath: /home/user/.local/share/buildkit
        name: cache
      - mountPath: /home/user/.config/buildkit
        name: buildkitd-config
  volumes:
  - name: workspace
    emptyDir:
      sizeLimit: 10Gi
  - name: cache
    emptyDir:
      sizeLimit: 20Gi
  - name: buildkitd-config
    configMap:
      name: docker-build-buildkitd
`

func buildPodExists(client *kube.Client) (bool, error) {
	pod, err := client.Clientset.CoreV1().Pods("default").Get(context.Background(), buildPodName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get build pod: %w", err)
	}

	return pod.Status.Phase == corev1.PodRunning, nil
}

func createBuildPod(client *kube.Client) error {
	pod, err := client.Clientset.CoreV1().Pods("default").Get(context.Background(), buildPodName, metav1.GetOptions{})
	switch {
	case k8serrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("get build pod: %w", err)
	case pod.DeletionTimestamp != nil:
		fmt.Println("\033[33mWaiting for previous build pod to terminate\033[0m")
		if err := waitForBuildPodGone(client, 90*time.Second); err != nil {
			return err
		}
	case pod.Status.Phase == corev1.PodRunning:
		return nil
	}

	fmt.Println("\033[32mCreating build container\033[0m")
	objs, err := deploy.DecodeManifestObjects([]byte(manifest))
	if err != nil {
		return fmt.Errorf("failed to decode build pod manifest: %w", err)
	}
	if err := deploy.ApplyObjects(context.Background(), client, objs); err != nil {
		return fmt.Errorf("failed to create build pod: %w", err)
	}

	if err := waitForBuildPodReady(client, 90*time.Second); err != nil {
		return fmt.Errorf("failed to wait for build pod to be ready: %w", err)
	}

	return nil
}

func deleteBuildPod(client *kube.Client) error {
	objs, err := deploy.DecodeManifestObjects([]byte(manifest))
	if err != nil {
		return fmt.Errorf("failed to decode build pod manifest: %w", err)
	}
	if err := deploy.DeleteObjects(context.Background(), client, objs); err != nil {
		return fmt.Errorf("failed to initiate build pod deletion: %w", err)
	}
	return nil
}

// A terminating pod still reports phase Running, so wait for NotFound.
func waitForBuildPodGone(client *kube.Client, timeout time.Duration) error {
	err := wait.PollUntilContextTimeout(context.Background(), 750*time.Millisecond, timeout, true,
		func(ctx context.Context) (bool, error) {
			_, err := client.Clientset.CoreV1().Pods("default").Get(ctx, buildPodName, metav1.GetOptions{})
			if k8serrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, fmt.Errorf("get build pod: %w", err)
			}
			return false, nil
		})
	if wait.Interrupted(err) {
		return fmt.Errorf("timed out waiting for build pod deletion")
	}
	return err
}

func waitForBuildPodReady(client *kube.Client, timeout time.Duration) error {
	lw := cache.NewListWatchFromClient(
		client.Clientset.CoreV1().RESTClient(),
		"pods",
		"default",
		fields.OneTermEqualSelector("metadata.name", buildPodName),
	)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, err := watchtools.UntilWithSync(ctx, lw, &corev1.Pod{}, nil, func(e watch.Event) (bool, error) {
		pod, ok := e.Object.(*corev1.Pod)
		if !ok {
			return false, nil
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("wait for pod ready: %w", err)
	}
	return nil
}
