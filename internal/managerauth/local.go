// Package managerauth owns the local bearer credential used for traffic-manager
// session API calls. The bearer token stays in the invoking user's .krun
// directory; only its SHA-256 verifier is installed into the cluster.
package managerauth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ftechmax/krun/internal/securetoken"
)

const (
	DirName       = ".krun"
	TokenFileName = "manager-token"
	TokenHashEnv  = "KRUN_MANAGER_AUTH_TOKEN_SHA256"
)

func DefaultTokenPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home for manager token: %w", err)
	}
	return filepath.Join(home, DirName, TokenFileName), nil
}

// EnsureLocalToken returns the existing token or atomically creates a new one.
// Reusing it on runtime upgrades keeps an already-running helper in sync.
func EnsureLocalToken(path string) (string, error) {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	if cleanPath == "." || cleanPath == "" {
		return "", errors.New("manager token path is required")
	}

	if _, err := os.Stat(cleanPath); err == nil {
		return ReadLocalToken(cleanPath)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect manager token %s: %w", cleanPath, err)
	}

	dir := filepath.Dir(cleanPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create manager token directory %s: %w", dir, err)
	}
	if err := secureDirectory(dir); err != nil {
		return "", err
	}

	token, err := securetoken.Hex(32)
	if err != nil {
		return "", fmt.Errorf("generate manager token: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".manager-token-*")
	if err != nil {
		return "", fmt.Errorf("create temporary manager token: %w", err)
	}
	tempName := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return "", fmt.Errorf("secure temporary manager token: %w", err)
	}
	if _, err := temp.WriteString(token + "\n"); err != nil {
		return "", fmt.Errorf("write manager token: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return "", fmt.Errorf("sync manager token: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close manager token: %w", err)
	}
	// Linking a complete temporary file into place is atomic and, unlike
	// Rename on Unix, never overwrites a token won by a concurrent installer.
	if err := os.Link(tempName, cleanPath); err != nil {
		if os.IsExist(err) {
			return ReadLocalToken(cleanPath)
		}
		return "", fmt.Errorf("install manager token %s: %w", cleanPath, err)
	}
	if err := os.Remove(tempName); err != nil {
		return "", fmt.Errorf("remove temporary manager token %s: %w", tempName, err)
	}
	committed = true
	return token, nil
}

func ReadLocalToken(path string) (string, error) {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	if cleanPath == "." || cleanPath == "" {
		return "", errors.New("manager token path is required")
	}
	if err := validateTokenPermissions(cleanPath); err != nil {
		return "", err
	}
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", fmt.Errorf("read manager token %s: %w", cleanPath, err)
	}
	token := strings.TrimSpace(string(data))
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("manager token %s is malformed; reinstall the debug runtime", cleanPath)
	}
	return token, nil
}

func Digest(token string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(digest[:])
}

func ParseDigest(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("manager auth token verifier is missing or malformed")
	}
	return decoded, nil
}
