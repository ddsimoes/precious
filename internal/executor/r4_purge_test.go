package executor

import (
	"bytes"
	"errors"
	"strconv"
	"testing"
	"time"

	"precious/internal/cleanup/stale"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// Paths of quarantined: plan 7 of a quarantine Precious made holds item 1,
// the folder Velho (x.txt, the link to it, sub/y.txt), and item 2, z.txt,
// each with its origin record; Docs/x.txt outside is a copy of Velho/x.txt.
const (
	plan7 = q + "/7"
	velho = plan7 + "/1/Velho"
	zTxt  = plan7 + "/2/z.txt"
)

// quarantined builds that source, scans it, and discards both items.
func quarantined(e *env) *synthfs.Node {
	x := bytes.Repeat([]byte("x"), 100)
	root := e.disk("/src", posix, func(r *synthfs.Node) {
		r.Dir("Docs").File("x.txt", 0, mtime2004).Content(x)
		p := r.Dir(q).Dir("7")
		v := p.Dir("1").Dir("Velho")
		v.File("x.txt", 0, mtime2004).Content(x)
		v.Symlink("link", "x.txt")
		v.Dir("sub").File("y.txt", 50, mtime2004)
		p.Dir("2").File("z.txt", 30, mtime2004)
	})
	e.markQuarantine(srcID)
	plan := root.Child(q).Child("7")
	for k, item := range map[string]string{"1": velho, "2": zTxt} {
		data, err := marshalRecord(originRecord{Version: 1, SourceID: string(srcID),
			EntryID: strconv.FormatInt(e.id(item), 10), PlanID: "7",
			Original: originPath{Path: "old", PathB64: []byte("old")}, QuarantinedAt: testNow.Format(time.RFC3339Nano)})
		if err != nil {
			e.t.Fatal(err)
		}
		plan.File(k+".json", 0, mtime2004).Content(data)
	}
	e.scan()
	laterChanges(root)
	e.decide(srcID, velho, "discard")
	e.decide(srcID, zTxt, "discard")
	return root
}

// sweep7 appends to a purge action the rmdir of the plan folder 7.
func (e *env) sweep7(action int64) {
	e.t.Helper()
	e.exec(`INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, from_name, from_path, state)
		SELECT ?, (SELECT max(seq) + 1 FROM action_items WHERE action_id = ?), 'rmdir', id, parent_id, name, path,
			'planned'
		FROM entries WHERE source_id = ? AND path = ?`, action, action, string(srcID), []byte(plan7))
}

// R4.5 at the executor (r4 D10, D11): verify, then each checked item
// deleted whole with its record and item folder, then the emptied plan
// folder swept. The index rows go, the report counts the files, their
// bytes, and the allocation of each file whose last link was removed, and
// the check stays ready.
func TestR4PurgeDeletesCheckedItems(t *testing.T) {
	e := newEnv(t)
	quarantined(e)
	check := e.check(srcID, velho, zTxt)
	e.relyOn(check, velho+"/x.txt", srcID, "Docs/x.txt")
	action := e.purgeAction(srcID, check, true, velho, zTxt)
	e.sweep7(action)
	e.run(action)
	e.wantStates(action, actionDone, stateDone, stateDone, stateDone, stateDone)
	for _, p := range []string{plan7, velho, zTxt} {
		if _, ok := e.lstat("/src", p); ok {
			t.Errorf("%s is still on the disk", p)
		}
		if s := e.state(srcID, p); s != "" {
			t.Errorf("%s is still indexed (%s)", p, s)
		}
	}
	if _, ok := e.lstat("/src", "Docs/x.txt"); !ok {
		t.Error("the copy was deleted")
	}
	if _, ok := e.lstat("/src", q); !ok {
		t.Error("the quarantine folder was removed")
	}
	files, size, freed := e.report(action)
	if files != 3 || size != 180 || freed != 3*4096 {
		t.Errorf("report %d files, %d bytes, %d freed; want 3, 180, %d", files, size, freed, 3*4096)
	}
	if st, _ := e.checkState(check); st != "ready" {
		t.Errorf("check %s after its purge, want ready", st)
	}
	// Each file, link, and folder once, the two records, and the folders
	// 7/1, 7/2, and 7.
	if n := e.rec.Count(instrument.OpUnlink); n != 6 {
		t.Errorf("%d unlinks, want 6", n)
	}
	if n := e.rec.Count(instrument.OpRmdir); n != 5 {
		t.Errorf("%d rmdirs, want 5", n)
	}
}

// R4.8 at the purge (spec cleanup, "A verified copy changes before the
// purge"): a set file or a relied-on copy changed on disk after the check,
// with no rescan, stops verify: nothing is deleted, the purge items are not
// attempted, and the check is stale.
func TestR4_8VerifyStopsOnADiskChange(t *testing.T) {
	for _, tc := range []struct {
		name, path, reason string
	}{
		{"a set file", velho + "/sub/y.txt", reasonFileChanged},
		{"a relied-on copy", "Docs/x.txt", reasonCopyChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			root := quarantined(e)
			check := e.check(srcID, velho, zTxt)
			e.relyOn(check, velho+"/x.txt", srcID, "Docs/x.txt")
			action := e.purgeAction(srcID, check, true, velho, zTxt)
			node := root
			for _, p := range splitPath(tc.path) {
				node = node.Child(p)
			}
			node.Patch(0, []byte("!"))
			e.run(action)
			e.wantStates(action, actionStopped, stateChanged, stateNotAttempted, stateNotAttempted)
			if r := e.item(action, 1).Reason; r != tc.reason {
				t.Errorf("verify reason %q, want %s", r, tc.reason)
			}
			if n := e.deletions(); n != 0 {
				t.Errorf("%d deletions", n)
			}
			if st, why := e.checkState(check); st != "stale" || why != staleDiskChanged {
				t.Errorf("check %s (%s), want stale disk_changed", st, why)
			}
		})
	}
}

// Spec source-writes, "Deletion stays inside the quarantine", and R4.8 (an
// unrecorded file found late in an item's tree): a purge item naming a path
// outside the quarantine deletes nothing and ends changed; a file the
// check did not record, deep in the item, leaves the whole item untouched,
// and the check stale.
func TestR4DeletionStaysInsideTheQuarantine(t *testing.T) {
	t.Run("outside", func(t *testing.T) {
		e := newEnv(t)
		quarantined(e)
		e.decide(srcID, "Docs", "discard")
		check := e.check(srcID, "Docs")
		action := e.purgeAction(srcID, check, false, "Docs")
		e.run(action)
		e.wantStates(action, actionDone, stateChanged)
		if n := e.deletions(); n != 0 {
			t.Errorf("%d deletions", n)
		}
	})
	t.Run("unrecorded", func(t *testing.T) {
		e := newEnv(t)
		root := quarantined(e)
		check := e.check(srcID, velho, zTxt)
		action := e.purgeAction(srcID, check, false, velho, zTxt)
		root.Child(q).Child("7").Child("1").Child("Velho").Child("sub").File("zz.txt", 5, mtime2004)
		e.run(action)
		e.wantStates(action, actionStopped, stateChanged, stateChanged)
		if r := e.item(action, 1).Reason; r != reasonFileChanged {
			t.Errorf("reason %q, want file_changed", r)
		}
		if r := e.item(action, 2).Reason; r != reasonCheckStale {
			t.Errorf("second item's reason %q, want check_stale", r)
		}
		if n := e.deletions(); n != 0 {
			t.Errorf("%d deletions", n)
		}
		for _, p := range []string{velho + "/x.txt", velho + "/sub/y.txt", zTxt} {
			if _, ok := e.lstat("/src", p); !ok {
				t.Errorf("%s was deleted", p)
			}
		}
		if st, _ := e.checkState(check); st != "stale" {
			t.Errorf("check %s, want stale", st)
		}
	})
}

// r4 D10 (tasks 2.4): two set files sharing an inode purge without a false
// changed. Removing the first name changes the second's change time and
// link count; the space is freed with the last link only.
func TestR4PurgeHardLinks(t *testing.T) {
	e := newEnv(t)
	root := e.disk("/src", posix, func(r *synthfs.Node) {
		p := r.Dir(q).Dir("7")
		x := p.Dir("1").File("x.bin", 8192, mtime2004)
		p.Dir("2").HardLink("y.bin", x)
	})
	e.markQuarantine(srcID)
	laterChanges(root)
	a, b := plan7+"/1/x.bin", plan7+"/2/y.bin"
	e.decide(srcID, a, "discard")
	e.decide(srcID, b, "discard")
	check := e.check(srcID, a, b)
	action := e.purgeAction(srcID, check, true, a, b)
	e.run(action)
	e.wantStates(action, actionDone, stateDone, stateDone, stateDone)
	files, size, freed := e.report(action)
	if files != 2 || size != 16384 || freed != 8192 {
		t.Errorf("report %d files, %d bytes, %d freed; want 2, 16384, 8192", files, size, freed)
	}
}

// r4 D11 (tasks 2.4): a crash midway through a purge step, reconciled,
// deletes the rest once. With writes turned off meanwhile, it deletes
// nothing more, records what was deleted, and ends changed writes_off.
func TestR4PurgeCrashMidway(t *testing.T) {
	for _, writesOff := range []bool{false, true} {
		t.Run(map[bool]string{false: "replayed", true: "writes off"}[writesOff], func(t *testing.T) {
			e := newEnv(t)
			quarantined(e)
			check := e.check(srcID, velho)
			action := e.purgeAction(srcID, check, false, velho)
			purgeID := e.item(action, 1).ID
			calls := 0
			e.hooks = Hooks{BeforeStep: func(item int64) error {
				if item == purgeID {
					if calls++; calls == 2 {
						return errCrash
					}
				}
				return nil
			}}
			e.executor()
			if err := e.attempt(e.jobOf(action)); !errors.Is(err, errCrash) {
				t.Fatalf("attempt: %v, want the crash", err)
			}
			e.wantStates(action, actionRunning, stateIntent)
			var first string
			for _, c := range e.rec.Calls() {
				if c.Op == instrument.OpUnlink || c.Op == instrument.OpRmdir {
					first = c.FullPath()
				}
			}
			if e.deletions() != 1 {
				t.Fatalf("%d deletions before the crash, want 1", e.deletions())
			}
			if writesOff {
				e.exec(`UPDATE sources SET write_enabled = 0 WHERE id = ?`, string(srcID))
			}
			e.hooks = Hooks{}
			e.executor()
			e.run(action)
			seen := map[string]int{}
			for _, c := range e.rec.Calls() {
				if c.Op == instrument.OpUnlink || c.Op == instrument.OpRmdir {
					seen[c.FullPath()]++
				}
			}
			for p, n := range seen {
				if n != 1 {
					t.Errorf("%s deleted %d times", p, n)
				}
			}
			files, _, _ := e.report(action)
			if !writesOff {
				e.wantStates(action, actionDone, stateDone)
				if _, ok := e.lstat("/src", plan7+"/1"); ok {
					t.Error("the item folder stays")
				}
				if files != 2 {
					t.Errorf("%d files deleted, want 2", files)
				}
				return
			}
			e.wantStates(action, actionStopped, stateChanged)
			if r := e.item(action, 1).Reason; r != reasonWritesOff {
				t.Errorf("reason %q, want writes_off", r)
			}
			if len(seen) != 1 {
				t.Errorf("deleted %v after the crash, want only the first", seen)
			}
			gone := 0
			for _, p := range []string{velho + "/x.txt", velho + "/link", velho + "/sub/y.txt"} {
				if e.state(srcID, p) == "" {
					gone++
					if "/src/"+p != first {
						t.Errorf("%s left the index, but %s was deleted", p, first)
					}
				}
			}
			if gone != 1 {
				t.Errorf("%d rows gone from the index, want the one deleted", gone)
			}
		})
	}
}

// r4 D10: every outcome marks stale the checks relying on its paths. An
// organize move of the folder holding a relied-on copy makes the check
// stale, and the purge then deletes nothing.
func TestR4MoveStalesACheck(t *testing.T) {
	e := newEnv(t)
	e.disk("/src", posix, func(r *synthfs.Node) {
		r.Dir("Docs").File("x.txt", 10, mtime2004)
		r.Dir("Arquivo")
		r.Dir(q).Dir("7").Dir("1").File("x.txt", 10, mtime2004)
	})
	e.markQuarantine(srcID)
	item := plan7 + "/1/x.txt"
	e.decide(srcID, item, "discard")
	check := e.check(srcID, item)
	e.relyOn(check, item, srcID, "Docs/x.txt")
	move := e.action("move", false, step{Op: opRename, From: "Docs", To: "Arquivo", Name: "Docs"})
	e.run(move)
	if st, why := e.checkState(check); st != "stale" || why != stale.ReasonIndexChanged {
		t.Fatalf("check %s (%s) after the move, want stale index_changed", st, why)
	}
	action := e.purgeAction(srcID, check, true, item)
	e.run(action)
	e.wantStates(action, actionStopped, stateChanged, stateNotAttempted)
	if r := e.item(action, 1).Reason; r != reasonCheckStale {
		t.Errorf("reason %q, want check_stale", r)
	}
	if n := e.deletions(); n != 0 {
		t.Errorf("%d deletions", n)
	}
}

// r4 D11: a purge waits for its gate. A gated file not confirmed stops it
// at verify with nothing deleted; a decision taken back ends that item
// changed decision_changed.
func TestR4PurgeRechecksTheGateAndDecisions(t *testing.T) {
	e := newEnv(t)
	quarantined(e)
	check := e.check(srcID, velho, zTxt)
	e.exec(`UPDATE purge_check_files SET confirmed_at = NULL WHERE check_id = ? AND path = ?`, check,
		[]byte(zTxt))
	action := e.purgeAction(srcID, check, true, velho, zTxt)
	e.run(action)
	e.wantStates(action, actionStopped, stateChanged, stateNotAttempted, stateNotAttempted)
	e.exec(`UPDATE purge_check_files SET confirmed_at = 1 WHERE check_id = ?`, check)
	e.decide(srcID, zTxt, "keep")
	again := e.purgeAction(srcID, check, true, velho, zTxt)
	e.run(again)
	e.wantStates(again, actionDone, stateDone, stateDone, stateChanged)
	if r := e.item(again, 3).Reason; r != reasonDecisionChanged {
		t.Errorf("reason %q, want decision_changed", r)
	}
	if _, ok := e.lstat("/src", zTxt); !ok {
		t.Error("the kept file was deleted")
	}
}
