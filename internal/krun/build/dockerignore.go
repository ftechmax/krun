package build

import (
	"os"
	"path/filepath"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

const dockerignoreFileName = ".dockerignore"

func loadDockerignore(root string) (*patternmatcher.PatternMatcher, error) {
	patterns := []string{"**/.github", "**/.vs", "**/.git", "**/.angular", "**/bin", "**/obj", "**/node_modules", "**/k8s", "**/docs"}
	file, err := os.Open(filepath.Join(root, dockerignoreFileName))
	if err == nil {
		defer file.Close()
		patterns, err = ignorefile.ReadAll(file)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return patternmatcher.New(patterns)
}
