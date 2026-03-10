package portforward

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestSelectPodForPortForwardPrefersReadyRunning(t *testing.T) {
	pod, err := selectPodForPortForward([]corev1.Pod{
		makePod("worker-b", corev1.PodRunning, false),
		makePod("worker-c", corev1.PodRunning, true),
		makePod("worker-a", corev1.PodRunning, true),
	})
	if err != nil {
		t.Fatalf("select pod: %v", err)
	}
	if pod.Name != "worker-a" {
		t.Fatalf("expected worker-a, got %s", pod.Name)
	}
}

func TestSelectPodForPortForwardFallsBackToPending(t *testing.T) {
	pod, err := selectPodForPortForward([]corev1.Pod{
		makePod("done", corev1.PodSucceeded, true),
		makePod("starting", corev1.PodPending, false),
	})
	if err != nil {
		t.Fatalf("select pod: %v", err)
	}
	if pod.Name != "starting" {
		t.Fatalf("expected starting, got %s", pod.Name)
	}
}

func TestResolveServiceTargetPortUsesNamedTargetPort(t *testing.T) {
	service := corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:       80,
					TargetPort: intstr.FromString("http"),
				},
			},
		},
	}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-a"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: 8080},
					},
				},
			},
		},
	}

	targetPort, err := resolveServiceTargetPort(service, pod, 80)
	if err != nil {
		t.Fatalf("resolve target port: %v", err)
	}
	if targetPort != 8080 {
		t.Fatalf("expected 8080, got %d", targetPort)
	}
}

func TestResolveServiceTargetPortFallsBackToRequestedPort(t *testing.T) {
	service := corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:       80,
					TargetPort: intstr.FromInt32(8080),
				},
			},
		},
	}

	targetPort, err := resolveServiceTargetPort(service, corev1.Pod{}, 9090)
	if err != nil {
		t.Fatalf("resolve target port: %v", err)
	}
	if targetPort != 9090 {
		t.Fatalf("expected 9090, got %d", targetPort)
	}
}

func makePod(name string, phase corev1.PodPhase, ready bool) corev1.Pod {
	conditionStatus := corev1.ConditionFalse
	if ready {
		conditionStatus = corev1.ConditionTrue
	}

	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.PodStatus{
			Phase: phase,
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: conditionStatus,
				},
			},
		},
	}
}
