package config

import (
	"path/filepath"
	"testing"
)

func TestExpandPathUsesKRUNHOMEForTilde(t *testing.T) {
	t.Setenv("KRUN_HOME", "/tmp/krun-home")

	got := ExpandPath("~/git/ftechmax")
	want := filepath.Join("/tmp/krun-home", "git", "ftechmax")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandPathExpandsEnvVars(t *testing.T) {
	t.Setenv("KRUN_HOME", "/tmp/krun-home")
	t.Setenv("KRUN_SRC", "/tmp/krun-home/src")

	got := ExpandPath("$KRUN_SRC/project")
	want := filepath.Join("/tmp/krun-home", "src", "project")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
