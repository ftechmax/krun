//go:build linux

package deploy

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ftechmax/krun/internal/contracts"
)

func assignFileOwnership(path string, requestUser contracts.DebugSessionUser) error {
	trimmedUID := strings.TrimSpace(requestUser.UID)
	trimmedGID := strings.TrimSpace(requestUser.GID)
	if trimmedUID == "" || trimmedGID == "" {
		return nil
	}

	uid, uidErr := strconv.Atoi(trimmedUID)
	if uidErr != nil || uid < 0 {
		return nil
	}
	gid, gidErr := strconv.Atoi(trimmedGID)
	if gidErr != nil || gid < 0 {
		return nil
	}

	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("set .env owner to %d:%d: %w", uid, gid, err)
	}
	return nil
}
