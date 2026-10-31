package debug

import (
	"fmt"
	"net/http"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/helperipc"
	"github.com/ftechmax/krun/internal/krun-helper/hostfile"
	"github.com/ftechmax/krun/internal/utils"
)

// ensureHostsDaemon starts the privileged writer if it is not already up. The
// elevation prompt happens here and only here.
func ensureHostsDaemon() error {
	if hostsDaemonRunning() {
		return nil
	}

	helperBinaryPath, err := resolveHelperBinaryPath()
	if err != nil {
		return err
	}

	fmt.Println("Starting the krun hosts writer; this needs elevation once.")
	if err := startElevatedProcess(helperBinaryPath, []string{"hosts-daemon"}); err != nil {
		return fmt.Errorf("start hosts writer: %w", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if hostsDaemonRunning() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("hosts writer failed to become healthy on %s", helperipc.HostsEndpoint)
}

func hostsDaemonRunning() bool {
	response, err := hostsCall[contracts.HelperResponse](http.MethodGet, "/healthz", nil, healthTimeout)
	return err == nil && response.Success
}

// stopHostsDaemon asks the writer to exit. It strips krun's hosts block on the
// way out, so this is also how the block is cleaned up.
func stopHostsDaemon() error {
	if !hostsDaemonRunning() {
		return nil
	}
	_, err := hostsCall[contracts.HelperResponse](http.MethodPost, "/v1/shutdown", nil, listTimeout)
	return err
}

func hostsStatusLine() string {
	if hostsDaemonRunning() {
		return utils.Colorize("hosts writer is running", utils.Green)
	}
	if present, err := hostfile.HasBlock(); err == nil && present {
		return utils.Colorize("hosts writer is not running, but a krun block remains in the hosts file", utils.Yellow)
	}
	return "hosts writer is not running"
}

func hostsCall[T any](method string, path string, payload any, timeout time.Duration) (T, error) {
	return helperipc.Call[T](helperipc.NewClientForHosts(timeout), method, path, payload)
}
