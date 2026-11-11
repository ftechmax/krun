//go:build linux

package helperipc

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HostsEndpoint is the socket of the small privileged hosts writer. It lives
// under /run rather than the user runtime dir because a root process creates
// it, and it is handed back to the invoking user so the unprivileged CLI can
// reach it.
const HostsEndpoint = "/run/krun/krun-hosts.sock"

// ListenElevated binds a socket from a root process and hands it to the user
// who asked for the elevation, mode 0600. Without the chown a root-owned 0600
// socket would lock that user out of the daemon they just started.
//
// Only the hosts writer uses this. The main helper daemon runs unprivileged
// and uses Listen, which needs none of it.
func ListenElevated(endpoint string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o755); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}

	if _, err := os.Stat(endpoint); err == nil {
		if conn, dialErr := net.Dial("unix", endpoint); dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("hosts writer already listening on %s", endpoint)
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

func secureSocket(endpoint string) error {
	if uid, gid, ok := invokingUser(os.Getenv); ok {
		if err := os.Chown(endpoint, uid, gid); err != nil {
			return fmt.Errorf("chown socket to invoking user: %w", err)
		}
	}
	if err := os.Chmod(endpoint, 0o600); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}
	return nil
}

// invokingUser resolves the pre-elevation user from sudo's environment.
// A gid of -1 leaves the group unchanged when SUDO_GID is unavailable.
func invokingUser(getenv func(string) string) (int, int, bool) {
	uid, err := strconv.Atoi(strings.TrimSpace(getenv("SUDO_UID")))
	if err != nil || uid < 0 {
		return 0, 0, false
	}
	gid, err := strconv.Atoi(strings.TrimSpace(getenv("SUDO_GID")))
	if err != nil || gid < 0 {
		gid = -1
	}
	return uid, gid, true
}
