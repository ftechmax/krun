package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
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
}

func ParseKrunConfig() (KrunConfig, error) {
	return ParseKrunConfigWithHome("")
}

func ParseKrunConfigWithHome(homeDir string) (KrunConfig, error) {
	exePath, err := utils.GetExecutablePath()
	if err != nil {
		return KrunConfig{}, fmt.Errorf("failed to get executable path: %w", err)
	}

	return ParseKrunConfigFromDirWithHome(filepath.Dir(exePath), homeDir)
}

func ParseKrunConfigFromDir(configDir string) (KrunConfig, error) {
	return ParseKrunConfigFromDirWithHome(configDir, "")
}

func ParseKrunConfigFromDirWithHome(configDir string, homeDir string) (KrunConfig, error) {
	configPath := filepath.Join(configDir, "krun-config.json")
	file, err := os.Open(configPath) //nolint:gosec // Path is derived from a trusted config directory and fixed filename.
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

	config.Path = ExpandPathWithHome(config.Path, homeDir)

	return config, nil
}

func ExpandPath(path string) string {
	return ExpandPathWithHome(path, "")
}

func ExpandPathWithHome(path string, homeDir string) string {
	return expandHomePath(os.ExpandEnv(path), homeDir)
}

func expandHomePath(path string, homeDir string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return path
	}

	home := strings.TrimSpace(homeDir)
	if home == "" {
		home = resolvePreferredHomeDir()
	}
	if home == "" {
		return path
	}

	if trimmed == "~" {
		return home
	}

	if strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~\\") {
		suffix := strings.TrimPrefix(trimmed, "~/")
		suffix = strings.TrimPrefix(suffix, "~\\")
		return filepath.Join(home, suffix)
	}

	return path
}

func resolvePreferredHomeDir() string {
	if home := strings.TrimSpace(os.Getenv("KRUN_HOME")); home != "" {
		return home
	}
	if home := lookupHomeByUsername(strings.TrimSpace(os.Getenv("SUDO_USER"))); home != "" {
		return home
	}
	if home := lookupHomeByUID(strings.TrimSpace(os.Getenv("PKEXEC_UID"))); home != "" {
		return home
	}
	if home := lookupHomeByUID(strings.TrimSpace(os.Getenv("SUDO_UID"))); home != "" {
		return home
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(home)
}

func lookupHomeByUsername(username string) string {
	if username == "" {
		return ""
	}
	record, err := user.Lookup(username)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(record.HomeDir)
}

func lookupHomeByUID(uid string) string {
	if uid == "" {
		return ""
	}
	record, err := user.LookupId(uid)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(record.HomeDir)
}

func DiscoverServices(sourceDir string, searchDepth int) ([]Service, map[string]string, error) {
	services := []Service{}
	projectPaths := map[string]string{}
	maxDepth := searchDepth + 1 // Add 1 to include the root directory itself

	// Walk the directory to discover services
	err := filepath.WalkDir(sourceDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		depth, err := pathDepth(sourceDir, path)
		if err != nil {
			return err
		}

		if d.IsDir() {
			if depth > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}

		if !isKrunConfigFile(d.Name(), depth, maxDepth) {
			return nil
		}

		svc, err := loadServicesFile(path)
		if err != nil {
			return err
		}
		if len(svc) == 0 {
			return nil
		}

		project, projectRelPath, err := projectInfo(sourceDir, path)
		if err != nil {
			return err
		}
		applyDiscoveredServiceDefaults(svc, project)
		projectPaths[project] = projectRelPath
		services = append(services, svc...)
		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	return services, projectPaths, nil
}

func pathDepth(sourceDir string, path string) (int, error) {
	rel, err := filepath.Rel(sourceDir, path)
	if err != nil {
		return 0, err
	}
	if rel == "." {
		return 0, nil
	}
	// filepath.SplitList splits PATH-like lists, not path segments.
	return len(strings.Split(rel, string(os.PathSeparator))), nil
}

func isKrunConfigFile(name string, depth int, maxDepth int) bool {
	return name == "krun.json" && depth <= maxDepth
}

func loadServicesFile(path string) ([]Service, error) {
	file, err := os.Open(path) //nolint:gosec // Path is discovered via WalkDir within the configured source root.
	if err != nil {
		return nil, err
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	var svc []Service
	if err := json.Unmarshal(bytes, &svc); err != nil {
		fmt.Printf("Warning: skipping invalid krun.json at %s: %v\n", path, err)
		return nil, nil
	}
	return svc, nil
}

func projectInfo(sourceDir string, path string) (string, string, error) {
	projectDir := filepath.Dir(path)
	project := filepath.Base(projectDir)
	projectRelPath, err := filepath.Rel(sourceDir, projectDir)
	if err != nil {
		return "", "", err
	}
	return project, filepath.ToSlash(projectRelPath), nil
}

func applyDiscoveredServiceDefaults(svc []Service, project string) {
	for i := range svc {
		// Set the project field based on the directory structure.
		svc[i].Project = project

		// Set the container port to a default value if not specified.
		if svc[i].ContainerPort == 0 {
			svc[i].ContainerPort = 8080
		}

		// Set the intercept port to a default value if not specified.
		if svc[i].InterceptPort == 0 {
			svc[i].InterceptPort = 5000
		}
	}
}
