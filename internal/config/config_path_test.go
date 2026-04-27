package config

import (
	"path/filepath"
	"testing"
)

func TestExpandPathUsesKRUNHOMEForTilde(t *testing.T) {
	t.Setenv("KRUN_HOME", "/tmp/krun-home")

	got := ExpandPath("~/git/testuser")
	want := filepath.Join("/tmp/krun-home", "git", "testuser")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandPathWithHomePrefersExplicitHome(t *testing.T) {
	t.Setenv("KRUN_HOME", "/tmp/krun-home")

	got := ExpandPathWithHome("~/git/testuser", "/tmp/service-owner")
	want := filepath.Join("/tmp/service-owner", "git", "testuser")
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
