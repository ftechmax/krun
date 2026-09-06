package build

import (
	"context"
	"io"

	"github.com/ftechmax/krun/internal/kube"
)

func execInBuildPod(ctx context.Context, client *kube.Client, command []string, stdin io.Reader) (string, string, error) {
	return kube.ExecCapture(ctx, client, "default", buildPodName, buildPodName, command, stdin)
}

func execInBuildPodStream(
	ctx context.Context,
	client *kube.Client,
	command []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	return kube.Exec(ctx, client, "default", buildPodName, buildPodName, command, stdin, stdout, stderr)
}
