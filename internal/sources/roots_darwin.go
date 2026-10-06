package sources

import "os"

// defaultRoots are the macOS allowed roots used when none are configured
// (design D5): the service account's home and /Volumes.
func defaultRoots() []string {
	roots := []string{"/Volumes"}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append([]string{home}, roots...)
	}
	return roots
}
