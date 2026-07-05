//go:build linux

package helperipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const DefaultEndpoint = "/run/krun/krun-helper.sock"

const staleProbeTimeout = 2 * time.Second

func dial(ctx context.Context, endpoint string) (net.Conn, error) {
	dialer := net.Dialer{}
	return dialer.DialContext(ctx, "unix", endpoint)
}

// Listen binds the helper's unix socket. An existing socket file is probed
// first: a live helper is an error, a stale leftover (e.g. after a crash)
// is removed. The socket is then restricted to the invoking user.
func Listen(endpoint string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o755); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
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

	if err := secureSocket(endpoint); err != nil {
		_ = listener.Close()
		_ = os.Remove(endpoint)
		return nil, err
	}
	return listener, nil
}

// Cleanup removes the socket file after the server has shut down.
func Cleanup(endpoint string) {
	_ = os.Remove(endpoint)
}

// secureSocket hands the socket to the pre-elevation user with mode 0600,
// so the non-root CLI can connect while other users cannot. Without the
// chown, a root-owned 0600 socket would lock the invoking user out.
func secureSocket(endpoint string) error {
	uid, gid, ok := invokingUser(os.Getenv)
	if ok {
		if err := os.Chown(endpoint, uid, gid); err != nil {
			return fmt.Errorf("chown socket to invoking user: %w", err)
		}
	}
	if err := os.Chmod(endpoint, 0o600); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}
	return nil
}

// invokingUser resolves the pre-elevation user from the env vars sudo and
// pkexec set. A gid of -1 leaves the group unchanged (pkexec exports no
// group variable).
func invokingUser(getenv func(string) string) (int, int, bool) {
	for _, uidVar := range []string{"SUDO_UID", "PKEXEC_UID"} {
		uidValue := strings.TrimSpace(getenv(uidVar))
		if uidValue == "" {
			continue
		}
		uid, err := strconv.Atoi(uidValue)
		if err != nil || uid < 0 {
			continue
		}

		gid := -1
		if uidVar == "SUDO_UID" {
			if gidValue := strings.TrimSpace(getenv("SUDO_GID")); gidValue != "" {
				if parsed, err := strconv.Atoi(gidValue); err == nil && parsed >= 0 {
					gid = parsed
				}
			}
		}
		return uid, gid, true
	}
	return 0, 0, false
}
