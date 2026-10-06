package sources

import "os"

// defaultRoots are the Linux allowed roots used when none are configured
// (design D5): the service account's home, /media, /mnt, /run/media, and
// /srv. allowedRoots keeps those that exist.
func defaultRoots() []string {
	roots := []string{"/media", "/mnt", "/run/media", "/srv"}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append([]string{home}, roots...)
	}
	return roots
}
