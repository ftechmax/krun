package portforward

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/kube"
	"github.com/ftechmax/krun/internal/sessionkey"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

const (
	defaultNamespace       = "default"
	readyTimeout           = 10 * time.Second
	forwardShutdownTimeout = 2 * time.Second
	reconnectInitialDelay  = 1 * time.Second
	reconnectMaxDelay      = 30 * time.Second
)

type SessionRegistry struct {
	mu             sync.Mutex
	client         *kube.Client
	startForwardFn func(contracts.PortForward) (*forwardHandle, error)
	sessions       map[string]map[string]*forwardHandle
	shared         map[string]*sharedForward
}

type sharedForward struct {
	handle   *forwardHandle
	refCount int
}

type forwardHandle struct {
	spec contracts.PortForward

	cancel   context.CancelFunc
	doneChan chan struct{}
	stopOnce sync.Once
}

func NewSessionRegistry(kubeConfigPath string) (*SessionRegistry, error) {
	client, err := kube.NewClient(kubeConfigPath)
	if err != nil {
		return nil, err
	}
	registry := &SessionRegistry{
		client:   client,
		sessions: map[string]map[string]*forwardHandle{},
		shared:   map[string]*sharedForward{},
	}
	registry.startForwardFn = registry.startForward
	return registry, nil
}

func (r *SessionRegistry) Upsert(sessionKey string, forwards []contracts.PortForward) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := sessionkey.Normalize(sessionKey)
	normalized := normalizePortForwards(forwards)
	if len(normalized) == 0 {
		r.removeLocked(key)
		return nil
	}

	existing := r.sessions[key]
	if existing == nil {
		existing = map[string]*forwardHandle{}
	}

	next := make(map[string]*forwardHandle, len(normalized))
	acquiredKeys := make([]string, 0, len(normalized))
	for _, forward := range normalized {
		forwardKey := portForwardKey(forward)
		if existingHandle, ok := existing[forwardKey]; ok {
			next[forwardKey] = existingHandle
			continue
		}

		// Check if another session already owns this forward.
		if sf, ok := r.shared[forwardKey]; ok {
			sf.refCount++
			next[forwardKey] = sf.handle
			acquiredKeys = append(acquiredKeys, forwardKey)
			continue
		}

		handle, err := r.startForwardFn(forward)
		if err != nil {
			for _, acquiredKey := range acquiredKeys {
				r.releaseSharedLocked(acquiredKey)
				delete(next, acquiredKey)
			}
			return err
		}

		fmt.Printf("Started port-forward %s/%s:%d -> 127.0.0.1:%d\n", forward.Namespace, forward.Service, forward.RemotePort, forward.LocalPort)
		r.shared[forwardKey] = &sharedForward{handle: handle, refCount: 1}
		next[forwardKey] = handle
		acquiredKeys = append(acquiredKeys, forwardKey)
	}

	for existingKey, existingHandle := range existing {
		if _, ok := next[existingKey]; ok {
			continue
		}
		fmt.Printf("Stopping port-forward %s/%s:%d -> 127.0.0.1:%d\n", existingHandle.spec.Namespace, existingHandle.spec.Service, existingHandle.spec.RemotePort, existingHandle.spec.LocalPort)
		r.releaseSharedLocked(existingKey)
	}

	r.sessions[key] = next
	return nil
}

func (r *SessionRegistry) Remove(sessionKey string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(sessionKey)
	return nil
}

func (r *SessionRegistry) Clear() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked("")
	return nil
}

func (r *SessionRegistry) removeLocked(sessionKey string) {
	if sessionkey.IsBlank(sessionKey) {
		for _, handles := range r.sessions {
			for forwardKey, handle := range handles {
				fmt.Printf("Stopping port-forward %s/%s:%d -> 127.0.0.1:%d\n", handle.spec.Namespace, handle.spec.Service, handle.spec.RemotePort, handle.spec.LocalPort)
				r.releaseSharedLocked(forwardKey)
			}
		}
		r.sessions = map[string]map[string]*forwardHandle{}
		return
	}

	key := sessionkey.Normalize(sessionKey)
	handles, ok := r.sessions[key]
	if !ok {
		return
	}
	for forwardKey, handle := range handles {
		fmt.Printf("Stopping port-forward %s/%s:%d -> 127.0.0.1:%d\n", handle.spec.Namespace, handle.spec.Service, handle.spec.RemotePort, handle.spec.LocalPort)
		r.releaseSharedLocked(forwardKey)
	}
	delete(r.sessions, key)
}

// releaseSharedLocked decrements the reference count for a shared forward
// and stops it only when no sessions reference it anymore.
func (r *SessionRegistry) releaseSharedLocked(forwardKey string) {
	sf, ok := r.shared[forwardKey]
	if !ok {
		return
	}
	sf.refCount--
	if sf.refCount <= 0 {
		sf.handle.stop()
		delete(r.shared, forwardKey)
	}
}

func (r *SessionRegistry) startForward(forward contracts.PortForward) (*forwardHandle, error) {
	// Dial once to verify the forward works before returning.
	stopChan, doneChan, err := r.dialForward(forward)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // Cancel func is stored on the handle and called by stop().
	supervisedDone := make(chan struct{})

	h := &forwardHandle{
		spec:     forward,
		cancel:   cancel,
		doneChan: supervisedDone,
	}

	// Supervision goroutine: watches the active forward and re-dials on failure.
	go func() {
		defer close(supervisedDone)
		currentStop := stopChan
		currentDone := doneChan

		for {
			select {
			case <-ctx.Done():
				close(currentStop)
				<-currentDone
				return
			case <-currentDone:
				// Port-forward died, reconnect with backoff.
				log.Printf("port-forward lost %s/%s %d -> 127.0.0.1:%d, reconnecting",
					forward.Namespace, forward.Service, forward.RemotePort, forward.LocalPort)
			}

			delay := reconnectInitialDelay
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}

				newStop, newDone, dialErr := r.dialForward(forward)
				if dialErr != nil {
					log.Printf("port-forward reconnect failed %s/%s: %v", forward.Namespace, forward.Service, dialErr)
					delay *= 2
					if delay > reconnectMaxDelay {
						delay = reconnectMaxDelay
					}
					continue
				}

				log.Printf("port-forward reconnected %s/%s %d -> 127.0.0.1:%d",
					forward.Namespace, forward.Service, forward.RemotePort, forward.LocalPort)
				currentStop = newStop
				currentDone = newDone
				break
			}
		}
	}()

	return h, nil
}

func (r *SessionRegistry) dialForward(forward contracts.PortForward) (stopChan chan struct{}, doneChan chan struct{}, err error) {
	targetPod, targetPort, err := r.resolvePodForwardTarget(forward)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve target for %s/%s: %w", forward.Namespace, forward.Service, err)
	}

	request := r.client.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(forward.Namespace).
		Name(targetPod).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(r.client.RestConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("build spdy round tripper: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, request.URL())

	ports := []string{fmt.Sprintf("%d:%d", forward.LocalPort, targetPort)}
	stop := make(chan struct{})
	readyChan := make(chan struct{})
	errOut := bytes.NewBuffer(nil)

	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, ports, stop, readyChan, io.Discard, errOut)
	if err != nil {
		return nil, nil, fmt.Errorf("create port-forwarder: %w", err)
	}

	forwardErr := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		forwardErr <- pf.ForwardPorts()
	}()

	select {
	case <-readyChan:
		return stop, done, nil
	case err := <-forwardErr:
		msg := strings.TrimSpace(errOut.String())
		if err == nil {
			err = fmt.Errorf("port-forward ended before becoming ready")
		}
		if msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return nil, nil, fmt.Errorf("start %s/%s (pod %s) %d -> 127.0.0.1:%d: %w", forward.Namespace, forward.Service, targetPod, targetPort, forward.LocalPort, err)
	case <-time.After(readyTimeout):
		close(stop)
		select {
		case <-done:
		case <-time.After(forwardShutdownTimeout):
		}
		return nil, nil, fmt.Errorf("timed out waiting for %s/%s (pod %s) %d -> 127.0.0.1:%d", forward.Namespace, forward.Service, targetPod, targetPort, forward.LocalPort)
	}
}

func (r *SessionRegistry) resolvePodForwardTarget(forward contracts.PortForward) (string, int, error) {
	service, err := r.client.Clientset.CoreV1().Services(forward.Namespace).Get(context.Background(), forward.Service, metav1.GetOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("get service: %w", err)
	}
	if len(service.Spec.Selector) == 0 {
		return "", 0, fmt.Errorf("service has no selector")
	}

	podList, err := r.client.Clientset.CoreV1().Pods(forward.Namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: labels.Set(service.Spec.Selector).AsSelector().String(),
	})
	if err != nil {
		return "", 0, fmt.Errorf("list pods: %w", err)
	}

	pod, err := selectPodForPortForward(podList.Items)
	if err != nil {
		return "", 0, err
	}

	targetPort, err := resolveServiceTargetPort(*service, pod, forward.RemotePort)
	if err != nil {
		return "", 0, err
	}

	return pod.Name, targetPort, nil
}

func selectPodForPortForward(pods []corev1.Pod) (corev1.Pod, error) {
	if len(pods) == 0 {
		return corev1.Pod{}, fmt.Errorf("no pods found for service selector")
	}

	readyRunning := make([]corev1.Pod, 0, len(pods))
	running := make([]corev1.Pod, 0, len(pods))
	pending := make([]corev1.Pod, 0, len(pods))

	for _, pod := range pods {
		if pod.DeletionTimestamp != nil {
			continue
		}

		switch pod.Status.Phase {
		case corev1.PodRunning:
			if isPodReady(pod) {
				readyRunning = append(readyRunning, pod)
			} else {
				running = append(running, pod)
			}
		case corev1.PodPending:
			pending = append(pending, pod)
		}
	}

	for _, group := range [][]corev1.Pod{readyRunning, running, pending} {
		if len(group) == 0 {
			continue
		}
		slices.SortFunc(group, func(a, b corev1.Pod) int {
			return strings.Compare(a.Name, b.Name)
		})
		return group[0], nil
	}

	return corev1.Pod{}, fmt.Errorf("no running or pending pods available for service selector")
}

func isPodReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func resolveServiceTargetPort(service corev1.Service, pod corev1.Pod, requestedRemotePort int) (int, error) {
	for _, servicePort := range service.Spec.Ports {
		if int(servicePort.Port) != requestedRemotePort {
			continue
		}

		switch servicePort.TargetPort.Type {
		case intstr.Int:
			target := servicePort.TargetPort.IntValue()
			if target > 0 {
				return target, nil
			}
		case intstr.String:
			targetName := strings.TrimSpace(servicePort.TargetPort.StrVal)
			if targetName == "" {
				break
			}
			for _, container := range pod.Spec.Containers {
				for _, containerPort := range container.Ports {
					if strings.TrimSpace(containerPort.Name) == targetName && containerPort.ContainerPort > 0 {
						return int(containerPort.ContainerPort), nil
					}
				}
			}
			return 0, fmt.Errorf("service port %d targets named port %q that was not found on pod %s", requestedRemotePort, targetName, pod.Name)
		}

		if servicePort.Port > 0 {
			return int(servicePort.Port), nil
		}
	}

	// Fallback for cases where the request already provided a pod port.
	return requestedRemotePort, nil
}

func (h *forwardHandle) stop() {
	h.stopOnce.Do(func() {
		h.cancel()
	})

	select {
	case <-h.doneChan:
	case <-time.After(forwardShutdownTimeout):
	}
}

func normalizePortForwards(forwards []contracts.PortForward) []contracts.PortForward {
	if len(forwards) == 0 {
		return nil
	}

	normalized := make([]contracts.PortForward, 0, len(forwards))
	seen := map[string]bool{}

	for _, forward := range forwards {
		namespace := strings.TrimSpace(forward.Namespace)
		service := strings.TrimSpace(forward.Service)
		if namespace == "" {
			namespace = defaultNamespace
		}
		if service == "" || forward.LocalPort <= 0 || forward.RemotePort <= 0 {
			continue
		}

		normalizedForward := contracts.PortForward{
			Namespace:  namespace,
			Service:    service,
			LocalPort:  forward.LocalPort,
			RemotePort: forward.RemotePort,
		}
		key := portForwardKey(normalizedForward)
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, normalizedForward)
	}

	slices.SortFunc(normalized, func(a, b contracts.PortForward) int {
		if a.Namespace == b.Namespace {
			if a.Service == b.Service {
				if a.LocalPort == b.LocalPort {
					switch {
					case a.RemotePort < b.RemotePort:
						return -1
					case a.RemotePort > b.RemotePort:
						return 1
					default:
						return 0
					}
				}
				switch {
				case a.LocalPort < b.LocalPort:
					return -1
				case a.LocalPort > b.LocalPort:
					return 1
				default:
					return 0
				}
			}
			return strings.Compare(a.Service, b.Service)
		}
		return strings.Compare(a.Namespace, b.Namespace)
	})

	return normalized
}

func portForwardKey(forward contracts.PortForward) string {
	return fmt.Sprintf("%s|%s|%d|%d", forward.Namespace, forward.Service, forward.LocalPort, forward.RemotePort)
}
