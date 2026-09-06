package hostfile

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/ftechmax/krun/internal/contracts"
)

// maxEntries bounds what one request can put in the hosts file.
const maxEntries = 256

// Validate checks entries before a privileged process writes them.
//
// The privileged writer accepts requests over a socket, so this is a trust
// boundary even though the socket is restricted to one user. The important
// restriction is that addresses must be loopback: krun only ever points
// dependency names at 127.0.0.1, and refusing anything else means a request
// can at worst redirect a name to the local machine, never to a host an
// attacker controls.
func Validate(entries []contracts.HostsEntry) error {
	if len(entries) > maxEntries {
		return fmt.Errorf("too many hosts entries: %d (max %d)", len(entries), maxEntries)
	}

	for _, entry := range entries {
		address, err := netip.ParseAddr(strings.TrimSpace(entry.IP))
		if err != nil {
			return fmt.Errorf("invalid hosts entry address %q", entry.IP)
		}
		if !address.IsLoopback() {
			return fmt.Errorf("refusing non-loopback hosts entry %q -> %q", entry.Hostname, entry.IP)
		}
		if err := validateHostname(entry.Hostname); err != nil {
			return err
		}
	}
	return nil
}

func validateHostname(hostname string) error {
	trimmed := strings.TrimSpace(hostname)
	if trimmed == "" {
		return fmt.Errorf("hosts entry has an empty hostname")
	}
	if len(trimmed) > 253 {
		return fmt.Errorf("hosts entry hostname %q is too long", trimmed)
	}
	for _, r := range trimmed {
		isAllowed := r == '-' || r == '.' || r == '_' ||
			(r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z')
		if !isAllowed {
			return fmt.Errorf("hosts entry hostname %q contains an invalid character %q", trimmed, r)
		}
	}
	return nil
}
