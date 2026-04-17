package portforward

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func newTestRegistry() (*SessionRegistry, *startTracker) {
	tracker := &startTracker{}
	r := &SessionRegistry{
		sessions: map[string]map[string]*forwardHandle{},
		shared:   map[string]*sharedForward{},
	}
	r.startForwardFn = tracker.start
	return r, tracker
}

type startTracker struct {
	mu    sync.Mutex
	calls []contracts.PortForward
}

func (s *startTracker) start(forward contracts.PortForward) (*forwardHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, forward)

	done := make(chan struct{})
	close(done)
	return &forwardHandle{
		spec:     forward,
		cancel:   context.CancelFunc(func() {}),
		doneChan: done,
	}, nil
}

func (s *startTracker) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func makeForward(svc string, port int) contracts.PortForward {
	return contracts.PortForward{Namespace: defaultNamespace, Service: svc, LocalPort: port, RemotePort: port}
}

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

func TestUpsertSharesForwardAcrossSessions(t *testing.T) {
	r, tracker := newTestRegistry()
	redis := []contracts.PortForward{makeForward("redis", 6379)}

	if err := r.Upsert("session-a", redis); err != nil {
		t.Fatalf("upsert session-a: %v", err)
	}
	if err := r.Upsert("session-b", redis); err != nil {
		t.Fatalf("upsert session-b: %v", err)
	}

	if tracker.count() != 1 {
		t.Fatalf("expected 1 startForward call, got %d", tracker.count())
	}
	if r.shared[portForwardKey(redis[0])].refCount != 2 {
		t.Fatalf("expected refCount 2, got %d", r.shared[portForwardKey(redis[0])].refCount)
	}
}

func TestRemoveOneSessionKeepsSharedForward(t *testing.T) {
	r, _ := newTestRegistry()
	redis := []contracts.PortForward{makeForward("redis", 6379)}

	r.Upsert("session-a", redis)
	r.Upsert("session-b", redis)

	if err := r.Remove("session-a"); err != nil {
		t.Fatalf("remove session-a: %v", err)
	}

	key := portForwardKey(redis[0])
	sf, ok := r.shared[key]
	if !ok {
		t.Fatal("shared forward was removed while session-b still uses it")
	}
	if sf.refCount != 1 {
		t.Fatalf("expected refCount 1, got %d", sf.refCount)
	}
}

func TestRemoveLastSessionStopsSharedForward(t *testing.T) {
	r, _ := newTestRegistry()
	redis := []contracts.PortForward{makeForward("redis", 6379)}

	r.Upsert("session-a", redis)
	r.Upsert("session-b", redis)
	r.Remove("session-a")
	r.Remove("session-b")

	if len(r.shared) != 0 {
		t.Fatalf("expected shared map to be empty, got %d entries", len(r.shared))
	}
}

func TestClearStopsAllSharedForwards(t *testing.T) {
	r, _ := newTestRegistry()
	redis := []contracts.PortForward{makeForward("redis", 6379)}
	rabbit := []contracts.PortForward{
		makeForward("redis", 6379),
		makeForward("rabbitmq", 5672),
	}

	r.Upsert("session-a", redis)
	r.Upsert("session-b", rabbit)

	if err := r.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}

	if len(r.shared) != 0 {
		t.Fatalf("expected shared map to be empty, got %d entries", len(r.shared))
	}
	if len(r.sessions) != 0 {
		t.Fatalf("expected sessions map to be empty, got %d entries", len(r.sessions))
	}
}

func TestUpsertRollsBackSharedRefsOnStartFailure(t *testing.T) {
	r, _ := newTestRegistry()
	redis := makeForward("redis", 6379)
	bad := makeForward("badservice", 9999)

	// Session A owns redis.
	r.Upsert("session-a", []contracts.PortForward{redis})

	// Override startForwardFn to fail on the bad service.
	r.startForwardFn = func(fwd contracts.PortForward) (*forwardHandle, error) {
		if fwd.Service == "badservice" {
			return nil, fmt.Errorf("simulated failure")
		}
		done := make(chan struct{})
		close(done)
		return &forwardHandle{spec: fwd, cancel: func() {}, doneChan: done}, nil
	}

	// Session B requests redis (shared) + badservice (will fail).
	err := r.Upsert("session-b", []contracts.PortForward{redis, bad})
	if err == nil {
		t.Fatal("expected error from upsert, got nil")
	}

	// Redis shared refCount should remain 1 (rolled back).
	key := portForwardKey(redis)
	if r.shared[key].refCount != 1 {
		t.Fatalf("expected refCount 1 after rollback, got %d", r.shared[key].refCount)
	}

	// Session B should not exist.
	if _, ok := r.sessions["session-b"]; ok {
		t.Fatal("session-b should not exist after failed upsert")
	}
}

func TestUpsertReplacingForwardsReleasesOldShared(t *testing.T) {
	r, _ := newTestRegistry()
	redis := []contracts.PortForward{makeForward("redis", 6379)}
	rabbit := []contracts.PortForward{makeForward("rabbitmq", 5672)}

	r.Upsert("session-a", redis)
	// Replace session-a's forwards: drop redis, add rabbitmq.
	r.Upsert("session-a", rabbit)

	redisKey := portForwardKey(redis[0])
	if _, ok := r.shared[redisKey]; ok {
		t.Fatal("redis shared forward should have been cleaned up")
	}

	rabbitKey := portForwardKey(rabbit[0])
	if _, ok := r.shared[rabbitKey]; !ok {
		t.Fatal("rabbitmq shared forward should exist")
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
