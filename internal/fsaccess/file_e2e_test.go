//go:build e2e && linux

package fsaccess_test

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
)

// Task 7.3 (M4): A11 bind-mounted file. A regular file bind-mounted from
// another filesystem is listed as a mount boundary and is never read
// (filesystem-boundary "File reads are identity-checked"; design D5).
func TestA11BindMountedFile(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		outside := t.TempDir()
		mount(t, "tmpfs", outside, "tmpfs", 0, "size=1m")
		secret := filepath.Join(outside, "secret")
		if err := os.WriteFile(secret, []byte("bytes from another filesystem"), 0o644); err != nil {
			t.Fatal(err)
		}
		src := t.TempDir()
		plain := filepath.Join(src, "plain")
		writeFile(t, plain)
		before := mustLstat(t, openRoot(t, fsaccess.NewOS(), src), "plain")
		mount(t, secret, plain, "", unix.MS_BIND, "")

		rec := instrument.Wrap(fsaccess.NewOS())
		d := openRoot(t, rec, src)
		info := mustLstat(t, d, "plain")
		if info.Kind != domain.EntryFile || !info.MountBoundary {
			t.Fatalf("Lstat(plain) = %+v, want a mount-boundary regular file", info)
		}
		// Listed as a boundary, it is refused before any system call.
		if f, err := d.OpenFile([]byte("plain"), info); err == nil {
			f.Close()
			t.Fatal("OpenFile opened a bind-mounted file")
		}
		// An identity recorded before the bind mount does not match the
		// mounted file: the open fails as changed and reads nothing.
		f, err := d.OpenFile([]byte("plain"), before)
		if err == nil {
			f.Close()
			t.Fatal("OpenFile opened the file mounted over the listed one")
		}
		if o, ok := fsaccess.OutcomeOf(err); !ok || o != domain.OutcomeChangedDuringObservation {
			t.Fatalf("OpenFile with the earlier identity = %v, want changed_during_observation", err)
		}
		if rec.BytesRead() != 0 {
			t.Fatalf("read %d bytes of a bind-mounted file", rec.BytesRead())
		}
		for _, c := range rec.Calls() {
			if c.Op == instrument.OpReadAt {
				t.Errorf("ReadAt %q on a bind-mounted file", c.FullPath())
			}
		}
	})
}
