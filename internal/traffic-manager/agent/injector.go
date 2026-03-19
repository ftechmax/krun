package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/ftechmax/krun/internal/contracts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
)

const (
	DefaultContainerName   = "krun-traffic-agent"
	DefaultImage           = "docker.io/ftechmax/krun-traffic-agent:latest"
	DefaultImagePullPolicy = "IfNotPresent"
	DefaultManagerAddress  = "http://krun-traffic-manager.krun-system.svc:8080"
	InjectedLabelKey   = "krun.ftechmax.com/traffic-agent-injected"
	injectedLabelValue = "true"
)

var ErrWorkloadNotFound = errors.New("target workload not found")

type Injector interface {
	Inject(ctx context.Context, session contracts.DebugSession) error
	Remove(ctx context.Context, session contracts.DebugSession) error
	Cleanup(ctx context.Context) error
}

type NoopInjector struct{}

func (NoopInjector) Inject(context.Context, contracts.DebugSession) error {
	return nil
}

func (NoopInjector) Remove(context.Context, contracts.DebugSession) error {
	return nil
}

func (NoopInjector) Cleanup(context.Context) error {
	return nil
}

type Options struct {
	ContainerName   string
	Image           string
	ImagePullPolicy string
	ManagerAddress  string
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

		return changed
	})
}

func (i *WorkloadInjector) Remove(ctx context.Context, session contracts.DebugSession) error {
	namespace, workload, err := resolveTarget(session)
	if err != nil {
		return err
	}

	return i.mutateWorkload(ctx, namespace, workload, i.findWorkloadTarget, "removing traffic-agent sidecar", func(target *workloadTarget) bool {
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
		return changed
	})
}

func (i *WorkloadInjector) Cleanup(ctx context.Context) error {
	kinds := []workloadKind{
		workloadKindDeployment,
		workloadKindStatefulSet,
		workloadKindDaemonSet,
	}
	var errs []error
	for _, kind := range kinds {
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
		target := target
		err := i.mutateWorkload(
			ctx,
			target.namespace,
			target.workload,
			func(ctx context.Context, namespace string, workload string) (*workloadTarget, error) {
				return i.findWorkloadTargetByKind(ctx, kind, namespace, workload)
			},
			"removing traffic-agent sidecar during startup cleanup",
			func(target *workloadTarget) bool {
				return i.removeInjectedSidecarAndAnnotation(target)
			},
		)
		if err != nil && !errors.Is(err, ErrWorkloadNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (i *WorkloadInjector) removeInjectedSidecarAndAnnotation(target *workloadTarget) bool {
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
	return changed
}

type workloadIdentifier struct {
	namespace string
	workload  string
}

func (i *WorkloadInjector) listLabeledWorkloadsByKind(ctx context.Context, kind workloadKind) ([]workloadIdentifier, error) {
	opts := metav1.ListOptions{LabelSelector: InjectedLabelKey + "=" + injectedLabelValue}
	switch kind {
	case workloadKindDeployment:
		list, err := i.client.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("list %ss for startup cleanup: %w", kind, err)
		}
		workloads := make([]workloadIdentifier, 0, len(list.Items))
		for _, item := range list.Items {
			workloads = append(workloads, workloadIdentifier{namespace: item.Namespace, workload: item.Name})
		}
		return workloads, nil
	case workloadKindStatefulSet:
		list, err := i.client.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("list %ss for startup cleanup: %w", kind, err)
		}
		workloads := make([]workloadIdentifier, 0, len(list.Items))
		for _, item := range list.Items {
			workloads = append(workloads, workloadIdentifier{namespace: item.Namespace, workload: item.Name})
		}
		return workloads, nil
	case workloadKindDaemonSet:
		list, err := i.client.AppsV1().DaemonSets(metav1.NamespaceAll).List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("list %ss for startup cleanup: %w", kind, err)
		}
		workloads := make([]workloadIdentifier, 0, len(list.Items))
		for _, item := range list.Items {
			workloads = append(workloads, workloadIdentifier{namespace: item.Namespace, workload: item.Name})
		}
		return workloads, nil
	default:
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}
}

type workloadFinder func(ctx context.Context, namespace string, workload string) (*workloadTarget, error)

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
	kinds := []workloadKind{
		workloadKindDeployment,
		workloadKindStatefulSet,
		workloadKindDaemonSet,
	}
	for _, kind := range kinds {
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
	switch kind {
	case workloadKindDeployment:
		deployment, err := i.client.AppsV1().Deployments(namespace).Get(ctx, workload, metav1.GetOptions{})
		if err == nil {
			updated := deployment.DeepCopy()
			return &workloadTarget{
				kind:     workloadKindDeployment,
				object:   updated,
				template: &updated.Spec.Template,
				update: func(ctx context.Context) error {
					_, err := i.client.AppsV1().Deployments(namespace).Update(ctx, updated, metav1.UpdateOptions{})
					return err
				},
			}, nil
		}
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s %s/%s", ErrWorkloadNotFound, kind, namespace, workload)
		}
		return nil, fmt.Errorf("get %s %s/%s: %w", kind, namespace, workload, err)
	case workloadKindStatefulSet:
		statefulSet, err := i.client.AppsV1().StatefulSets(namespace).Get(ctx, workload, metav1.GetOptions{})
		if err == nil {
			updated := statefulSet.DeepCopy()
			return &workloadTarget{
				kind:     workloadKindStatefulSet,
				object:   updated,
				template: &updated.Spec.Template,
				update: func(ctx context.Context) error {
					_, err := i.client.AppsV1().StatefulSets(namespace).Update(ctx, updated, metav1.UpdateOptions{})
					return err
				},
			}, nil
		}
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s %s/%s", ErrWorkloadNotFound, kind, namespace, workload)
		}
		return nil, fmt.Errorf("get %s %s/%s: %w", kind, namespace, workload, err)
	case workloadKindDaemonSet:
		daemonSet, err := i.client.AppsV1().DaemonSets(namespace).Get(ctx, workload, metav1.GetOptions{})
		if err == nil {
			updated := daemonSet.DeepCopy()
			return &workloadTarget{
				kind:     workloadKindDaemonSet,
				object:   updated,
				template: &updated.Spec.Template,
				update: func(ctx context.Context) error {
					_, err := i.client.AppsV1().DaemonSets(namespace).Update(ctx, updated, metav1.UpdateOptions{})
					return err
				},
			}, nil
		}
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s %s/%s", ErrWorkloadNotFound, kind, namespace, workload)
		}
		return nil, fmt.Errorf("get %s %s/%s: %w", kind, namespace, workload, err)
	default:
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}
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
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser: ptr.To(int64(0)),
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"NET_ADMIN"},
			},
		},
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
