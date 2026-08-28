package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	DefaultContainerName   = "krun-traffic-agent"
	DefaultImage           = "docker.io/ftechmax/krun-traffic-agent:latest"
	DefaultImagePullPolicy = "IfNotPresent"
	DefaultManagerAddress  = "http://krun-traffic-manager.krun-system.svc:8080"
	DefaultProbePort       = 8082
	InjectedLabelKey       = "krun.ftechmax.net/traffic-agent-injected"
	injectedLabelValue     = "true"
	// OriginalProbesAnnotation stores the pre-rewrite probe specs so
	// Remove/Cleanup can restore them when the sidecar is taken out.
	OriginalProbesAnnotation = "krun.ftechmax.net/original-probes"
)

var ErrWorkloadNotFound = errors.New("target workload not found")

type Injector interface {
	Inject(ctx context.Context, session contracts.DebugSession) error
	Remove(ctx context.Context, session contracts.DebugSession) error
	Cleanup(ctx context.Context) error
}

type Options struct {
	ContainerName   string
	Image           string
	ImagePullPolicy string
	ManagerAddress  string
	ProbePort       int
}

type WorkloadInjector struct {
	operations map[workloadKind]workloadOperation
	options    Options
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

type workloadFinder func(ctx context.Context, namespace string, workload string) (*workloadTarget, error)

type workloadOperation struct {
	list func(context.Context) ([]workloadIdentifier, error)
	find workloadFinder
}

func NewWorkloadInjector(client kubernetes.Interface, options Options) *WorkloadInjector {
	return &WorkloadInjector{
		operations: newWorkloadOperations(client),
		options:    normalizeOptions(options),
	}
}

func (i *WorkloadInjector) Inject(ctx context.Context, session contracts.DebugSession) error {
	namespace, workload, err := resolveTarget(session)
	if err != nil {
		return err
	}

	desiredContainer := i.buildContainer(session)
	return i.mutateWorkload(ctx, namespace, workload, i.findWorkloadTarget, "with traffic-agent sidecar", func(target *workloadTarget) bool {
		changed := false

		updated := append([]corev1.Container(nil), target.template.Spec.Containers...)
		index := findContainerIndex(updated, i.options.ContainerName)
		if index >= 0 {
			if !reflect.DeepEqual(updated[index], desiredContainer) {
				updated[index] = desiredContainer
				target.template.Spec.Containers = updated
				changed = true
			}
		} else {
			target.template.Spec.Containers = append(updated, desiredContainer)
			changed = true
		}

		if ensureInjectedLabel(target.object) {
			changed = true
		}

		if i.rewriteProbes(target, session.ServicePort) {
			changed = true
		}

		return changed
	})
}

func (i *WorkloadInjector) Remove(ctx context.Context, session contracts.DebugSession) error {
	namespace, workload, err := resolveTarget(session)
	if err != nil {
		return err
	}

	return i.mutateWorkload(ctx, namespace, workload, i.findWorkloadTarget, "removing traffic-agent sidecar", i.removeInjectedSidecar)
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
	targets, err := i.listLabeledWorkloadsByKind(ctx, kind)
	if err != nil {
		return err
	}

	var errs []error
	for _, target := range targets {
		err := i.mutateWorkload(
			ctx,
			target.namespace,
			target.workload,
			func(ctx context.Context, namespace string, workload string) (*workloadTarget, error) {
				return i.findWorkloadTargetByKind(ctx, kind, namespace, workload)
			},
			"removing traffic-agent sidecar during startup cleanup",
			i.removeInjectedSidecar,
		)
		if err != nil && !errors.Is(err, ErrWorkloadNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// removeInjectedSidecar drops the agent container, the injected label and the
// probe rewrites, reporting whether anything changed.
func (i *WorkloadInjector) removeInjectedSidecar(target *workloadTarget) bool {
	filtered := make([]corev1.Container, 0, len(target.template.Spec.Containers))
	removed := false
	for _, container := range target.template.Spec.Containers {
		if container.Name == i.options.ContainerName {
			removed = true
			continue
		}
		filtered = append(filtered, container)
	}

	changed := false
	if removed {
		target.template.Spec.Containers = filtered
		changed = true
	}
	if removeInjectedLabel(target.object) {
		changed = true
	}
	if restoreProbes(target) {
		changed = true
	}
	return changed
}

type workloadIdentifier struct {
	namespace string
	workload  string
}

func (i *WorkloadInjector) listLabeledWorkloadsByKind(ctx context.Context, kind workloadKind) ([]workloadIdentifier, error) {
	operation, ok := i.operations[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}
	workloads, err := operation.list(ctx)
	if err != nil {
		return nil, fmt.Errorf("list %ss for startup cleanup: %w", kind, err)
	}
	return workloads, nil
}

func identifiers[T any, PT interface {
	*T
	metav1.Object
}](items []T) []workloadIdentifier {
	workloads := make([]workloadIdentifier, 0, len(items))
	for index := range items {
		object := PT(&items[index])
		workloads = append(workloads, workloadIdentifier{
			namespace: object.GetNamespace(),
			workload:  object.GetName(),
		})
	}
	return workloads
}

func (i *WorkloadInjector) mutateWorkload(
	ctx context.Context,
	namespace string,
	workload string,
	findTarget workloadFinder,
	updateAction string,
	mutate func(target *workloadTarget) bool,
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		target, err := findTarget(ctx, namespace, workload)
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

func (i *WorkloadInjector) findWorkloadTarget(ctx context.Context, namespace string, workload string) (*workloadTarget, error) {
	for _, kind := range workloadKinds {
		target, err := i.findWorkloadTargetByKind(ctx, kind, namespace, workload)
		if err == nil {
			return target, nil
		}
		if !errors.Is(err, ErrWorkloadNotFound) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w: %s/%s", ErrWorkloadNotFound, namespace, workload)
}

func (i *WorkloadInjector) findWorkloadTargetByKind(ctx context.Context, kind workloadKind, namespace string, workload string) (*workloadTarget, error) {
	operation, ok := i.operations[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}
	return operation.find(ctx, namespace, workload)
}

func newWorkloadOperations(client kubernetes.Interface) map[workloadKind]workloadOperation {
	listOptions := metav1.ListOptions{LabelSelector: InjectedLabelKey + "=" + injectedLabelValue}
	return map[workloadKind]workloadOperation{
		workloadKindDeployment: {
			list: func(ctx context.Context) ([]workloadIdentifier, error) {
				list, err := client.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, listOptions)
				if err != nil {
					return nil, err
				}
				return identifiers(list.Items), nil
			},
			find: func(ctx context.Context, namespace string, workload string) (*workloadTarget, error) {
				api := client.AppsV1().Deployments(namespace)
				return fetchWorkload(ctx, workloadKindDeployment, namespace, workload, api.Get,
					func(obj *appsv1.Deployment) *corev1.PodTemplateSpec { return &obj.Spec.Template },
					func(ctx context.Context, obj *appsv1.Deployment) error {
						_, err := api.Update(ctx, obj, metav1.UpdateOptions{})
						return err
					})
			},
		},
		workloadKindStatefulSet: {
			list: func(ctx context.Context) ([]workloadIdentifier, error) {
				list, err := client.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, listOptions)
				if err != nil {
					return nil, err
				}
				return identifiers(list.Items), nil
			},
			find: func(ctx context.Context, namespace string, workload string) (*workloadTarget, error) {
				api := client.AppsV1().StatefulSets(namespace)
				return fetchWorkload(ctx, workloadKindStatefulSet, namespace, workload, api.Get,
					func(obj *appsv1.StatefulSet) *corev1.PodTemplateSpec { return &obj.Spec.Template },
					func(ctx context.Context, obj *appsv1.StatefulSet) error {
						_, err := api.Update(ctx, obj, metav1.UpdateOptions{})
						return err
					})
			},
		},
		workloadKindDaemonSet: {
			list: func(ctx context.Context) ([]workloadIdentifier, error) {
				list, err := client.AppsV1().DaemonSets(metav1.NamespaceAll).List(ctx, listOptions)
				if err != nil {
					return nil, err
				}
				return identifiers(list.Items), nil
			},
			find: func(ctx context.Context, namespace string, workload string) (*workloadTarget, error) {
				api := client.AppsV1().DaemonSets(namespace)
				return fetchWorkload(ctx, workloadKindDaemonSet, namespace, workload, api.Get,
					func(obj *appsv1.DaemonSet) *corev1.PodTemplateSpec { return &obj.Spec.Template },
					func(ctx context.Context, obj *appsv1.DaemonSet) error {
						_, err := api.Update(ctx, obj, metav1.UpdateOptions{})
						return err
					})
			},
		},
	}
}

func fetchWorkload[T interface {
	metav1.Object
	DeepCopy() T
}](
	ctx context.Context,
	kind workloadKind,
	namespace string,
	workload string,
	get func(context.Context, string, metav1.GetOptions) (T, error),
	template func(T) *corev1.PodTemplateSpec,
	update func(context.Context, T) error,
) (*workloadTarget, error) {
	found, err := get(ctx, workload, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s %s/%s", ErrWorkloadNotFound, kind, namespace, workload)
		}
		return nil, fmt.Errorf("get %s %s/%s: %w", kind, namespace, workload, err)
	}

	updated := found.DeepCopy()
	return &workloadTarget{
		kind:     kind,
		object:   updated,
		template: template(updated),
		update:   func(ctx context.Context) error { return update(ctx, updated) },
	}, nil
}

func (i *WorkloadInjector) buildContainer(session contracts.DebugSession) corev1.Container {
	return corev1.Container{
		Name:            i.options.ContainerName,
		Image:           i.options.Image,
		ImagePullPolicy: parsePullPolicy(i.options.ImagePullPolicy),
		Env: []corev1.EnvVar{
			{Name: "KRUN_SESSION_ID", Value: session.SessionID},
			{Name: "KRUN_SESSION_TOKEN", Value: session.SessionToken},
			{Name: "KRUN_MANAGER_ADDRESS", Value: i.options.ManagerAddress},
			{Name: "KRUN_TARGET_NAMESPACE", Value: session.Namespace},
			{Name: "KRUN_TARGET_WORKLOAD", Value: session.Workload},
			{Name: "KRUN_TARGET_PORT", Value: strconv.Itoa(session.ServicePort)},
			{Name: "KRUN_AGENT_PROBE_PORT", Value: strconv.Itoa(i.options.ProbePort)},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser: new(int64(0)),
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
func (i *WorkloadInjector) rewriteProbes(target *workloadTarget, targetPort int) bool {
	saved := map[string]savedProbes{}
	changed := false

	for idx := range target.template.Spec.Containers {
		container := &target.template.Spec.Containers[idx]
		if container.Name == i.options.ContainerName {
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
			rewritten := (*slot.probe).DeepCopy()
			rewriteProbePort(rewritten, i.options.ProbePort)
			*slot.probe = rewritten
			rewrote = true
		}
		if rewrote {
			saved[container.Name] = record
			changed = true
		}
	}

	if changed {
		annotations := target.object.GetAnnotations()
		// Keep an existing annotation: it holds the true originals from the
		// first injection; a re-inject sees already-rewritten probes.
		if _, exists := annotations[OriginalProbesAnnotation]; !exists {
			data, err := json.Marshal(saved)
			if err == nil {
				if annotations == nil {
					annotations = map[string]string{}
				}
				annotations[OriginalProbesAnnotation] = string(data)
				target.object.SetAnnotations(annotations)
			}
		}
	}
	return changed
}

// restoreProbes puts the original probe specs back and drops the annotation.
func restoreProbes(target *workloadTarget) bool {
	annotations := target.object.GetAnnotations()
	raw, ok := annotations[OriginalProbesAnnotation]
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

	delete(annotations, OriginalProbesAnnotation)
	if len(annotations) == 0 {
		target.object.SetAnnotations(nil)
	} else {
		target.object.SetAnnotations(annotations)
	}
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

func resolveTarget(session contracts.DebugSession) (string, string, error) {
	namespace := strings.TrimSpace(session.Namespace)
	if namespace == "" {
		namespace = "default"
	}
	workload := strings.TrimSpace(session.Workload)
	if workload == "" {
		workload = strings.TrimSpace(session.ServiceName)
	}
	if workload == "" {
		return "", "", errors.New("target workload is required")
	}
	return namespace, workload, nil
}

func findContainerIndex(containers []corev1.Container, name string) int {
	for idx, container := range containers {
		if container.Name == name {
			return idx
		}
	}
	return -1
}

func normalizeOptions(options Options) Options {
	options.ContainerName = strings.TrimSpace(options.ContainerName)
	if options.ContainerName == "" {
		options.ContainerName = DefaultContainerName
	}

	options.Image = strings.TrimSpace(options.Image)
	if options.Image == "" {
		options.Image = DefaultImage
	}

	options.ImagePullPolicy = strings.TrimSpace(options.ImagePullPolicy)
	if options.ImagePullPolicy == "" {
		options.ImagePullPolicy = DefaultImagePullPolicy
	}

	options.ManagerAddress = strings.TrimSpace(options.ManagerAddress)
	if options.ManagerAddress == "" {
		options.ManagerAddress = DefaultManagerAddress
	}

	if options.ProbePort < 1 || options.ProbePort > 65535 {
		options.ProbePort = DefaultProbePort
	}

	return options
}

func ensureInjectedLabel(obj metav1.Object) bool {
	labels := obj.GetLabels()
	if labels == nil {
		obj.SetLabels(map[string]string{InjectedLabelKey: injectedLabelValue})
		return true
	}
	if labels[InjectedLabelKey] == injectedLabelValue {
		return false
	}
	labels[InjectedLabelKey] = injectedLabelValue
	obj.SetLabels(labels)
	return true
}

func removeInjectedLabel(obj metav1.Object) bool {
	labels := obj.GetLabels()
	if len(labels) == 0 {
		return false
	}
	if _, found := labels[InjectedLabelKey]; !found {
		return false
	}
	delete(labels, InjectedLabelKey)
	if len(labels) == 0 {
		obj.SetLabels(nil)
		return true
	}
	obj.SetLabels(labels)
	return true
}

func parsePullPolicy(value string) corev1.PullPolicy {
	switch corev1.PullPolicy(value) {
	case corev1.PullAlways, corev1.PullNever, corev1.PullIfNotPresent:
		return corev1.PullPolicy(value)
	default:
		return corev1.PullIfNotPresent
	}
}
