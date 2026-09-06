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
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"
)

const manifest = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: docker-build-registries
  namespace: default
data:
  registries.conf: |
    [registries.search]
    registries = ['docker.io']

    [registries.insecure]
    registries = ['registry:5000']
---
apiVersion: v1
kind: Pod
metadata:
  labels:
    app: docker-build
  name: docker-build
  namespace: default
spec:
  securityContext:
    fsGroup: 1001
  containers:
  - image: quay.io/buildah/stable:latest
    name: docker-build
    securityContext:
      privileged: true
      runAsGroup: 1001
    command: ["sleep", "infinity"]
    volumeMounts:
      - mountPath: /var/workspace
        name: workspace
      - mountPath: /etc/containers/registries.conf
        name: docker-build-registries
        subPath: registries.conf
  volumes:
  - name: workspace
    emptyDir: {}
  - name: docker-build-registries
    configMap:
      name: docker-build-registries
      items:
        - key: registries.conf
          path: registries.conf
`

func buildPodExists(kubeConfig string) (bool, error) {
	client, err := kube.NewClient(kubeConfig)
	if err != nil {
		return false, fmt.Errorf("create kube client: %w", err)
	}

	pod, err := client.Clientset.CoreV1().Pods("default").Get(context.Background(), buildPodName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get build pod: %w", err)
	}

	return pod.Status.Phase == corev1.PodRunning, nil
}

func createBuildPod(kubeConfig string) error {
	client, err := kube.NewClient(kubeConfig)
	if err != nil {
		return fmt.Errorf("create kube client: %w", err)
	}

	exists, err := buildPodExists(kubeConfig)
	if err != nil {
		return fmt.Errorf("failed to check if build pod exists: %w", err)
	}
	if exists {
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

func deleteBuildPod(kubeConfig string) error {
	client, err := kube.NewClient(kubeConfig)
	if err != nil {
		return fmt.Errorf("create kube client: %w", err)
	}
	objs, err := deploy.DecodeManifestObjects([]byte(manifest))
	if err != nil {
		return fmt.Errorf("failed to decode build pod manifest: %w", err)
	}
	if err := deploy.DeleteObjects(context.Background(), client, objs); err != nil {
		return fmt.Errorf("failed to initiate build pod deletion: %w", err)
	}
	return nil
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
