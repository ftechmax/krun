package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voortman-steel-machinery/krun/internal/utils"
)

type Service struct {
    Name          string `json:"name"`
    Project       string `json:"project"` // This will be set based on the directory structure
    Path          string `json:"path"`
    Dockerfile    string `json:"dockerfile"`
    Context       string `json:"context"`
    ContainerPort int    `json:"container_port"` // Default is "8080"
}

type KrunUserConfig struct {
    Username   string `json:"username"`
    PrivateKey string `json:"private_key"`
}

type KrunSourceConfig struct {
    Path        string `json:"path"`
    SearchDepth int    `json:"search_depth"`
}

type KrunConfig struct {
    KrunUserConfig `json:"user"`
    KrunSourceConfig `json:"source"`
    Hostname   string `json:"hostname"`
    LocalRegistry string `json:"local_registry"`
    RemoteRegistry string `json:"remote_registry"`
}

type Config struct {
    KrunConfig
    KubeConfig string
    Registry   string
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

    return config, nil
}

func DiscoverServices(sourceDir string, searchDepth int, cacheFile string, cacheTtl time.Duration) ([]Service, error) {
    var services []Service
    maxDepth := searchDepth + 1 // Add 1 to include the root directory itself
    
    exePath, _ := utils.GetExecutablePath()
    cachePath := filepath.Join(filepath.Dir(exePath), cacheFile)

    // Check if cache file exists and is not older than cacheTtl
    info, err := os.Stat(cachePath)
    if err == nil && !info.IsDir() && info.ModTime().Add(cacheTtl).After(time.Now()) {
        fmt.Println("Using cached services from", cachePath)
        
        // Cache file is valid, read from it
        file, err := os.Open(cachePath)

        if err != nil {
            return nil, fmt.Errorf("failed to open cache file: %w", err)
        }
        defer file.Close()

        cacheBytes, err := io.ReadAll(file)
        if err != nil {
            return nil, fmt.Errorf("failed to read cache file: %w", err)
        }

        if err := json.Unmarshal(cacheBytes, &services); err != nil {
            return nil, fmt.Errorf("failed to unmarshal cache file: %w", err)
        }

        return services, nil
    }
    
    // If cache file is not valid, walk the directory to discover services
    err = filepath.WalkDir(sourceDir, func(path string, d os.DirEntry, err error) error {
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

            project := filepath.Base(filepath.Dir(path))
            for i := range svc {
                // Set the project field based on the directory structure
                svc[i].Project = project

                // Set the container port to a default value if not specified
                if svc[i].ContainerPort == 0 {
                    svc[i].ContainerPort = 8080 // Default port if not specified
                }
            }

            services = append(services, svc...)
        }
        return nil
    })

    if err != nil {
        return nil, err
    }

    // Cache the services in a file
    cacheData, err := json.Marshal(services)
    if err != nil {
        return nil, fmt.Errorf("failed to marshal services: %w", err)
    }
    err = os.WriteFile(cachePath, cacheData, 0644)
    if err != nil {
        return nil, fmt.Errorf("failed to write cache file: %w", err)
    }

    return services, nil
}