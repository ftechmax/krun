package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ftechmax/krun/internal/contracts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

const (
	defaultImage          = "docker.io/ftechmax/krun-traffic-agent:latest"
	defaultManagerAddress = "http://krun-traffic-manager.krun-system.svc:8080"
	defaultProbePort      = 8082
	injectedLabelKey      = "krun.ftechmax.net/traffic-agent-injected"
	injectedLabelValue    = "true"
	// originalProbesAnnotation stores the pre-rewrite probe specs so
	// Remove/Cleanup can restore them when the sidecar is taken out.
	originalProbesAnnotation = "krun.ftechmax.net/original-probes"
)

var ErrWorkloadNotFound = errors.New("target workload not found")

type Injector interface {
	Inject(ctx context.Context, session contracts.DebugSession) error
	Remove(ctx context.Context, session contracts.DebugSession) error
	Cleanup(ctx context.Context) error
}

type Options struct {
	Image           string
	ImagePullPolicy string
	ManagerAddress  string
	ProbePort       int
}

type WorkloadInjector struct {
	client  kubernetes.Interface
	options Options
}

type workloadKind string

const (
	workloadKindDeployment  workloadKind = "deployment"
	workloadKindStatefulSet workloadKind = "statefulset"
	workloadKindDaemonSet   workloadKind = "daemonset"
)

var workloadKinds = []workloadKind{
	workloadKindDeployment,
	workloadKindStatefulSet,
	workloadKindDaemonSet,
}

type workloadTarget struct {
	kind     workloadKind
	object   metav1.Object
	template *corev1.PodTemplateSpec
	update   func(ctx context.Context) error
}

func NewWorkloadInjector(client kubernetes.Interface, options Options) *WorkloadInjector {
	return &WorkloadInjector{
		client:  client,
		options: normalizeOptions(options),
	}
}

func (i *WorkloadInjector) Inject(ctx context.Context, session contracts.DebugSession) error {
	namespace, workload := session.Namespace, session.ServiceName

	desiredContainer := i.buildContainer(session)
	return i.mutateWorkload(ctx, namespace, workload, "", "with traffic-agent sidecar", func(target *workloadTarget) bool {
		containers := target.template.Spec.Containers
		index := slices.IndexFunc(containers, func(container corev1.Container) bool {
			return container.Name == contracts.AgentContainerName
		})
		if index < 0 {
			target.template.Spec.Containers = append(containers, desiredContainer)
		} else {
			containers[index] = desiredContainer
		}

		labels := target.object.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[injectedLabelKey] = injectedLabelValue
		target.object.SetLabels(labels)

		i.rewriteProbes(target, session.ServicePort)
		return true // Each enable supplies a new session ID and token.
	})
}

func (i *WorkloadInjector) Remove(ctx context.Context, session contracts.DebugSession) error {
	namespace, workload := session.Namespace, session.ServiceName

	return i.mutateWorkload(ctx, namespace, workload, "", "removing traffic-agent sidecar", i.removeInjectedSidecar)
}

func (i *WorkloadInjector) Cleanup(ctx context.Context) error {
	var errs []error
	for _, kind := range workloadKinds {
		if err := i.cleanupLabeledByKind(ctx, kind); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (i *WorkloadInjector) cleanupLabeledByKind(ctx context.Context, kind workloadKind) error {
	options := metav1.ListOptions{LabelSelector: injectedLabelKey + "=" + injectedLabelValue}
	var objects []metav1.Object
	switch kind {
	case workloadKindDeployment:
		list, err := i.client.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, options)
		if err != nil {
			return err
		}
		for index := range list.Items {
			objects = append(objects, &list.Items[index])
		}
	case workloadKindStatefulSet:
		list, err := i.client.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, options)
		if err != nil {
			return err
		}
		for index := range list.Items {
			objects = append(objects, &list.Items[index])
		}
	case workloadKindDaemonSet:
		list, err := i.client.AppsV1().DaemonSets(metav1.NamespaceAll).List(ctx, options)
		if err != nil {
			return err
		}
		for index := range list.Items {
			objects = append(objects, &list.Items[index])
		}
	}
	var errs []error
	for _, object := range objects {
		err := i.mutateWorkload(ctx, object.GetNamespace(), object.GetName(), kind, "during startup cleanup", i.removeInjectedSidecar)
		if err != nil && !errors.Is(err, ErrWorkloadNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// removeInjectedSidecar drops the agent container, the injected label and the
// probe rewrites, reporting whether anything changed.
func (i *WorkloadInjector) removeInjectedSidecar(target *workloadTarget) bool {
	containers := target.template.Spec.Containers
	target.template.Spec.Containers = slices.DeleteFunc(containers, func(container corev1.Container) bool {
		return container.Name == contracts.AgentContainerName
	})
	changed := len(containers) != len(target.template.Spec.Containers)
	if removeInjectedLabel(target.object) {
		changed = true
	}
	if restoreProbes(target) {
		changed = true
	}
	return changed
}

func (i *WorkloadInjector) mutateWorkload(
	ctx context.Context,
	namespace string,
	workload string,
	kind workloadKind,
	updateAction string,
	mutate func(target *workloadTarget) bool,
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		target, err := i.findWorkloadTarget(ctx, kind, namespace, workload)
		if err != nil {
			return err
		}

		changed := mutate(target)
		if !changed {
			return nil
		}

		if err := target.update(ctx); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("%w: %s %s/%s", ErrWorkloadNotFound, target.kind, namespace, workload)
			}
			return fmt.Errorf("update %s %s/%s %s: %w", target.kind, namespace, workload, updateAction, err)
		}
		return nil
	})
}

func (i *WorkloadInjector) findWorkloadTarget(ctx context.Context, kind workloadKind, namespace, name string) (*workloadTarget, error) {
	// Enable/disable search in order; startup cleanup already knows the kind.
	kinds := workloadKinds
	if kind != "" {
		kinds = []workloadKind{kind}
	}
	for _, kind := range kinds {
		var target *workloadTarget
		var err error
		switch kind {
		case workloadKindDeployment:
			api := i.client.AppsV1().Deployments(namespace)
			var object *appsv1.Deployment
			object, err = api.Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				target = &workloadTarget{kind: kind, object: object, template: &object.Spec.Template,
					update: func(ctx context.Context) error { _, err := api.Update(ctx, object, metav1.UpdateOptions{}); return err },
				}
			}
		case workloadKindStatefulSet:
			api := i.client.AppsV1().StatefulSets(namespace)
			var object *appsv1.StatefulSet
			object, err = api.Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				target = &workloadTarget{kind: kind, object: object, template: &object.Spec.Template,
					update: func(ctx context.Context) error { _, err := api.Update(ctx, object, metav1.UpdateOptions{}); return err },
				}
			}
		case workloadKindDaemonSet:
			api := i.client.AppsV1().DaemonSets(namespace)
			var object *appsv1.DaemonSet
			object, err = api.Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				target = &workloadTarget{kind: kind, object: object, template: &object.Spec.Template,
					update: func(ctx context.Context) error { _, err := api.Update(ctx, object, metav1.UpdateOptions{}); return err },
				}
			}
		}
		if err == nil {
			return target, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("get %s %s/%s: %w", kind, namespace, name, err)
		}
	}
	return nil, fmt.Errorf("%w: %s/%s", ErrWorkloadNotFound, namespace, name)
}

func (i *WorkloadInjector) buildContainer(session contracts.DebugSession) corev1.Container {
	return corev1.Container{
		Name:            contracts.AgentContainerName,
		Image:           i.options.Image,
		ImagePullPolicy: corev1.PullPolicy(i.options.ImagePullPolicy),
		Env: []corev1.EnvVar{
			{Name: "KRUN_SESSION_ID", Value: session.SessionID},
			{Name: "KRUN_SESSION_TOKEN", Value: session.SessionToken},
			{Name: "KRUN_MANAGER_ADDRESS", Value: i.options.ManagerAddress},
			{Name: "KRUN_TARGET_PORT", Value: strconv.Itoa(session.ServicePort)},
			{Name: "KRUN_AGENT_PROBE_PORT", Value: strconv.Itoa(i.options.ProbePort)},
		},
		// iptables needs root. RunAsNonRoot is set explicitly so a pod-level
		// runAsNonRoot: true doesn't make kubelet reject the container.
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:    new(int64(0)),
			RunAsNonRoot: new(false),
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"NET_ADMIN"},
			},
		},
	}
}

// savedProbes records a container's pre-rewrite probe specs. Only probes
// that were rewritten are stored; nil means the probe was left untouched.
type savedProbes struct {
	Liveness  *corev1.Probe `json:"liveness,omitempty"`
	Readiness *corev1.Probe `json:"readiness,omitempty"`
	Startup   *corev1.Probe `json:"startup,omitempty"`
}

// rewriteProbes points every probe that targets the intercepted port at the
// agent's probe server instead. Otherwise kubelet probes get redirected to
// the developer's local app, and the pod goes NotReady the moment that app
// is stopped or paused on a breakpoint.
func (i *WorkloadInjector) rewriteProbes(target *workloadTarget, targetPort int) {
	saved := map[string]savedProbes{}

	for idx := range target.template.Spec.Containers {
		container := &target.template.Spec.Containers[idx]
		if container.Name == contracts.AgentContainerName {
			continue
		}

		var record savedProbes
		rewrote := false
		slots := []struct {
			probe    **corev1.Probe
			original **corev1.Probe
		}{
			{&container.LivenessProbe, &record.Liveness},
			{&container.ReadinessProbe, &record.Readiness},
			{&container.StartupProbe, &record.Startup},
		}
		for _, slot := range slots {
			if !probeTargetsPort(*slot.probe, *container, targetPort) {
				continue
			}
			*slot.original = (*slot.probe).DeepCopy()
			rewriteProbePort(*slot.probe, i.options.ProbePort)
			rewrote = true
		}
		if rewrote {
			saved[container.Name] = record
		}
	}

	if len(saved) > 0 {
		annotations := target.object.GetAnnotations()
		// Keep an existing annotation: it holds the true originals from the
		// first injection; a re-inject sees already-rewritten probes.
		if _, exists := annotations[originalProbesAnnotation]; !exists {
			data, _ := json.Marshal(saved)
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[originalProbesAnnotation] = string(data)
			target.object.SetAnnotations(annotations)
		}
	}
}

// restoreProbes puts the original probe specs back and drops the annotation.
func restoreProbes(target *workloadTarget) bool {
	annotations := target.object.GetAnnotations()
	raw, ok := annotations[originalProbesAnnotation]
	if !ok {
		return false
	}

	var saved map[string]savedProbes
	if err := json.Unmarshal([]byte(raw), &saved); err == nil {
		for idx := range target.template.Spec.Containers {
			container := &target.template.Spec.Containers[idx]
			record, ok := saved[container.Name]
			if !ok {
				continue
			}
			if record.Liveness != nil {
				container.LivenessProbe = record.Liveness
			}
			if record.Readiness != nil {
				container.ReadinessProbe = record.Readiness
			}
			if record.Startup != nil {
				container.StartupProbe = record.Startup
			}
		}
	}

	delete(annotations, originalProbesAnnotation)
	target.object.SetAnnotations(annotations)
	return true
}

func probeTargetsPort(probe *corev1.Probe, container corev1.Container, targetPort int) bool {
	if probe == nil {
		return false
	}
	var port intstr.IntOrString
	switch {
	case probe.HTTPGet != nil:
		port = probe.HTTPGet.Port
	case probe.TCPSocket != nil:
		port = probe.TCPSocket.Port
	default:
		return false
	}

	switch port.Type {
	case intstr.Int:
		return port.IntValue() == targetPort
	case intstr.String:
		for _, containerPort := range container.Ports {
			if containerPort.Name == port.StrVal {
				return int(containerPort.ContainerPort) == targetPort
			}
		}
	}
	return false
}

func rewriteProbePort(probe *corev1.Probe, probePort int) {
	if probe.HTTPGet != nil {
		probe.HTTPGet.Port = intstr.FromInt(probePort)
	}
	if probe.TCPSocket != nil {
		probe.TCPSocket.Port = intstr.FromInt(probePort)
	}
}

func normalizeOptions(options Options) Options {
	options.Image = strings.TrimSpace(options.Image)
	if options.Image == "" {
		options.Image = defaultImage
	}

	options.ImagePullPolicy = strings.TrimSpace(options.ImagePullPolicy)
	if options.ImagePullPolicy == "" {
		options.ImagePullPolicy = string(corev1.PullIfNotPresent)
	}

	options.ManagerAddress = strings.TrimSpace(options.ManagerAddress)
	if options.ManagerAddress == "" {
		options.ManagerAddress = defaultManagerAddress
	}

	if options.ProbePort < 1 || options.ProbePort > 65535 {
		options.ProbePort = defaultProbePort
	}

	return options
}

func removeInjectedLabel(obj metav1.Object) bool {
	labels := obj.GetLabels()
	_, found := labels[injectedLabelKey]
	delete(labels, injectedLabelKey)
	obj.SetLabels(labels)
	return found
}
