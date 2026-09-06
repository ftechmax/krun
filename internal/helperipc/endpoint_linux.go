//go:build linux

package helperipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

const staleProbeTimeout = 2 * time.Second

// DefaultEndpoint is the unix socket the helper listens on. The helper runs as
// the invoking user, so the socket lives in that user's runtime directory
// rather than a root-owned path under /run: no elevation is needed to create
// it, and it is naturally per-user instead of one-per-machine.
func DefaultEndpoint() string {
	return filepath.Join(runtimeDir(), "krun-helper.sock")
}

// runtimeDir prefers XDG_RUNTIME_DIR, which systemd creates user-owned at 0700
// and removes at logout. Where it is unset (bare containers, cron, ssh without
// pam_systemd) fall back to a uid-scoped directory under the temp dir.
func runtimeDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "krun")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("krun-%d", os.Getuid()))
}

func dial(ctx context.Context, endpoint string) (net.Conn, error) {
	dialer := net.Dialer{}
	return dialer.DialContext(ctx, "unix", endpoint)
}

// Listen binds the helper's unix socket. An existing socket file is probed
// first: a live helper is an error, a stale leftover (e.g. after a crash) is
// removed.
func Listen(endpoint string) (net.Listener, error) {
	dir := filepath.Dir(endpoint)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	// Only the default path is hardened: it is predictable and its temp-dir
	// fallback sits under a world-writable parent, so another user could race
	// us to create it. An explicit --socket is the caller's business.
	if endpoint == DefaultEndpoint() {
		if err := checkPrivateDir(dir); err != nil {
			return nil, err
		}
	}

	if _, err := os.Stat(endpoint); err == nil {
		probeCtx, cancel := context.WithTimeout(context.Background(), staleProbeTimeout)
		conn, dialErr := dial(probeCtx, endpoint)
		cancel()
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("helper already listening on %s", endpoint)
		}
		if err := os.Remove(endpoint); err != nil {
			return nil, fmt.Errorf("remove stale socket %s: %w", endpoint, err)
		}
	}

	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return nil, err
	}

	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(endpoint)
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return listener, nil
}

// checkPrivateDir refuses a socket directory that someone else could have
// created or could write through.
func checkPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect socket directory %s: %w", dir, err)
	}
	if !info.Mode().IsDir() {
		return fmt.Errorf("socket directory %s is not a directory", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("socket directory %s permissions are %04o; want 0700", dir, info.Mode().Perm())
	}
	if owner, ok := dirOwnerUID(info); ok && owner != os.Getuid() {
		return fmt.Errorf("socket directory %s is owned by uid %d, not %d", dir, owner, os.Getuid())
	}
	return nil
}

// Cleanup removes the socket file after the server has shut down.
func Cleanup(endpoint string) {
	_ = os.Remove(endpoint)
}
