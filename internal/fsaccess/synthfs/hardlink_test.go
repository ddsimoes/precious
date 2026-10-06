package synthfs_test

import (
	"testing"

	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
)

// Hard links share the target's identity and data, and every name counts in
// the link count; Nlink overrides the count.
func TestHardLinks(t *testing.T) {
	f := synthfs.New()
	root := f.Root("/src")
	big := root.File("big.iso", 1<<30, synthfs.DefaultModTime)
	root.Dir("copy").HardLink("same.iso", big)
	root.HardLink("again.iso", root.Child("copy").Child("same.iso"))
	root.File("elsewhere", 10, synthfs.DefaultModTime).Nlink(3)

	d := open(t, f, "/src")
	want, err := d.Lstat([]byte("big.iso"))
	must(t, err)
	if want.Nlink != 3 {
		t.Fatalf("big.iso Nlink = %d, want 3 (itself and two links)", want.Nlink)
	}
	again, err := d.Lstat([]byte("again.iso"))
	must(t, err)
	copyInfo, err := d.Lstat([]byte("copy"))
	must(t, err)
	sub, err := d.OpenDir([]byte("copy"), copyInfo)
	must(t, err)
	defer sub.Close()
	same, err := sub.Lstat([]byte("same.iso"))
	must(t, err)
	for name, info := range map[string]fsaccess.EntryInfo{"again.iso": again, "same.iso": same} {
		if string(info.Name) != name {
			t.Errorf("link %s reports name %q", name, info.Name)
		}
		if info.Ino != want.Ino || info.Dev != want.Dev || info.Nlink != 3 || info.Size != want.Size || info.Blocks != want.Blocks {
			t.Errorf("%s = %+v, want the identity, size, and blocks of big.iso with 3 links", name, info)
		}
	}
	other, err := d.Lstat([]byte("elsewhere"))
	must(t, err)
	if other.Nlink != 3 {
		t.Errorf("elsewhere Nlink = %d, want the set 3", other.Nlink)
	}
}
