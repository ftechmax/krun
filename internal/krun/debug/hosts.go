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

// applyHostsEntries writes the helper's desired hosts block through the
// privileged hosts writer, starting it if it is not already running.
//
// Starting it is the only time krun asks for elevation. The writer then stays
// up, so a developer enables and disables debug sessions all day without
// being prompted again. The unprivileged helper daemon cannot do this itself:
// it is detached and has no controlling terminal, so sudo would refuse to
// prompt it.
//
// The revision lets the writer drop this snapshot if a newer one from a
// concurrent command already reached it.
func applyHostsEntries(entries []contracts.HostsEntry, revision contracts.HostsRevision) error {
	if err := ensureHostsDaemon(); err != nil {
		return err
	}

	_, err := hostsCall[contracts.HelperResponse](
		http.MethodPost, "/v1/hosts", contracts.ApplyHostsRequest{Entries: entries, Revision: revision}, listTimeout)
	return err
}

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

// reportHostsUpdate applies the block and explains the consequence if it
// fails, rather than failing the whole command: the session is already up,
// only dependency name resolution is missing.
func reportHostsUpdate(entries []contracts.HostsEntry, revision contracts.HostsRevision) {
	if err := applyHostsEntries(entries, revision); err != nil {
		utils.Errf("could not update the hosts file: %v", err)
		utils.Warnf("the debug session is active, but dependency hostnames will not resolve locally")
	}
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
	return ipcCall[T](helperipc.NewClientForHosts(timeout), method, path, payload)
}
