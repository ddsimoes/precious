//go:build !linux

package store

// checkLocalFilesystem performs no check on this platform yet: recognising a
// network filesystem needs the platform's own statfs or volume API, which
// arrives with the native darwin and windows support of R8 (design D3). Until
// then the operator is responsible for keeping the state directory local.
func checkLocalFilesystem(string) error { return nil }
