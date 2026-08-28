package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to create Kubernetes client: %s", err.Error()), utils.Red))
		return
	}

	targets, err := handle(context.Background(), client, config, projectName, false, createNamespace)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Deploy failed: %s", err.Error()), utils.Red))
		return
	}

	if restart && len(targets) > 0 {
		if err := restartWorkloads(context.Background(), client, targets); err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("Restart failed: %s", err.Error()), utils.Red))
		}
	}
}

func Delete(projectName string, config cfg.Config) {
	fmt.Printf("Deleting %s\n", projectName)
	client, err := kube.NewClient(config.KubeConfig)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to create Kubernetes client: %s", err.Error()), utils.Red))
		return
	}

	if _, err := handle(context.Background(), client, config, projectName, true, false); err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Delete failed: %s", err.Error()), utils.Red))
	}
}

func handle(ctx context.Context, client *kube.Client, config cfg.Config, projectName string, deleteMode bool, createNamespace bool) ([]restartTarget, error) {
	overlayPaths, err := resolveOverlayPaths(config, projectName)
	if err != nil {
		return nil, err
	}

	var restartCandidates []*unstructured.Unstructured
	for _, overlayPath := range overlayPaths {
		objs, err := RenderKustomizeObjects(overlayPath)
		if err != nil {
			return nil, fmt.Errorf("render kustomize %s: %w", overlayPath, err)
		}

		replaceRegistryInObjects(objs, config.LocalRegistry, config.Registry)
		if deleteMode {
			if err := DeleteObjects(ctx, client, objs); err != nil {
				return nil, fmt.Errorf("delete objects from %s: %w", overlayPath, err)
			}
			continue
		}

		if createNamespace {
			if err := EnsureNamespaces(ctx, client, objs); err != nil {
				return nil, err
			}
		}

		if err := ApplyObjects(ctx, client, objs); err != nil {
			return nil, fmt.Errorf("apply objects from %s: %w", overlayPath, err)
		}
		restartCandidates = append(restartCandidates, objs...)
	}

	return collectRestartTargets(restartCandidates), nil
}

func resolveOverlayPaths(config cfg.Config, projectName string) ([]string, error) {
	projectRelPath := projectName
	if config.ProjectPaths != nil {
		if p, ok := config.ProjectPaths[projectName]; ok && p != "" {
			projectRelPath = p
		}
	}

	servicePath := filepath.Join(config.KrunSourceConfig.Path, projectRelPath)
	k8sPath := filepath.Join(servicePath, "k8s")
	k8sCloudPath := filepath.Join(k8sPath, "cloud")
	k8sEdgePath := filepath.Join(k8sPath, "edge")

	if isDir(k8sCloudPath) {
		overlayPaths := []string{
			filepath.Join(k8sCloudPath, "overlays", "local"),
			filepath.Join(k8sEdgePath, "overlays", "local"),
		}
		for _, p := range overlayPaths {
			if !isDir(p) {
				return nil, fmt.Errorf("kustomize overlay not found: %s", p)
			}
		}
		return overlayPaths, nil
	}

	localOverlay := filepath.Join(k8sPath, "overlays", "local")
	if isDir(localOverlay) {
		return []string{localOverlay}, nil
	}

	if hasKustomization(k8sPath) {
		return []string{k8sPath}, nil
	}

	return nil, fmt.Errorf("kustomize overlay not found: %s", localOverlay)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func hasKustomization(dir string) bool {
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func RenderKustomizeObjects(overlayPath string) ([]*unstructured.Unstructured, error) {
	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = kusttypes.LoadRestrictionsNone

	k := krusty.MakeKustomizer(opts)
	resMap, err := k.Run(filesys.MakeFsOnDisk(), overlayPath)
	if err != nil {
		return nil, err
	}

	manifestYAML, err := resMap.AsYaml()
	if err != nil {
		return nil, fmt.Errorf("render manifest yaml: %w", err)
	}

	return DecodeManifestObjects(manifestYAML)
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

// EnsureNamespaces creates missing target namespaces for an explicitly
// requested --create-namespace deployment.
func EnsureNamespaces(ctx context.Context, client *kube.Client, objs []*unstructured.Unstructured) error {
	provided := map[string]bool{}
	for _, obj := range objs {
		if obj.GetKind() == "Namespace" {
			provided[strings.TrimSpace(obj.GetName())] = true
		}
	}

	seen := map[string]bool{}
	var required []string
	for _, obj := range objs {
		namespace := strings.TrimSpace(obj.GetNamespace())
		if namespace == "" || provided[namespace] || seen[namespace] {
			continue
		}
		seen[namespace] = true
		required = append(required, namespace)
	}
	sort.Strings(required)

	var missing []string
	for _, namespace := range required {
		_, err := client.Clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
		if err == nil {
			continue
		}
		if !k8serrors.IsNotFound(err) {
			if k8serrors.IsForbidden(err) {
				return fmt.Errorf("check namespace %s: access forbidden; grant get access to namespaces or create it before deploying: %w", namespace, err)
			}
			return fmt.Errorf("check namespace %s: %w", namespace, err)
		}
		missing = append(missing, namespace)
	}
	if len(missing) == 0 {
		return nil
	}

	for _, namespace := range missing {
		_, err := client.Clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		}, metav1.CreateOptions{})
		if k8serrors.IsAlreadyExists(err) {
			continue
		}
		if k8serrors.IsForbidden(err) {
			return fmt.Errorf("create namespace %s: access forbidden; grant create access to namespaces or run kubectl create namespace %s: %w", namespace, namespace, err)
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
	seen := map[string]struct{}{}

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
		key := fmt.Sprintf("%s|%s|%s|%s|%s", target.gvk.Group, target.gvk.Version, target.gvk.Kind, target.namespace, target.name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
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
		if _, err := ri.Get(ctx, target.name, metav1.GetOptions{}); err != nil {
			if k8serrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("check %s %s exists: %w", target.gvk.Kind, target.name, err)
		}

		if _, err := ri.Patch(ctx, target.name, types.MergePatchType, patchData, metav1.PatchOptions{}); err != nil {
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
