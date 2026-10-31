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
		shared: map[contracts.PortForward]*sharedForward{},
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

func makeForward(ns, svc string, port int) contracts.PortForward {
	return contracts.PortForward{Namespace: ns, Service: svc, LocalPort: port, RemotePort: port}
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
	if pod.Name != "worker-c" {
		t.Fatalf("expected first ready pod worker-c, got %s", pod.Name)
	}
}

func TestSelectPodForPortForwardRequiresRunningPod(t *testing.T) {
	_, err := selectPodForPortForward([]corev1.Pod{
		makePod("done", corev1.PodSucceeded, true),
		makePod("starting", corev1.PodPending, false),
	})
	if err == nil {
		t.Fatal("expected an error when no pod is running")
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

func TestResolveServiceTargetPortRejectsMissingServicePort(t *testing.T) {
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

	if _, err := resolveServiceTargetPort(service, corev1.Pod{}, 9090); err == nil {
		t.Fatal("expected an error for a port the service does not expose")
	}
}

func TestUpsertSharesForwardAcrossSessions(t *testing.T) {
	r, tracker := newTestRegistry()
	redis := []contracts.PortForward{makeForward("default", "redis", 6379)}

	if err := r.Upsert("session-a", redis); err != nil {
		t.Fatalf("upsert session-a: %v", err)
	}
	if err := r.Upsert("session-b", redis); err != nil {
		t.Fatalf("upsert session-b: %v", err)
	}

	if tracker.count() != 1 {
		t.Fatalf("expected 1 startForward call, got %d", tracker.count())
	}
	if len(r.shared[redis[0]].users) != 2 {
		t.Fatalf("expected user count 2, got %d", len(r.shared[redis[0]].users))
	}
}

func TestUpsertDeduplicatesWithinSession(t *testing.T) {
	r, tracker := newTestRegistry()
	redis := makeForward("default", "redis", 6379)
	if err := r.Upsert("session-a", []contracts.PortForward{redis, redis}); err != nil {
		t.Fatal(err)
	}
	if tracker.count() != 1 || len(r.shared[redis].users) != 1 {
		t.Fatal("duplicate dependency acquired the forward twice")
	}
	r.Remove("session-a")
	if len(r.shared) != 0 {
		t.Fatal("duplicate dependency left a forward behind")
	}
}

func TestBoundLocalPortUsesAllocationForOriginalSpec(t *testing.T) {
	r, tracker := newTestRegistry()
	forward := contracts.PortForward{Namespace: "krun-system", Service: "manager", RemotePort: 8080}
	r.startForwardFn = func(spec contracts.PortForward) (*forwardHandle, error) {
		handle, err := tracker.start(spec)
		handle.spec.LocalPort = 39481
		return handle, err
	}
	if err := r.Upsert("manager", []contracts.PortForward{forward}); err != nil {
		t.Fatal(err)
	}
	if port, ok := r.BoundLocalPort("manager", forward); !ok || port != 39481 {
		t.Fatalf("bound port = %d, found = %v", port, ok)
	}
}

func TestRemoveOneSessionKeepsSharedForward(t *testing.T) {
	r, _ := newTestRegistry()
	redis := []contracts.PortForward{makeForward("default", "redis", 6379)}

	r.Upsert("session-a", redis)
	r.Upsert("session-b", redis)

	r.Remove("session-a")

	key := redis[0]
	sf, ok := r.shared[key]
	if !ok {
		t.Fatal("shared forward was removed while session-b still uses it")
	}
	if len(sf.users) != 1 {
		t.Fatalf("expected user count 1, got %d", len(sf.users))
	}
}

func TestRemoveLastSessionStopsSharedForward(t *testing.T) {
	r, _ := newTestRegistry()
	redis := []contracts.PortForward{makeForward("default", "redis", 6379)}

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
	redis := []contracts.PortForward{makeForward("default", "redis", 6379)}
	rabbit := []contracts.PortForward{
		makeForward("default", "redis", 6379),
		makeForward("default", "rabbitmq", 5672),
	}

	r.Upsert("session-a", redis)
	r.Upsert("session-b", rabbit)

	r.Clear()

	if len(r.shared) != 0 {
		t.Fatalf("expected shared map to be empty, got %d entries", len(r.shared))
	}
}

func TestUpsertRollsBackOwnershipOnStartFailure(t *testing.T) {
	r, _ := newTestRegistry()
	redis := makeForward("default", "redis", 6379)
	bad := makeForward("default", "badservice", 9999)

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

	// Only session-a should still own redis after rollback.
	key := redis
	if len(r.shared[key].users) != 1 {
		t.Fatalf("expected user count 1 after rollback, got %d", len(r.shared[key].users))
	}

	// Session B should not exist.
	if r.shared[redis].users["session-b"] {
		t.Fatal("session-b retained ownership after failed upsert")
	}
}

func TestUpsertReplacingForwardsReleasesOldShared(t *testing.T) {
	r, _ := newTestRegistry()
	redis := []contracts.PortForward{makeForward("default", "redis", 6379)}
	rabbit := []contracts.PortForward{makeForward("default", "rabbitmq", 5672)}

	r.Upsert("session-a", redis)
	// Replace session-a's forwards: drop redis, add rabbitmq.
	r.Upsert("session-a", rabbit)

	redisKey := redis[0]
	if _, ok := r.shared[redisKey]; ok {
		t.Fatal("redis shared forward should have been cleaned up")
	}

	rabbitKey := rabbit[0]
	if _, ok := r.shared[rabbitKey]; !ok {
		t.Fatal("rabbitmq shared forward should exist")
	}
}

func TestUpsertReplacingForwardsKeepsOverlappingHandle(t *testing.T) {
	r, tracker := newTestRegistry()
	redis := makeForward("default", "redis", 6379)
	rabbit := makeForward("default", "rabbitmq", 5672)
	postgres := makeForward("default", "postgres", 5432)

	if err := r.Upsert("session-a", []contracts.PortForward{redis, rabbit}); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	redisKey := redis
	originalRedis := r.shared[redisKey].handle

	if err := r.Upsert("session-a", []contracts.PortForward{redis, postgres}); err != nil {
		t.Fatalf("replacement upsert: %v", err)
	}
	if tracker.count() != 3 {
		t.Fatalf("expected only the new postgres forward to start, total starts = %d", tracker.count())
	}
	if got := r.shared[redisKey].handle; got != originalRedis {
		t.Fatal("replacement restarted the overlapping redis forward")
	}
	if _, ok := r.shared[rabbit]; ok {
		t.Fatal("obsolete rabbitmq forward was retained")
	}
	if _, ok := r.shared[postgres]; !ok {
		t.Fatal("new postgres forward was not registered")
	}
}

func TestFailedReplacementKeepsExistingForwardAndStopsNewOne(t *testing.T) {
	r, tracker := newTestRegistry()
	redis := makeForward("default", "redis", 6379)
	postgres := makeForward("default", "postgres", 5432)
	bad := makeForward("default", "missing", 9999)
	stopped := false
	r.startForwardFn = func(spec contracts.PortForward) (*forwardHandle, error) {
		if spec == bad {
			return nil, fmt.Errorf("service unavailable")
		}
		handle, err := tracker.start(spec)
		if spec == postgres {
			handle.cancel = func() { stopped = true }
		}
		return handle, err
	}
	if err := r.Upsert("session", []contracts.PortForward{redis}); err != nil {
		t.Fatal(err)
	}
	original := r.shared[redis].handle
	if err := r.Upsert("session", []contracts.PortForward{redis, postgres, bad}); err == nil {
		t.Fatal("expected replacement to fail")
	}
	if !stopped || len(r.shared) != 1 || r.shared[redis].handle != original || !r.shared[redis].users["session"] {
		t.Fatal("failed replacement did not roll back only its new forward")
	}
	r.Remove("unknown-session")
	if len(r.shared) != 1 {
		t.Fatal("removing an unknown session stopped an active forward")
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
