package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setTestHome(t *testing.T, home string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		t.Setenv("HOMEDRIVE", "")
		t.Setenv("HOMEPATH", "")
		return
	}

	t.Setenv("HOME", home)
}

func TestDefaultKrunDirUsesHomeKrunDirectory(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	got, err := defaultKrunDir()
	if err != nil {
		t.Fatalf("defaultKrunDir() error = %v", err)
	}

	want := filepath.Join(home, KrunDirName)
	if got != want {
		t.Fatalf("defaultKrunDir() = %q, want %q", got, want)
	}
}

func TestLoadKrunConfigReadsHomeKrunConfig(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	configDir := filepath.Join(home, KrunDirName)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	configPath := filepath.Join(configDir, ConfigFileName)
	configJSON := []byte(`{
  "source": {
    "path": "~/git/",
    "search_depth": 2
  },
  "local_registry": "registry:5000",
  "remote_registry": "docker.io/ftechmax"
}`)
	if err := os.WriteFile(configPath, configJSON, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := LoadKrunConfig()
	if err != nil {
		t.Fatalf("LoadKrunConfig() error = %v", err)
	}

	wantPath := filepath.ToSlash(filepath.Join(home, "git"))
	if got.Path != wantPath {
		t.Fatalf("LoadKrunConfig().Path = %q, want %q", got.Path, wantPath)
	}
	if got.SearchDepth != 2 {
		t.Fatalf("LoadKrunConfig().SearchDepth = %d, want 2", got.SearchDepth)
	}
	if got.LocalRegistry != "registry:5000" {
		t.Fatalf("LoadKrunConfig().LocalRegistry = %q, want registry:5000", got.LocalRegistry)
	}
	if got.RemoteRegistry != "docker.io/ftechmax" {
		t.Fatalf("LoadKrunConfig().RemoteRegistry = %q, want docker.io/ftechmax", got.RemoteRegistry)
	}
}

func TestLoadTokenBase64EncodesBinaryToken(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	configDir := filepath.Join(home, KrunDirName)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	tokenBytes := []byte{0x00, 0x01, '\n', 0x7f, 0x80, 0xff}
	tokenPath := filepath.Join(configDir, TokenFileName)
	if err := os.WriteFile(tokenPath, tokenBytes, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken() error = %v", err)
	}

	want := base64.StdEncoding.EncodeToString(tokenBytes)
	if got != want {
		t.Fatalf("LoadToken() = %q, want %q", got, want)
	}
}
