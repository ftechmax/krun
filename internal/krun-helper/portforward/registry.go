package portforward

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

const redialBackoff = 2 * time.Second

type Registry struct {
	mu       sync.Mutex
	sessions map[string]map[string]struct{}
	forwards map[string]*entry
}

type entry struct {
	forward   contracts.PortForward
	refCount  int
	cancel    context.CancelFunc
	ready     chan struct{}
	readyOnce sync.Once
}

func (e *entry) signalReady() {
	e.readyOnce.Do(func() { close(e.ready) })
}

func NewRegistry() *Registry {
	return &Registry{
		sessions: map[string]map[string]struct{}{},
		forwards: map[string]*entry{},
	}
}

func (r *Registry) Upsert(sessionID, kubeConfigPath string, forwards []contracts.PortForward) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("upsert port-forwards: session id is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	desired := make(map[string]contracts.PortForward, len(forwards))
	for _, f := range forwards {
		desired[forwardKey(f)] = f
	}

	owned := r.sessions[sessionID]
	if owned == nil {
		owned = map[string]struct{}{}
	}

	for key := range owned {
		if _, keep := desired[key]; keep {
			continue
		}
		r.releaseLocked(key)
		delete(owned, key)
	}

	for key, f := range desired {
		if _, has := owned[key]; has {
			continue
		}
		r.acquireLocked(key, f, kubeConfigPath)
		owned[key] = struct{}{}
	}

	if len(owned) == 0 {
		delete(r.sessions, sessionID)
	} else {
		r.sessions[sessionID] = owned
	}

	return nil
}

func (r *Registry) Remove(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	keys, ok := r.sessions[sessionID]
	if !ok {
		return nil
	}
	for key := range keys {
		r.releaseLocked(key)
	}
	delete(r.sessions, sessionID)
	return nil
}

func (r *Registry) acquireLocked(key string, f contracts.PortForward, kubeConfigPath string) {
	if existing, ok := r.forwards[key]; ok {
		existing.refCount++
		return
	}

	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec
	e := &entry{
		forward:  f,
		refCount: 1,
		cancel:   cancel,
		ready:    make(chan struct{}),
	}
	r.forwards[key] = e

	go runForward(ctx, kubeConfigPath, f, e.signalReady)
}

func (r *Registry) WaitReady(f contracts.PortForward, timeout time.Duration) error {
	key := forwardKey(f)
	r.mu.Lock()
	e, ok := r.forwards[key]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("port-forward %s not registered", key)
	}
	select {
	case <-e.ready:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for port-forward %s/%s %d->%d", f.Namespace, f.Service, f.LocalPort, f.RemotePort)
	}
}

func (r *Registry) releaseLocked(key string) {
	e, ok := r.forwards[key]
	if !ok {
		return
	}
	e.refCount--
	if e.refCount > 0 {
		return
	}
	e.cancel()
	delete(r.forwards, key)
}

func forwardKey(f contracts.PortForward) string {
	ns := strings.TrimSpace(f.Namespace)
	if ns == "" {
		ns = "default"
	}
	return fmt.Sprintf("%s|%s|%d|%d", ns, f.Service, f.LocalPort, f.RemotePort)
}

func runForward(ctx context.Context, kubeConfigPath string, f contracts.PortForward, signalReady func()) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := dialForward(ctx, kubeConfigPath, f, signalReady); err != nil && ctx.Err() == nil {
			fmt.Printf("port-forward %s/%s %d->%d failed: %v\n", f.Namespace, f.Service, f.LocalPort, f.RemotePort, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(redialBackoff):
		}
	}
}

func dialForward(ctx context.Context, kubeConfigPath string, f contracts.PortForward, signalReady func()) error {
	client, err := kube.NewClient(kubeConfigPath)
	if err != nil {
		return fmt.Errorf("kube client: %w", err)
	}

	podName, err := pickPodForService(ctx, client, f.Namespace, f.Service)
	if err != nil {
		return err
	}

	transport, upgrader, err := spdy.RoundTripperFor(client.RestConfig)
	if err != nil {
		return fmt.Errorf("spdy round tripper: %w", err)
	}

	req := client.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(f.Namespace).
		Name(podName).
		SubResource("portforward")

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, req.URL())

	ports := []string{fmt.Sprintf("%d:%d", f.LocalPort, f.RemotePort)}
	stopCh := make(chan struct{})
	readyCh := make(chan struct{})

	fw, err := portforward.New(dialer, ports, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("create forwarder: %w", err)
	}

	forwardErr := make(chan error, 1)
	go func() { forwardErr <- fw.ForwardPorts() }()

	go func() {
		select {
		case <-ctx.Done():
			select {
			case <-stopCh:
			default:
				close(stopCh)
			}
		case <-stopCh:
		}
	}()

	select {
	case err := <-forwardErr:
		return err
	case <-readyCh:
	}
	if signalReady != nil {
		signalReady()
	}

	err = <-forwardErr
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func pickPodForService(ctx context.Context, client *kube.Client, namespace, serviceName string) (string, error) {
	if namespace == "" {
		namespace = "default"
	}
	svc, err := client.Clientset.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get service %s/%s: %w", namespace, serviceName, err)
	}
	if len(svc.Spec.Selector) == 0 {
		return "", fmt.Errorf("service %s/%s has no selector", namespace, serviceName)
	}

	selector := labels.SelectorFromSet(svc.Spec.Selector).String()
	pods, err := client.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return "", fmt.Errorf("list pods for %s/%s: %w", namespace, serviceName, err)
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if !isPodReady(pod) {
			continue
		}
		return pod.Name, nil
	}
	return "", fmt.Errorf("no ready pods for service %s/%s", namespace, serviceName)
}

func isPodReady(pod corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
