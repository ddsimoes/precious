package sources

import "os"

// defaultRoots are the Windows allowed roots used when none are configured
// (design D5): the user profile folder and every drive letter. allowedRoots
// keeps those that exist.
func defaultRoots() []string {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, home)
	}
	for d := 'A'; d <= 'Z'; d++ {
		roots = append(roots, string(d)+`:\`)
	}
	return roots
}
