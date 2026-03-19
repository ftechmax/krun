package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWorkloadInjectorInjectAndRemove(t *testing.T) {
	cases := []struct {
		name string
		kind workloadKind
	}{
		{name: "deployment", kind: workloadKindDeployment},
		{name: "statefulset", kind: workloadKindStatefulSet},
		{name: "daemonset", kind: workloadKindDaemonSet},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(newTestWorkload(tc.kind, "default", "orders-api"))
			injector := NewWorkloadInjector(client, Options{
				Image:           "registry.local/krun-traffic-agent:latest",
				ImagePullPolicy: string(corev1.PullAlways),
				ManagerAddress:  "http://manager:8080",
			})

			session := contracts.DebugSession{
				SessionID:    "session-1",
				SessionToken: "token-1",
				Namespace:    "default",
				ServiceName:  "orders-api",
				Workload:     "orders-api",
				ServicePort:  8080,
			}

			if err := injector.Inject(context.Background(), session); err != nil {
				t.Fatalf("inject sidecar: %v", err)
			}

			containers := getWorkloadContainers(t, client, tc.kind, "default", "orders-api")
			if len(containers) != 2 {
				t.Fatalf("expected 2 containers, got %d", len(containers))
			}
			sidecar := containers[1]
			if sidecar.Name != DefaultContainerName {
				t.Fatalf("expected sidecar name %q, got %q", DefaultContainerName, sidecar.Name)
			}
			if sidecar.Image != "registry.local/krun-traffic-agent:latest" {
				t.Fatalf("unexpected sidecar image: %q", sidecar.Image)
			}
			if sidecar.ImagePullPolicy != corev1.PullAlways {
				t.Fatalf("unexpected pull policy: %q", sidecar.ImagePullPolicy)
			}
			if len(sidecar.Env) == 0 || sidecar.Env[0].Name != "KRUN_SESSION_ID" || sidecar.Env[0].Value != "session-1" {
				t.Fatalf("expected session env vars to be set, got %+v", sidecar.Env)
			}
			annotations := getWorkloadLabels(t, client, tc.kind, "default", "orders-api")
			if annotations[InjectedLabelKey] != "true" {
				t.Fatalf("expected injected label to be set, got %v", annotations)
			}

			if err := injector.Remove(context.Background(), session); err != nil {
				t.Fatalf("remove sidecar: %v", err)
			}
			containers = getWorkloadContainers(t, client, tc.kind, "default", "orders-api")
			if len(containers) != 1 {
				t.Fatalf("expected only app container after remove, got %d", len(containers))
			}
			if containers[0].Name != "app" {
				t.Fatalf("unexpected remaining container: %q", containers[0].Name)
			}
			annotations = getWorkloadLabels(t, client, tc.kind, "default", "orders-api")
			if _, exists := annotations[InjectedLabelKey]; exists {
				t.Fatalf("expected injected label to be removed, got %v", annotations)
			}
		})
	}
}

func TestWorkloadInjectorCleanupRemovesAnnotatedAgents(t *testing.T) {
	deployment := newTestDeployment("default", "orders-api")
	deployment.Labels[InjectedLabelKey] = "true"
	deployment.Spec.Template.Spec.Containers = append(deployment.Spec.Template.Spec.Containers, corev1.Container{
		Name:  DefaultContainerName,
		Image: "agent:latest",
	})

	statefulSet := newTestStatefulSet("team-a", "billing-api")
	statefulSet.Labels[InjectedLabelKey] = "true"
	statefulSet.Spec.Template.Spec.Containers = append(statefulSet.Spec.Template.Spec.Containers, corev1.Container{
		Name:  DefaultContainerName,
		Image: "agent:latest",
	})

	daemonSet := newTestDaemonSet("team-b", "node-proxy")
	daemonSet.Labels[InjectedLabelKey] = "true"

	unannotatedDeployment := newTestDeployment("default", "legacy-api")
	unannotatedDeployment.Spec.Template.Spec.Containers = append(unannotatedDeployment.Spec.Template.Spec.Containers, corev1.Container{
		Name:  DefaultContainerName,
		Image: "agent:latest",
	})

	client := fake.NewSimpleClientset(deployment, statefulSet, daemonSet, unannotatedDeployment)
	injector := NewWorkloadInjector(client, Options{})

	if err := injector.Cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup injected sidecars: %v", err)
	}

	containers := getWorkloadContainers(t, client, workloadKindDeployment, "default", "orders-api")
	if len(containers) != 1 || containers[0].Name != "app" {
		t.Fatalf("expected deployment sidecar to be removed, got %+v", containers)
	}
	annotations := getWorkloadLabels(t, client, workloadKindDeployment, "default", "orders-api")
	if _, exists := annotations[InjectedLabelKey]; exists {
		t.Fatalf("expected deployment annotation to be removed, got %v", annotations)
	}

	containers = getWorkloadContainers(t, client, workloadKindStatefulSet, "team-a", "billing-api")
	if len(containers) != 1 || containers[0].Name != "app" {
		t.Fatalf("expected statefulset sidecar to be removed, got %+v", containers)
	}
	annotations = getWorkloadLabels(t, client, workloadKindStatefulSet, "team-a", "billing-api")
	if _, exists := annotations[InjectedLabelKey]; exists {
		t.Fatalf("expected statefulset annotation to be removed, got %v", annotations)
	}

	containers = getWorkloadContainers(t, client, workloadKindDaemonSet, "team-b", "node-proxy")
	if len(containers) != 1 || containers[0].Name != "app" {
		t.Fatalf("expected daemonset app container to remain unchanged, got %+v", containers)
	}
	annotations = getWorkloadLabels(t, client, workloadKindDaemonSet, "team-b", "node-proxy")
	if _, exists := annotations[InjectedLabelKey]; exists {
		t.Fatalf("expected daemonset annotation to be removed, got %v", annotations)
	}

	containers = getWorkloadContainers(t, client, workloadKindDeployment, "default", "legacy-api")
	if len(containers) != 2 {
		t.Fatalf("expected unannotated workload to remain unchanged, got %+v", containers)
	}
}

func TestWorkloadInjectorLookupOrder(t *testing.T) {
	client := fake.NewSimpleClientset(
		newTestDeployment("default", "orders-api"),
		newTestStatefulSet("default", "orders-api"),
		newTestDaemonSet("default", "orders-api"),
	)
	injector := NewWorkloadInjector(client, Options{})
	session := contracts.DebugSession{
		SessionID:    "session-1",
		SessionToken: "token-1",
		Namespace:    "default",
		ServiceName:  "orders-api",
		Workload:     "orders-api",
		ServicePort:  8080,
	}

	if err := injector.Inject(context.Background(), session); err != nil {
		t.Fatalf("inject sidecar: %v", err)
	}

	deploymentContainers := getWorkloadContainers(t, client, workloadKindDeployment, "default", "orders-api")
	statefulSetContainers := getWorkloadContainers(t, client, workloadKindStatefulSet, "default", "orders-api")
	daemonSetContainers := getWorkloadContainers(t, client, workloadKindDaemonSet, "default", "orders-api")
	if len(deploymentContainers) != 2 {
		t.Fatalf("expected deployment to be updated first, got %d containers", len(deploymentContainers))
	}
	if len(statefulSetContainers) != 1 {
		t.Fatalf("expected statefulset to remain unchanged, got %d containers", len(statefulSetContainers))
	}
	if len(daemonSetContainers) != 1 {
		t.Fatalf("expected daemonset to remain unchanged, got %d containers", len(daemonSetContainers))
	}
}

func TestWorkloadInjectorInjectMissingWorkload(t *testing.T) {
	client := fake.NewSimpleClientset()
	injector := NewWorkloadInjector(client, Options{})
	err := injector.Inject(context.Background(), contracts.DebugSession{
		Namespace:   "default",
		ServiceName: "missing",
		Workload:    "missing",
		ServicePort: 8080,
	})
	if !errors.Is(err, ErrWorkloadNotFound) {
		t.Fatalf("expected ErrWorkloadNotFound, got %v", err)
	}
}

func TestWorkloadInjectorDefaults(t *testing.T) {
	client := fake.NewSimpleClientset(newTestDeployment("default", "orders-api"))
	injector := NewWorkloadInjector(client, Options{})
	session := contracts.DebugSession{
		SessionID:    "session-1",
		SessionToken: "token-1",
		Namespace:    "default",
		ServiceName:  "orders-api",
		Workload:     "orders-api",
		ServicePort:  8080,
	}
	if err := injector.Inject(context.Background(), session); err != nil {
		t.Fatalf("inject sidecar with defaults: %v", err)
	}
	containers := getWorkloadContainers(t, client, workloadKindDeployment, "default", "orders-api")
	sidecar := containers[1]
	if sidecar.Image != DefaultImage {
		t.Fatalf("expected default image %q, got %q", DefaultImage, sidecar.Image)
	}
	if sidecar.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Fatalf("expected default pull policy %q, got %q", corev1.PullIfNotPresent, sidecar.ImagePullPolicy)
	}
}

func getWorkloadContainers(t *testing.T, client *fake.Clientset, kind workloadKind, namespace string, name string) []corev1.Container {
	t.Helper()
	switch kind {
	case workloadKindDeployment:
		deployment, err := client.AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get deployment: %v", err)
		}
		return deployment.Spec.Template.Spec.Containers
	case workloadKindStatefulSet:
		statefulSet, err := client.AppsV1().StatefulSets(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get statefulset: %v", err)
		}
		return statefulSet.Spec.Template.Spec.Containers
	case workloadKindDaemonSet:
		daemonSet, err := client.AppsV1().DaemonSets(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get daemonset: %v", err)
		}
		return daemonSet.Spec.Template.Spec.Containers
	default:
		t.Fatalf("unsupported workload kind: %s", kind)
		return nil
	}
}

func getWorkloadLabels(t *testing.T, client *fake.Clientset, kind workloadKind, namespace string, name string) map[string]string {
	t.Helper()
	switch kind {
	case workloadKindDeployment:
		deployment, err := client.AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get deployment: %v", err)
		}
		return deployment.Labels
	case workloadKindStatefulSet:
		statefulSet, err := client.AppsV1().StatefulSets(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get statefulset: %v", err)
		}
		return statefulSet.Labels
	case workloadKindDaemonSet:
		daemonSet, err := client.AppsV1().DaemonSets(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get daemonset: %v", err)
		}
		return daemonSet.Labels
	default:
		t.Fatalf("unsupported workload kind: %s", kind)
		return nil
	}
}

func newTestWorkload(kind workloadKind, namespace string, name string) runtime.Object {
	switch kind {
	case workloadKindDeployment:
		return newTestDeployment(namespace, name)
	case workloadKindStatefulSet:
		return newTestStatefulSet(namespace, name)
	case workloadKindDaemonSet:
		return newTestDaemonSet(namespace, name)
	default:
		panic("unsupported workload kind")
	}
}

func newTestDeployment(namespace string, name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{},
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
							Image: "app:latest",
						},
					},
				},
			},
		},
	}
}

func newTestStatefulSet(namespace string, name string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: name,
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
							Image: "app:latest",
						},
					},
				},
			},
		},
	}
}

func newTestDaemonSet(namespace string, name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{},
		},
		Spec: appsv1.DaemonSetSpec{
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
							Image: "app:latest",
						},
					},
				},
			},
		},
	}
}
