package executor

import (
	"context"
	"errors"
	"testing"

	"precious/internal/fsaccess/synthfs"
)

// crashAtSecond is a BeforeStep hook that kills the attempt at the second
// deletion of the item id: the first deletion of its step is done.
func crashAtSecond(id *int64) func(int64) error {
	calls := 0
	return func(item int64) error {
		if item == *id {
			if calls++; calls == 2 {
				return errCrash
			}
		}
		return nil
	}
}

// G2 (r4 D10): each purge item compares the copies its records rely on
// again just before its first deletion. A purge interrupted after its first
// item (a lost lease) resumes without verifying again; a copy of the second
// item removed meanwhile, or its source gone offline, ends that item changed
// copy_changed with its file on the disk, the check stale, and the action
// stopped.
func TestR4PurgeRechecksCopiesBeforeEachItem(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(map[bool]string{false: "copy removed", true: "copy offline"}[offline], func(t *testing.T) {
			e := newEnv(t)
			root := quarantined(e)
			copySrc, copyPath := srcID, "Docs/x.txt"
			if offline {
				e.diskAs(otherID, "/other", posix, func(r *synthfs.Node) {
					r.Dir("Docs").File("z.txt", 30, mtime2004)
				})
				copySrc, copyPath = otherID, "Docs/z.txt"
			}
			check := e.check(srcID, velho, zTxt)
			e.relyOn(check, zTxt, copySrc, copyPath)
			action := e.purgeAction(srcID, check, true, velho, zTxt)
			first := e.item(action, 2).ID
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.hooks = Hooks{AfterStep: func(item int64) error {
				if item == first {
					cancel()
				}
				return nil
			}}
			e.executor()
			if err := e.attemptWith(ctx, e.jobOf(action), &fakeRuntime{}); !errors.Is(err, context.Canceled) {
				t.Fatalf("attempt: %v, want context.Canceled", err)
			}
			e.wantStates(action, actionRunning, stateDone, stateDone, statePlanned)
			if offline {
				e.sfs.Vanish("/other")
			} else {
				root.Child("Docs").Remove("x.txt")
			}
			e.hooks = Hooks{}
			e.executor()
			e.run(action)
			e.wantStates(action, actionStopped, stateDone, stateDone, stateChanged)
			if it := e.item(action, 3); it.Reason != reasonCopyChanged || it.Detail != copyPath {
				t.Errorf("second item %s %q, want copy_changed %s", it.Reason, it.Detail, copyPath)
			}
			if _, ok := e.lstat("/src", zTxt); !ok {
				t.Error("the item whose copy is gone was deleted")
			}
			if st, why := e.checkState(check); st != "stale" || why != staleDiskChanged {
				t.Errorf("check %s (%s), want stale disk_changed", st, why)
			}
		})
	}
}

// G2 (r4 D10, D11): a crash midway through an item, then its copy deleted:
// reconciling replays the step, compares the copy again, and deletes
// nothing more. The item ends changed copy_changed with what the crash
// deleted recorded, and the check is stale.
func TestR4PurgeReplayRechecksCopies(t *testing.T) {
	e := newEnv(t)
	root := quarantined(e)
	check := e.check(srcID, velho, zTxt)
	e.relyOn(check, velho+"/x.txt", srcID, "Docs/x.txt")
	action := e.purgeAction(srcID, check, true, zTxt, velho)
	second := e.item(action, 3).ID
	e.hooks = Hooks{BeforeStep: crashAtSecond(&second)}
	e.executor()
	if err := e.attempt(e.jobOf(action)); !errors.Is(err, errCrash) {
		t.Fatalf("attempt: %v, want the crash", err)
	}
	e.wantStates(action, actionRunning, stateDone, stateDone, stateIntent)
	before := e.deletions()
	root.Child("Docs").Remove("x.txt")
	e.hooks = Hooks{}
	e.executor()
	e.run(action)
	e.wantStates(action, actionStopped, stateDone, stateDone, stateChanged)
	if r := e.item(action, 3).Reason; r != reasonCopyChanged {
		t.Errorf("reason %q, want copy_changed", r)
	}
	if n := e.deletions(); n != before {
		t.Errorf("%d deletions after reconciling, want none (%d before)", n-before, before)
	}
	if st, why := e.checkState(check); st != "stale" || why != staleDiskChanged {
		t.Errorf("check %s (%s), want stale disk_changed", st, why)
	}
}

// G2 (r4 D10): a hard-link copy keeps its data when a set file of the same
// inode is purged; the change time that removal gives it is no difference
// for a later item relying on the same copy.
func TestR4PurgeHardLinkCopyOfAnEarlierItem(t *testing.T) {
	e := newEnv(t)
	root := e.disk("/src", posix, func(r *synthfs.Node) {
		x := r.Dir("Docs").File("x.bin", 8192, mtime2004)
		p := r.Dir(q).Dir("7")
		p.Dir("1").HardLink("x.bin", x)
		p.Dir("2").File("y.bin", 8192, mtime2004)
	})
	e.markQuarantine(srcID)
	laterChanges(root)
	a, b := plan7+"/1/x.bin", plan7+"/2/y.bin"
	e.decide(srcID, a, "discard")
	e.decide(srcID, b, "discard")
	check := e.check(srcID, a, b)
	e.relyOn(check, a, srcID, "Docs/x.bin")
	e.relyOn(check, b, srcID, "Docs/x.bin")
	action := e.purgeAction(srcID, check, true, a, b)
	e.run(action)
	e.wantStates(action, actionDone, stateDone, stateDone, stateDone)
	if _, ok := e.lstat("/src", "Docs/x.bin"); !ok {
		t.Error("the copy was deleted")
	}
}

// G3 (r4 D11): a lasting difference met while reconciling a purge ends the
// item manual_recovery instead of failing the attempt: a <seq> folder
// replaced while the re-checks fail (writes turned off), or a folder inside
// the item made unreadable before a replay. The attempt ends without an
// error, and a following action of the source reaches its intent.
func TestR4PurgeReconcileLastingDifference(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(e *env, root *synthfs.Node)
	}{
		{"seq replaced, writes off", func(e *env, root *synthfs.Node) {
			e.exec(`UPDATE sources SET write_enabled = 0 WHERE id = ?`, string(srcID))
			plan := root.Child(q).Child("7")
			plan.Remove("1")
			plan.Dir("1").Dir("Velho")
		}},
		{"folder inside unreadable, replayed", func(e *env, root *synthfs.Node) {
			root.Child(q).Child("7").Child("1").Child("Velho").Child("sub").Unreadable()
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			root := quarantined(e)
			check := e.check(srcID, velho)
			action := e.purgeAction(srcID, check, false, velho)
			purgeID := e.item(action, 1).ID
			e.hooks = Hooks{BeforeStep: crashAtSecond(&purgeID)}
			e.executor()
			if err := e.attempt(e.jobOf(action)); !errors.Is(err, errCrash) {
				t.Fatalf("attempt: %v, want the crash", err)
			}
			c.change(e, root)
			e.hooks = Hooks{}
			e.executor()
			e.run(action)
			e.wantStates(action, actionStopped, stateManualRecovery)
			if n := e.deletions(); n != 1 {
				t.Errorf("%d deletions, want the one before the crash", n)
			}
			e.exec(`UPDATE sources SET write_enabled = 1 WHERE id = ?`, string(srcID))
			next := e.action("create_folder", false, step{Op: opMkdir, To: "Docs", Name: "Novo"})
			e.run(next)
			e.wantStates(next, actionStopped, stateNotAttempted)
		})
	}
}

// G4 (r4 D11): reconciling a purge item left intent by an action already
// stopped stops only that action. Another action's job that reconciles it,
// its check gone stale meanwhile, ends the item changed check_stale and
// still runs its own items.
func TestR4PurgeReconcileStopsOnlyItsAction(t *testing.T) {
	e := newEnv(t)
	quarantined(e)
	check := e.check(srcID, velho)
	purge := e.purgeAction(srcID, check, false, velho)
	purgeID := e.item(purge, 1).ID
	e.hooks = Hooks{BeforeStep: crashAtSecond(&purgeID)}
	e.executor()
	if err := e.attempt(e.jobOf(purge)); !errors.Is(err, errCrash) {
		t.Fatalf("attempt: %v, want the crash", err)
	}
	// The startup sweep found its job gone and stopped the action.
	e.exec(`UPDATE jobs SET state = 'failed' WHERE id = ?`, int64(e.jobOf(purge)))
	e.exec(`UPDATE actions SET state = 'stopped' WHERE id = ?`, purge)
	e.exec(`UPDATE purge_checks SET state = 'stale', stale_reason = 'index_changed' WHERE id = ?`, check)
	e.hooks = Hooks{}
	e.executor()
	next := e.action("create_folder", false, step{Op: opMkdir, To: "Docs", Name: "Novo"})
	e.run(next)
	e.wantStates(purge, actionStopped, stateChanged)
	if r := e.item(purge, 1).Reason; r != reasonCheckStale {
		t.Errorf("purge item reason %q, want check_stale", r)
	}
	e.wantStates(next, actionDone, stateDone)
	if _, ok := e.lstat("/src", "Docs/Novo"); !ok {
		t.Error("the other action's folder was not made")
	}
}

// G5 (r4 D13): an organize rmdir (an undo of a folder made, since
// quarantined) never removes a folder in the quarantine: refused
// in_quarantine, the folder still on the disk.
func TestR4OrganizeRmdirLeavesTheQuarantine(t *testing.T) {
	e := newEnv(t)
	e.disk("/src", posix, func(r *synthfs.Node) { r.Dir(q).Dir("7").Dir("1").Dir("Tmp") })
	e.markQuarantine(srcID)
	tmp := plan7 + "/1/Tmp"
	action := e.action("undo", false, step{Op: opRmdir, From: tmp})
	e.run(action)
	e.wantStates(action, actionDone, stateRefused)
	if r := e.item(action, 1).Reason; r != reasonInQuarantine {
		t.Errorf("reason %q, want in_quarantine", r)
	}
	if _, ok := e.lstat("/src", tmp); !ok {
		t.Error("the quarantined folder was removed")
	}
	if e.state(srcID, tmp) != "present" {
		t.Errorf("the quarantined folder is %q in the index", e.state(srcID, tmp))
	}
}
