package debug

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ftechmax/krun/internal/managerauth"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCheckKubernetesAPI(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"reachable", http.StatusOK},
		{"unavailable", http.StatusServiceUnavailable},
		{"access denied", http.StatusForbidden},
		{"connection refused", -1},
		{"timeout", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/version" {
					t.Errorf("requested path %q, want /version", r.URL.Path)
				}
				if tc.status == 0 {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			if tc.status == -1 {
				server.Close()
			}
			path := filepath.Join(t.TempDir(), "kubeconfig")
			config := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
contexts:
- name: test
  context:
    cluster: test
current-context: test
`, server.URL)
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			err := checkKubernetesAPI(path)
			if tc.status == http.StatusOK {
				if err != nil {
					t.Fatalf("reachable API: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "Kubernetes API check failed at "+server.URL) {
				t.Fatalf("expected Kubernetes API address and failure, got %v", err)
			}
			if tc.status == 0 && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected deadline exceeded, got %v", err)
			}
		})
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
