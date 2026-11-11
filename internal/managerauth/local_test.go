package managerauth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureLocalTokenCreatesAndReusesPrivateToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), dirName, tokenFileName)
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
	stored, err := ReadLocalToken(path)
	if err != nil {
		t.Fatalf("read private token: %v", err)
	}
	if stored != first {
		t.Fatal("stored token differs from created token")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat token: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("token permissions = %04o, want 0600", got)
		}
		info, err = os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("stat token directory: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("token directory permissions = %04o, want 0700", got)
		}
	}
}

func TestReadLocalTokenRejectsMalformedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), tokenFileName)
	if err := os.WriteFile(path, []byte("not-a-token"), 0o600); err != nil {
		t.Fatalf("write malformed token: %v", err)
	}
	if _, err := ReadLocalToken(path); err == nil {
		t.Fatal("expected malformed token error")
	}
	if _, err := EnsureLocalToken(path); err == nil {
		t.Fatal("expected install to reject an existing malformed token")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read malformed token after install: %v", err)
	}
	if string(data) != "not-a-token" {
		t.Fatal("install overwrote an existing malformed token")
	}
}

func TestReadLocalTokenRejectsLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not authoritative on Windows")
	}
	path := filepath.Join(t.TempDir(), tokenFileName)
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
	path := filepath.Join(directory, tokenFileName)
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
