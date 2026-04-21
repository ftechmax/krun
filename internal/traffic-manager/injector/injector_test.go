package injector

import (
	"context"
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newFixture(t *testing.T, target *appsv1.Deployment) (*Injector, *fake.Clientset) {
	t.Helper()
	cs := fake.NewClientset(target)
	inj := &Injector{
		Clientset:       cs,
		AgentImage:      "registry/krun-traffic-agent:test",
		AgentPullPolicy: corev1.PullIfNotPresent,
		ManagerAddress:  "krun-traffic-manager.krun-system.svc:8080",
		AgentListenPort: DefaultAgentListenPort,
	}
	return inj, cs
}

func baseDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "smoke-app", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "smoke-app", Image: "smoke-app:latest"},
					},
				},
			},
		},
	}
}

func session() contracts.DebugSession {
	return contracts.DebugSession{
		SessionID:    "sess-1",
		SessionToken: "tok-1",
		Namespace:    "default",
		Workload:     "smoke-app",
		ServiceName:  "smoke-app",
		ServicePort:  80,
	}
}

func TestInjectAddsSidecar(t *testing.T) {
	inj, cs := newFixture(t, baseDeployment())
	if err := inj.Inject(context.Background(), session()); err != nil {
		t.Fatalf("inject: %v", err)
	}

	dep, err := cs.AppsV1().Deployments("default").Get(context.Background(), "smoke-app", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := len(dep.Spec.Template.Spec.Containers); got != 2 {
		t.Fatalf("want 2 containers, got %d", got)
	}
	side := dep.Spec.Template.Spec.Containers[1]
	if side.Name != SidecarContainerName {
		t.Fatalf("sidecar name=%q", side.Name)
	}
	wantEnv := map[string]string{
		envManagerAddress:  inj.ManagerAddress,
		envSessionID:       "sess-1",
		envSessionToken:    "tok-1",
		envTargetPort:      "80",
		envAgentListenPort: "15001",
	}
	for k, v := range wantEnv {
		if got := envValue(side.Env, k); got != v {
			t.Fatalf("env %s=%q want %q", k, got, v)
		}
	}
	if side.SecurityContext == nil || side.SecurityContext.Capabilities == nil {
		t.Fatalf("missing security context capabilities")
	}
	if !hasCapability(side.SecurityContext.Capabilities.Add, "NET_ADMIN") {
		t.Fatalf("missing NET_ADMIN cap")
	}
	if dep.Spec.Template.Annotations[sessionAnnotation] != "sess-1" {
		t.Fatalf("missing session annotation")
	}
}

func TestInjectIsIdempotent(t *testing.T) {
	inj, cs := newFixture(t, baseDeployment())
	if err := inj.Inject(context.Background(), session()); err != nil {
		t.Fatalf("inject 1: %v", err)
	}
	if err := inj.Inject(context.Background(), session()); err != nil {
		t.Fatalf("inject 2: %v", err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(context.Background(), "smoke-app", metav1.GetOptions{})
	if got := len(dep.Spec.Template.Spec.Containers); got != 2 {
		t.Fatalf("want 2 containers after re-inject, got %d", got)
	}
}

func TestRemoveStripsSidecar(t *testing.T) {
	inj, cs := newFixture(t, baseDeployment())
	if err := inj.Inject(context.Background(), session()); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if err := inj.Remove(context.Background(), session()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(context.Background(), "smoke-app", metav1.GetOptions{})
	if got := len(dep.Spec.Template.Spec.Containers); got != 1 {
		t.Fatalf("want 1 container after remove, got %d", got)
	}
	if _, ok := dep.Spec.Template.Annotations[sessionAnnotation]; ok {
		t.Fatalf("annotation not cleared")
	}
}

func TestRemoveMissingDeploymentNoError(t *testing.T) {
	inj, _ := newFixture(t, baseDeployment())
	s := session()
	s.Workload = "does-not-exist"
	if err := inj.Remove(context.Background(), s); err != nil {
		t.Fatalf("remove missing: %v", err)
	}
}

func TestPickListenPortAvoidsTarget(t *testing.T) {
	inj := &Injector{AgentListenPort: DefaultAgentListenPort}
	if got := inj.pickListenPort(DefaultAgentListenPort); got == DefaultAgentListenPort {
		t.Fatalf("expected alt port when target collides, got %d", got)
	}
	if got := inj.pickListenPort(80); got != DefaultAgentListenPort {
		t.Fatalf("expected default port, got %d", got)
	}
}

func envValue(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func hasCapability(caps []corev1.Capability, name corev1.Capability) bool {
	for _, c := range caps {
		if c == name {
			return true
		}
	}
	return false
}
