package build

import (
	"slices"
	"testing"

	cfg "github.com/ftechmax/krun/internal/config"
	deploy "github.com/ftechmax/krun/internal/krun/deploy"
)

func TestManifestDecodes(t *testing.T) {
	objs, err := deploy.DecodeManifestObjects([]byte(manifest))
	if err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(objs))
	}

	pod := objs[1]
	if pod.GetKind() != "Pod" || pod.GetName() != buildPodName {
		t.Fatalf("expected Pod %s, got %s %s", buildPodName, pod.GetKind(), pod.GetName())
	}
	containers := pod.Object["spec"].(map[string]any)["containers"].([]any)
	c := containers[0].(map[string]any)
	if c["name"] != buildPodName || c["image"] != buildkitImage {
		t.Fatalf("unexpected container name/image: %v %v", c["name"], c["image"])
	}
	if sc := c["securityContext"].(map[string]any); sc["privileged"] != nil {
		t.Fatalf("build container must not be privileged")
	}
}

func TestBuildctlArgs(t *testing.T) {
	svc := cfg.Service{
		Name:       "my-api",
		Project:    "proj",
		Context:    "src",
		Path:       "src/api",
		Dockerfile: "docker",
	}
	got := buildctlArgs(svc, "registry:5000")
	want := []string{
		"buildctl", "build",
		"--progress=plain",
		"--frontend=dockerfile.v0",
		"--local", "context=/var/workspace/proj/src",
		"--local", "dockerfile=/var/workspace/proj/src/api/docker",
		"--output", "type=image,name=registry:5000/my-api:latest,push=true",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected args:\n got  %q\n want %q", got, want)
	}
}

func TestDockerfilePathsMatchBuildctlDockerfileDir(t *testing.T) {
	svc := cfg.Service{Project: "proj", Path: "src/api", Dockerfile: "docker"}
	got := dockerfilePaths([]cfg.Service{svc, {Project: "proj", Path: "src/worker"}})
	want := []string{"src/api/docker/Dockerfile", "src/worker/Dockerfile"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected Dockerfile paths: got %q want %q", got, want)
	}
}
