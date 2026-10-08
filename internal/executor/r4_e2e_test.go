//go:build e2e && linux

package executor

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"

	"precious/internal/fsaccess"
)

// r4 D3, D11, D12 with the real system calls: on a tmpfs, through the Linux
// backend, a cleanup plan quarantines a folder and a file (renameat2, then
// each origin record created exclusively with its folder's mode), and a
// purge of the checked set deletes both items whole, a hard-linked pair
// included without a false changed, removes the records and item folders,
// sweeps the plan folder, and reports the space of the last links.
func TestR4_5QuarantineAndPurgeOnTmpfs(t *testing.T) {
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
		if err := os.MkdirAll(filepath.Join(mnt, "Velho", "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(mnt, "Velho", "x.txt"), "xxxxxxxxxx")
		writeFile(t, filepath.Join(mnt, "Velho", "sub", "y.txt"), "yyyy")
		writeFile(t, filepath.Join(mnt, "solto.txt"), "solto")
		if err := os.Link(filepath.Join(mnt, "Velho", "x.txt"), filepath.Join(mnt, "Velho", "h.txt")); err != nil {
			t.Fatal(err)
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
			_, err := e.src.Add(ctx, tx, cand)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		e.exec(`UPDATE sources SET write_enabled = 1 WHERE id = ?`, string(srcID))
		e.scan()
		e.decide(srcID, "Velho", "discard")
		e.decide(srcID, "solto.txt", "discard")

		cleanup := e.cleanupAction(srcID, "discard", "Velho", "solto.txt")
		e.run(cleanup)
		if got := e.cleanupStates(cleanup); len(got) != 2 || got[0] != "done/done/done" || got[1] != "done/done/done" {
			t.Fatalf("cleanup items %v, want done", got)
		}
		plan := filepath.Join(mnt, q, strconv.FormatInt(cleanup, 10))
		if got := readFile(t, filepath.Join(plan, "1", "Velho", "x.txt")); got != "xxxxxxxxxx" {
			t.Errorf("the quarantined x.txt holds %q", got)
		}
		rec, err := os.Lstat(filepath.Join(plan, "2.json"))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Mode().Perm() != 0o644 {
			t.Errorf("the record's mode is %v, want its folder's 0755 & 0666", rec.Mode().Perm())
		}
		if r, ok := parseRecord([]byte(readFile(t, filepath.Join(plan, "2.json")))); !ok ||
			r.Original.Path != "solto.txt" {
			t.Errorf("record %+v, %v", r, ok)
		}

		// The pre-delete check reads a scan of the quarantine.
		e.scan()
		items := []string{planDir(cleanup) + "/1/Velho", planDir(cleanup) + "/2/solto.txt"}
		check := e.check(srcID, items...)
		purge := e.purgeAction(srcID, check, true, items...)
		e.exec(`INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, from_name, from_path, state)
			SELECT ?, 4, 'rmdir', id, parent_id, name, path, 'planned' FROM entries WHERE source_id = ? AND path = ?`,
			purge, string(srcID), []byte(planDir(cleanup)))
		e.run(purge)
		e.wantStates(purge, actionDone, stateDone, stateDone, stateDone, stateDone)
		if _, err := os.Lstat(plan); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the plan folder: %v, want it gone", err)
		}
		files, size, freed := e.report(purge)
		if files != 4 || size != 10+10+4+5 || freed != 3*4096 {
			t.Errorf("report %d files, %d bytes, %d freed; want 4, 29, %d", files, size, freed, 3*4096)
		}
	})
}
