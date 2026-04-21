package injector

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ftechmax/krun/internal/contracts"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

const (
	SidecarContainerName   = "krun-traffic-agent"
	DefaultAgentListenPort = 15001

	sessionAnnotation = "krun.ftechmax/session-id"

	envManagerAddress  = "KRUN_MANAGER_ADDRESS"
	envSessionID       = "KRUN_SESSION_ID"
	envSessionToken    = "KRUN_SESSION_TOKEN"
	envTargetPort      = "KRUN_TARGET_PORT"
	envAgentListenPort = "KRUN_AGENT_LISTEN_PORT"

	envAgentImage      = "KRUN_AGENT_IMAGE"
	envAgentPullPolicy = "KRUN_AGENT_IMAGE_PULL_POLICY"
	envManagerAdvert   = "KRUN_MANAGER_ADVERTISE_ADDRESS"

	defaultAgentImageRepo = "docker.io/ftechmax/krun-traffic-agent"
	defaultManagerAddr    = "krun-traffic-manager.krun-system.svc.cluster.local:8080"
	defaultListenPortAlt  = 15002
)

// Injector mutates target Deployments to add or remove the krun-traffic-agent
// sidecar container that bridges intercepted traffic to the manager relay.
type Injector struct {
	Clientset       kubernetes.Interface
	AgentImage      string
	AgentPullPolicy corev1.PullPolicy
	ManagerAddress  string
	AgentListenPort int
}

// NewFromEnv builds an Injector from the manager pod's environment. The
// version pins the default agent image tag to the manager build.
func NewFromEnv(clientset kubernetes.Interface, version string) *Injector {
	image := strings.TrimSpace(os.Getenv(envAgentImage))
	if image == "" {
		image = defaultAgentImageRepo + ":" + defaultImageTag(version)
	}
	pull := corev1.PullPolicy(strings.TrimSpace(os.Getenv(envAgentPullPolicy)))
	if pull == "" {
		pull = corev1.PullIfNotPresent
	}
	addr := strings.TrimSpace(os.Getenv(envManagerAdvert))
	if addr == "" {
		addr = defaultManagerAddr
	}
	return &Injector{
		Clientset:       clientset,
		AgentImage:      image,
		AgentPullPolicy: pull,
		ManagerAddress:  addr,
		AgentListenPort: DefaultAgentListenPort,
	}
}

// Inject patches the target Deployment to add the sidecar.
// Re-injecting the same deployment refreshes the sidecar.
func (i *Injector) Inject(ctx context.Context, session contracts.DebugSession) error {
	if session.Workload == "" {
		return fmt.Errorf("inject: workload is required")
	}
	if session.ServicePort <= 0 {
		return fmt.Errorf("inject: service_port must be > 0")
	}
	listenPort := i.pickListenPort(session.ServicePort)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployments := i.Clientset.AppsV1().Deployments(session.Namespace)
		dep, err := deployments.Get(ctx, session.Workload, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get deployment %s/%s: %w", session.Namespace, session.Workload, err)
		}

		sidecar := i.buildSidecar(session, listenPort)
		spec := &dep.Spec.Template.Spec
		if idx := containerIndex(spec.Containers, SidecarContainerName); idx >= 0 {
			spec.Containers[idx] = sidecar
		} else {
			spec.Containers = append(spec.Containers, sidecar)
		}
		if dep.Spec.Template.Annotations == nil {
			dep.Spec.Template.Annotations = map[string]string{}
		}
		dep.Spec.Template.Annotations[sessionAnnotation] = session.SessionID

		if _, err := deployments.Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			return err
		}
		return nil
	})
}

// Remove strips the sidecar from the target Deployment.
// Missing deployment or missing sidecar will not return an error.
func (i *Injector) Remove(ctx context.Context, session contracts.DebugSession) error {
	if session.Workload == "" {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployments := i.Clientset.AppsV1().Deployments(session.Namespace)
		dep, err := deployments.Get(ctx, session.Workload, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("get deployment %s/%s: %w", session.Namespace, session.Workload, err)
		}

		spec := &dep.Spec.Template.Spec
		idx := containerIndex(spec.Containers, SidecarContainerName)
		annotated := dep.Spec.Template.Annotations[sessionAnnotation] != ""
		if idx < 0 && !annotated {
			return nil
		}
		if idx >= 0 {
			spec.Containers = append(spec.Containers[:idx], spec.Containers[idx+1:]...)
		}
		if annotated {
			delete(dep.Spec.Template.Annotations, sessionAnnotation)
		}

		if _, err := deployments.Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			return err
		}
		return nil
	})
}

func (i *Injector) pickListenPort(target int) int {
	if i.AgentListenPort != target {
		return i.AgentListenPort
	}
	return defaultListenPortAlt
}

func (i *Injector) buildSidecar(session contracts.DebugSession, listenPort int) corev1.Container {
	return corev1.Container{
		Name:            SidecarContainerName,
		Image:           i.AgentImage,
		ImagePullPolicy: i.AgentPullPolicy,
		Env: []corev1.EnvVar{
			{Name: envManagerAddress, Value: i.ManagerAddress},
			{Name: envSessionID, Value: session.SessionID},
			{Name: envSessionToken, Value: session.SessionToken},
			{Name: envTargetPort, Value: strconv.Itoa(session.ServicePort)},
			{Name: envAgentListenPort, Value: strconv.Itoa(listenPort)},
		},
		Ports: []corev1.ContainerPort{
			{Name: "agent", ContainerPort: int32(listenPort), Protocol: corev1.ProtocolTCP}, //nolint:gosec
		},
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"NET_ADMIN", "NET_RAW"},
			},
		},
	}
}

func defaultImageTag(version string) string {
	v := strings.TrimSpace(version)
	if v == "" || v == "debug" {
		return "latest"
	}
	return v
}

func containerIndex(cs []corev1.Container, name string) int {
	for i, c := range cs {
		if c.Name == name {
			return i
		}
	}
	return -1
}
