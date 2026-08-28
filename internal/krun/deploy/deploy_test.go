package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/kube"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var (
	configMapGVK = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}
	deployGVK    = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	stsGVK       = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}

	configMapGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	deployGVR    = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	stsGVR       = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
)

func TestResolveOverlayPathsDefault(t *testing.T) {
	root := t.TempDir()
	overlay := filepath.Join(root, "apps", "awesome-api", "k8s", "overlays", "local")
	mkdirAll(t, overlay)

	config := cfg.Config{
		KrunConfig: cfg.KrunConfig{
			KrunSourceConfig: cfg.KrunSourceConfig{Path: root},
		},
		ProjectPaths: map[string]string{
			"awesome-api": "apps/awesome-api",
		},
	}

	paths, err := resolveOverlayPaths(config, "awesome-api")
	if err != nil {
		t.Fatalf("resolveOverlayPaths returned error: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 overlay path, got %d", len(paths))
	}
	if paths[0] != overlay {
		t.Fatalf("unexpected overlay path: got %q want %q", paths[0], overlay)
	}
}

func TestResolveOverlayPathsCloudEdge(t *testing.T) {
	root := t.TempDir()
	cloudOverlay := filepath.Join(root, "svc", "k8s", "cloud", "overlays", "local")
	edgeOverlay := filepath.Join(root, "svc", "k8s", "edge", "overlays", "local")
	mkdirAll(t, cloudOverlay)
	mkdirAll(t, edgeOverlay)

	config := cfg.Config{
		KrunConfig: cfg.KrunConfig{
			KrunSourceConfig: cfg.KrunSourceConfig{Path: root},
		},
	}

	paths, err := resolveOverlayPaths(config, "svc")
	if err != nil {
		t.Fatalf("resolveOverlayPaths returned error: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 overlay paths, got %d", len(paths))
	}
	if paths[0] != cloudOverlay || paths[1] != edgeOverlay {
		t.Fatalf("unexpected overlay paths: %#v", paths)
	}
}

func TestResolveOverlayPathsK8sRoot(t *testing.T) {
	root := t.TempDir()
	k8sDir := filepath.Join(root, "svc", "k8s")
	mkdirAll(t, k8sDir)
	writeFile(t, filepath.Join(k8sDir, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)

	config := cfg.Config{
		KrunConfig: cfg.KrunConfig{
			KrunSourceConfig: cfg.KrunSourceConfig{Path: root},
		},
	}

	paths, err := resolveOverlayPaths(config, "svc")
	if err != nil {
		t.Fatalf("resolveOverlayPaths returned error: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 overlay path, got %d", len(paths))
	}
	if paths[0] != k8sDir {
		t.Fatalf("unexpected overlay path: got %q want %q", paths[0], k8sDir)
	}
}

func TestResolveOverlayPathsMissingOverlay(t *testing.T) {
	root := t.TempDir()
	config := cfg.Config{
		KrunConfig: cfg.KrunConfig{
			KrunSourceConfig: cfg.KrunSourceConfig{Path: root},
		},
	}

	_, err := resolveOverlayPaths(config, "missing")
	if err == nil {
		t.Fatalf("expected error for missing overlay, got nil")
	}
}

func TestDecodeManifestObjects(t *testing.T) {
	manifest := []byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
  namespace: default
data:
  key: value
---

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: default
`)

	objs, err := DecodeManifestObjects(manifest)
	if err != nil {
		t.Fatalf("decodeManifestObjects returned error: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(objs))
	}
	if objs[0].GetKind() != "ConfigMap" || objs[1].GetKind() != "Deployment" {
		t.Fatalf("unexpected kinds: %s, %s", objs[0].GetKind(), objs[1].GetKind())
	}
}

func TestReplaceRegistryInObjects(t *testing.T) {
	objs := []*unstructured.Unstructured{
		newDeployment("default", "api", "registry:5000/my-api:latest"),
	}

	replaceRegistryInObjects(objs, "registry:5000", "remote-registry:5001")
	containers, found, err := unstructured.NestedSlice(objs[0].Object, "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("expected one container, err=%v found=%v len=%d", err, found, len(containers))
	}
	first, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected container type: %T", containers[0])
	}
	image, ok := first["image"].(string)
	if !ok {
		t.Fatalf("container image missing or not string")
	}
	if image != "remote-registry:5001/my-api:latest" {
		t.Fatalf("unexpected image after replacement: %q", image)
	}
}

func TestCollectRestartTargets(t *testing.T) {
	objs := []*unstructured.Unstructured{
		newDeployment("default", "api", "registry:5000/api:latest"),
		newDeployment("default", "api", "registry:5000/api:latest"),
		newStatefulSet("default", "db"),
		{
			Object: map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "DaemonSet",
				"metadata": map[string]any{
					"name":      "agent",
					"namespace": "default",
				},
			},
		},
	}

	targets := collectRestartTargets(objs)
	if len(targets) != 2 {
		t.Fatalf("expected 2 restart targets, got %d", len(targets))
	}
}

func TestRenderReplaceAndCollectRestartTargets(t *testing.T) {
	root := t.TempDir()
	overlay := filepath.Join(root, "awesome", "k8s", "overlays", "local")
	mkdirAll(t, overlay)

	writeFile(t, filepath.Join(overlay, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - resources.yaml
`)

	writeFile(t, filepath.Join(overlay, "resources.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: default
data:
  MODE: local
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: awesome-api
  namespace: default
spec:
  selector:
    matchLabels:
      app: awesome-api
  template:
    metadata:
      labels:
        app: awesome-api
    spec:
      containers:
        - name: api
          image: registry:5000/awesome-api:latest
`)

	config := cfg.Config{
		KrunConfig: cfg.KrunConfig{
			LocalRegistry: "registry:5000",
		},
		Registry: "remote-registry:5001",
	}

	objs, err := RenderKustomizeObjects(overlay)
	if err != nil {
		t.Fatalf("renderKustomizeObjects returned error: %v", err)
	}
	replaceRegistryInObjects(objs, config.LocalRegistry, config.Registry)
	targets := collectRestartTargets(objs)
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects from kustomize render, got %d", len(objs))
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 restart target, got %d", len(targets))
	}
	if targets[0].gvk != deployGVK || targets[0].name != "awesome-api" || targets[0].namespace != "default" {
		t.Fatalf("unexpected restart target: %+v", targets[0])
	}

	var deployment *unstructured.Unstructured
	for _, obj := range objs {
		if obj.GetKind() == "Deployment" {
			deployment = obj
			break
		}
	}
	if deployment == nil {
		t.Fatalf("expected rendered deployment object")
	}

	containers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("expected one container, err=%v found=%v len=%d", err, found, len(containers))
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected container value type: %T", containers[0])
	}
	image, _ := container["image"].(string)
	if image != "remote-registry:5001/awesome-api:latest" {
		t.Fatalf("registry replacement not applied, image=%q", image)
	}
}

func TestHandleDefaultDeployMakesNoNamespaceAPICalls(t *testing.T) {
	root := t.TempDir()
	overlay := filepath.Join(root, "app", "k8s", "overlays", "local")
	mkdirAll(t, overlay)
	writeFile(t, filepath.Join(overlay, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - configmap.yaml
`)
	writeFile(t, filepath.Join(overlay, "configmap.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: application
data:
  MODE: local
`)

	client := newFakeKubeClient(seedConfigMap("application", "app-config", map[string]string{"MODE": "old"}))
	client.DynamicClient.(*dynamicfake.FakeDynamicClient).PrependReactor("patch", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, newConfigMap("application", "app-config", map[string]string{"MODE": "local"}), nil
	})
	clientset := kubefake.NewSimpleClientset()
	client.Clientset = clientset
	config := cfg.Config{KrunConfig: cfg.KrunConfig{KrunSourceConfig: cfg.KrunSourceConfig{Path: root}}}

	if _, err := handle(context.Background(), client, config, "app", false, false); err != nil {
		t.Fatalf("handle default deploy: %v", err)
	}
	for _, action := range clientset.Actions() {
		if action.GetResource().Resource == "namespaces" {
			t.Fatalf("default deploy made Namespace API call: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func TestEnsureNamespacesCreatesMissingInSortedOrder(t *testing.T) {
	clientset := kubefake.NewSimpleClientset()
	client := &kube.Client{Clientset: clientset}
	objects := []*unstructured.Unstructured{
		newConfigMap("zeta", "z", nil),
		newConfigMap("alpha", "a", nil),
		newConfigMap("zeta", "duplicate", nil),
	}

	if err := EnsureNamespaces(context.Background(), client, objects); err != nil {
		t.Fatalf("EnsureNamespaces: %v", err)
	}
	var created []string
	for _, action := range clientset.Actions() {
		if action.GetVerb() != "create" || action.GetResource().Resource != "namespaces" {
			continue
		}
		created = append(created, action.(k8stesting.CreateAction).GetObject().(*corev1.Namespace).Name)
	}
	if !slices.Equal(created, []string{"alpha", "zeta"}) {
		t.Fatalf("created namespaces = %v, want [alpha zeta]", created)
	}
}

func TestEnsureNamespacesIgnoresExistingAndManifestNamespaces(t *testing.T) {
	clientset := kubefake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "existing"}})
	client := &kube.Client{Clientset: clientset}
	namespaceObject := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": "supplied"},
	}}
	objects := []*unstructured.Unstructured{
		newConfigMap("existing", "one", nil),
		namespaceObject,
		newConfigMap("supplied", "two", nil),
	}

	if err := EnsureNamespaces(context.Background(), client, objects); err != nil {
		t.Fatalf("EnsureNamespaces: %v", err)
	}
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "create" {
			t.Fatalf("unexpected create action: %+v", action)
		}
		if action.GetVerb() == "get" && action.(k8stesting.GetAction).GetName() == "supplied" {
			t.Fatal("manifest-supplied namespace was queried")
		}
	}
}

func TestEnsureNamespacesIgnoresAlreadyExistsRace(t *testing.T) {
	clientset := kubefake.NewSimpleClientset()
	clientset.PrependReactor("create", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewAlreadyExists(schema.GroupResource{Resource: "namespaces"}, "racing")
	})
	client := &kube.Client{Clientset: clientset}

	if err := EnsureNamespaces(context.Background(), client, []*unstructured.Unstructured{newConfigMap("racing", "cfg", nil)}); err != nil {
		t.Fatalf("EnsureNamespaces: %v", err)
	}
}

func TestEnsureNamespacesReturnsActionableForbiddenErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verb     string
		resource string
		want     string
	}{
		{name: "get", verb: "get", resource: "namespaces", want: "grant get access to namespaces"},
		{name: "create", verb: "create", resource: "namespaces", want: "kubectl create namespace locked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientset := kubefake.NewSimpleClientset()
			clientset.PrependReactor(tc.verb, tc.resource, func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, k8serrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "locked", errors.New("denied"))
			})
			client := &kube.Client{Clientset: clientset}
			err := EnsureNamespaces(context.Background(), client, []*unstructured.Unstructured{newConfigMap("locked", "cfg", nil)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("EnsureNamespaces error = %v, want text %q", err, tc.want)
			}
		})
	}
}

func TestHandleDelete(t *testing.T) {
	root := t.TempDir()
	overlay := filepath.Join(root, "awesome", "k8s", "overlays", "local")
	mkdirAll(t, overlay)

	writeFile(t, filepath.Join(overlay, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - resources.yaml
`)

	writeFile(t, filepath.Join(overlay, "resources.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: default
data:
  MODE: local
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: awesome-api
  namespace: default
`)

	config := cfg.Config{
		KrunConfig: cfg.KrunConfig{
			KrunSourceConfig: cfg.KrunSourceConfig{Path: root},
		},
	}
	client := newFakeKubeClient(
		seedConfigMap("default", "app-config", map[string]string{"MODE": "local"}),
		seedDeployment("default", "awesome-api", "registry:5000/awesome-api:latest"),
	)

	if _, err := handle(context.Background(), client, config, "awesome", true, false); err != nil {
		t.Fatalf("handle delete returned error: %v", err)
	}
	if _, err := client.DynamicClient.Resource(deployGVR).Namespace("default").Get(context.Background(), "awesome-api", metav1.GetOptions{}); !k8serrors.IsNotFound(err) {
		t.Fatalf("expected deployment to be deleted, got err=%v", err)
	}
	if _, err := client.DynamicClient.Resource(configMapGVR).Namespace("default").Get(context.Background(), "app-config", metav1.GetOptions{}); !k8serrors.IsNotFound(err) {
		t.Fatalf("expected configmap to be deleted, got err=%v", err)
	}
}

func TestRestartWorkloadsSkipsMissingAndPatchesExisting(t *testing.T) {
	existing := newDeployment("default", "api", "remote-registry/api:latest")
	if err := unstructured.SetNestedStringMap(existing.Object, map[string]string{
		"existing": "true",
	}, "spec", "template", "metadata", "annotations"); err != nil {
		t.Fatalf("set initial annotations: %v", err)
	}

	client := newFakeKubeClient(existing)
	targets := []restartTarget{
		{gvk: deployGVK, name: "api", namespace: "default"},
		{gvk: stsGVK, name: "db-missing", namespace: "default"},
	}

	if err := restartWorkloads(context.Background(), client, targets); err != nil {
		t.Fatalf("restartWorkloads returned error: %v", err)
	}

	deployment, err := client.DynamicClient.Resource(deployGVR).Namespace("default").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to fetch deployment after restart: %v", err)
	}
	annotations, found, err := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "annotations")
	if err != nil || !found {
		t.Fatalf("expected annotations after restart, err=%v found=%v", err, found)
	}
	if annotations["existing"] != "true" {
		t.Fatalf("existing annotation should be preserved")
	}
	restartedAt := annotations[restartedAtAnnotation]
	if restartedAt == "" {
		t.Fatalf("expected %s annotation to be set", restartedAtAnnotation)
	}
	if _, err := time.Parse(time.RFC3339, restartedAt); err != nil {
		t.Fatalf("restartedAt annotation is not RFC3339: %q", restartedAt)
	}
}

func newFakeKubeClient(objects ...runtime.Object) *kube.Client {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		panic(err)
	}

	mapper := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
		{Group: "apps", Version: "v1"},
	})
	mapper.Add(configMapGVK, apimeta.RESTScopeNamespace)
	mapper.Add(deployGVK, apimeta.RESTScopeNamespace)
	mapper.Add(stsGVK, apimeta.RESTScopeNamespace)

	return &kube.Client{
		Clientset:     kubefake.NewSimpleClientset(),
		DynamicClient: dynamicfake.NewSimpleDynamicClient(scheme, objects...),
		Mapper:        mapper,
	}
}

func newDeployment(namespace, name, image string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]any{
				"template": map[string]any{
					"metadata": map[string]any{},
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name":  "app",
								"image": image,
							},
						},
					},
				},
			},
		},
	}
}

func newConfigMap(namespace, name string, data map[string]string) *unstructured.Unstructured {
	dataMap := map[string]any{}
	for k, v := range data {
		dataMap[k] = v
	}
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"data": dataMap,
		},
	}
}

func seedConfigMap(namespace, name string, data map[string]string) runtime.Object {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: data,
	}
}

func seedDeployment(namespace, name, image string) runtime.Object {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: image,
						},
					},
				},
			},
		},
	}
}

func newStatefulSet(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "StatefulSet",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
		},
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
