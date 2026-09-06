package debug

import (
	"encoding/json"
	"fmt"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/utils"
)

// applyHostsEntries writes the helper's desired hosts block through a
// short-lived elevated krun-helper process.
//
// This runs from the CLI rather than the helper daemon on purpose: the daemon
// is detached and has no controlling terminal, so it cannot answer a sudo
// password prompt. The CLI has the user's terminal, so it is the only place an
// interactive elevation can succeed.
func applyHostsEntries(entries []contracts.HostsEntry) error {
	helperBinaryPath, err := resolveHelperBinaryPath()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode hosts entries: %w", err)
	}

	if err := runElevatedWithInput(helperBinaryPath, []string{"apply-hosts"}, payload); err != nil {
		return fmt.Errorf("update hosts file: %w", err)
	}
	return nil
}

// reportHostsUpdate applies the block and explains the consequence if it
// fails, rather than failing the whole command: the session itself is already
// up, only dependency name resolution is missing.
func reportHostsUpdate(entries []contracts.HostsEntry) {
	if err := applyHostsEntries(entries); err != nil {
		utils.Errf("could not update the hosts file: %v", err)
		utils.Warnf("the debug session is active, but dependency hostnames will not resolve locally")
	}
}
