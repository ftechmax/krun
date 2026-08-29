package debug

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ftechmax/krun/internal/managerauth"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestResolveHelperBinaryPathFallsBackToWorkingDirectory(t *testing.T) {
	helperName := "krun-helper"
	if runtime.GOOS == "windows" {
		helperName = "krun-helper.exe"
	}

	tempDirectory := t.TempDir()
	helperPath := filepath.Join(tempDirectory, helperName)
	if err := os.WriteFile(helperPath, []byte("helper"), 0o700); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(tempDirectory); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalDirectory)
	})

	resolvedPath, err := resolveHelperBinaryPath()
	if err != nil {
		t.Fatalf("resolve helper binary path: %v", err)
	}
	if resolvedPath != helperPath {
		t.Fatalf("expected %q, got %q", helperPath, resolvedPath)
	}
}

func TestConfigureManagerAuthInjectsDigestAndPreservesEnvironment(t *testing.T) {
	digest := managerauth.Digest("manager-token")
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      "krun-traffic-manager",
			"namespace": "krun-system",
		},
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name": "manager",
						"env": []any{
							map[string]any{"name": "KRUN_AGENT_IMAGE", "value": "agent:dev"},
							map[string]any{"name": managerauth.TokenHashEnv, "value": "old"},
						},
					}},
				},
			},
		},
	}}

	if err := configureManagerAuth([]*unstructured.Unstructured{deployment}, digest); err != nil {
		t.Fatalf("configure manager auth: %v", err)
	}
	containers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		t.Fatalf("read containers: found=%v err=%v", found, err)
	}
	manager := containers[0].(map[string]any)
	entries := manager["env"].([]any)
	values := map[string]string{}
	for _, item := range entries {
		entry := item.(map[string]any)
		values[entry["name"].(string)] = entry["value"].(string)
	}
	if values["KRUN_AGENT_IMAGE"] != "agent:dev" {
		t.Fatalf("existing environment was not preserved: %+v", values)
	}
	if values[managerauth.TokenHashEnv] != digest {
		t.Fatalf("manager digest = %q, want %q", values[managerauth.TokenHashEnv], digest)
	}
	if len(entries) != 2 {
		t.Fatalf("expected duplicate verifier to be replaced, got %+v", entries)
	}
}

func TestResolveManagerTokenPathOverrideIsAbsolute(t *testing.T) {
	t.Setenv(managerTokenFileEnv, filepath.Join("relative", "manager-token"))
	path, err := resolveManagerTokenPath()
	if err != nil {
		t.Fatalf("resolve manager token path: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("expected absolute token path, got %q", path)
	}
}
