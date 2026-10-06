//go:build !windows

package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"precious/internal/store"
)

// server-config "Fresh state directory": the directory is created 0700 under
// any umask, and the database and its WAL files are 0600. The umask is
// process-wide, so no test in this package runs in parallel.
func TestFreshStateDirectory(t *testing.T) {
	for _, umask := range []int{0o000, 0o022, 0o277} {
		t.Run(fmt.Sprintf("umask_%04o", umask), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			old := syscall.Umask(umask)
			err := EnsureStateDir(dir)
			syscall.Umask(old)
			if err != nil {
				t.Fatal(err)
			}
			assertMode(t, dir, os.ModeDir|0o700)

			st, err := store.Open(context.Background(), dir, store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			// The database is in WAL mode and still open, so its -wal and -shm
			// files exist; each must be owner-only too.
			for _, suffix := range []string{"", "-wal", "-shm"} {
				assertMode(t, st.Path()+suffix, 0o600)
			}

			// A later start accepts the directory it created.
			if err := EnsureStateDir(dir); err != nil {
				t.Fatalf("EnsureStateDir on its own directory: %v", err)
			}
		})
	}
}

// server-config "Over-permissive state directory", plus the other refusals.
func TestExistingStateDirectory(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, base string) (path string)
		wantErr []string // empty: accepted
	}{
		{"owner-only accepted", func(t *testing.T, base string) string {
			return mkdirMode(t, base+"/state", 0o700)
		}, nil},
		{"0755 refused", func(t *testing.T, base string) string {
			return mkdirMode(t, base+"/state", 0o755)
		}, []string{"has mode 0755", "required 0700"}},
		{"0750 refused", func(t *testing.T, base string) string {
			return mkdirMode(t, base+"/state", 0o750)
		}, []string{"has mode 0750", "required 0700"}},
		{"0701 refused", func(t *testing.T, base string) string {
			return mkdirMode(t, base+"/state", 0o701)
		}, []string{"has mode 0701", "required 0700"}},
		{"regular file refused", func(t *testing.T, base string) string {
			if err := os.WriteFile(base+"/state", nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return base + "/state"
		}, []string{"not a directory"}},
		{"symlink to an owner-only directory refused", func(t *testing.T, base string) string {
			if err := os.Symlink(mkdirMode(t, base+"/real", 0o700), base+"/state"); err != nil {
				t.Fatal(err)
			}
			return base + "/state"
		}, []string{"not a directory"}},
		{"missing parent refused", func(t *testing.T, base string) string {
			return base + "/missing/state"
		}, []string{"no such file or directory"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t, t.TempDir())
			err := EnsureStateDir(path)
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("EnsureStateDir accepted it")
			}
			for _, s := range append(tc.wantErr, path) {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not contain %q", err, s)
				}
			}
			if _, statErr := os.Lstat(filepath.Join(path, store.FileName)); statErr == nil {
				t.Error("a database file exists after the refusal")
			}
		})
	}
}

func mkdirMode(t *testing.T, path string, mode os.FileMode) string {
	t.Helper()
	if err := os.Mkdir(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode() & (os.ModeType | os.ModePerm); got != want {
		t.Errorf("%s mode = %v, want %v", path, got, want)
	}
}
