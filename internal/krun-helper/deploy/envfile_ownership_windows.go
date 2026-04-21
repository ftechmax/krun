//go:build windows

package deploy

import "github.com/ftechmax/krun/internal/contracts"

func assignFileOwnership(_ string, _ contracts.DebugSessionUser) error {
	return nil
}
