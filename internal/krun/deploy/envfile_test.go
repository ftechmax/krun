package deploy

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	cfg "github.com/ftechmax/krun/internal/config"
)

func TestServiceDir(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name         string
		base         string
		projectPaths map[string]string
		service      cfg.Service
		want         string
		wantErr      bool
	}{
		{
			name:    "base directory when the service has no project or path",
			base:    root,
			service: cfg.Service{Name: "api"},
			want:    root,
		},
		{
			name:         "project resolves through the project map",
			base:         root,
			projectPaths: map[string]string{"myproject": "apps/myproject"},
			service:      cfg.Service{Name: "api", Project: "myproject"},
			want:         filepath.Join(root, "apps", "myproject"),
		},
		{
			name:    "service path appends to the base",
			base:    root,
			service: cfg.Service{Name: "api", Path: "src/api"},
			want:    filepath.Join(root, "src", "api"),
		},
		{
			name:         "project and service path compose",
			base:         root,
			projectPaths: map[string]string{"myproject": "apps/myproject"},
			service:      cfg.Service{Name: "api", Project: "myproject", Path: "src/api"},
			want:         filepath.Join(root, "apps", "myproject", "src", "api"),
		},
		{
			name:         "unknown project falls back to the base",
			base:         root,
			projectPaths: map[string]string{"other": "apps/other"},
			service:      cfg.Service{Name: "api", Project: "unknown"},
			want:         root,
		},
		{
			name:    "empty base directory is an error",
			base:    "",
			service: cfg.Service{Name: "api"},
			wantErr: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := cfg.Config{
				KrunConfig:   cfg.KrunConfig{KrunSourceConfig: cfg.KrunSourceConfig{Path: testCase.base}},
				ProjectPaths: testCase.projectPaths,
			}

			dir, err := serviceDir(testCase.service, config)
			if testCase.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dir != testCase.want {
				t.Fatalf("got %q, want %q", dir, testCase.want)
			}
		})
	}
}

func TestRemoveEnvFileExisting(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".env"), "KEY=value\n")

	config := cfg.Config{
		KrunConfig: cfg.KrunConfig{
			KrunSourceConfig: cfg.KrunSourceConfig{Path: root},
		},
	}
	service := cfg.Service{Name: "api"}

	if err := RemoveEnvFile(service, config); err != nil {
		t.Fatalf("RemoveEnvFile returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, ".env")); !os.IsNotExist(err) {
		t.Fatalf("expected .env to be removed, got err=%v", err)
	}
}

func TestRemoveEnvFileNotExisting(t *testing.T) {
	root := t.TempDir()
	config := cfg.Config{
		KrunConfig: cfg.KrunConfig{
			KrunSourceConfig: cfg.KrunSourceConfig{Path: root},
		},
	}
	service := cfg.Service{Name: "api"}

	if err := RemoveEnvFile(service, config); err != nil {
		t.Fatalf("RemoveEnvFile returned error for non-existing file: %v", err)
	}
}

func TestParseEnvVars(t *testing.T) {
	raw := "KEY1=value1\nKEY2=value2\n\n=invalid\nNOEQUALS\nKEY3=val=ue3\n"
	vars := parseEnvVars(raw)

	want := []envVar{
		{Key: "KEY1", Value: "value1"},
		{Key: "KEY2", Value: "value2"},
		{Key: "KEY3", Value: "val=ue3"},
	}

	if len(vars) != len(want) {
		t.Fatalf("got %d vars, want %d", len(vars), len(want))
	}
	for i, v := range vars {
		if v != want[i] {
			t.Errorf("vars[%d] = %v, want %v", i, v, want[i])
		}
	}
}

func TestFilterEnvVars(t *testing.T) {
	input := []envVar{
		// Should keep: app config
		{Key: "rabbitmq__host", Value: "rabbitmq.default.svc"},
		{Key: "mongodb__connection-string", Value: "mongodb://localhost"},
		{Key: "ASPNETCORE_HTTP_PORTS", Value: "8080"},
		// Should filter: system vars
		{Key: "PATH", Value: "/usr/bin"},
		{Key: "HOME", Value: "/home/dotnet"},
		{Key: "HOSTNAME", Value: "pod-abc123"},
		// Should filter: Kubernetes
		{Key: "KUBERNETES_PORT", Value: "tcp://10.43.0.1:443"},
		{Key: "KUBERNETES_SERVICE_HOST", Value: "10.43.0.1"},
		// Should filter: K8s service discovery
		{Key: "MYAPP_SERVICE_HOST", Value: "10.43.1.1"},
		{Key: "MYAPP_SERVICE_PORT", Value: "80"},
		{Key: "MYAPP_SERVICE_PORT_HTTP", Value: "80"},
		{Key: "MYAPP_PORT", Value: "tcp://10.43.1.1:80"},
		{Key: "MYAPP_PORT_80_TCP", Value: "tcp://10.43.1.1:80"},
		{Key: "MYAPP_PORT_80_TCP_ADDR", Value: "10.43.1.1"},
		{Key: "MYAPP_PORT_80_TCP_PORT", Value: "80"},
		{Key: "MYAPP_PORT_80_TCP_PROTO", Value: "tcp"},
		// Should filter: container runtime
		{Key: "DOTNET_RUNNING_IN_CONTAINER", Value: "true"},
		{Key: "DOTNET_VERSION", Value: "10.0.4"},
		{Key: "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT", Value: "true"},
	}

	filtered := filterEnvVars(input)

	want := []envVar{
		{Key: "rabbitmq__host", Value: "rabbitmq.default.svc"},
		{Key: "mongodb__connection-string", Value: "mongodb://localhost"},
		{Key: "ASPNETCORE_HTTP_PORTS", Value: "8080"},
	}

	if len(filtered) != len(want) {
		t.Fatalf("got %d vars, want %d: %v", len(filtered), len(want), filtered)
	}
	for i, v := range filtered {
		if v != want[i] {
			t.Errorf("filtered[%d] = %v, want %v", i, v, want[i])
		}
	}
}

func TestWriteDotEnv(t *testing.T) {
	dir := t.TempDir()
	vars := []envVar{
		{Key: "KEY1", Value: "value1"},
		{Key: "KEY2", Value: "value2"},
	}

	if err := writeDotEnv(dir, vars); err != nil {
		t.Fatalf("writeDotEnv: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}

	want := "KEY1=value1\nKEY2=value2\n"
	if string(data) != want {
		t.Fatalf("got %q, want %q", string(data), want)
	}
	assertOwnerOnlyMode(t, filepath.Join(dir, ".env"))
}

func TestWriteDotEnvCorrectsExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("OLD=value\n"), 0o644); err != nil {
		t.Fatalf("seed .env: %v", err)
	}
	if err := writeDotEnv(dir, []envVar{{Key: "NEW", Value: "value"}}); err != nil {
		t.Fatalf("writeDotEnv: %v", err)
	}
	assertOwnerOnlyMode(t, path)
}

func assertOwnerOnlyMode(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s permissions = %o, want 600", path, got)
	}
}
