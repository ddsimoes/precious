package fsaccess

import (
	"os"
	"path/filepath"
	"testing"
)

// NewOSAt is NewOS reading the mount table below procRoot, udev's links below
// devRoot, and block devices below sysRoot.
func NewOSAt(procRoot, devRoot, sysRoot string) FS { return newOS(procRoot, devRoot, sysRoot) }

// NewOSWithMountinfo is NewOS with a substitute mount table and no udev
// links, so tests can exercise mount-table boundaries on a real directory
// without privileges.
func NewOSWithMountinfo(t testing.TB, table string) FS {
	return newOS(fakeProc(t, table), t.TempDir(), t.TempDir())
}

// fakeProc returns a directory holding table as self/mountinfo.
func fakeProc(t testing.TB, table string) string {
	t.Helper()
	proc := t.TempDir()
	writeFixture(t, filepath.Join(proc, "self", "mountinfo"), table)
	return proc
}

// writeFixture writes data to path, creating its directories.
func writeFixture(t testing.TB, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
