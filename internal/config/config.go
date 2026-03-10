package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ftechmax/krun/internal/utils"
)

type Service struct {
	Name                string              `json:"name"`
	Project             string              `json:"project"`   // This will be set based on the directory structure
	Namespace           string              `json:"namespace"` // Kubernetes namespace (default: "default")
	Path                string              `json:"path"`
	Dockerfile          string              `json:"dockerfile"`
	Context             string              `json:"context"`
	ContainerPort       int                 `json:"container_port"` // Default is "8080"
	InterceptPort       int                 `json:"intercept_port"` // Default is "5000"
	ServiceDependencies []ServiceDependency `json:"service_dependencies"`
}

type ServiceDependency struct {
	Host      string `json:"host"`
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Port      int    `json:"port"`
}

type KrunSourceConfig struct {
	Path        string `json:"path"`
	SearchDepth int    `json:"search_depth"`
}

type KrunConfig struct {
	KrunSourceConfig `json:"source"`
	Hostname         string `json:"hostname"`
	LocalRegistry    string `json:"local_registry"`
	RemoteRegistry   string `json:"remote_registry"`
}

type Config struct {
	KrunConfig
	KubeConfig   string
	Registry     string
	ProjectPaths map[string]string
}

func ParseKrunConfig() (KrunConfig, error) {
	exePath, err := utils.GetExecutablePath()
	if err != nil {
		return KrunConfig{}, fmt.Errorf("failed to get executable path: %w", err)
	}

	configPath := filepath.Join(filepath.Dir(exePath), "krun-config.json")
	file, err := os.Open(configPath)
	if err != nil {
		return KrunConfig{}, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		return KrunConfig{}, fmt.Errorf("failed to read file: %w", err)
	}

	var config KrunConfig
	if err := json.Unmarshal(bytes, &config); err != nil {
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
	maxDepth := searchDepth + 1 // Add 1 to include the root directory itself

	// Walk the directory to discover services
	err := filepath.WalkDir(sourceDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Calculate depth
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		depth := 0
		if rel != "." {
			depth = len(filepath.SplitList(rel))
			// On Windows, SplitList splits on ';', so use filepath.Separator instead
			depth = len(strings.Split(rel, string(os.PathSeparator)))
		}

		if d.IsDir() {
			if depth > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}

		if d.Name() == "krun.json" && depth <= maxDepth {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			bytes, err := io.ReadAll(file)
			if err != nil {
				return err
			}

			var svc []Service
			if err := json.Unmarshal(bytes, &svc); err != nil {
				return err
			}

			projectDir := filepath.Dir(path)
			project := filepath.Base(projectDir)
			projectRelPath, err := filepath.Rel(sourceDir, projectDir)
			if err != nil {
				return err
			}
			projectRelPath = filepath.ToSlash(projectRelPath)
			for i := range svc {
				// Set the project field based on the directory structure
				svc[i].Project = project
				projectPaths[project] = projectRelPath

				// Set the container port to a default value if not specified
				if svc[i].ContainerPort == 0 {
					svc[i].ContainerPort = 8080 // Default port if not specified
				}

				// Set the intercept port to a default value if not specified
				if svc[i].InterceptPort == 0 {
					svc[i].InterceptPort = 5000 // Default intercept port if not specified
				}
			}

			services = append(services, svc...)
		}
		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	return services, projectPaths, nil
}
