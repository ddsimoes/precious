//go:build !windows

package config

import (
	"fmt"
	"io/fs"
)

// checkStateDirMode refuses a state directory accessible to group or other
// users.
func checkStateDirMode(path string, fi fs.FileInfo) error {
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("state directory %s has mode %04o; it must not be accessible to group or other users (required %04o)", path, perm, StateDirMode)
	}
	return nil
}
