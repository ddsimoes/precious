package synthfs_test

import (
	"testing"

	"precious/internal/fsaccess/synthfs"
)

// Task 1.4: synthfs reports allocation as size rounded up to 4 KiB for files
// and directories, nothing for links, and a test-set value when given.
func TestBlocks(t *testing.T) {
	f := synthfs.New()
	root := f.Root("/src")
	root.File("small", 1, synthfs.DefaultModTime)
	root.File("exact", 8192, synthfs.DefaultModTime)
	root.File("sparse", 1<<30, synthfs.DefaultModTime).Blocks(16)
	root.Dir("dir")
	root.Symlink("link", "elsewhere")
	root.Dir("gen").Generate(3, 3)

	d := open(t, f, "/src")
	for name, want := range map[string]int64{"small": 8, "exact": 16, "sparse": 16, "dir": 8, "link": 0} {
		info, err := d.Lstat([]byte(name))
		must(t, err)
		if info.Blocks != want {
			t.Errorf("%s: Blocks = %d, want %d", name, info.Blocks, want)
		}
	}
	g, err := d.Lstat([]byte("gen"))
	must(t, err)
	gd, err := d.OpenDir([]byte("gen"), g)
	must(t, err)
	defer gd.Close()
	entries, err := gd.ReadBatch(10)
	must(t, err)
	for _, e := range entries {
		info, err := gd.Lstat(e.Name)
		must(t, err)
		if want := (info.Size + 4095) / 4096 * 8; info.Blocks != want {
			t.Errorf("generated %s: Blocks = %d, want %d", e.Name, info.Blocks, want)
		}
	}
}
