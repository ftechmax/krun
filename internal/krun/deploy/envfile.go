package deploy

import (
	"fmt"
	"path/filepath"
	"strings"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
)

func Create(service cfg.Service, config cfg.Config, session contracts.DebugSession, containerName string) error {
	path, err := debugEnvFilePath(service, config)
	if err != nil {
		return err
	}

	namespace := session.Namespace
	if strings.TrimSpace(namespace) == "" {
		namespace = "default"
	}

	fmt.Print(path)

	panic("unimplemented")
}

func Remove(service cfg.Service, config cfg.Config, session contracts.DebugSession) error {
	panic("unimplemented")
}

func debugEnvFilePath(service cfg.Service, config cfg.Config) (string, error) {
	baseDir := config.KrunConfig.Path
	if service.Project != "" && config.ProjectPaths != nil {
		if rel, ok := config.ProjectPaths[service.Project]; ok && rel != "" {
			baseDir = filepath.Join(baseDir, filepath.FromSlash(rel))
		}
	}
	if strings.TrimSpace(baseDir) == "" {
		return "", fmt.Errorf("project path not available for service %s", service.Name)
	}
	if strings.TrimSpace(service.Path) != "" {
		baseDir = filepath.Join(baseDir, filepath.FromSlash(service.Path))
	}
	return filepath.Join(baseDir, "appsettings-debug.env"), nil
}
