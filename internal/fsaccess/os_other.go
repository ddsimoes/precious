//go:build !linux

package fsaccess

// NewOS returns the default backend for this platform, the portable one
// (NewPortable). Native darwin and windows backends, with real volume
// identity and capabilities, are not part of R1 (design D3).
func NewOS() FS { return NewPortable() }
