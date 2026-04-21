package workspace

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	cfg "github.com/ftechmax/krun/internal/config"
)

func DiscoverServices(sourceDir string, searchDepth int) ([]cfg.Service, map[string]string, error) {
	services := []cfg.Service{}
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

func loadServicesFile(path string) ([]cfg.Service, error) {
	file, err := os.Open(path) //nolint:gosec // Path is discovered via WalkDir within the configured source root.
	if err != nil {
		return nil, err
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	var svc []cfg.Service
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

func applyDiscoveredServiceDefaults(svc []cfg.Service, project string) {
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
