package kube

import (
	"bytes"
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

type Client struct {
	RestConfig    *rest.Config
	Clientset     kubernetes.Interface
	DynamicClient dynamic.Interface
	Mapper        meta.RESTMapper
}

func NewClient(kubeConfigPath string) (*Client, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("build kube rest config: %w", err)
	}

	return newClientFromRestConfig(restCfg)
}

func NewInClusterClient() (*Client, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("build in-cluster kube rest config: %w", err)
	}

	return newClientFromRestConfig(restCfg)
}

func newClientFromRestConfig(restCfg *rest.Config) (*Client, error) {
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create typed kube client: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create dynamic kube client: %w", err)
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(
		memory.NewMemCacheClient(clientset.Discovery()),
	)

	return &Client{
		RestConfig:    restCfg,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		Mapper:        mapper,
	}, nil
}

// Exec runs a command in a container via the pod exec subresource, streaming
// stdin/stdout/stderr. A nil stream is simply not requested.
func Exec(ctx context.Context, client *Client, namespace, pod, container string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	req := client.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     stdin != nil,
			Stdout:    stdout != nil,
			Stderr:    stderr != nil,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(client.RestConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("create exec stream: %w", err)
	}

	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
}

// ExecCapture is Exec with stdout and stderr buffered into strings.
func ExecCapture(ctx context.Context, client *Client, namespace, pod, container string, command []string, stdin io.Reader) (string, string, error) {
	var stdout, stderr bytes.Buffer
	err := Exec(ctx, client, namespace, pod, container, command, stdin, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}
