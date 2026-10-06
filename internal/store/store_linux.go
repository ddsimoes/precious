package store

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Filesystem magic numbers from statfs(2) that must not hold the database.
const (
	magicNFS  = 0x6969
	magicSMB  = 0x517B
	magicCIFS = 0xFF534D42
	magicSMB2 = 0xFE534D42
)

func isNetworkFilesystem(fsType int64) bool {
	switch uint32(fsType) {
	case magicNFS, magicSMB, magicCIFS, magicSMB2:
		return true
	}
	return false
}

// checkLocalFilesystem refuses a state directory on NFS, SMB, or CIFS, where
// SQLite's locking is unreliable.
func checkLocalFilesystem(dir string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return fmt.Errorf("store: statfs %s: %w", dir, err)
	}
	if isNetworkFilesystem(int64(st.Type)) {
		return fmt.Errorf("%w (%s)", ErrNetworkFilesystem, dir)
	}
	return nil
}
