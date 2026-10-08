package executor

import (
	"bytes"
	"errors"
	"strconv"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// discards is a source with Fotos/2004/{a,b}.jpg, the folder Velho holding
// x.txt and sub/y.txt, and solto.txt, each discarded, and an empty Arquivo.
func discards(e *env) *synthfs.Node {
	root := e.disk("/src", posix, func(r *synthfs.Node) {
		y := r.Dir("Fotos").Dir("2004")
		y.File("a.jpg", 100, mtime2004).Content(bytes.Repeat([]byte("a"), 100))
		y.File("b.jpg", 200, mtime2004)
		v := r.Dir("Velho")
		v.File("x.txt", 10, mtime2004)
		v.Dir("sub").File("y.txt", 20, mtime2004)
		r.File("solto.txt", 30, mtime2004)
		r.Dir("Arquivo")
	})
	laterChanges(root)
	for _, p := range []string{"Fotos/2004/a.jpg", "Fotos/2004/b.jpg", "Velho", "solto.txt"} {
		e.decide(srcID, p, "discard")
	}
	return root
}

// planDir is the quarantine path of an action's plan folder.
func planDir(action int64) string { return q + "/" + strconv.FormatInt(action, 10) }

// r4 D1, D3: a cleanup plan makes the quarantine and its plan folder, moves
// each item into <plan>/<k>/ under its own name, keeping its ID, and writes
// the origin record <k>.json beside it. The quarantine is recorded as the
// source's.
func TestR4CleanupQuarantinesItems(t *testing.T) {
	e := newEnv(t)
	discards(e)
	a, velho, y := e.id("Fotos/2004/a.jpg"), e.id("Velho"), e.id("Velho/sub/y.txt")
	action := e.cleanupAction(srcID, "discard", "Fotos/2004/a.jpg", "Velho")
	e.run(action)
	e.wantStates(action, actionDone, stateDone, stateDone, stateDone, stateDone, stateDone, stateDone, stateDone,
		stateDone)
	plan := planDir(action)
	for p, id := range map[string]int64{plan + "/1/a.jpg": a, plan + "/2/Velho": velho, plan + "/2/Velho/sub/y.txt": y} {
		if got := e.id(p); got != id {
			t.Errorf("%s has ID %d, want %d", p, got, id)
		}
		if _, ok := e.lstat("/src", p); !ok {
			t.Errorf("%s is not on the disk", p)
		}
	}
	for _, p := range []string{"Fotos/2004/a.jpg", "Velho"} {
		if _, ok := e.lstat("/src", p); ok {
			t.Errorf("%s is still in place", p)
		}
	}
	rec, ok := parseRecord(e.readFile("/src", plan+"/1.json"))
	if !ok {
		t.Fatalf("the record is not one: %q", e.readFile("/src", plan+"/1.json"))
	}
	want := originRecord{Version: 1, SourceID: string(srcID), EntryID: strconv.FormatInt(a, 10),
		PlanID: strconv.FormatInt(action, 10), Original: originPath{Path: "Fotos/2004/a.jpg",
			PathB64: []byte("Fotos/2004/a.jpg")}, QuarantinedAt: rec.QuarantinedAt}
	if rec.QuarantinedAt == "" || rec.SourceID != want.SourceID || rec.EntryID != want.EntryID ||
		rec.PlanID != want.PlanID || rec.Original.Path != want.Original.Path ||
		!bytes.Equal(rec.Original.PathB64, want.Original.PathB64) {
		t.Errorf("record %+v, want %+v", rec, want)
	}
	if n := e.count(`SELECT count(*) FROM sources WHERE id = ? AND quarantine_entry_id = ?`, string(srcID),
		e.id(q)); n != 1 {
		t.Error("the quarantine is not recorded as the source's")
	}
	if n := e.rec.Count(instrument.OpCreate); n != 2 {
		t.Errorf("%d records created, want 2", n)
	}
}

// r4 D1: a plan drafted while the source had no quarantine finds, at run
// time, the quarantine another plan made: its mkdir is done with nothing
// made, and the plan folder goes into that quarantine.
func TestR4QuarantineMadeByAnotherPlan(t *testing.T) {
	e := newEnv(t)
	discards(e)
	first := e.cleanupAction(srcID, "discard", "Fotos/2004/a.jpg")
	second := e.cleanupAction(srcID, "discard", "Fotos/2004/b.jpg")
	e.run(first)
	mkdirs := e.rec.Count(instrument.OpMkdir)
	e.run(second)
	e.wantStates(second, actionDone, stateDone, stateDone, stateDone, stateDone, stateDone)
	if it := e.item(second, 1); it.Created || it.Entry != e.id(q) {
		t.Errorf("the second plan's quarantine mkdir is %+v, want done on %d without creating", it, e.id(q))
	}
	if n := e.rec.Count(instrument.OpMkdir) - mkdirs; n != 2 {
		t.Errorf("the second plan made %d folders, want its plan folder and item folder", n)
	}
	if _, ok := e.lstat("/src", planDir(second)+"/1/b.jpg"); !ok {
		t.Error("b.jpg is not in the quarantine")
	}
}

// R4.2 (spec cleanup, "A changed entry is skipped" and "A rescan between
// draft and run"): a planned file modified on disk after drafting ends
// changed, identity_changed, with its three steps and nothing made, also
// when a rescan saw the change before the run; the other item is
// quarantined.
func TestR4_2ChangedAfterDraft(t *testing.T) {
	for _, rescan := range []bool{false, true} {
		t.Run(map[bool]string{false: "on disk", true: "after a rescan"}[rescan], func(t *testing.T) {
			e := newEnv(t)
			root := discards(e)
			action := e.cleanupAction(srcID, "discard", "Fotos/2004/a.jpg", "Fotos/2004/b.jpg")
			root.Child("Fotos").Child("2004").Child("a.jpg").Content([]byte("edited after the draft"))
			if rescan {
				e.scan()
			}
			e.run(action)
			if got := e.cleanupStates(action); len(got) != 2 || got[0] != "changed/changed/changed" ||
				got[1] != "done/done/done" {
				t.Fatalf("items %v, want the first changed and the second done", got)
			}
			if r := e.renameOf(action, 1).Reason; r != reasonIdentityChanged {
				t.Errorf("reason %q, want identity_changed", r)
			}
			if _, ok := e.lstat("/src", "Fotos/2004/a.jpg"); !ok {
				t.Error("a.jpg moved")
			}
			if _, ok := e.lstat("/src", planDir(action)+"/1"); ok {
				t.Error("the changed item's folder was made")
			}
			if _, ok := e.lstat("/src", planDir(action)+"/2/b.jpg"); !ok {
				t.Error("b.jpg is not in the quarantine")
			}
		})
	}
}

// R4.1 at run time (spec cleanup, "A keep set after drafting"): a keep set
// below a planned folder ends that item blocked holds_kept, and a keep on
// a planned item itself ends it changed decision_changed, each with nothing
// made or moved; the plan's other item runs.
func TestR4_1KeepSetAfterDrafting(t *testing.T) {
	e := newEnv(t)
	discards(e)
	action := e.cleanupAction(srcID, "discard", "Velho", "Fotos/2004/a.jpg", "solto.txt")
	e.decide(srcID, "Velho/sub/y.txt", "keep")
	e.decide(srcID, "Fotos/2004/a.jpg", "keep")
	writes := e.writes()
	e.run(action)
	got := e.cleanupStates(action)
	if len(got) != 3 || got[0] != "blocked/blocked/blocked" || got[1] != "changed/changed/changed" ||
		got[2] != "done/done/done" {
		t.Fatalf("items %v, want blocked, changed, done", got)
	}
	if r := e.renameOf(action, 1).Reason; r != reasonHoldsKept {
		t.Errorf("Velho's reason %q, want holds_kept", r)
	}
	if r := e.renameOf(action, 2).Reason; r != reasonDecisionChanged {
		t.Errorf("a.jpg's reason %q, want decision_changed", r)
	}
	for _, p := range []string{"Velho/sub/y.txt", "Velho/x.txt", "Fotos/2004/a.jpg"} {
		if _, ok := e.lstat("/src", p); !ok {
			t.Errorf("%s moved", p)
		}
	}
	for _, k := range []string{"/1", "/2"} {
		if _, ok := e.lstat("/src", planDir(action)+k); ok {
			t.Errorf("item folder %s was made", k)
		}
	}
	// The quarantine, the plan folder, the third item's folder, its rename,
	// and its record.
	if n := e.writes() - writes; n != 5 {
		t.Errorf("%d writes, want 5", n)
	}
}

// curricula is a source holding Docs/curriculo.doc and Backup/curriculo.doc,
// identical and hashed, the first discarded.
func curricula(e *env) *synthfs.Node {
	data := bytes.Repeat([]byte("cv"), 600)
	root := e.disk("/src", posix, func(r *synthfs.Node) {
		r.Dir("Docs").File("curriculo.doc", 0, mtime2004).Content(data)
		r.Dir("Backup").File("curriculo.doc", 0, mtime2004).Content(data)
	})
	laterChanges(root)
	e.hashed(srcID, "Docs/curriculo.doc", data)
	e.hashed(srcID, "Backup/curriculo.doc", data)
	e.decide(srcID, "Docs/curriculo.doc", "discard")
	return root
}

// R4.4 at run time (r4 D5): a duplicate-ground item is quarantined after its
// staying copy was read in full and matched; when that copy changed on disk
// before the run, the item ends changed no_verified_copy and nothing moves.
func TestR4_4CopyVerifiedBeforeTheMove(t *testing.T) {
	t.Run("copy unchanged", func(t *testing.T) {
		e := newEnv(t)
		curricula(e)
		action := e.cleanupAction(srcID, groundDuplicate, "Docs/curriculo.doc")
		e.run(action)
		if got := e.cleanupStates(action); len(got) != 1 || got[0] != "done/done/done" {
			t.Fatalf("items %v, want done", got)
		}
		if n := e.rec.BytesRead(); n < 1200 {
			t.Errorf("%d bytes read, want the copy's 1200 in full", n)
		}
		if _, ok := e.lstat("/src", "Backup/curriculo.doc"); !ok {
			t.Error("the copy moved")
		}
	})
	t.Run("copy changed", func(t *testing.T) {
		e := newEnv(t)
		root := curricula(e)
		action := e.cleanupAction(srcID, groundDuplicate, "Docs/curriculo.doc")
		root.Child("Backup").Child("curriculo.doc").Patch(0, []byte("CV"))
		e.run(action)
		if got := e.cleanupStates(action); len(got) != 1 || got[0] != "changed/changed/changed" {
			t.Fatalf("items %v, want changed", got)
		}
		if r := e.renameOf(action, 1).Reason; r != reasonNoVerifiedCopy {
			t.Errorf("reason %q, want no_verified_copy", r)
		}
		if _, ok := e.lstat("/src", "Docs/curriculo.doc"); !ok {
			t.Error("the item moved")
		}
	})
}

// R4.4 (r4 D5, Concurrency): two duplicate-ground plans on two sources,
// each relying on the other's file, cannot both quarantine. The second runs
// while the first's rename is recorded intent and not yet done: it may not
// rely on a copy being quarantined, and ends no_verified_copy.
func TestR4_4TwoPlansOnTwoSources(t *testing.T) {
	data := bytes.Repeat([]byte("cv"), 600)
	e := newEnv(t)
	var (
		second   int64
		renameID int64
		ran      bool
		runErr   error
	)
	e.hooks = Hooks{BeforeStep: func(item int64) error {
		if item == renameID && !ran {
			ran = true
			runErr = e.attempt(e.jobOf(second))
		}
		return nil
	}}
	e.executor()
	e.disk("/src", posix, func(r *synthfs.Node) { r.Dir("Docs").File("curriculo.doc", 0, mtime2004).Content(data) })
	e.diskAs(otherID, "/other", posix, func(r *synthfs.Node) {
		r.Dir("Docs").File("curriculo.doc", 0, mtime2004).Content(data)
	})
	for _, src := range []domain.SourceID{srcID, otherID} {
		e.hashed(src, "Docs/curriculo.doc", data)
		e.decide(src, "Docs/curriculo.doc", "discard")
	}
	first := e.cleanupAction(srcID, groundDuplicate, "Docs/curriculo.doc")
	second = e.cleanupAction(otherID, groundDuplicate, "Docs/curriculo.doc")
	renameID = e.renameOf(first, 1).ID
	e.run(first)
	if !ran || runErr != nil {
		t.Fatalf("the second plan ran %v: %v", ran, runErr)
	}
	if got := e.cleanupStates(first); len(got) != 1 || got[0] != "done/done/done" {
		t.Errorf("first plan %v, want done", got)
	}
	if got := e.cleanupStates(second); len(got) != 1 || got[0] != "changed/changed/changed" {
		t.Errorf("second plan %v, want changed", got)
	}
	if r := e.renameOf(second, 1).Reason; r != reasonNoVerifiedCopy {
		t.Errorf("second plan's reason %q, want no_verified_copy", r)
	}
	if _, ok := e.lstat("/other", "Docs/curriculo.doc"); !ok {
		t.Error("the last copy outside the quarantine moved")
	}
}

// r4 D1, D13: a rename never gives an entry the reserved name at a source's
// top, a folder of that name is made only by a cleanup, and an organize
// move never goes into the quarantine.
func TestR4ReservedName(t *testing.T) {
	e := newEnv(t)
	discards(e)
	cleanup := e.cleanupAction(srcID, "discard", "solto.txt")
	e.run(cleanup)
	rename := e.action("rename", false, step{Op: opRename, From: "Arquivo", To: "", Name: q})
	e.run(rename)
	mkdir := e.action("create_folder", false, step{Op: opMkdir, To: "Velho", Name: q},
		step{Op: opMkdir, To: "", Name: q + "x"})
	move := e.action("move", false, step{Op: opRename, From: "Fotos/2004/b.jpg", To: planDir(cleanup), Name: "b.jpg"})
	e.run(mkdir)
	e.run(move)
	e.wantStates(rename, actionDone, stateRefused)
	e.wantStates(mkdir, actionDone, stateDone, stateDone)
	e.wantStates(move, actionDone, stateRefused)
	if r := e.item(rename, 1).Reason; r != reasonReservedName {
		t.Errorf("rename reason %q, want reserved_name", r)
	}
	if r := e.item(move, 1).Reason; r != reasonInQuarantine {
		t.Errorf("move reason %q, want in_quarantine", r)
	}
	root := e.action("create_folder", false, step{Op: opMkdir, To: "", Name: q})
	e.run(root)
	e.wantStates(root, actionDone, stateRefused)
	if r := e.item(root, 1).Reason; r != reasonReservedName {
		t.Errorf("mkdir reason %q, want reserved_name", r)
	}
	if _, ok := e.lstat("/src", "Arquivo"); !ok {
		t.Error("Arquivo was renamed")
	}
}

// Spec source-writes, "An origin record never replaces a file": a file
// that holds the record's name when the record is written ends the record
// conflict name_taken, the file unchanged; the entry stays in the
// quarantine, and its origin stays in the history.
func TestR4OriginRecordNeverReplacesAFile(t *testing.T) {
	e := newEnv(t)
	root := discards(e)
	var (
		action   int64
		recordID int64
	)
	e.hooks = Hooks{BeforeStep: func(item int64) error {
		if item == recordID {
			root.Child(q).Child(strconv.FormatInt(action, 10)).File("1.json", 0, mtime2004).Content([]byte("mine"))
		}
		return nil
	}}
	e.executor()
	action = e.cleanupAction(srcID, "discard", "Fotos/2004/a.jpg")
	recordID = e.item(action, 5).ID
	e.run(action)
	e.wantStates(action, actionDone, stateDone, stateDone, stateDone, stateDone, stateConflict)
	if r := e.item(action, 5).Reason; r != reasonNameTaken {
		t.Errorf("record reason %q, want name_taken", r)
	}
	if got := e.readFile("/src", planDir(action)+"/1.json"); string(got) != "mine" {
		t.Errorf("the file holds %q", got)
	}
	if _, ok := e.lstat("/src", planDir(action)+"/1/a.jpg"); !ok {
		t.Error("a.jpg is not in the quarantine")
	}
	var from []byte
	if err := e.st.Reader().QueryRow(`SELECT from_path FROM action_items WHERE action_id = ? AND seq = 4`, action).
		Scan(&from); err != nil || string(from) != "Fotos/2004/a.jpg" {
		t.Errorf("the history's origin %q, %v", from, err)
	}
}

// r4 D4: a record whose step was cut short is decided by its name. Absent,
// it is written once; present with the record's bytes, it is done without
// writing; present with other bytes, the item needs a check.
func TestR4RecordReconciled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after bool
		spoil bool
		want  string
	}{
		{"crash before the write", false, false, stateDone},
		{"crash after the write", true, false, stateDone},
		{"another file there", true, true, stateManualRecovery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			root := discards(e)
			var recordID int64
			crash := func(item int64) error {
				if item == recordID {
					recordID = -1
					return errCrash
				}
				return nil
			}
			if tc.after {
				e.hooks = Hooks{AfterStep: crash}
			} else {
				e.hooks = Hooks{BeforeStep: crash}
			}
			e.executor()
			action := e.cleanupAction(srcID, "discard", "Fotos/2004/a.jpg")
			recordID = e.item(action, 5).ID
			job := e.jobOf(action)
			if err := e.attempt(job); !errors.Is(err, errCrash) {
				t.Fatalf("attempt: %v, want the crash", err)
			}
			if tc.spoil {
				plan := root.Child(q).Child(strconv.FormatInt(action, 10))
				plan.Remove("1.json")
				plan.File("1.json", 0, mtime2004).Content([]byte("not the record"))
			}
			e.hooks = Hooks{}
			e.executor()
			e.run(action)
			if got := e.item(action, 5).State; got != tc.want {
				t.Fatalf("record %s, want %s", got, tc.want)
			}
			if n := e.rec.Count(instrument.OpCreate); n != 1 {
				t.Errorf("%d creates, want 1", n)
			}
			if !tc.spoil {
				if _, ok := parseRecord(e.readFile("/src", planDir(action)+"/1.json")); !ok {
					t.Error("the record is not one")
				}
			}
		})
	}
}

// r4 D2, D4 (tasks 2.4): a crash after the rename into the quarantine and
// before its outcome, with a new file at the old name, needs a check;
// resolved, the next scan indexes the entry at its quarantine path.
func TestR4CrashAfterRenameResolvedIsIndexed(t *testing.T) {
	e := newEnv(t)
	root := discards(e)
	var renameID int64
	e.hooks = Hooks{AfterStep: func(item int64) error {
		if item == renameID {
			renameID = -1
			return errCrash
		}
		return nil
	}}
	e.executor()
	action := e.cleanupAction(srcID, "discard", "Fotos/2004/a.jpg")
	renameID = e.renameOf(action, 1).ID
	if err := e.attempt(e.jobOf(action)); !errors.Is(err, errCrash) {
		t.Fatalf("attempt: %v, want the crash", err)
	}
	root.Child("Fotos").Child("2004").File("a.jpg", 5, mtime2004)
	e.hooks = Hooks{}
	e.executor()
	e.run(action)
	if got := e.renameOf(action, 1).State; got != stateManualRecovery {
		t.Fatalf("rename %s, want manual_recovery", got)
	}
	// resolve-recovery: the item is resolved, and a scan follows.
	e.exec(`UPDATE action_items SET state = 'resolved' WHERE state = 'manual_recovery'`)
	e.scan()
	quarantined := planDir(action) + "/1/a.jpg"
	if s := e.state(srcID, quarantined); s != "present" {
		t.Errorf("%s is %q in the index, want present", quarantined, s)
	}
	if s := e.state(srcID, "Fotos/2004/a.jpg"); s != "present" {
		t.Errorf("the new file is %q in the index, want present", s)
	}
}

// restoreAction inserts a queued restore of the quarantined entry at path
// (its item folder <plan>/<k>) back into the folder to, with the unlink of
// its record, the rmdir of its item folder, and, when sweep, the rmdir of
// the plan folder (r4 D6).
func (e *env) restoreAction(path, to string, sweep bool) int64 {
	e.t.Helper()
	parts := splitPath(path)
	planPath := parts[0] + "/" + parts[1]
	seqPath := planPath + "/" + parts[2]
	action := e.action("restore", false, step{Op: opRename, From: path, To: to, Name: parts[len(parts)-1]})
	// The record has no index row to name: the unlink names its folder.
	e.exec(`INSERT INTO action_items (action_id, seq, op, from_parent, from_name, state) VALUES (?, 2, 'unlink', ?, ?,
		'planned')`, action, e.id(planPath), []byte(parts[2]+".json"))
	rmdir := func(seq int, p string) {
		e.exec(`INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, from_name, from_path, state)
			SELECT ?, ?, 'rmdir', id, parent_id, name, path, 'planned' FROM entries WHERE source_id = ? AND path = ?`,
			action, seq, string(srcID), []byte(p))
	}
	rmdir(3, seqPath)
	if sweep {
		rmdir(4, planPath)
	}
	return action
}

// r4 D6 (R4.3 at the executor): a restore moves the entry back with its ID,
// unlinks its record, removes its item folder, and sweeps the emptied plan
// folder. A previous folder inside the quarantine is gone for a restore:
// that item ends conflict, its record stays with it, and its folder stays.
func TestR4RestoreReversesTheQuarantine(t *testing.T) {
	e := newEnv(t)
	discards(e)
	cleanup := e.cleanupAction(srcID, "discard", "Fotos/2004/a.jpg", "solto.txt")
	e.run(cleanup)
	plan := planDir(cleanup)
	a := e.id(plan + "/1/a.jpg")

	bad := e.restoreAction(plan+"/2/solto.txt", plan, false)
	e.run(bad)
	e.wantStates(bad, actionDone, stateConflict, stateNotAttempted, stateNotEmpty)
	if r := e.item(bad, 1).Reason; r != reasonPreviousFolderGone {
		t.Errorf("reason %q, want previous_folder_gone", r)
	}
	if _, ok := e.lstat("/src", plan+"/2.json"); !ok {
		t.Error("the record of an item still in the quarantine was removed")
	}

	restore := e.restoreAction(plan+"/1/a.jpg", "Fotos/2004", false)
	e.run(restore)
	e.wantStates(restore, actionDone, stateDone, stateDone, stateDone)
	if got := e.id("Fotos/2004/a.jpg"); got != a {
		t.Errorf("a.jpg is back with ID %d, want %d", got, a)
	}
	for _, p := range []string{plan + "/1.json", plan + "/1"} {
		if _, ok := e.lstat("/src", p); ok {
			t.Errorf("%s is still in the quarantine", p)
		}
	}
	e.idx.mu.Lock()
	unlinks := len(e.idx.unlinks)
	e.idx.mu.Unlock()
	if unlinks != 1 {
		t.Errorf("%d record rows dropped, want 1", unlinks)
	}

	last := e.restoreAction(plan+"/2/solto.txt", "", true)
	e.run(last)
	e.wantStates(last, actionDone, stateDone, stateDone, stateDone, stateDone)
	if _, ok := e.lstat("/src", plan); ok {
		t.Error("the emptied plan folder stays")
	}
	if _, ok := e.lstat("/src", "solto.txt"); !ok {
		t.Error("solto.txt is not back")
	}
}

// r4 D4: an unlink cut short is decided by its name: gone, it is done;
// still the record, it runs once.
func TestR4UnlinkReconciled(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			e := newEnv(t)
			discards(e)
			cleanup := e.cleanupAction(srcID, "discard", "Fotos/2004/a.jpg")
			e.run(cleanup)
			restore := e.restoreAction(planDir(cleanup)+"/1/a.jpg", "Fotos/2004", false)
			unlinkID := e.item(restore, 2).ID
			crash := func(item int64) error {
				if item == unlinkID {
					unlinkID = -1
					return errCrash
				}
				return nil
			}
			if after {
				e.hooks = Hooks{AfterStep: crash}
			} else {
				e.hooks = Hooks{BeforeStep: crash}
			}
			e.executor()
			if err := e.attempt(e.jobOf(restore)); !errors.Is(err, errCrash) {
				t.Fatalf("attempt: %v, want the crash", err)
			}
			e.hooks = Hooks{}
			e.executor()
			e.run(restore)
			e.wantStates(restore, actionDone, stateDone, stateDone, stateDone)
			if n := e.rec.Count(instrument.OpUnlink); n != 1 {
				t.Errorf("%d unlinks, want 1", n)
			}
		})
	}
}
