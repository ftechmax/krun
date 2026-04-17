package build

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ftechmax/krun/internal/kube"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
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

func buildPodExists(ctx context.Context, kubeConfig string) (bool, error) {
	client, err := kube.NewClient(kubeConfig)
	if err != nil {
		return false, fmt.Errorf("create kube client: %w", err)
	}

	pod, err := client.Clientset.CoreV1().Pods("default").Get(ctx, buildPodName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get build pod: %w", err)
	}

	return pod.Status.Phase == corev1.PodRunning, nil
}

func createBuildPod(ctx context.Context, out io.Writer, kubeConfig string) error {
	client, err := kube.NewClient(kubeConfig)
	if err != nil {
		return fmt.Errorf("create kube client: %w", err)
	}

	exists, err := buildPodExists(ctx, kubeConfig)
	if err != nil {
		return fmt.Errorf("failed to check if build pod exists: %w", err)
	}
	if exists {
		return nil
	}

	fmt.Fprintln(out, "\033[32mCreating build container\033[0m")
	if err := applyManifest(ctx, client, manifest); err != nil {
		return fmt.Errorf("failed to create build pod: %w", err)
	}

	if err := waitForBuildPodReady(ctx, client, 90*time.Second); err != nil {
		return fmt.Errorf("failed to wait for build pod to be ready: %w", err)
	}

	return nil
}

func deleteBuildPod(ctx context.Context, kubeConfig string) error {
	client, err := kube.NewClient(kubeConfig)
	if err != nil {
		return fmt.Errorf("create kube client: %w", err)
	}
	if err := deleteManifest(ctx, client, manifest); err != nil {
		return fmt.Errorf("failed to initiate build pod deletion: %w", err)
	}
	return nil
}

func waitForBuildPodReady(ctx context.Context, client *kube.Client, timeout time.Duration) error {
	lw := cache.NewListWatchFromClient(
		client.Clientset.CoreV1().RESTClient(),
		"pods",
		"default",
		fields.OneTermEqualSelector("metadata.name", buildPodName),
	)

	ctx, cancel := context.WithTimeout(ctx, timeout)
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

func applyManifest(ctx context.Context, client *kube.Client, manifestYAML string) error {
	objs, err := decodeManifestObjects(manifestYAML)
	if err != nil {
		return err
	}

	for _, obj := range objs {
		gvk := obj.GroupVersionKind()
		mapping, err := client.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return fmt.Errorf("resolve resource mapping for %s: %w", gvk.String(), err)
		}

		ri := resourceInterface(client.DynamicClient, mapping, obj.GetNamespace())
		data, err := json.Marshal(obj)
		if err != nil {
			return fmt.Errorf("marshal %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}

		force := true
		if _, err := ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
			FieldManager: "krun",
			Force:        &force,
		}); err != nil {
			return fmt.Errorf("apply %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}

	return nil
}

func deleteManifest(ctx context.Context, client *kube.Client, manifestYAML string) error {
	objs, err := decodeManifestObjects(manifestYAML)
	if err != nil {
		return err
	}

	background := metav1.DeletePropagationBackground
	for _, obj := range objs {
		gvk := obj.GroupVersionKind()
		mapping, err := client.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return fmt.Errorf("resolve resource mapping for %s: %w", gvk.String(), err)
		}

		ri := resourceInterface(client.DynamicClient, mapping, obj.GetNamespace())
		err = ri.Delete(ctx, obj.GetName(), metav1.DeleteOptions{
			PropagationPolicy: &background,
		})
		if err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	return nil
}

func decodeManifestObjects(manifestYAML string) ([]*unstructured.Unstructured, error) {
	decoder := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(manifestYAML), 4096)
	var objs []*unstructured.Unstructured

	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode manifest: %w", err)
		}
		if len(obj.Object) == 0 || obj.GetKind() == "" {
			continue
		}
		objs = append(objs, obj)
	}

	return objs, nil
}

func resourceInterface(
	dynamicClient dynamic.Interface,
	mapping *apimeta.RESTMapping,
	namespace string,
) dynamic.ResourceInterface {
	if mapping.Scope.Name() == apimeta.RESTScopeNameNamespace {
		ns := namespace
		if ns == "" {
			ns = "default"
		}
		return dynamicClient.Resource(mapping.Resource).Namespace(ns)
	}
	return dynamicClient.Resource(mapping.Resource)
}
