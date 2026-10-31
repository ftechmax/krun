package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverServicesDepthAndDefaults(t *testing.T) {
	root := t.TempDir()
	rootProject := filepath.Base(root)
	for dir, contents := range map[string]string{
		".":           `[{"name":"root-service","project":"ignored"}]`,
		"app":         `[{"name":" api ","namespace":" dev ","container_port":9000,"intercept_port":9001},{"name":"worker"}]`,
		"team/nested": `[{"name":"nested-service"}]`,
		"empty":       `[]`,
		"invalid":     `[{`,
		"trailing":    `[] []`,
	} {
		projectDir := filepath.Join(root, dir)
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(projectDir, "krun.json"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rootService := Service{Namespace: "default", Name: "root-service", Project: rootProject, ContainerPort: 8080, InterceptPort: 5000}
	api := Service{Namespace: "dev", Name: "api", Project: "app", ContainerPort: 9000, InterceptPort: 9001}
	worker := Service{Namespace: "default", Name: "worker", Project: "app", ContainerPort: 8080, InterceptPort: 5000}
	nested := Service{Namespace: "default", Name: "nested-service", Project: "nested", ContainerPort: 8080, InterceptPort: 5000}
	for _, tt := range []struct {
		name     string
		depth    int
		services []Service
		paths    map[string]string
	}{
		{"root only", 0, []Service{rootService}, map[string]string{rootProject: "."}},
		{"one level", 1, []Service{api, worker, rootService}, map[string]string{rootProject: ".", "app": "app"}},
		{"nested", 2, []Service{api, worker, rootService, nested}, map[string]string{rootProject: ".", "app": "app", "nested": "team/nested"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			services, paths, err := DiscoverServices(root, tt.depth)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(services, tt.services) {
				t.Errorf("services = %#v, want %#v", services, tt.services)
			}
			if !reflect.DeepEqual(paths, tt.paths) {
				t.Errorf("project paths = %#v, want %#v", paths, tt.paths)
			}
		})
	}
}

func TestDiscoverServicesMissingSource(t *testing.T) {
	_, _, err := DiscoverServices(filepath.Join(t.TempDir(), "missing"), 1)
	if !os.IsNotExist(err) {
		t.Fatalf("expected missing source error, got %v", err)
	}
}

func TestDiscoverServicesResolvesDependencies(t *testing.T) {
	root := t.TempDir()
	contents := `[{"name":"api","service_dependencies":[
  {"host":" rabbitmq.default.svc ","port":5672},
  {"host":"ferretdb.ferretdb-system.svc","port":27017},
  {"host":" cache ","service":" redis ","namespace":" shared ","port":6379,"aliases":[" cache.local "]}
 ]}]`
	if err := os.WriteFile(filepath.Join(root, "krun.json"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	services, _, err := DiscoverServices(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []ServiceDependency{
		{Host: "rabbitmq.default.svc", Service: "rabbitmq", Namespace: "default", Port: 5672},
		{Host: "ferretdb.ferretdb-system.svc", Service: "ferretdb", Namespace: "ferretdb-system", Port: 27017},
		{Host: "cache", Service: "redis", Namespace: "shared", Port: 6379, Aliases: []string{"cache.local"}},
	}
	if !reflect.DeepEqual(services[0].ServiceDependencies, want) {
		t.Fatalf("dependencies = %+v, want %+v", services[0].ServiceDependencies, want)
	}
}
