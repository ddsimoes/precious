//go:build !linux && !darwin && !windows

package sources

import "os"

// defaultRoots is the service account's home on systems without their own
// defaults.
func defaultRoots() []string {
	if home, err := os.UserHomeDir(); err == nil {
		return []string{home}
	}
	return nil
}
