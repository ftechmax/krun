package build

import (
	"path/filepath"
	"strings"
	"testing"
)

func newTestMatcher(t *testing.T, content string) *ignoreMatcher {
	t.Helper()
	m, err := parseDockerignore(strings.NewReader(content))
	if err != nil {
		t.Fatalf("parseDockerignore returned error: %v", err)
	}
	return m
}

func TestDockerignoreMatchesBasicPatterns(t *testing.T) {
	m := newTestMatcher(t, strings.Join([]string{
		"# comment",
		"",
		"  ",
		"secrets.env",
		"/tmp",
		"*.log",
		"src/**/generated",
	}, "\n"))

	cases := map[string]bool{
		"secrets.env":              true,
		"nested/secrets.env":       false,
		"tmp/cache.bin":            true,
		"app.log":                  true,
		"logs/app.log":             false,
		"src/a/b/generated/out.go": true,
		"src/generated/out.go":     true,
		"other/generated/out.go":   false,
		"src/main.go":              false,
		"#comment":                 false,
	}

	for p, want := range cases {
		if got := m.matches(p); got != want {
			t.Errorf("matches(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestDockerignoreNegationReincludes(t *testing.T) {
	m := newTestMatcher(t, "*.md\n!README.md\n!CHANGELOG.md\nCHANGELOG.md\n")

	if !m.matches("CHANGELOG.md") {
		t.Errorf("expected CHANGELOG.md to be ignored")
	}
	if m.matches("README.md") {
		t.Errorf("expected README.md to be re-included by negation")
	}
}

func TestDockerignoreNegationInsideIgnoredDirectory(t *testing.T) {
	m := newTestMatcher(t, "build\n!build/keep.txt\n")

	if !m.matches("build/drop.txt") {
		t.Errorf("expected build/drop.txt to be ignored via parent match")
	}
	if m.matches("build/keep.txt") {
		t.Errorf("expected build/keep.txt to be re-included")
	}
	if m.canSkipDir("build") {
		t.Errorf("directory with negations must not be pruned")
	}
}

func TestDockerignoreCanSkipDirWithoutNegations(t *testing.T) {
	m := newTestMatcher(t, "dist\n")

	if !m.canSkipDir("dist") {
		t.Errorf("expected dist to be prunable")
	}
	if m.canSkipDir("src") {
		t.Errorf("unmatched directory must not be pruned")
	}
}

func TestDockerignoreSingleStarStopsAtSeparator(t *testing.T) {
	m := newTestMatcher(t, "src/*/temp\n")

	if !m.matches("src/a/temp/file.txt") {
		t.Errorf("expected src/a/temp/file.txt to be ignored")
	}
	if m.matches("src/a/b/temp/file.txt") {
		t.Errorf("* must not span a path separator")
	}
}

func TestDockerignoreTrailingDoubleStar(t *testing.T) {
	m := newTestMatcher(t, "vendor/**\n")

	if !m.matches("vendor/pkg/file.go") {
		t.Errorf("expected vendor/pkg/file.go to be ignored")
	}
}

func TestDockerignoreQuestionMark(t *testing.T) {
	m := newTestMatcher(t, "file?.txt\n")

	if !m.matches("file1.txt") {
		t.Errorf("expected file1.txt to be ignored")
	}
	if m.matches("file10.txt") {
		t.Errorf("? must match exactly one character")
	}
}

func TestDockerignoreSpecialCharactersAreLiteral(t *testing.T) {
	m := newTestMatcher(t, "a+b(c).txt\n")

	if !m.matches("a+b(c).txt") {
		t.Errorf("expected literal path to be ignored")
	}
	if m.matches("aab(c)Xtxt") {
		t.Errorf("regexp metacharacters must be escaped")
	}
}

func TestDockerignoreBareExclamationIsAnError(t *testing.T) {
	if _, err := parseDockerignore(strings.NewReader("!\n")); err == nil {
		t.Fatalf("expected error for bare exclusion pattern")
	}
}

func TestNilMatcherIgnoresNothing(t *testing.T) {
	var m *ignoreMatcher

	if m.matches("anything") || m.canSkipDir("anything") {
		t.Fatalf("nil matcher must not match")
	}
}

func TestLoadDockerignoreMissingFile(t *testing.T) {
	m, err := loadDockerignore(t.TempDir())
	if err != nil {
		t.Fatalf("loadDockerignore returned error: %v", err)
	}
	if m != nil {
		t.Fatalf("expected nil matcher when .dockerignore is absent, got %#v", m)
	}
}

func TestCollectLocalFilesRespectsDockerignore(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".dockerignore"), "*.log\n**/*.tmp\ndist\n!dist/keep.txt\n")
	writeTestFile(t, filepath.Join(root, "build.log"), "noise")
	writeTestFile(t, filepath.Join(root, "service", "api.go"), "package main")
	writeTestFile(t, filepath.Join(root, "service", "debug.log"), "kept: * is root-anchored")
	writeTestFile(t, filepath.Join(root, "service", "scratch.tmp"), "noise")
	writeTestFile(t, filepath.Join(root, "dist", "drop.txt"), "drop")
	writeTestFile(t, filepath.Join(root, "dist", "keep.txt"), "keep")

	files, err := collectLocalFiles(root, false)
	if err != nil {
		t.Fatalf("collectLocalFiles returned error: %v", err)
	}

	if _, ok := files["service/api.go"]; !ok {
		t.Errorf("expected service/api.go to be included")
	}
	if _, ok := files["build.log"]; ok {
		t.Errorf("expected root-level *.log to be ignored")
	}
	if _, ok := files["service/debug.log"]; !ok {
		t.Errorf("expected *.log to stay anchored to the project root")
	}
	if _, ok := files["service/scratch.tmp"]; ok {
		t.Errorf("expected **/*.tmp to be ignored at any depth")
	}
	if _, ok := files["dist/drop.txt"]; ok {
		t.Errorf("expected dist contents to be ignored")
	}
	if _, ok := files["dist/keep.txt"]; !ok {
		t.Errorf("expected dist/keep.txt to be re-included by negation")
	}
}

func TestCollectLocalFilesWithoutDockerignore(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "service", "api.go"), "package main")
	writeTestFile(t, filepath.Join(root, "service", "debug.log"), "noise")

	files, err := collectLocalFiles(root, false)
	if err != nil {
		t.Fatalf("collectLocalFiles returned error: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("expected all files without .dockerignore, got %#v", files)
	}
}
