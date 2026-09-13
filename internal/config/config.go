package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ftechmax/krun/internal/utils"
)

type Service struct {
	Name                string              `json:"name"`
	Project             string              `json:"project"`   // Derived from the project directory.
	Namespace           string              `json:"namespace"` // Kubernetes namespace (default: "default")
	Path                string              `json:"path"`
	Dockerfile          string              `json:"dockerfile"`
	Context             string              `json:"context"`
	ContainerPort       int                 `json:"container_port"` // Defaults to 8080.
	InterceptPort       int                 `json:"intercept_port"` // Defaults to 5000.
	ServiceDependencies []ServiceDependency `json:"service_dependencies"`
}

type ServiceDependency struct {
	Host      string   `json:"host"`
	Namespace string   `json:"namespace"`
	Service   string   `json:"service"`
	Port      int      `json:"port"`
	Aliases   []string `json:"aliases"`
}

type KrunSourceConfig struct {
	Path        string `json:"path"`
	SearchDepth int    `json:"search_depth"`
}

type KrunConfig struct {
	KrunSourceConfig `json:"source"`
	LocalRegistry    string `json:"local_registry"`
	RemoteRegistry   string `json:"remote_registry"`
}

type Config struct {
	KrunConfig
	KubeConfig   string
	Registry     string
	ProjectPaths map[string]string
	// Version is the krun build version, used to detect a helper or cluster
	// runtime built from a different version.
	Version string
}

func ParseKrunConfig() (KrunConfig, error) {
	exePath, err := utils.GetExecutablePath()
	if err != nil {
		return KrunConfig{}, fmt.Errorf("failed to get executable path: %w", err)
	}

	configPath := filepath.Join(filepath.Dir(exePath), "krun-config.json")
	contents, err := os.ReadFile(configPath)
	if err != nil {
		return KrunConfig{}, fmt.Errorf("read config %s: %w", configPath, err)
	}

	var config KrunConfig
	if err := json.Unmarshal(contents, &config); err != nil {
		return KrunConfig{}, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	config.KrunSourceConfig.Path = expandHomePath(config.KrunSourceConfig.Path)

	return config, nil
}

func expandHomePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return path
	}

	if trimmed == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}

	if strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		suffix := strings.TrimPrefix(trimmed, "~/")
		suffix = strings.TrimPrefix(suffix, "~\\")
		return filepath.Join(home, suffix)
	}

	return path
}

func DiscoverServices(sourceDir string, searchDepth int) ([]Service, map[string]string, error) {
	var services []Service
	projectPaths := map[string]string{}
	maxDepth := searchDepth + 1 // Include the krun.json filename in the depth.

	err := filepath.WalkDir(sourceDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		depth := 0
		if rel != "." {
			depth = len(strings.Split(rel, string(os.PathSeparator)))
		}

		if d.IsDir() {
			if depth > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}

		if d.Name() != "krun.json" || depth > maxDepth {
			return nil
		}

		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		var projectServices []Service
		if err := json.Unmarshal(contents, &projectServices); err != nil {
			fmt.Printf("Warning: skipping invalid krun.json at %s: %v\n", path, err)
			return nil
		}

		projectDir := filepath.Dir(path)
		project := filepath.Base(projectDir)
		projectRelPath, err := filepath.Rel(sourceDir, projectDir)
		if err != nil {
			return err
		}
		if len(projectServices) > 0 {
			projectPaths[project] = filepath.ToSlash(projectRelPath)
		}
		for i := range projectServices {
			service := &projectServices[i]
			service.Project = project
			if service.ContainerPort == 0 {
				service.ContainerPort = 8080
			}
			if service.InterceptPort == 0 {
				service.InterceptPort = 5000
			}
		}
		services = append(services, projectServices...)
		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	return services, projectPaths, nil
}
