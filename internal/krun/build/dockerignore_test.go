package build

import (
	"path/filepath"
	"testing"
)

func TestCollectLocalFilesRespectsDockerignore(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".dockerignore"), "*.log\n**/*.tmp\ndist\n!dist/keep.txt\n")
	writeTestFile(t, filepath.Join(root, "build.log"), "noise")
	writeTestFile(t, filepath.Join(root, "service", "api.go"), "package main")
	writeTestFile(t, filepath.Join(root, "service", "debug.log"), "kept: * is root-anchored")
	writeTestFile(t, filepath.Join(root, "service", "scratch.tmp"), "noise")
	writeTestFile(t, filepath.Join(root, "dist", "drop.txt"), "drop")
	writeTestFile(t, filepath.Join(root, "dist", "keep.txt"), "keep")

	files, err := collectLocalFiles(root)
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

	files, err := collectLocalFiles(root)
	if err != nil {
		t.Fatalf("collectLocalFiles returned error: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("expected all files without .dockerignore, got %#v", files)
	}
}

func TestDockerignoreOverridesDefaults(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".dockerignore"), "bin\n!bin/keep.txt\n**/test[0-9].log\n")
	for _, file := range []string{"bin/drop.txt", "bin/keep.txt", "docs/guide.md", "nested/test1.log"} {
		writeTestFile(t, filepath.Join(root, file), "contents")
	}
	files, err := collectLocalFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]bool{"bin/drop.txt": false, "bin/keep.txt": true, "docs/guide.md": true, "nested/test1.log": false} {
		if _, got := files[file]; got != want {
			t.Errorf("included %q = %v, want %v", file, got, want)
		}
	}
}
