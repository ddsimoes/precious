package executor

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/content"
	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/jobs"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// R5.4 at run time (r5 task 2.9): set_mtime actions are driven by inserting
// queued rows, as plan-set-mtime, plan-undo, and run-action leave them, on
// the synthfs corpus with its photos hashed.

// ouroPreto is the corpus folder of R5.4's scenario: DSCN0001–0003 carry
// EXIF captures with an offset, precise to the second.
const ouroPreto = "Viagens/2008-03 Ouro Preto"

// dscn are DSCN0001–0003's paths.
var dscn = []string{ouroPreto + "/DSCN0001.JPG", ouroPreto + "/DSCN0002.JPG", ouroPreto + "/DSCN0003.JPG"}

// dated is the corpus as srcID, scanned, with DSCN0001–0003 hashed and their
// folder's media read.
type dated struct {
	*env
	root  *synthfs.Node
	truth map[string]corpus.Entry
}

// newDated returns an env holding the corpus at /corpus as srcID on a POSIX
// device, with copies of DSCN0001–0003 in "Copias Ouro Preto" so the hashing
// job read them (they share a size), scanned and hashed, and every file of
// the Ouro Preto folder with a read media_meta row of its identity. Each
// test starts from a copy of one database built so, and a synthfs built the
// same way, which gives every file the same identity.
func newDated(t *testing.T) *dated {
	t.Helper()
	db := datedTemplate(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, store.FileName), db, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sfs := synthfs.New()
	e := newEnvWith(t, st, sfs, t.TempDir())
	e.sfs = sfs
	d := buildDated(e)
	e.mount(d.root, posix)
	return d
}

// buildDated builds the corpus and the copies at /corpus.
func buildDated(e *env) *dated {
	e.t.Helper()
	root, gt := corpus.BuildSynth(e.sfs, "/corpus", corpus.Corpus())
	d := &dated{env: e, root: root, truth: map[string]corpus.Entry{}}
	for _, g := range gt.Entries {
		d.truth[g.Path] = g
	}
	copies := root.Dir("Copias Ouro Preto")
	for _, p := range dscn {
		info, ok := e.lstat("/corpus", p)
		if !ok {
			e.t.Fatalf("%s is not in the corpus", p)
		}
		copies.File(path.Base(p), 0, info.ModTime).Content(e.readFile("/corpus", p))
	}
	return d
}

var datedDB struct {
	sync.Mutex
	db []byte
}

// datedTemplate returns the bytes of a closed database holding the dated
// corpus, building it on first use.
func datedTemplate(t *testing.T) []byte {
	t.Helper()
	datedDB.Lock()
	defer datedDB.Unlock()
	if datedDB.db != nil {
		return datedDB.db
	}
	dir := storetest.Dir(t)
	st, err := store.Open(context.Background(), dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sfs := synthfs.New()
	e := newEnvWith(t, st, sfs, t.TempDir())
	e.sfs = sfs
	d := buildDated(e)
	e.addDisk("/corpus", d.root, posix)
	if n := e.hash(); n == 0 {
		t.Fatal("the hashing job read nothing")
	}
	for _, p := range dscn {
		if id, state := e.contentOf(p); id == 0 || state != "hashed" {
			t.Fatalf("%s is %q after hashing", p, state)
		}
	}
	e.exec(`INSERT INTO media_meta (entry_id, source_id, state, size, mtime_ns, ctime_ns, ino, make, model, serial, read_at)
		SELECT id, source_id, 'read', size, mtime_ns, ctime_ns, ino, 'NIKON', 'COOLPIX P5000', '3012345', 0
		FROM entries WHERE source_id = ? AND parent_id = ? AND kind = 'file'`, string(srcID), e.id(ouroPreto))
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, store.FileName+"-wal")); err == nil && fi.Size() > 0 {
		t.Fatal("the dated database kept a non-empty write-ahead log after Close")
	}
	db, err := os.ReadFile(filepath.Join(dir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	datedDB.db = db
	return db
}

// effective is the truth's effective date of a corpus photo.
func (d *dated) effective(p string) time.Time {
	d.t.Helper()
	g, ok := d.truth[p]
	if !ok || g.Date == nil || g.Date.Effective == nil || g.Date.Precision != "second" {
		d.t.Fatalf("%s has no date to the second in the truth", p)
	}
	return *g.Date.Effective
}

// steps are the set_mtime steps of DSCN0001–0003 to their effective dates.
func (d *dated) steps() []mtimeStep {
	out := make([]mtimeStep, len(dscn))
	for i, p := range dscn {
		out[i] = mtimeStep{Path: p, To: d.effective(p)}
	}
	return out
}

// nodeAt returns the synthfs node at the slash-separated path below root.
func nodeAt(root *synthfs.Node, p string) *synthfs.Node {
	n := root
	for _, c := range strings.Split(p, "/") {
		n = n.Child(c)
		if n == nil {
			panic("no node " + p)
		}
	}
	return n
}

// mtimeStep is one planned set_mtime: the file at Path to To, reversing the
// item Reverses (0 for none).
type mtimeStep struct {
	Path     string
	To       time.Time
	Reverses int64
}

// mtimeAction inserts a queued action of kind (set_mtime, or undo) with one
// planned set_mtime item per step, as plan-set-mtime or plan-undo, then
// run-action, leave it: the entry, its place, and the new time. It enqueues
// the action's job and returns the action ID.
func (e *env) mtimeAction(kind string, steps ...mtimeStep) int64 {
	e.t.Helper()
	var action int64
	err := e.r.Write(context.Background(), func(tx *jobs.Tx) error {
		q := tx.SQL()
		if err := q.QueryRow(`INSERT INTO actions (kind, source_id, state, bulk, created_at)
			VALUES (?, ?, 'queued', 1, 0) RETURNING id`, kind, string(srcID)).Scan(&action); err != nil {
			return err
		}
		for i, s := range steps {
			var reverses any
			if s.Reverses != 0 {
				reverses = s.Reverses
			}
			res, err := q.Exec(`INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, from_name, from_path,
				new_mtime_ns, reverses, state)
				SELECT ?, ?, 'set_mtime', id, parent_id, name, path, ?, ?, 'planned' FROM entries
				WHERE source_id = ? AND path = ?`, action, i+1, s.To.UnixNano(), reverses, string(srcID), []byte(s.Path))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return errors.New("no entry at " + s.Path)
			}
		}
		_, err := Enqueue(tx, srcID, action)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return action
}

// undoOf inserts the undo of a set_mtime action as plan-undo leaves it: its
// done items reversed, last first, each back to the time journaled before it
// was written.
func (e *env) undoOf(action int64) int64 {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT id, from_path, prev_mtime_ns FROM action_items
		WHERE action_id = ? AND state = 'done' ORDER BY seq DESC`, action)
	if err != nil {
		e.t.Fatal(err)
	}
	var steps []mtimeStep
	for rows.Next() {
		var (
			id   int64
			p    []byte
			prev sql.NullInt64
		)
		if err := rows.Scan(&id, &p, &prev); err != nil {
			e.t.Fatal(err)
		}
		if !prev.Valid {
			e.t.Fatalf("done item %d journaled no previous time", id)
		}
		steps = append(steps, mtimeStep{Path: string(p), To: time.Unix(0, prev.Int64), Reverses: id})
	}
	rows.Close()
	return e.mtimeAction("undo", steps...)
}

// facts are a file's identity as a table of the index holds it.
type facts struct{ Size, Mtime, Ctime, Ino sql.NullInt64 }

// factsOf returns the identity the table (entries, file_content, or
// media_meta) holds for the entry at path; ok is false without a row.
func (e *env) factsOf(table, p string) (facts, bool) {
	e.t.Helper()
	key := "entry_id"
	if table == "entries" {
		key = "id"
	}
	var f facts
	err := e.st.Reader().QueryRow(`SELECT t.size, t.mtime_ns, t.ctime_ns, t.ino FROM `+table+` t WHERE t.`+key+` =
		(SELECT id FROM entries WHERE source_id = ? AND path = ?)`, string(srcID), []byte(p)).
		Scan(&f.Size, &f.Mtime, &f.Ctime, &f.Ino)
	if errors.Is(err, sql.ErrNoRows) {
		return f, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return f, true
}

// contentOf returns the file_content row of path: its content and state.
func (e *env) contentOf(p string) (int64, string) {
	e.t.Helper()
	var (
		id    sql.NullInt64
		state string
	)
	err := e.st.Reader().QueryRow(`SELECT content_id, state FROM file_content WHERE entry_id =
		(SELECT id FROM entries WHERE source_id = ? AND path = ?)`, string(srcID), []byte(p)).Scan(&id, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return id.Int64, state
}

// hash runs a hashing job of srcID to its end on a runner of its own,
// started for it and stopped after, and returns the files it opened.
func (e *env) hash() int {
	e.t.Helper()
	cfg := config.Defaults()
	r, err := jobs.NewRunner(jobs.Options{Store: e.st, Registry: e.src, Config: cfg.Jobs, Clock: clock.Real{},
		Logger: discard(), TickInterval: 10 * time.Millisecond, ProgressInterval: 10 * time.Millisecond})
	if err != nil {
		e.t.Fatal(err)
	}
	content.NewService(e.st, e.src, clock.Real{}, cfg.Hashing, cfg.Archives, cfg.Duplicates).Register(r)
	opened := e.rec.Count(instrument.OpOpenFile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		e.t.Fatal(err)
	}
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		if err := r.Stop(sctx); err != nil {
			e.t.Errorf("stop the hashing runner: %v", err)
		}
	}()
	if err := r.Write(ctx, func(tx *jobs.Tx) error { return content.EnqueueHashing(ctx, tx, srcID) }); err != nil {
		e.t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for e.count(`SELECT count(*) FROM jobs WHERE kind = 'hash' AND state IN ('queued', 'running')`) > 0 {
		if time.Now().After(deadline) {
			e.t.Fatal("the hashing job did not end")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'hash' AND state <> 'succeeded'`); n != 0 {
		e.t.Fatalf("%d hashing jobs did not succeed", n)
	}
	return e.rec.Count(instrument.OpOpenFile) - opened
}

// since returns the calls the recorder logged after the first mark ones.
func (e *env) since(mark int) []instrument.Call {
	return e.rec.Calls()[mark:]
}

// setTimes returns the SetModTime calls among calls, by file name.
func setTimes(calls []instrument.Call) map[string][]time.Time {
	out := map[string][]time.Time{}
	for _, c := range calls {
		if c.Op == instrument.OpSetModTime {
			name := string(c.Path[len(c.Path)-1])
			out[name] = append(out[name], c.ModTime)
		}
	}
	return out
}

// writesOn reports the source's write permission.
func (e *env) writesOn() bool {
	return e.count(`SELECT write_enabled FROM sources WHERE id = ?`, string(srcID)) == 1
}

// prevOf returns the time an item journaled before its write.
func (e *env) prevOf(action int64, seq int) sql.NullInt64 {
	e.t.Helper()
	var prev sql.NullInt64
	if err := e.st.Reader().QueryRow(`SELECT prev_mtime_ns FROM action_items WHERE action_id = ? AND seq = ?`, action,
		seq).Scan(&prev); err != nil {
		e.t.Fatal(err)
	}
	return prev
}

// Spec source-writes, "R5.4 Setting a time changes nothing else", and
// organizing, "R5.4 Writing back a modification time changes no file
// content": one SetModTime per photo and no other Writer call or open; the
// photos keep their size, inode, and bytes (re-hashed with content.HashEntry
// against the truth's digest); the index shows the new times; file_content
// keeps its content and media_meta its row, carried to the new times; and a
// hashing job after reads nothing.
func TestR5_4SettingATimeChangesNothingElse(t *testing.T) {
	d := newDated(t)
	e := d.env
	before := map[string]fsaccess.EntryInfo{}
	contents := map[string]int64{}
	for _, p := range dscn {
		before[p], _ = e.lstat("/corpus", p)
		contents[p], _ = e.contentOf(p)
	}
	mark := len(e.rec.Calls())
	action := e.mtimeAction("set_mtime", d.steps()...)
	e.run(action)
	e.wantStates(action, actionDone, stateDone, stateDone, stateDone)

	calls := e.since(mark)
	set := setTimes(calls)
	for _, c := range calls {
		switch c.Op {
		case instrument.OpSetModTime:
		case instrument.OpRename, instrument.OpMkdir, instrument.OpRmdir, instrument.OpCreate, instrument.OpUnlink,
			instrument.OpSync, instrument.OpOpenFile, instrument.OpReadAt:
			t.Errorf("the action called %s on %s", c.Op, c.FullPath())
		}
	}
	root, err := e.sfs.OpenRoot("/corpus")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for i, p := range dscn {
		want := d.effective(p)
		if got := set[path.Base(p)]; len(got) != 1 || !got[0].Equal(want) {
			t.Errorf("%s: SetModTime calls %v, want one to %v", p, got, want)
		}
		after, _ := e.lstat("/corpus", p)
		was := before[p]
		if !after.ModTime.Equal(want) || after.Size != was.Size || after.Ino != was.Ino || after.Dev != was.Dev ||
			after.Nlink != 1 {
			t.Errorf("%s on disk: %v, size %d, inode %d; want %v, size %d, inode %d", p, after.ModTime, after.Size,
				after.Ino, want, was.Size, was.Ino)
		}
		if prev := e.prevOf(action, i+1); !prev.Valid || prev.Int64 != was.ModTime.UnixNano() {
			t.Errorf("%s journaled %v, want the time found on disk %v", p, prev, was.ModTime)
		}
		idx, _ := e.factsOf("entries", p)
		if idx.Mtime.Int64 != want.UnixNano() || idx.Ctime.Int64 != after.Ctime.UnixNano() {
			t.Errorf("%s indexed at %v (change %v), want %v (%v)", p, idx.Mtime, idx.Ctime, want, after.Ctime)
		}
		sum, _, err := content.HashEntry(context.Background(), root, content.Row{Path: []byte(p), Size: idx.Size.Int64,
			MtimeNs: idx.Mtime, CtimeNs: idx.Ctime, Ino: idx.Ino}, posix)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if got := hex.EncodeToString(sum[:]); got != d.truth[p].SHA256 {
			t.Errorf("%s re-hashed to %s, want %s", p, got, d.truth[p].SHA256)
		}
		if id, state := e.contentOf(p); id != contents[p] || state != "hashed" {
			t.Errorf("%s: file_content %d %q, want %d hashed", p, id, state, contents[p])
		}
		for _, table := range []string{"file_content", "media_meta"} {
			if f, ok := e.factsOf(table, p); !ok || f != idx {
				t.Errorf("%s: %s holds %+v (row %v), want the entry's %+v", p, table, f, ok, idx)
			}
		}
	}
	if !e.writesOn() {
		t.Error("the source's writes are off")
	}
	if n := e.hash(); n != 0 {
		t.Errorf("the hashing job after opened %d files, want none", n)
	}
	for _, p := range dscn {
		if id, state := e.contentOf(p); id != contents[p] || state != "hashed" {
			t.Errorf("%s after hashing again: %d %q, want %d hashed", p, id, state, contents[p])
		}
	}
}

// Spec organizing, "R5.4 Undo restores the previous time": the undo
// restores each journaled time, and each photo keeps its digest.
func TestR5_4UndoRestoresThePreviousTime(t *testing.T) {
	d := newDated(t)
	e := d.env
	before := map[string]fsaccess.EntryInfo{}
	contents := map[string]int64{}
	for _, p := range dscn {
		before[p], _ = e.lstat("/corpus", p)
		contents[p], _ = e.contentOf(p)
	}
	action := e.mtimeAction("set_mtime", d.steps()...)
	e.run(action)
	undo := e.undoOf(action)
	e.run(undo)
	e.wantStates(undo, actionDone, stateDone, stateDone, stateDone)
	for i, p := range dscn {
		after, _ := e.lstat("/corpus", p)
		if !after.ModTime.Equal(before[p].ModTime) {
			t.Errorf("%s after undo: %v, want %v", p, after.ModTime, before[p].ModTime)
		}
		if id, state := e.contentOf(p); id != contents[p] || state != "hashed" {
			t.Errorf("%s: file_content %d %q, want %d hashed", p, id, state, contents[p])
		}
		r := e.item(action, i+1)
		if want := e.item(undo, len(dscn)-i).ID; r.ReversedBy != want {
			t.Errorf("%s: reversed by %d, want %d", p, r.ReversedBy, want)
		}
	}
}

// fatCard is the corpus's FAT card as srcID: 2-second times, no stable
// identity.
func fatCard(e *env) *synthfs.Node {
	e.t.Helper()
	e.fsType = "vfat"
	root, _ := corpus.BuildSynth(e.sfs, "/card", corpus.FATFixture())
	e.addDisk("/card", root, fat)
	return root
}

// The card's photos.
const (
	img1201 = "DCIM/100CANON/IMG_1201.JPG"
	img1202 = "DCIM/100CANON/IMG_1202.JPG"
)

// Task 2.9: on a FAT device whose index time differs from the disk's
// within tolerance (a scan keeps such a time, r5 D3), the step journals the
// disk's time, and the undo restores it exactly, not the index's.
func TestR5_4UndoRestoresExactlyOnFAT(t *testing.T) {
	e := newEnv(t)
	fatCard(e)
	paths := []string{img1201, img1202}
	before := map[string]time.Time{}
	for _, p := range paths {
		info, _ := e.lstat("/card", p)
		before[p] = info.ModTime
		e.exec(`UPDATE entries SET mtime_ns = mtime_ns + ? WHERE source_id = ? AND path = ?`, int64(time.Second),
			string(srcID), []byte(p))
	}
	to := time.Date(2008, 10, 18, 12, 0, 0, 0, time.UTC)
	action := e.mtimeAction("set_mtime", mtimeStep{Path: img1201, To: to}, mtimeStep{Path: img1202, To: to.Add(2 * time.Second)})
	e.run(action)
	e.wantStates(action, actionDone, stateDone, stateDone)
	for i, p := range paths {
		if prev := e.prevOf(action, i+1); prev.Int64 != before[p].UnixNano() {
			t.Errorf("%s journaled %v, want the disk's %v, not the index's", p, time.Unix(0, prev.Int64).UTC(), before[p])
		}
	}
	undo := e.undoOf(action)
	e.run(undo)
	e.wantStates(undo, actionDone, stateDone, stateDone)
	for _, p := range paths {
		if after, _ := e.lstat("/card", p); !after.ModTime.Equal(before[p]) {
			t.Errorf("%s after undo: %v, want exactly %v", p, after.ModTime, before[p])
		}
	}
}

// Spec organizing, "R5.4 A file changed since is skipped": an undo after a
// hand edit ends that item changed (identity_changed) with nothing written,
// and the others are restored, whether or not a rescan saw the edit.
func TestR5_4AFileChangedSinceIsSkipped(t *testing.T) {
	for _, rescan := range []bool{false, true} {
		t.Run(map[bool]string{false: "not rescanned", true: "rescanned"}[rescan], func(t *testing.T) {
			d := newDated(t)
			e := d.env
			before := map[string]time.Time{}
			for _, p := range dscn {
				info, _ := e.lstat("/corpus", p)
				before[p] = info.ModTime
			}
			action := e.mtimeAction("set_mtime", d.steps()...)
			e.run(action)
			edited := time.Date(2012, 5, 1, 8, 0, 0, 0, time.UTC)
			nodeAt(d.root, dscn[1]).ModTime(edited)
			if rescan {
				e.scan()
			}
			mark := len(e.rec.Calls())
			undo := e.undoOf(action)
			e.run(undo)
			// The undo reverses the items last first: DSCN0002 is its second.
			e.wantStates(undo, actionDone, stateDone, stateChanged, stateDone)
			if r := e.item(undo, 2); r.Reason != reasonIdentityChanged {
				t.Errorf("reason %q, want identity_changed", r.Reason)
			}
			set := setTimes(e.since(mark))
			if got := set["DSCN0002.JPG"]; len(got) != 0 {
				t.Errorf("DSCN0002.JPG was set to %v", got)
			}
			for _, p := range dscn {
				want := before[p]
				if p == dscn[1] {
					want = edited
				}
				if after, _ := e.lstat("/corpus", p); !after.ModTime.Equal(want) {
					t.Errorf("%s: %v, want %v", p, after.ModTime, want)
				}
			}
		})
	}
}

// Spec source-writes, "A file changed before its time is set": a photo
// modified after planning, or edited in place with its size and
// modification time put back, ends changed with nothing written, its
// file_content and media_meta keep their old identity, and the action goes
// on.
func TestR5_4AFileChangedBeforeItsTimeIsSet(t *testing.T) {
	edits := map[string]func(n *synthfs.Node){
		"modified":                       func(n *synthfs.Node) { n.ModTime(time.Date(2012, 5, 1, 8, 0, 0, 0, time.UTC)) },
		"edited in place, time put back": func(n *synthfs.Node) { n.Patch(64, []byte("retouched")) },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			d := newDated(t)
			e := d.env
			p := dscn[0]
			was := map[string]facts{}
			for _, table := range []string{"entries", "file_content", "media_meta"} {
				was[table], _ = e.factsOf(table, p)
			}
			action := e.mtimeAction("set_mtime", d.steps()...)
			edit(nodeAt(d.root, p))
			if info, _ := e.lstat("/corpus", p); name != "modified" &&
				(info.Size != was["entries"].Size.Int64 || info.ModTime.UnixNano() != was["entries"].Mtime.Int64) {
				t.Fatal("the in-place edit changed the size or the time")
			}
			mark := len(e.rec.Calls())
			e.run(action)
			e.wantStates(action, actionDone, stateChanged, stateDone, stateDone)
			if r := e.item(action, 1); r.Reason != reasonIdentityChanged {
				t.Errorf("reason %q, want identity_changed", r.Reason)
			}
			if got := setTimes(e.since(mark))["DSCN0001.JPG"]; len(got) != 0 {
				t.Errorf("DSCN0001.JPG was set to %v", got)
			}
			for table, f := range was {
				if got, ok := e.factsOf(table, p); !ok || got != f {
					t.Errorf("%s of %s is %+v (row %v), want the old %+v", table, p, got, ok, f)
				}
			}
		})
	}
}

// Task 2.9: a hard link made after the scan is refused hard_link at the
// step, with nothing written, and the other items run.
func TestR5_4AHardLinkMadeAfterTheScan(t *testing.T) {
	d := newDated(t)
	e := d.env
	n := nodeAt(d.root, dscn[0])
	nodeAt(d.root, ouroPreto).HardLink("DSCN0001 (link).JPG", n)
	mark := len(e.rec.Calls())
	action := e.mtimeAction("set_mtime", d.steps()...)
	e.run(action)
	e.wantStates(action, actionDone, stateRefused, stateDone, stateDone)
	if r := e.item(action, 1); r.Reason != reasonHardLink {
		t.Errorf("reason %q, want hard_link", r.Reason)
	}
	set := setTimes(e.since(mark))
	if len(set["DSCN0001.JPG"]) != 0 || len(set["DSCN0001 (link).JPG"]) != 0 || len(set) != 2 {
		t.Errorf("SetModTime calls %v, want DSCN0002 and DSCN0003 only", set)
	}
	if prev := e.prevOf(action, 1); prev.Valid {
		t.Errorf("the refused item journaled %v", prev)
	}
}

// Spec source-writes, "A file the service does not own": the system's
// refusal ends that item failed (not_owner), the other items run, and the
// source's write permission stays on.
func TestR5_4AFileTheServiceDoesNotOwn(t *testing.T) {
	d := newDated(t)
	e := d.env
	nodeAt(d.root, dscn[1]).Foreign()
	action := e.mtimeAction("set_mtime", d.steps()...)
	e.run(action)
	e.wantStates(action, actionDone, stateDone, stateFailed, stateDone)
	if r := e.item(action, 2); r.Reason != reasonNotOwner || r.Detail == "" {
		t.Errorf("reason %q, detail %q, want not_owner with the system's message", r.Reason, r.Detail)
	}
	if !e.writesOn() {
		t.Error("the source's writes are off")
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE kind = ?`, auditSourceWritesSet); n != 0 {
		t.Errorf("%d write-permission audit events", n)
	}
	if f, _ := e.factsOf("entries", dscn[1]); f.Mtime.Int64 == d.effective(dscn[1]).UnixNano() {
		t.Error("the index shows the refused time")
	}
}

// restart is a new process over the same database after a crash: the
// runner gave the action's job up, Startup stops the action and enqueues
// the source's reconcile job, which runs.
func (e *env) restart(job domain.JobID) {
	e.t.Helper()
	e.hooks = Hooks{}
	e.executor()
	e.exec(`UPDATE jobs SET state = 'failed', terminal_code = 'attempts_exhausted' WHERE id = ?`, int64(job))
	if err := e.ex.Startup(context.Background(), e.r); err != nil {
		e.t.Fatal(err)
	}
	var reconcile int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM jobs WHERE kind = 'organize' AND state = 'queued'
		AND scope_key = 'organize-reconcile:disk'`).Scan(&reconcile); err != nil {
		e.t.Fatalf("Startup enqueued no reconcile job: %v", err)
	}
	if err := e.attempt(domain.JobID(reconcile)); err != nil {
		e.t.Fatal(err)
	}
}

// resume is a new process over the same database whose runner requeued the
// action's job, which runs again.
func (e *env) resume(action int64) {
	e.t.Helper()
	e.hooks = Hooks{}
	e.executor()
	e.exec(`UPDATE jobs SET state = 'queued' WHERE id = ?`, int64(e.jobOf(action)))
	if err := e.ex.Startup(context.Background(), e.r); err != nil {
		e.t.Fatal(err)
	}
	e.run(action)
}

// Spec source-writes, "Crash after setting the time, before recording it",
// and task 2.9's crash before journaling and between journaling and the
// call: the time is set exactly once, the item is done, and the index shows
// the new time with the content rows carried.
func TestR5_4Crashes(t *testing.T) {
	t.Run("after setting the time, before recording it", func(t *testing.T) {
		d := newDated(t)
		e := d.env
		e.hooks = Hooks{AfterStep: crashOnce()}
		e.executor()
		action := e.mtimeAction("set_mtime", d.steps()[:2]...)
		job := e.jobOf(action)
		if err := e.attempt(job); !errors.Is(err, errCrash) {
			t.Fatalf("first attempt: %v, want the simulated crash", err)
		}
		e.wantStates(action, actionRunning, stateIntent, statePlanned)
		e.restart(job)
		e.wantStates(action, actionStopped, stateDone, stateNotAttempted)
		wantSetOnce(t, d, dscn[0])
	})
	t.Run("before journaling", func(t *testing.T) {
		d := newDated(t)
		e := d.env
		e.exec(`CREATE TRIGGER crash_before_journal BEFORE UPDATE OF prev_mtime_ns ON action_items
			BEGIN SELECT RAISE(ABORT, 'the process died here'); END`)
		action := e.mtimeAction("set_mtime", d.steps()[:2]...)
		if err := e.attempt(e.jobOf(action)); err == nil {
			t.Fatal("first attempt ended without the crash")
		}
		e.wantStates(action, actionRunning, stateIntent, statePlanned)
		if prev := e.prevOf(action, 1); prev.Valid || e.rec.Count(instrument.OpSetModTime) != 0 {
			t.Fatalf("journaled %v, %d times set, before the crash", prev, e.rec.Count(instrument.OpSetModTime))
		}
		e.exec(`DROP TRIGGER crash_before_journal`)
		e.resume(action)
		e.wantStates(action, actionDone, stateDone, stateDone)
		wantSetOnce(t, d, dscn[0])
	})
	t.Run("between journaling and the call", func(t *testing.T) {
		d := newDated(t)
		e := d.env
		e.hooks = Hooks{BeforeStep: crashOnce()}
		e.executor()
		action := e.mtimeAction("set_mtime", d.steps()[:2]...)
		if err := e.attempt(e.jobOf(action)); !errors.Is(err, errCrash) {
			t.Fatalf("first attempt: %v, want the simulated crash", err)
		}
		e.wantStates(action, actionRunning, stateIntent, statePlanned)
		if prev := e.prevOf(action, 1); !prev.Valid || e.rec.Count(instrument.OpSetModTime) != 0 {
			t.Fatalf("journaled %v, %d times set, before the crash", prev, e.rec.Count(instrument.OpSetModTime))
		}
		e.resume(action)
		e.wantStates(action, actionDone, stateDone, stateDone)
		wantSetOnce(t, d, dscn[0])
	})
}

// wantSetOnce checks that p was set once, to its date, and that the index
// and its content rows follow.
func wantSetOnce(t *testing.T, d *dated, p string) {
	t.Helper()
	want := d.effective(p)
	if got := setTimes(d.rec.Calls())[path.Base(p)]; len(got) != 1 || !got[0].Equal(want) {
		t.Errorf("%s: SetModTime calls %v, want one to %v", p, got, want)
	}
	idx, _ := d.factsOf("entries", p)
	if idx.Mtime.Int64 != want.UnixNano() {
		t.Errorf("%s indexed at %v, want %v", p, time.Unix(0, idx.Mtime.Int64).UTC(), want)
	}
	for _, table := range []string{"file_content", "media_meta"} {
		if f, ok := d.factsOf(table, p); !ok || f != idx {
			t.Errorf("%s: %s holds %+v (row %v), want the entry's %+v", p, table, f, ok, idx)
		}
	}
}

// Spec source-writes, "A FAT card rounds the time": a time with odd
// seconds is planned rounded down to an even second, set, and recognized as
// done by the reconciliation after a crash before its outcome.
func TestR5_4AFATCardRoundsTheTime(t *testing.T) {
	e := newEnv(t)
	fatCard(e)
	captured := time.Date(2008, 10, 18, 12, 0, 3, 500_000_000, time.UTC)
	planned := captured.Truncate(fat.TimeResolution)
	if planned.Second() != 2 || planned.Nanosecond() != 0 {
		t.Fatalf("planned %v, want 12:00:02", planned)
	}
	e.hooks = Hooks{AfterStep: crashOnce()}
	e.executor()
	action := e.mtimeAction("set_mtime", mtimeStep{Path: img1201, To: planned})
	job := e.jobOf(action)
	if err := e.attempt(job); !errors.Is(err, errCrash) {
		t.Fatalf("first attempt: %v, want the simulated crash", err)
	}
	e.restart(job)
	e.wantStates(action, actionStopped, stateDone)
	if n := e.rec.Count(instrument.OpSetModTime); n != 1 {
		t.Errorf("%d times set, want 1", n)
	}
	if info, _ := e.lstat("/card", img1201); !info.ModTime.Equal(planned) {
		t.Errorf("on disk %v, want %v", info.ModTime, planned)
	}
	if f, _ := e.factsOf("entries", img1201); f.Mtime.Int64 != planned.UnixNano() {
		t.Errorf("indexed at %v, want %v", time.Unix(0, f.Mtime.Int64).UTC(), planned)
	}
}

// Spec organizing, "A rescan after writing back": the rescan finds the
// photos unchanged, so they keep their digests and media metadata.
func TestR5_4ARescanAfterWritingBack(t *testing.T) {
	d := newDated(t)
	e := d.env
	action := e.mtimeAction("set_mtime", d.steps()...)
	e.run(action)
	written := map[string]facts{}
	contents := map[string]int64{}
	for _, p := range dscn {
		written[p], _ = e.factsOf("entries", p)
		contents[p], _ = e.contentOf(p)
	}
	e.scan()
	for _, p := range dscn {
		if f, _ := e.factsOf("entries", p); f != written[p] {
			t.Errorf("%s rescanned as %+v, want %+v", p, f, written[p])
		}
		if id, state := e.contentOf(p); id != contents[p] || state != "hashed" {
			t.Errorf("%s: file_content %d %q, want %d hashed", p, id, state, contents[p])
		}
		if f, ok := e.factsOf("media_meta", p); !ok || f != written[p] {
			t.Errorf("%s: media_meta %+v (row %v), want kept at %+v", p, f, ok, written[p])
		}
	}
	if n := e.hash(); n != 0 {
		t.Errorf("the hashing job after the rescan opened %d files", n)
	}
}
