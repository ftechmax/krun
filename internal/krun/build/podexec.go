package build

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/ftechmax/krun/internal/kube"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

func execInBuildPod(ctx context.Context, client *kube.Client, command []string, stdin io.Reader) (string, string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := execInBuildPodStream(ctx, client, command, stdin, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func execInBuildPodStream(
	ctx context.Context,
	client *kube.Client,
	command []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	req := client.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(buildPodName).
		Namespace("default").
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: buildPodName,
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
