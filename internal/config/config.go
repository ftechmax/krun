package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	AuthToken    string
}

const (
	KrunDirName    = ".krun"
	ConfigFileName = "config.json"
	TokenFileName  = "token.bin"
)

func LoadKrunConfig() (KrunConfig, error) {
	dir, err := defaultKrunDir()
	if err != nil {
		return KrunConfig{}, err
	}
	return LoadKrunConfigDir(dir)
}

// LoadKrunConfigDir reads <dir>/config.json.
func LoadKrunConfigDir(dir string) (KrunConfig, error) {
	if strings.TrimSpace(dir) == "" {
		return KrunConfig{}, fmt.Errorf("config dir is required")
	}
	file := filepath.Join(dir, ConfigFileName)
	if _, err := os.Stat(file); os.IsNotExist(err) {
		return KrunConfig{}, fmt.Errorf("file %s does not exist: %w", file, err)
	}

	bytes, err := os.ReadFile(file) //nolint:gosec // Path derived from caller-supplied dir + fixed filename.
	if err != nil {
		return KrunConfig{}, fmt.Errorf("failed to read file: %w", err)
	}

	var config KrunConfig
	if err := json.Unmarshal(bytes, &config); err != nil {
		return KrunConfig{}, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	config.Path = expandPathWithHome(config.Path, dir)

	return config, nil
}

func LoadToken() (string, error) {
	dir, err := defaultKrunDir()
	if err != nil {
		return "", err
	}
	return LoadTokenDir(dir)
}

// LoadTokenDir reads <dir>/token.bin.
func LoadTokenDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("config dir is required")
	}
	file := filepath.Join(dir, TokenFileName)
	if _, err := os.Stat(file); os.IsNotExist(err) {
		return "", fmt.Errorf("file %s does not exist: %w", file, err)
	}

	bytes, err := os.ReadFile(file) //nolint:gosec // Path derived from caller-supplied dir + fixed filename.
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	return base64.StdEncoding.EncodeToString(bytes), nil
}

func defaultKrunDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	return filepath.Join(homeDir, KrunDirName), nil
}

func expandPathWithHome(path, configDir string) string {
	if path == "" {
		return path
	}

	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		homeDir := homeFromConfigDir(configDir)
		if homeDir == "" {
			if h, err := os.UserHomeDir(); err == nil {
				homeDir = h
			} else {
				return filepath.ToSlash(path)
			}
		}
		if path == "~" {
			return filepath.ToSlash(homeDir)
		}
		return filepath.ToSlash(filepath.Join(homeDir, path[2:]))
	}

	return filepath.ToSlash(path)
}

func homeFromConfigDir(configDir string) string {
	if strings.TrimSpace(configDir) == "" {
		return ""
	}
	abs, err := filepath.Abs(configDir)
	if err != nil {
		return ""
	}
	parent := filepath.Dir(abs)
	if parent == "" || parent == abs {
		return ""
	}
	return parent
}
