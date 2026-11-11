// Package managerauth owns the local bearer credential used for traffic-manager
// session API calls. The bearer token stays in the invoking user's .krun
// directory; only its SHA-256 verifier is installed into the cluster.
package managerauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	dirName       = ".krun"
	tokenFileName = "manager-token"
	TokenHashEnv  = "KRUN_MANAGER_AUTH_TOKEN_SHA256"
)

func DefaultTokenPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home for manager token: %w", err)
	}
	return filepath.Join(home, dirName, tokenFileName), nil
}

// EnsureLocalToken returns the existing token or creates a new one.
// Reusing it on runtime upgrades keeps an already-running helper in sync.
// Runtime installations must run one at a time.
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

	var random [32]byte
	rand.Read(random[:])
	token := hex.EncodeToString(random[:])
	file, err := os.OpenFile(cleanPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create manager token %s: %w", cleanPath, err)
	}
	written := false
	defer func() {
		if !written {
			_ = file.Close()
			_ = os.Remove(cleanPath)
		}
	}()
	if _, err := file.WriteString(token + "\n"); err != nil {
		return "", fmt.Errorf("write manager token: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync manager token: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close manager token: %w", err)
	}
	written = true
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
