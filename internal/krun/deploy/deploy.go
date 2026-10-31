package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/kube"
	"github.com/ftechmax/krun/internal/utils"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/kustomize/api/krusty"
	kusttypes "sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

const (
	fieldManager          = "krun"
	restartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"
)

func Deploy(projectName string, config cfg.Config, restart bool, createNamespace bool) {
	fmt.Printf("Deploying %s (use remote registry: %v)\n", projectName, config.Registry == config.RemoteRegistry)

	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		utils.Errf("Failed to create Kubernetes client: %s", err.Error())
		return
	}

	targets, err := handle(context.Background(), client, config, projectName, false, createNamespace)
	if err != nil {
		utils.Errf("Deploy failed: %s", err.Error())
		return
	}

	if restart && len(targets) > 0 {
		if err := restartWorkloads(context.Background(), client, targets); err != nil {
			utils.Errf("Restart failed: %s", err.Error())
		}
	}
}

func Delete(projectName string, config cfg.Config) {
	fmt.Printf("Deleting %s\n", projectName)
	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		utils.Errf("Failed to create Kubernetes client: %s", err.Error())
		return
	}

	if _, err := handle(context.Background(), client, config, projectName, true, false); err != nil {
		utils.Errf("Delete failed: %s", err.Error())
	}
}

func handle(ctx context.Context, client *kube.Client, config cfg.Config, projectName string, deleteMode bool, createNamespace bool) ([]restartTarget, error) {
	overlayPath, err := resolveOverlayPath(config, projectName)
	if err != nil {
		return nil, err
	}

	objs, err := RenderKustomizeObjects(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("render kustomize %s: %w", overlayPath, err)
	}
	replaceRegistryInObjects(objs, config.LocalRegistry, config.Registry)
	if deleteMode {
		return nil, DeleteObjects(ctx, client, objs)
	}
	if createNamespace {
		if err := ensureNamespaces(ctx, client, objs); err != nil {
			return nil, err
		}
	}
	if err := ApplyObjects(ctx, client, objs); err != nil {
		return nil, err
	}
	return collectRestartTargets(objs), nil
}

func resolveOverlayPath(config cfg.Config, projectName string) (string, error) {
	switch config.Environment {
	case "local", "development", "production":
	default:
		return "", fmt.Errorf("unknown environment %q: use local, development, or production", config.Environment)
	}
	return filepath.Join(config.Path, config.ProjectPaths[projectName], "k8s", "overlays", config.Environment), nil
}

func RenderKustomizeObjects(overlayPath string) ([]*unstructured.Unstructured, error) {
	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = kusttypes.LoadRestrictionsNone

	k := krusty.MakeKustomizer(opts)
	resMap, err := k.Run(filesys.MakeFsOnDisk(), overlayPath)
	if err != nil {
		return nil, err
	}

	objects := make([]*unstructured.Unstructured, 0, resMap.Size())
	for _, resource := range resMap.Resources() {
		fields, err := resource.Map()
		if err != nil {
			return nil, err
		}
		// Kubernetes unstructured objects use int64 for integer fields.
		fields, err = runtime.DefaultUnstructuredConverter.ToUnstructured(&fields)
		if err != nil {
			return nil, err
		}
		objects = append(objects, &unstructured.Unstructured{Object: fields})
	}
	return objects, nil
}

func DecodeManifestObjects(manifestYAML []byte) ([]*unstructured.Unstructured, error) {
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifestYAML), 4096)
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

func replaceRegistryInObjects(objs []*unstructured.Unstructured, from, to string) {
	if from == "" || from == to {
		return
	}

	for _, obj := range objs {
		obj.Object = replaceRegistryInValue(obj.Object, from, to).(map[string]any)
	}
}

func replaceRegistryInValue(value any, from, to string) any {
	switch typed := value.(type) {
	case map[string]any:
		for k, v := range typed {
			typed[k] = replaceRegistryInValue(v, from, to)
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = replaceRegistryInValue(typed[i], from, to)
		}
		return typed
	case string:
		return strings.ReplaceAll(typed, from, to)
	default:
		return value
	}
}

// ensureNamespaces creates missing target namespaces for an explicitly
// requested --create-namespace deployment.
func ensureNamespaces(ctx context.Context, client *kube.Client, objs []*unstructured.Unstructured) error {
	provided := map[string]bool{}
	for _, obj := range objs {
		if obj.GetKind() == "Namespace" {
			provided[strings.TrimSpace(obj.GetName())] = true
		}
	}

	for _, obj := range objs {
		namespace := obj.GetNamespace()
		if namespace == "" || provided[namespace] {
			continue
		}
		provided[namespace] = true
		_, err := client.Clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		}, metav1.CreateOptions{})
		if k8serrors.IsAlreadyExists(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create namespace %s: %w", namespace, err)
		}
		fmt.Printf("Created namespace %s\n", namespace)
	}
	return nil
}

func ApplyObjects(ctx context.Context, client *kube.Client, objs []*unstructured.Unstructured) error {
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
			FieldManager: fieldManager,
			Force:        &force,
		}); err != nil {
			return fmt.Errorf("apply %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}

	return nil
}

func DeleteObjects(ctx context.Context, client *kube.Client, objs []*unstructured.Unstructured) error {
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

type restartTarget struct {
	gvk       schema.GroupVersionKind
	name      string
	namespace string
}

func collectRestartTargets(objs []*unstructured.Unstructured) []restartTarget {
	targets := make([]restartTarget, 0)
	seen := map[restartTarget]struct{}{}

	for _, obj := range objs {
		switch obj.GetKind() {
		case "Deployment", "StatefulSet":
		default:
			continue
		}
		if obj.GetName() == "" {
			continue
		}

		target := restartTarget{
			gvk:       obj.GroupVersionKind(),
			name:      obj.GetName(),
			namespace: obj.GetNamespace(),
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}

	return targets
}

func restartWorkloads(ctx context.Context, client *kube.Client, targets []restartTarget) error {
	restartedAt := time.Now().UTC().Format(time.RFC3339)
	patchData, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{
						restartedAtAnnotation: restartedAt,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal restart patch: %w", err)
	}

	for _, target := range targets {
		mapping, err := client.Mapper.RESTMapping(target.gvk.GroupKind(), target.gvk.Version)
		if err != nil {
			return fmt.Errorf("resolve resource mapping for %s: %w", target.gvk.String(), err)
		}

		ri := resourceInterface(client.DynamicClient, mapping, target.namespace)

		if _, err := ri.Patch(ctx, target.name, types.MergePatchType, patchData, metav1.PatchOptions{}); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("restart %s %s: %w", target.gvk.Kind, target.name, err)
		}
	}

	return nil
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
