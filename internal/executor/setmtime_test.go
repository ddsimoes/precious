package executor

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"precious/internal/cleanup/stale"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// newTime is a time the tests' files never have.
var newTime = time.Date(2004, 6, 30, 8, 0, 0, 0, time.UTC)

// r5 D13, intent: a time the filesystem cannot tell apart is refused
// no_change, a file with another link in the index hard_link, a quarantined
// one in_quarantine, a missing one changed; nothing is written for them, and
// the action goes on. An undo whose file's indexed time is no longer the one
// written ends changed identity_changed.
func TestSetMtimeIntentRechecks(t *testing.T) {
	e := newEnv(t)
	var y *synthfs.Node
	e.disk("/src", posix, func(r *synthfs.Node) {
		y = r.Dir("Fotos").Dir("2004")
		y.File("a.jpg", 100, mtime2004)
		y.File("b.jpg", 200, mtime2004)
		c := y.File("c.jpg", 300, mtime2004)
		y.HardLink("c2.jpg", c)
		y.File("d.jpg", 400, mtime2004)
		r.Dir(q).Dir("7").Dir("1").File("q.jpg", 500, mtime2004)
	})
	action := e.mtimeAction("set_mtime",
		mtimeStep{Path: "Fotos/2004/a.jpg", To: mtime2004},
		mtimeStep{Path: "Fotos/2004/c.jpg", To: newTime},
		mtimeStep{Path: q + "/7/1/q.jpg", To: newTime},
		mtimeStep{Path: "Fotos/2004/d.jpg", To: newTime},
		mtimeStep{Path: "Fotos/2004/b.jpg", To: newTime})
	y.Remove("d.jpg")
	e.scan()
	e.run(action)
	e.wantStates(action, actionDone, stateRefused, stateRefused, stateRefused, stateChanged, stateDone)
	for seq, want := range map[int]string{1: reasonNoChange, 2: reasonHardLink, 3: reasonInQuarantine, 4: ""} {
		if r := e.item(action, seq); r.Reason != want {
			t.Errorf("item %d: reason %q, want %q", seq, r.Reason, want)
		}
	}
	if n := e.rec.Count(instrument.OpSetModTime); n != 1 {
		t.Fatalf("%d times set, want 1 (b.jpg)", n)
	}
	var ctime int64
	if err := e.st.Reader().QueryRow(`SELECT ctime_ns FROM action_items WHERE action_id = ? AND seq = 5`, action).
		Scan(&ctime); err != nil || ctime == 0 {
		t.Errorf("intent recorded change time %d (%v), want the index's", ctime, err)
	}

	y.Child("b.jpg").ModTime(time.Date(2012, 5, 1, 8, 0, 0, 0, time.UTC))
	e.scan()
	undo := e.undoOf(action)
	e.run(undo)
	e.wantStates(undo, actionDone, stateChanged)
	if r := e.item(undo, 1); r.Reason != reasonIdentityChanged {
		t.Errorf("undo: reason %q, want identity_changed", r.Reason)
	}
	if n := e.rec.Count(instrument.OpSetModTime); n != 1 {
		t.Errorf("%d times set after the undo, want still 1", n)
	}
}

// r5 D13: on a local-time filesystem, a time an hour off is the same time
// (no_change); one further off is set.
func TestSetMtimeNoChangeOnALocalTimeDisk(t *testing.T) {
	e := newEnv(t)
	local := fsaccess.Capabilities{Known: true, TimeResolution: 2 * time.Second, LocalTime: true, NoReplaceRename: true}
	e.disk("/card", local, func(r *synthfs.Node) {
		r.File("x.jpg", 100, mtime2004)
		r.File("y.jpg", 100, mtime2004)
	})
	action := e.mtimeAction("set_mtime", mtimeStep{Path: "x.jpg", To: mtime2004.Add(time.Hour)},
		mtimeStep{Path: "y.jpg", To: mtime2004.Add(time.Hour + 4*time.Second)})
	e.run(action)
	e.wantStates(action, actionDone, stateRefused, stateDone)
	if r := e.item(action, 1); r.Reason != reasonNoChange {
		t.Errorf("reason %q, want no_change", r.Reason)
	}
}

// r5 H1 and K3, intent: a time the source's filesystem would clamp without
// an error (before 1980 on FAT) is refused date_out_of_range, and one the
// index reads as unknown (before 1970-01-02: 1965, or the epoch's first
// day, on ext4, which holds both) date_before_1970; neither is written and
// the action goes on. An undo restores what the disk held, even such a time.
func TestSetMtimeIntentRefusesATimeTheDiskCannotHold(t *testing.T) {
	t.Run("FAT", func(t *testing.T) {
		e := newEnv(t)
		e.fsType = "vfat"
		local := fsaccess.Capabilities{Known: true, TimeResolution: 2 * time.Second, LocalTime: true, NoReplaceRename: true}
		e.disk("/card", local, func(r *synthfs.Node) {
			r.File("x.jpg", 100, mtime2004)
			r.File("y.jpg", 100, mtime2004)
		})
		action := e.mtimeAction("set_mtime", mtimeStep{Path: "x.jpg", To: time.Date(1975, 6, 1, 12, 0, 0, 0, time.UTC)},
			mtimeStep{Path: "y.jpg", To: newTime})
		e.run(action)
		e.wantStates(action, actionDone, stateRefused, stateDone)
		if r := e.item(action, 1); r.Reason != reasonDateOutOfRange {
			t.Errorf("reason %q, want date_out_of_range", r.Reason)
		}
		if got := setTimes(e.rec.Calls()); len(got["x.jpg"]) != 0 || len(got["y.jpg"]) != 1 {
			t.Errorf("SetModTime calls %v, want y.jpg's only", got)
		}
		if info, _ := e.lstat("/card", "x.jpg"); !info.ModTime.Equal(mtime2004) {
			t.Errorf("x.jpg holds %v, want %v", info.ModTime, mtime2004)
		}
	})
	t.Run("ext4", func(t *testing.T) {
		e := newEnv(t)
		epoch := time.Unix(0, 340_000_000)
		e.disk("/src", posix, func(r *synthfs.Node) {
			r.File("x.jpg", 100, mtime2004)
			r.File("old.jpg", 100, mtime2004)
			r.File("lost.jpg", 100, epoch)
		})
		action := e.mtimeAction("set_mtime", mtimeStep{Path: "lost.jpg", To: newTime},
			mtimeStep{Path: "x.jpg", To: time.Date(1970, 1, 1, 12, 0, 0, 0, time.UTC)},
			mtimeStep{Path: "old.jpg", To: time.Date(1965, 8, 14, 10, 0, 0, 0, time.UTC)})
		e.run(action)
		e.wantStates(action, actionDone, stateDone, stateRefused, stateRefused)
		for _, n := range []int{2, 3} {
			if r := e.item(action, n); r.Reason != reasonDateBefore1970 {
				t.Errorf("item %d: reason %q, want date_before_1970", n, r.Reason)
			}
		}
		if got := setTimes(e.rec.Calls()); len(got["x.jpg"]) != 0 || len(got["old.jpg"]) != 0 {
			t.Errorf("SetModTime calls %v, want none on x.jpg or old.jpg", got)
		}
		undo := e.undoOf(action)
		e.run(undo)
		e.wantStates(undo, actionDone, stateDone)
		if info, _ := e.lstat("/src", "lost.jpg"); !info.ModTime.Equal(epoch) {
			t.Errorf("lost.jpg holds %v after the undo, want %v", info.ModTime, epoch)
		}
	})
}

// r5 D13, errors: a refused ownership ends that item failed not_owner and
// the action goes on; a read-only filesystem ends it failed and stops the
// action; an absent name is changed; anything else is failed with the
// system's message, and the action goes on. Writes stay on in every case.
func TestSetMtimeErrors(t *testing.T) {
	for _, tc := range []struct {
		name          string
		errno         syscall.Errno
		action        string
		first, second string
		reason        string
	}{
		{"not the owner", syscall.EPERM, actionDone, stateFailed, stateDone, reasonNotOwner},
		{"read-only", syscall.EROFS, actionStopped, stateFailed, stateNotAttempted, ""},
		{"absent", syscall.ENOENT, actionDone, stateChanged, stateDone, ""},
		{"input/output", syscall.EIO, actionDone, stateFailed, stateDone, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			photos(e)
			e.rec.InjectError(instrument.OpSetModTime, "/src/Fotos/2004/a.jpg",
				fsaccess.WriteError("SetModTime", []byte("a.jpg"), tc.errno))
			action := e.mtimeAction("set_mtime", mtimeStep{Path: "Fotos/2004/a.jpg", To: newTime},
				mtimeStep{Path: "Fotos/2004/b.jpg", To: newTime})
			e.run(action)
			e.wantStates(action, tc.action, tc.first, tc.second)
			r := e.item(action, 1)
			if r.Reason != tc.reason {
				t.Errorf("reason %q, want %q", r.Reason, tc.reason)
			}
			if tc.first == stateFailed && r.Detail != tc.errno.Error() {
				t.Errorf("detail %q, want %q", r.Detail, tc.errno.Error())
			}
			if !e.writesOn() {
				t.Error("the source's writes are off")
			}
			if f, _ := e.factsOf("entries", "Fotos/2004/a.jpg"); f.Mtime.Int64 != mtime2004.UnixNano() {
				t.Errorf("the index shows %v", time.Unix(0, f.Mtime.Int64).UTC())
			}
		})
	}
}

// r5 D13, reconcile: a set_mtime left intent whose name is gone, or holds
// another file, ends manual_recovery with the findings, and the index is
// not updated.
func TestSetMtimeReconcileNeedsCheck(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(y *synthfs.Node)
		findings string
	}{
		{"absent", func(y *synthfs.Node) { y.Remove("a.jpg") }, `{"from":"absent","to":"absent"}`},
		{"other", func(y *synthfs.Node) {
			y.Remove("a.jpg")
			y.File("a.jpg", 100, newTime)
		}, `{"from":"other","to":"absent"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			root := photos(e)
			e.hooks = Hooks{AfterStep: crashOnce()}
			e.executor()
			action := e.mtimeAction("set_mtime", mtimeStep{Path: "Fotos/2004/a.jpg", To: newTime})
			job := e.jobOf(action)
			if err := e.attempt(job); !errors.Is(err, errCrash) {
				t.Fatalf("first attempt: %v, want the simulated crash", err)
			}
			tc.change(root.Child("Fotos").Child("2004"))
			e.restart(job)
			e.wantStates(action, actionStopped, stateManualRecovery)
			if r := e.item(action, 1); r.Detail != tc.findings {
				t.Errorf("findings %s, want %s", r.Detail, tc.findings)
			}
			e.idx.mu.Lock()
			applied := len(e.idx.modTimes)
			e.idx.mu.Unlock()
			if applied != 0 || e.rec.Count(instrument.OpSetModTime) != 1 {
				t.Errorf("%d index updates, %d times set; want 0 and 1", applied, e.rec.Count(instrument.OpSetModTime))
			}
		})
	}
}

// r5 D13, outcome: a done set_mtime marks stale the pre-delete checks
// relying on its file (r4 D10), and the action's end calls ActionDone.
func TestSetMtimeStalesACheck(t *testing.T) {
	e := newEnv(t)
	e.disk("/src", posix, func(r *synthfs.Node) {
		r.Dir("Docs").File("x.jpg", 10, mtime2004)
		r.Dir(q).Dir("7").Dir("1").File("x.jpg", 10, mtime2004)
	})
	e.markQuarantine(srcID)
	item := plan7 + "/1/x.jpg"
	e.decide(srcID, item, "discard")
	check := e.check(srcID, item)
	e.relyOn(check, item, srcID, "Docs/x.jpg")
	action := e.mtimeAction("set_mtime", mtimeStep{Path: "Docs/x.jpg", To: newTime})
	e.run(action)
	e.wantStates(action, actionDone, stateDone)
	if st, why := e.checkState(check); st != "stale" || why != stale.ReasonIndexChanged {
		t.Errorf("check %s (%s), want stale index_changed", st, why)
	}
	if _, _, _, done := e.idx.counts(); done != 1 {
		t.Errorf("ActionDone called %d times, want 1", done)
	}
}
