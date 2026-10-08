//go:build e2e && linux

package executor

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
)

// nsEnv names the test this binary re-runs as root of a user and mount
// namespace.
const nsEnv = "PRECIOUS_EXECUTOR_E2E_NS"

// R3.1 with the real renameat2 (spec source-writes, "A destination that
// appears during execution stops that item"): on a tmpfs, through the Linux
// backend and the source registry, a file created at the destination of the
// second item after its intent and before its rename ends that item
// conflict, both files keep their content and inodes, and the other two
// files move.
//
// Mounting the tmpfs needs root: the test runs directly as root, inside a
// user and mount namespace (unshare(1)) otherwise, and skips, naming the
// reason, when neither works.
func TestR3_1NewcomerAtTheDestinationRealRename(t *testing.T) {
	asRoot(t, func(t *testing.T) {
		ctx := context.Background()
		mnt := filepath.Join(t.TempDir(), "disk")
		if err := os.Mkdir(mnt, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mount("tmpfs", mnt, "tmpfs", 0, "mode=0755"); errors.Is(err, unix.EPERM) {
			t.Skipf("cannot mount a tmpfs (needs CAP_SYS_ADMIN, e.g. docker run --privileged): %v", err)
		} else if err != nil {
			t.Fatalf("mount a tmpfs on %s: %v", mnt, err)
		}
		t.Cleanup(func() { unix.Unmount(mnt, unix.MNT_DETACH) })
		for _, d := range []string{"A", "B"} {
			if err := os.Mkdir(filepath.Join(mnt, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
			writeFile(t, filepath.Join(mnt, "A", f), "old "+f)
		}

		e := newEnvOn(t, fsaccess.NewOS(), mnt)
		picked, err := e.src.PickerRoots(ctx)
		if err != nil || len(picked) != 1 {
			t.Fatalf("picker roots %v, %v: want the tmpfs", picked, err)
		}
		cand, err := e.src.PrepareAdd(ctx, picked[0].Handle, string(srcID))
		if err != nil {
			t.Fatal(err)
		}
		if err := e.st.Write(ctx, func(tx *sql.Tx) error {
			src, err := e.src.Add(ctx, tx, cand)
			if err == nil && src.ID != srcID {
				t.Fatalf("source %q, want %q", src.ID, srcID)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		e.exec(`UPDATE sources SET write_enabled = 1 WHERE id = ?`, string(srcID))
		e.scan()

		action := e.action("move", true,
			step{Op: opRename, From: "A/a.txt", To: "B", Name: "a.txt"},
			step{Op: opRename, From: "A/b.txt", To: "B", Name: "b.txt"},
			step{Op: opRename, From: "A/c.txt", To: "B", Name: "c.txt"})
		second := e.item(action, 2).ID
		newcomer := filepath.Join(mnt, "B", "b.txt")
		var stateAtStep string
		created := false
		e.rec.SetBeforeCall(func(c instrument.Call) {
			if c.Op != instrument.OpRename || c.To != newcomer || created {
				return
			}
			created = true
			if err := e.st.Reader().QueryRow(`SELECT state FROM action_items WHERE id = ?`, second).
				Scan(&stateAtStep); err != nil {
				t.Error(err)
			}
			writeFile(t, newcomer, "newcomer")
		})
		oldIno := ino(t, filepath.Join(mnt, "A", "b.txt"))
		e.run(action)

		if stateAtStep != stateIntent {
			t.Errorf("the second item was %q when its rename was called, want intent", stateAtStep)
		}
		e.wantStates(action, actionDone, stateDone, stateConflict, stateDone)
		if got := e.item(action, 2).Reason; got != reasonNameTaken {
			t.Errorf("second item reason %q, want %q", got, reasonNameTaken)
		}
		if got := readFile(t, filepath.Join(mnt, "A", "b.txt")); got != "old b.txt" {
			t.Errorf("A/b.txt holds %q", got)
		}
		if got := ino(t, filepath.Join(mnt, "A", "b.txt")); got != oldIno {
			t.Errorf("A/b.txt has inode %d, want %d", got, oldIno)
		}
		if got := readFile(t, newcomer); got != "newcomer" {
			t.Errorf("B/b.txt holds %q, want the newcomer", got)
		}
		for _, f := range []string{"a.txt", "c.txt"} {
			if got := readFile(t, filepath.Join(mnt, "B", f)); got != "old "+f {
				t.Errorf("B/%s holds %q", f, got)
			}
			if _, err := os.Lstat(filepath.Join(mnt, "A", f)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("A/%s: %v, want it gone", f, err)
			}
		}
		if n := e.rec.Count(instrument.OpRename); n != 3 {
			t.Errorf("%d renames called, want 3", n)
		}
	})
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ino(t *testing.T, path string) uint64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return st.Ino
}

// asRoot runs body as root: directly when the test runs as root, else in a
// re-run of this test inside a fresh user and mount namespace.
func asRoot(t *testing.T, body func(t *testing.T)) {
	t.Helper()
	if os.Geteuid() == 0 || os.Getenv(nsEnv) == t.Name() {
		body(t)
		return
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skipf("not root, and unshare(1) is not installed: %v", err)
	}
	ns := []string{"--user", "--mount", "--map-root-user"}
	if out, err := exec.Command("unshare", append(ns, "true")...).CombinedOutput(); err != nil {
		t.Skipf("not root, and unprivileged user namespaces are denied (unshare --user --mount --map-root-user: %v: %s)",
			err, bytes.TrimSpace(out))
	}
	cmd := exec.Command("unshare", append(ns, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")...)
	cmd.Env = append(os.Environ(), nsEnv+"="+t.Name())
	out, err := cmd.CombinedOutput()
	switch {
	case bytes.Contains(out, []byte("--- SKIP: "+t.Name())):
		t.Skipf("the namespaced run skipped:\n%s", out)
	case err != nil || !bytes.Contains(out, []byte("--- PASS: "+t.Name())):
		t.Fatalf("namespaced run failed: %v\n%s", err, out)
	}
}
