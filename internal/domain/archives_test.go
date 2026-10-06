package domain

import (
	"slices"
	"testing"
)

func TestArchiveFormats(t *testing.T) {
	t.Parallel()
	want := []string{"zip", "tar", "tar_gzip", "tar_bzip2", "gzip", "bzip2"}
	var got []string
	for _, f := range ArchiveFormats {
		if !f.Valid() {
			t.Errorf("%q is not valid", f)
		}
		if f.Streamed() == (f == ArchiveZip) {
			t.Errorf("%q: Streamed() = %v", f, f.Streamed())
		}
		got = append(got, string(f))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("formats %q, want %q (the archives.format CHECK list)", got, want)
	}
	for _, bad := range []ArchiveFormat{"", "7z", "rar", "ZIP", "tgz"} {
		if bad.Valid() || bad.Streamed() {
			t.Errorf("%q is valid or streamed", bad)
		}
	}
}

func TestArchiveStates(t *testing.T) {
	t.Parallel()
	// The archives.state CHECK list.
	want := []string{"listing", "complete", "partial", "rejected", "encrypted", "corrupt", "unsupported",
		"changed", "unreadable"}
	var got []string
	for _, s := range ArchiveStates {
		if !s.Valid() {
			t.Errorf("%q is not valid", s)
		}
		got = append(got, string(s))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("states %q, want %q", got, want)
	}
	for _, bad := range []ArchiveState{"", "opening", "pending", "cached", "unstable", "not_opened", "Complete"} {
		if bad.Valid() {
			t.Errorf("%q is accepted", bad)
		}
	}
}

func TestMemberKinds(t *testing.T) {
	t.Parallel()
	var got []string
	for _, k := range MemberKinds {
		if !k.Valid() {
			t.Errorf("%q is not valid", k)
		}
		got = append(got, string(k))
	}
	// The archive_members.kind CHECK list.
	if want := []string{"directory", "file", "symlink", "special"}; !slices.Equal(got, want) {
		t.Fatalf("member kinds %q, want %q", got, want)
	}
	for _, bad := range []MemberKind{"", "dir", "link", "hardlink", "File"} {
		if bad.Valid() {
			t.Errorf("%q is accepted", bad)
		}
	}
}
