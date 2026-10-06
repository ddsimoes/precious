//go:build !linux && !darwin

package fsaccess

// sysInfo leaves identity, link count, allocation, and change time zero: on
// this platform FileInfo.Sys() exposes no inode identity (on Windows it is a
// Win32FileAttributeData), so EntryInfo carries only what fs.FileInfo
// reports, and sameObject compares kinds alone.
func sysInfo(*EntryInfo, any) {}
