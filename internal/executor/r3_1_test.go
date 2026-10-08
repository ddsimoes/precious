package executor

import (
	"testing"
	"time"

	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// R3.1 (spec organizing, "A name taken after planning"): a file created at
// the destination of the second item, after that item's intent and before
// its rename, ends the item conflict with both files unchanged, and the
// other two items move.
func TestR3_1NewcomerAtTheDestinationIsAConflict(t *testing.T) {
	e := newEnv(t)
	var dest *synthfs.Node
	mtime := time.Date(2007, 5, 1, 10, 0, 0, 0, time.UTC)
	e.disk("/src", posix, func(root *synthfs.Node) {
		a := root.Dir("A")
		a.File("a.txt", 10, mtime)
		a.File("b.txt", 20, mtime)
		a.File("c.txt", 30, mtime)
		dest = root.Dir("B")
	})
	action := e.action("move", true,
		step{Op: opRename, From: "A/a.txt", To: "B", Name: "a.txt"},
		step{Op: opRename, From: "A/b.txt", To: "B", Name: "b.txt"},
		step{Op: opRename, From: "A/c.txt", To: "B", Name: "c.txt"})
	second := e.item(action, 2).ID

	var newcomer *synthfs.Node
	var stateAtStep string
	e.rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op != instrument.OpRename || c.To != "/src/B/b.txt" || newcomer != nil {
			return
		}
		if err := e.st.Reader().QueryRow(`SELECT state FROM action_items WHERE id = ?`, second).Scan(&stateAtStep); err != nil {
			t.Error(err)
		}
		newcomer = dest.File("b.txt", 99, mtime.Add(time.Hour))
	})
	before, _ := e.lstat("/src", "A/b.txt")
	e.run(action)

	if stateAtStep != stateIntent {
		t.Errorf("the second item was %q when its rename was called, want intent", stateAtStep)
	}
	e.wantStates(action, actionDone, stateDone, stateConflict, stateDone)
	if got := e.item(action, 2); got.Reason != reasonNameTaken {
		t.Errorf("second item reason = %q, want %q", got.Reason, reasonNameTaken)
	}
	if n := e.rec.Count(instrument.OpRename); n != 3 {
		t.Errorf("%d renames called, want 3", n)
	}
	// Both files of the conflict are unchanged.
	old, ok := e.lstat("/src", "A/b.txt")
	if !ok || old.Ino != before.Ino || old.Size != 20 {
		t.Errorf("A/b.txt after the run: %+v (present %v), want inode %d of 20 bytes", old, ok, before.Ino)
	}
	got, ok := e.lstat("/src", "B/b.txt")
	if want := newcomer.Info(); !ok || got.Ino != want.Ino || got.Size != 99 {
		t.Errorf("B/b.txt after the run: %+v (present %v), want the newcomer, inode %d of 99 bytes", got, ok, want.Ino)
	}
	for _, p := range []string{"B/a.txt", "B/c.txt"} {
		if _, ok := e.lstat("/src", p); !ok {
			t.Errorf("%s was not moved", p)
		}
	}
	for _, p := range []string{"A/a.txt", "A/c.txt"} {
		if _, ok := e.lstat("/src", p); ok {
			t.Errorf("%s is still there", p)
		}
	}
	if renames, _, _, done := e.idx.counts(); renames != 2 || done != 1 {
		t.Errorf("index: %d renames applied and %d ActionDone, want 2 and 1", renames, done)
	}
	// The index followed the two moves only.
	if e.id("B/a.txt") == 0 || e.id("A/b.txt") == 0 || e.id("B/c.txt") == 0 {
		t.Error("the index does not hold the moved and the left entries")
	}
}

// A rename onto a taken name never replaces it: neither onto an indexed
// name nor onto one created after the scan, nor, on a case-insensitive
// disk, onto a name that differs only in letter case.
func TestR3_1RenameNeverReplaces(t *testing.T) {
	t.Run("indexed", func(t *testing.T) {
		e := newEnv(t)
		mtime := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
		e.disk("/src", posix, func(root *synthfs.Node) {
			root.File("a.txt", 1, mtime)
			root.File("b.txt", 2, mtime)
		})
		a, _ := e.lstat("/src", "a.txt")
		b, _ := e.lstat("/src", "b.txt")
		action := e.action("rename", false, step{Op: opRename, From: "a.txt", To: "", Name: "b.txt"})
		e.run(action)
		e.wantStates(action, actionDone, stateConflict)
		wantSame(t, e, "a.txt", a)
		wantSame(t, e, "b.txt", b)
	})
	t.Run("created after the scan", func(t *testing.T) {
		e := newEnv(t)
		mtime := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
		var root *synthfs.Node
		e.disk("/src", posix, func(r *synthfs.Node) {
			root = r
			r.File("a.txt", 1, mtime)
		})
		a, _ := e.lstat("/src", "a.txt")
		action := e.action("rename", false, step{Op: opRename, From: "a.txt", To: "", Name: "b.txt"})
		root.File("b.txt", 2, mtime)
		b, _ := e.lstat("/src", "b.txt")
		e.run(action)
		e.wantStates(action, actionDone, stateConflict)
		wantSame(t, e, "a.txt", a)
		wantSame(t, e, "b.txt", b)
	})
	t.Run("case-insensitive", func(t *testing.T) {
		e := newEnv(t)
		mtime := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
		e.disk("/src", fat, func(root *synthfs.Node) {
			root.File("a.txt", 1, mtime)
			root.File("FOTO.JPG", 2, mtime)
		})
		a, _ := e.lstat("/src", "a.txt")
		foto, _ := e.lstat("/src", "FOTO.JPG")
		action := e.action("rename", false, step{Op: opRename, From: "a.txt", To: "", Name: "foto.jpg"})
		e.run(action)
		e.wantStates(action, actionDone, stateConflict)
		wantSame(t, e, "a.txt", a)
		wantSame(t, e, "FOTO.JPG", foto)
	})
}

// wantSame fails unless rel is still the entry before describes.
func wantSame(t *testing.T, e *env, rel string, before fsaccess.EntryInfo) {
	t.Helper()
	got, ok := e.lstat("/src", rel)
	if !ok || got.Ino != before.Ino || got.Size != before.Size || !got.ModTime.Equal(before.ModTime) ||
		string(got.Name) != string(before.Name) {
		t.Errorf("%s after the run: %+v (present %v), want %+v", rel, got, ok, before)
	}
}
