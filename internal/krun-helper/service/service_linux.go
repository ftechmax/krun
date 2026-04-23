//go:build linux

package service

import (
	"fmt"
	"net"
	"os"
	"strings"
)

var dialNotifySocket = func(addr *net.UnixAddr) (net.Conn, error) {
	return net.DialUnix("unixgram", nil, addr)
}

// ShouldRunAsService reports whether the helper should enter service mode.
// On Linux, this is driven by the caller-supplied --service flag (set by
// systemd's ExecStart).
func ShouldRunAsService(serviceFlag bool) bool {
	return serviceFlag
}

// RunAsService starts the helper daemon in systemd service mode.
// It calls startDaemon (provided by the caller) and sends sd_notify READY=1
// once the listener is bound.
func RunAsService(listenAddress, kubeConfigPath string, startDaemon StartDaemonFunc) error {
	return startDaemon(listenAddress, kubeConfigPath, DaemonOptions{
		OnReady: sdNotifyReady,
	})
}

func sdNotifyReady() {
	if err := sdNotify("READY=1"); err != nil {
		fmt.Fprintf(os.Stderr, "sd_notify READY failed: %v\n", err)
	}
}

func sdNotify(payload string) error {
	addr := strings.TrimSpace(os.Getenv("NOTIFY_SOCKET"))
	if addr == "" {
		return nil
	}

	// systemd may provide abstract namespace sockets in "@name" form.
	// The kernel expects a leading NUL byte for those addresses.
	normalizedAddr := normalizeNotifySocketAddress(addr)
	conn, err := dialNotifySocket(&net.UnixAddr{
		Net:  "unixgram",
		Name: normalizedAddr,
	})
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = conn.Write([]byte(payload))
	return err
}

func normalizeNotifySocketAddress(addr string) string {
	if strings.HasPrefix(addr, "@") {
		return "\x00" + strings.TrimPrefix(addr, "@")
	}
	return addr
}
