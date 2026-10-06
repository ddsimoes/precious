package store

import "testing"

func TestNetworkFilesystemMagicRejected(t *testing.T) {
	for _, tc := range []struct {
		name  string
		magic int64
		want  bool
	}{
		{"nfs", 0x6969, true},
		{"smb", 0x517B, true},
		{"cifs", 0xFF534D42, true},
		{"smb2", 0xFE534D42, true},
		{"ext4", 0xEF53, false},
		{"tmpfs", 0x01021994, false},
		{"btrfs", 0x9123683E, false},
	} {
		if got := isNetworkFilesystem(tc.magic); got != tc.want {
			t.Errorf("%s (%#x): got %v, want %v", tc.name, tc.magic, got, tc.want)
		}
	}
}
