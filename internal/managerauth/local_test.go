package managerauth

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestEnsureLocalTokenCreatesAndReusesPrivateToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), DirName, TokenFileName)
	first, err := EnsureLocalToken(path)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	second, err := EnsureLocalToken(path)
	if err != nil {
		t.Fatalf("reuse token: %v", err)
	}
	if first != second {
		t.Fatal("runtime upgrade rotated the local manager token")
	}
	if len(first) != 64 {
		t.Fatalf("token length = %d, want 64 hex characters", len(first))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat token: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("token permissions = %04o, want 0600", got)
		}
	}
}

func TestEnsureLocalTokenConcurrentInstallersReuseWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), DirName, TokenFileName)
	const installers = 12
	tokens := make(chan string, installers)
	errors := make(chan error, installers)
	var wait sync.WaitGroup
	for range installers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			token, err := EnsureLocalToken(path)
			if err != nil {
				errors <- err
				return
			}
			tokens <- token
		}()
	}
	wait.Wait()
	close(tokens)
	close(errors)
	for err := range errors {
		t.Fatalf("concurrent installer: %v", err)
	}
	winner, err := ReadLocalToken(path)
	if err != nil {
		t.Fatalf("read installed token: %v", err)
	}
	for token := range tokens {
		if token != winner {
			t.Fatalf("installer returned losing token %q, winner is %q", token, winner)
		}
	}
}

func TestReadLocalTokenRejectsMalformedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), TokenFileName)
	if err := os.WriteFile(path, []byte("not-a-token"), 0o600); err != nil {
		t.Fatalf("write malformed token: %v", err)
	}
	if _, err := ReadLocalToken(path); err == nil {
		t.Fatal("expected malformed token error")
	}
}

func TestReadLocalTokenRejectsLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not authoritative on Windows")
	}
	path := filepath.Join(t.TempDir(), TokenFileName)
	if err := os.WriteFile(path, []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"), 0o644); err != nil {
		t.Fatalf("write token: %v", err)
	}
	if _, err := ReadLocalToken(path); err == nil {
		t.Fatal("expected insecure permission error")
	}
}

func TestReadLocalTokenRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks normally requires elevated Windows privileges")
	}
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	path := filepath.Join(directory, TokenFileName)
	if err := os.WriteFile(target, []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"), 0o600); err != nil {
		t.Fatalf("write token target: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := ReadLocalToken(path); err == nil {
		t.Fatal("expected symlink token error")
	}
}

func TestDigestRoundTrip(t *testing.T) {
	digest := Digest("token")
	decoded, err := ParseDigest(digest)
	if err != nil {
		t.Fatalf("parse digest: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("digest length = %d, want 32", len(decoded))
	}
}
