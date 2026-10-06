//go:build linux

package fsaccess_test

import (
	"os"
	"path/filepath"
	"testing"

	"precious/internal/fsaccess"
)

// Task 1.4: Lstat reports st_blocks, so a sparse file's allocation is visibly
// smaller than its logical size (§9.2 logical vs allocated bytes).
func TestLstatReportsAllocatedBlocks(t *testing.T) {
	src := t.TempDir()
	sparse := filepath.Join(src, "sparse.img")
	f, err := os.Create(sparse)
	if err != nil {
		t.Fatal(err)
	}
	const logical = 64 << 20
	if err := f.Truncate(logical); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.WriteFile(filepath.Join(src, "dense.bin"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	d := openRoot(t, fsaccess.NewOS(), src)
	si, err := d.Lstat([]byte("sparse.img"))
	if err != nil {
		t.Fatal(err)
	}
	if si.Size != logical || si.Blocks*512 >= logical {
		t.Fatalf("sparse file: size %d, allocated %d; want allocated below logical", si.Size, si.Blocks*512)
	}
	di, err := d.Lstat([]byte("dense.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if di.Blocks*512 < di.Size {
		t.Fatalf("dense file: size %d, allocated %d; want allocated >= logical", di.Size, di.Blocks*512)
	}
}
