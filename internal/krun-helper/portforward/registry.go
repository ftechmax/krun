package portforward

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

const (
	readyTimeout           = 10 * time.Second
	forwardShutdownTimeout = 2 * time.Second
	reconnectInitialDelay  = 1 * time.Second
	reconnectMaxDelay      = 30 * time.Second
)

type SessionRegistry struct {
	mu             sync.Mutex
	client         *kube.Client
	startForwardFn func(contracts.PortForward) (*forwardHandle, error)
	shared         map[contracts.PortForward]*sharedForward
}

type sharedForward struct {
	handle *forwardHandle
	users  map[string]bool
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
		client: client,
		shared: map[contracts.PortForward]*sharedForward{},
	}
	registry.startForwardFn = registry.startForward
	return registry, nil
}

func (r *SessionRegistry) Upsert(sessionKey string, forwards []contracts.PortForward) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	wanted := make(map[contracts.PortForward]bool, len(forwards))
	var acquired []contracts.PortForward
	for _, forward := range forwards {
		wanted[forward] = true
		shared := r.shared[forward]
		if shared == nil {
			handle, err := r.startForwardFn(forward)
			if err != nil {
				for _, spec := range acquired {
					r.release(spec, sessionKey)
				}
				return err
			}
			shared = &sharedForward{handle: handle, users: map[string]bool{}}
			r.shared[forward] = shared
			fmt.Printf("Started port-forward %s/%s:%d -> 127.0.0.1:%d\n", forward.Namespace, forward.Service, forward.RemotePort, handle.spec.LocalPort)
		}
		if !shared.users[sessionKey] {
			shared.users[sessionKey] = true
			acquired = append(acquired, forward)
		}
	}

	for forward, shared := range r.shared {
		if shared.users[sessionKey] && !wanted[forward] {
			r.release(forward, sessionKey)
		}
	}
	return nil
}

// BoundLocalPort reports the local port allocated for a session's forward.
func (r *SessionRegistry) BoundLocalPort(sessionKey string, forward contracts.PortForward) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	shared := r.shared[forward]
	if shared == nil || !shared.users[sessionKey] {
		return 0, false
	}
	return shared.handle.spec.LocalPort, true
}

func (r *SessionRegistry) Remove(sessionKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for forward := range r.shared {
		r.release(forward, sessionKey)
	}
}

func (r *SessionRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, forward := range r.shared {
		forward.handle.stop()
	}
	clear(r.shared)
}

// release is called with mu held. The last user owns forward shutdown.
func (r *SessionRegistry) release(forward contracts.PortForward, sessionKey string) {
	shared := r.shared[forward]
	delete(shared.users, sessionKey)
	if len(shared.users) == 0 {
		shared.handle.stop()
		delete(r.shared, forward)
	}
}

func (r *SessionRegistry) startForward(forward contracts.PortForward) (*forwardHandle, error) {
	// Dial once to verify the forward works before returning.
	stopChan, doneChan, boundLocalPort, err := r.dialForward(forward)
	if err != nil {
		return nil, err
	}

	// A requested local port of 0 lets the OS pick one; pin the allocation
	// so supervision re-dials rebind the same port that consumers use.
	if forward.LocalPort == 0 {
		forward.LocalPort = boundLocalPort
	}

	ctx, cancel := context.WithCancel(context.Background())
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

				newStop, newDone, _, dialErr := r.dialForward(forward)
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

func (r *SessionRegistry) dialForward(forward contracts.PortForward) (stopChan chan struct{}, doneChan chan struct{}, boundLocalPort int, err error) {
	targetPod, targetPort, err := r.resolvePodForwardTarget(forward)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("resolve target for %s/%s: %w", forward.Namespace, forward.Service, err)
	}

	request := r.client.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(forward.Namespace).
		Name(targetPod).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(r.client.RestConfig)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("build spdy round tripper: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, request.URL())

	ports := []string{fmt.Sprintf("%d:%d", forward.LocalPort, targetPort)}
	stop := make(chan struct{})
	readyChan := make(chan struct{})
	errOut := bytes.NewBuffer(nil)

	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, ports, stop, readyChan, io.Discard, errOut)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("create port-forwarder: %w", err)
	}

	forwardErr := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		forwardErr <- pf.ForwardPorts()
	}()

	select {
	case <-readyChan:
		boundLocalPort = forward.LocalPort
		if boundLocalPort == 0 {
			forwardedPorts, portsErr := pf.GetPorts()
			if portsErr != nil || len(forwardedPorts) == 0 {
				close(stop)
				select {
				case <-done:
				case <-time.After(forwardShutdownTimeout):
				}
				return nil, nil, 0, fmt.Errorf("resolve bound local port for %s/%s: %v", forward.Namespace, forward.Service, portsErr)
			}
			boundLocalPort = int(forwardedPorts[0].Local)
		}
		return stop, done, boundLocalPort, nil
	case err := <-forwardErr:
		msg := strings.TrimSpace(errOut.String())
		if err == nil {
			err = fmt.Errorf("port-forward ended before becoming ready")
		}
		if msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return nil, nil, 0, fmt.Errorf("start %s/%s (pod %s) %d -> 127.0.0.1:%d: %w", forward.Namespace, forward.Service, targetPod, targetPort, forward.LocalPort, err)
	case <-time.After(readyTimeout):
		close(stop)
		select {
		case <-done:
		case <-time.After(forwardShutdownTimeout):
		}
		return nil, nil, 0, fmt.Errorf("timed out waiting for %s/%s (pod %s) %d -> 127.0.0.1:%d", forward.Namespace, forward.Service, targetPod, targetPort, forward.LocalPort)
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
	var selected *corev1.Pod
	for idx := range pods {
		pod := &pods[idx]
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if selected == nil || (isPodReady(*pod) && !isPodReady(*selected)) ||
			(isPodReady(*pod) == isPodReady(*selected) && pod.Name < selected.Name) {
			selected = pod
		}
	}
	if selected == nil {
		return corev1.Pod{}, fmt.Errorf("no running pods available for service selector")
	}
	return *selected, nil
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
				return int(target), nil
			}
		case intstr.String:
			targetName := servicePort.TargetPort.StrVal
			for _, container := range pod.Spec.Containers {
				for _, containerPort := range container.Ports {
					if containerPort.Name == targetName {
						return int(containerPort.ContainerPort), nil
					}
				}
			}
			return 0, fmt.Errorf("service port %d targets named port %q that was not found on pod %s", requestedRemotePort, targetName, pod.Name)
		}

		return int(servicePort.Port), nil
	}

	return 0, fmt.Errorf("service %s has no port %d", service.Name, requestedRemotePort)
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
