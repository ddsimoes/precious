package config

import "io/fs"

// checkStateDirMode accepts every directory: Windows permissions are ACLs,
// which this release neither checks nor sets (server-config "Private state
// directory").
func checkStateDirMode(string, fs.FileInfo) error { return nil }
