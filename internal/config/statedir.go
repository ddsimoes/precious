package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// StateDirMode is the required permission of the state directory.
const StateDirMode fs.FileMode = 0o700

// EnsureStateDir creates a missing state directory with mode 0700 (its parent
// must exist) and refuses an existing one that is not a directory or, outside
// Windows, is accessible to group or other users (checkStateDirMode).
func EnsureStateDir(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(path, StateDirMode); err != nil {
			return fmt.Errorf("state directory %s: %w", path, err)
		}
		// Mkdir is subject to umask; set the exact mode.
		if err := os.Chmod(path, StateDirMode); err != nil {
			return fmt.Errorf("state directory %s: %w", path, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("state directory %s: %w", path, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("state directory %s: not a directory", path)
	}
	return checkStateDirMode(path, fi)
}
