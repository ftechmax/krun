package deploy

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestServiceDir(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name         string
		base         string
		projectPaths map[string]string
		service      cfg.Service
		want         string
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
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := cfg.Config{
				KrunConfig:   cfg.KrunConfig{KrunSourceConfig: cfg.KrunSourceConfig{Path: testCase.base}},
				ProjectPaths: testCase.projectPaths,
			}

			dir := serviceDir(testCase.service, config)
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

func TestSelectContainer(t *testing.T) {
	container := func(name string, ports ...int32) corev1.Container {
		c := corev1.Container{Name: name}
		for _, port := range ports {
			c.Ports = append(c.Ports, corev1.ContainerPort{ContainerPort: port})
		}
		return c
	}
	agent := container(contracts.AgentContainerName)

	cases := []struct {
		name       string
		containers []corev1.Container
		service    cfg.Service
		override   string
		want       string
		wantErr    bool
	}{
		{
			name:       "explicit container wins",
			containers: []corev1.Container{container("api", 8080), agent},
			service:    cfg.Service{Name: "uma-api", ContainerPort: 8080},
			override:   "other",
			want:       "other",
		},
		{
			name:       "container named after the service",
			containers: []corev1.Container{container("sidecar", 8080), container("uma-api"), agent},
			service:    cfg.Service{Name: "uma-api", ContainerPort: 8080},
			want:       "uma-api",
		},
		{
			name:       "container exposing the service port",
			containers: []corev1.Container{container("metrics", 9090), container("api", 8080), agent},
			service:    cfg.Service{Name: "uma-api", ContainerPort: 8080},
			want:       "api",
		},
		{
			name:       "only container besides the agent",
			containers: []corev1.Container{container("api"), agent},
			service:    cfg.Service{Name: "uma-api"},
			want:       "api",
		},
		{
			name:       "ambiguous containers are an error",
			containers: []corev1.Container{container("a"), container("b"), agent},
			service:    cfg.Service{Name: "uma-api"},
			wantErr:    true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "uma-api-abc"},
				Spec:       corev1.PodSpec{Containers: testCase.containers},
			}
			got, err := selectContainer(pod, testCase.service, testCase.override)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("got %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestFilterEnvVars(t *testing.T) {
	raw := `KEY1=value1

=invalid
NOEQUALS
KEY2=val=ue2
rabbitmq__host=rabbitmq.default.svc
mongodb__connection-string=mongodb://localhost
ASPNETCORE_HTTP_PORTS=8080
PATH=/usr/bin
HOME=/home/dotnet
HOSTNAME=pod-abc123
KUBERNETES_PORT=tcp://10.43.0.1:443
KUBERNETES_SERVICE_HOST=10.43.0.1
MYAPP_SERVICE_HOST=10.43.1.1
MYAPP_SERVICE_PORT=80
MYAPP_SERVICE_PORT_HTTP=80
MYAPP_PORT=tcp://10.43.1.1:80
MYAPP_PORT_80_TCP=tcp://10.43.1.1:80
MYAPP_PORT_80_TCP_ADDR=10.43.1.1
MYAPP_PORT_80_TCP_PORT=80
MYAPP_PORT_80_TCP_PROTO=tcp
DOTNET_RUNNING_IN_CONTAINER=true
DOTNET_VERSION=10.0.4
DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=true
`
	want := `KEY1=value1
KEY2=val=ue2
rabbitmq__host=rabbitmq.default.svc
mongodb__connection-string=mongodb://localhost
ASPNETCORE_HTTP_PORTS=8080
`
	if got := filterEnvVars(raw); got != want {
		t.Fatalf("filtered env = %q, want %q", got, want)
	}
}

func TestWriteDotEnv(t *testing.T) {
	dir := t.TempDir()
	vars := "KEY1=value1\nKEY2=value2\n"

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
	if err := writeDotEnv(dir, "NEW=value\n"); err != nil {
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
