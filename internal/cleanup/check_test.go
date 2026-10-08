package cleanup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"precious/internal/cleanup/stale"
	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/jobs"
)

// mtime is the modification time of the small fixtures' files.
var mtime = time.Date(2009, 5, 1, 10, 0, 0, 0, time.UTC)

// verdictTotals is one verdict's files and bytes.
type verdictTotals struct{ files, bytes int64 }

// totals reads the check's files and bytes by verdict.
func (e *checkEnv) totals(check int64) map[string]verdictTotals {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT verdict, count(*), sum(size) FROM purge_check_files WHERE check_id = ?
		GROUP BY verdict`, check)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]verdictTotals{}
	for rows.Next() {
		var v string
		var t verdictTotals
		if err := rows.Scan(&v, &t.files, &t.bytes); err != nil {
			e.t.Fatal(err)
		}
		out[v] = t
	}
	return out
}

// contentRows is every file_content row, which a check never writes.
func (e *checkEnv) contentRows() string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT entry_id, state, coalesce(content_id, 0), coalesce(checked_at, 0),
		coalesce(mtime_ns, 0), coalesce(ctime_ns, 0) FROM file_content ORDER BY entry_id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, content, checked, mt, ct int64
		var state string
		if err := rows.Scan(&id, &state, &content, &checked, &mt, &ct); err != nil {
			e.t.Fatal(err)
		}
		fmt.Fprintf(&b, "%d %s %d %d %d %d\n", id, state, content, checked, mt, ct)
	}
	return b.String()
}

// R4.6, the scenario "A purge set with copies and unique files", on the
// corpus: a set of a duplicate (curriculo (1).doc, whose twins stay outside
// quarantine), a folder holding a unique photo and a zip whose members have
// no copy outside the set, an empty folder, a folder holding a symlink,
// system junk with a copy, and a temporary file with none. The check, run
// by the runner:
//   - reads every file in full exactly once, and the zip once, through the
//     instrument, and writes nothing to the disk nor to file_content;
//   - records every entry of the set, of every kind, with its lstat, and
//     every file member of the zip, with the digests of the ground truth;
//   - gives the duplicate safe with its twin named and its identity, the
//     photo and the zip's members unique, the symlink, folders, empty files,
//     and the zip itself no_content, and the exact files and bytes of each
//     verdict, as the ground truth computes them;
//   - ranks the photo possibly_valuable and the temporary file likely_junk;
//   - ends ready, with its progress at of_files and of_bytes.
func TestR4_6CheckOfASetWithCopiesAndUniqueFiles(t *testing.T) {
	e := newCheckEnv(t)
	root, truth := corpus.BuildSynth(e.sfs, "/casa", corpus.Corpus())
	e.add("casa", "/casa", root)

	// A Word temporary file in the old backup's profile, which has no copy.
	var temp string
	for _, en := range truth.Entries {
		if p := mustB64(t, en.PathB64); strings.HasSuffix(p, "/Temp/~WRL0003.tmp") {
			temp = p
		}
	}
	set := []string{"Documentos/curriculo (1).doc", "Documentos/Nova pasta (3)", "Fotos/2006/Praia/Thumbs.db", "Midia",
		"Projetos/app_react/node_modules/.bin", temp}
	items := map[string]int64{}
	for _, p := range set {
		items[p] = e.id("casa", p)
	}
	quarantined := e.quarantine("casa", set...)
	// qpath is where an entry of the set now is; ok is false outside it.
	qpath := func(p string) (string, string, bool) {
		for i, s := range set {
			if p == s || strings.HasPrefix(p, s+"/") {
				return quarantined[i] + p[len(s):], s, true
			}
		}
		return "", "", false
	}
	// outside reports whether content sha has a copy outside the set: a
	// file, or a member of an archive, that is not in it.
	copiesOf := map[string][]string{}
	for _, d := range truth.Duplicates {
		for _, c := range d.Copies {
			raw := mustB64(t, c.PathB64)
			copiesOf[d.SHA256] = append(copiesOf[d.SHA256], raw)
		}
	}
	outside := func(sha string) []string {
		var out []string
		for _, c := range copiesOf[sha] {
			file, _, _ := strings.Cut(c, "!")
			if _, _, in := qpath(file); !in {
				out = append(out, c)
			}
		}
		return out
	}

	before := e.contentRows()
	e.rec.Reset()
	check := e.newCheck("casa", true, quarantined...)
	e.idle()
	opens, reads := e.opened("casa")
	if w := e.writes(); w != 0 {
		t.Errorf("the check made %d changes on a disk", w)
	}
	if after := e.contentRows(); after != before {
		t.Errorf("the check changed file_content:\n before %s\n after  %s", before, after)
	}
	if c := e.check(check); c.state != checkReady || !c.finished {
		t.Fatalf("the check ended %+v; want ready", c)
	}
	var jobState, progress string
	if err := e.st.Reader().QueryRow(`SELECT state, progress FROM jobs WHERE kind = ?`, string(KindPurgeCheck)).
		Scan(&jobState, &progress); err != nil {
		t.Fatal(err)
	}
	if jobState != string(domain.JobSucceeded) {
		t.Errorf("the job ended %s", jobState)
	}

	files := e.files(check)
	recs := recordsByPath(files)
	want := map[string]verdictTotals{}
	var readFiles, readBytes int64
	entries := 0
	for _, en := range truth.Entries {
		raw := mustB64(t, en.PathB64)
		q, item, in := qpath(raw)
		if !in {
			continue
		}
		entries++
		r, ok := recs[q]
		if !ok {
			t.Errorf("%q: not recorded", raw)
			continue
		}
		if r.item != items[item] || r.entry != e.id("casa", q) || r.kind != string(en.Kind) {
			t.Errorf("%q: recorded item %d entry %d kind %s; want item %d entry %d kind %s", raw, r.item, r.entry, r.kind,
				items[item], e.id("casa", q), en.Kind)
		}
		sameRecordIdentity(t, raw, r, e.lstat("casa", q))
		verdict, size := verdictNoContent, int64(0)
		if en.Size != nil {
			size = *en.Size
		}
		switch {
		case en.Kind != domain.EntryFile || size == 0:
			if opens[q] != 0 {
				t.Errorf("%q: opened %d times; want never", raw, opens[q])
			}
		case raw == "Midia/videos.zip":
			readFiles, readBytes = readFiles+1, readBytes+size
			if opens[q] != 1 {
				t.Errorf("%q: opened %d times; want once", raw, opens[q])
			}
		default:
			readFiles, readBytes = readFiles+1, readBytes+size
			if opens[q] != 1 || reads[q] != size {
				t.Errorf("%q: opened %d times, %d bytes read; want once, %d", raw, opens[q], reads[q], size)
			}
			if got := hex.EncodeToString(r.sha); got != en.SHA256 {
				t.Errorf("%q: digest %s; want %s", raw, got, en.SHA256)
			}
			verdict = verdictUnique
			if len(outside(en.SHA256)) > 0 {
				verdict = verdictSafe
			}
		}
		if en.Kind == domain.EntryFile && r.size != size {
			t.Errorf("%q: size %d; want %d", raw, r.size, size)
		}
		if r.verdict != verdict || (r.class != "") != (verdict == verdictUnique) {
			t.Errorf("%q: verdict %s class %q; want %s", raw, r.verdict, r.class, verdict)
		}
		w := want[verdict]
		w.files++
		w.bytes += r.size // a folder's or link's own size, as recorded
		want[verdict] = w
	}
	if len(recs) != entries {
		t.Errorf("%d entries recorded; want the %d of the set", len(recs), entries)
	}

	// The zip's file members, each once, with the ground truth's digest.
	zip, _, _ := qpath("Midia/videos.zip")
	members := 0
	for _, a := range truth.Members {
		if mustB64(t, a.PathB64) != "Midia/videos.zip" {
			continue
		}
		for _, m := range a.Members {
			if m.Kind != string(domain.EntryFile) {
				continue
			}
			members++
			mpath := mustB64(t, m.PathB64)
			var id int64
			if err := e.st.Reader().QueryRow(`SELECT id FROM archive_members WHERE archive_id = ? AND path = ?`,
				e.id("casa", zip), []byte(mpath)).Scan(&id); err != nil {
				t.Fatal(err)
			}
			var r *checkFile
			for i := range files {
				if files[i].member == id {
					r = &files[i]
				}
			}
			if r == nil {
				t.Errorf("member %q: not recorded", mpath)
				continue
			}
			verdict := verdictUnique
			if len(outside(m.SHA256)) > 0 {
				verdict = verdictSafe
			}
			if r.path != zip || r.entry != e.id("casa", zip) || r.size != *m.Size || hex.EncodeToString(r.sha) != m.SHA256 ||
				r.verdict != verdict || r.mtime.Valid {
				t.Errorf("member %q: recorded %+v; want at %q, %d bytes, %s, %s, no disk identity", mpath, *r, zip, *m.Size,
					m.SHA256, verdict)
			}
			w := want[verdict]
			w.files++
			w.bytes += *m.Size
			want[verdict] = w
		}
	}
	if len(files) != entries+members {
		t.Errorf("%d records; want %d entries and %d members", len(files), entries, members)
	}
	if members != 2 || want[verdictUnique].files < 4 || want[verdictSafe].files < 1 {
		t.Fatalf("the fixture lost its point: %d members, %+v", members, want)
	}
	if got := e.totals(check); !mapsEqual(got, want) {
		t.Errorf("totals by verdict %+v; want %+v", got, want)
	}

	// The duplicate names a twin outside quarantine, read and verified.
	dup, _, _ := qpath("Documentos/curriculo (1).doc")
	r := recs[dup]
	twins := outside(truth.Entries[indexOf(t, truth, "Documentos/curriculo (1).doc")].SHA256)
	if r.verdict != verdictSafe || r.copySource != "casa" || !contains(twins, r.copyPath) || r.copyMember.Valid ||
		r.hardLink {
		t.Fatalf("the duplicate: %+v; want safe with one of %q", r, twins)
	}
	twin := e.lstat("casa", r.copyPath)
	ti := identityOf(twin)
	if r.copyEntry.Int64 != e.id("casa", r.copyPath) || r.copySize.Int64 != twin.Size || r.copyMtime != ti.mtime ||
		r.copyCtime != ti.ctime || r.copyIno != ti.ino || r.copyDev != ti.dev {
		t.Errorf("the twin's identity: %+v; want entry %d, %+v", r, e.id("casa", r.copyPath), ti)
	}
	if opens[r.copyPath] != 1 {
		t.Errorf("the twin was opened %d times; want once", opens[r.copyPath])
	}
	for p, class := range map[string]string{"Midia/foto.jpg": classValuable, temp: classJunk} {
		q, _, _ := qpath(p)
		if got := recs[q]; got.verdict != verdictUnique || got.class != class {
			t.Errorf("%q: %s %s; want unique %s", p, got.verdict, got.class, class)
		}
	}
	link, _, _ := qpath("Projetos/app_react/node_modules/.bin/react-scripts")
	empty, _, _ := qpath("Documentos/Nova pasta (3)")
	for _, q := range []string{link, empty, zip} {
		if got := recs[q]; got.verdict != verdictNoContent || got.sha != nil {
			t.Errorf("%q: %s; want no_content", q, got.verdict)
		}
	}
	if n := e.count(`SELECT count(*) FROM purge_check_items WHERE check_id = ? AND readable = 1`, check); n != int64(len(set)) {
		t.Errorf("%d readable items; want %d", n, len(set))
	}

	var p map[string]int64
	if err := json.Unmarshal([]byte(progress), &p); err != nil {
		t.Fatal(err)
	}
	if p["files"] != readFiles || p["of_files"] != readFiles || p["bytes"] != readBytes || p["of_bytes"] != readBytes {
		t.Errorf("progress %v; want %d files and %d bytes of as many", p, readFiles, readBytes)
	}
}

// The scenario "A copy on an offline disk": a quarantined file whose only
// copy is on a source that is offline during the check is copy_offline,
// not safe, and so is an archive Precious does not open whose only copy is
// there; nothing on that disk is read. An unopened archive with no copy is
// opaque_archive and uncertain. Once the disk is back, a new check reads
// the copies and verifies them: the file is safe, and the archive stays
// opaque_archive, with its copy named.
func TestCheckCopyOnAnOfflineDisk(t *testing.T) {
	e := newCheckEnv(t)
	carta := []byte("Querida avó, as fotos do Natal seguem em anexo.\n")
	backup := bytes.Repeat([]byte("7z-backup "), 4000)
	solo := bytes.Repeat([]byte("7z-solo "), 3000)
	e.disk("casa", "/casa", func(r *synthfs.Node) {
		d := r.Dir("Cartas")
		d.File("carta.txt", 0, mtime).Content(carta)
		d.File("backup.7z", 0, mtime).Content(backup)
		d.File("solo.7z", 0, mtime).Content(solo)
	})
	usb := e.disk("usb", "/usb", func(r *synthfs.Node) {
		d := r.Dir("Copia")
		d.File("carta.txt", 0, mtime).Content(carta)
		d.File("backup.7z", 0, mtime).Content(backup)
	})
	e.scan("casa") // hashes casa's files, now in size groups with usb's
	if n := e.count(`SELECT count(*) FROM file_content WHERE content_id IS NOT NULL`); n != 4 {
		t.Fatalf("%d files hashed; want both copies of both files", n)
	}
	q := e.quarantine("casa", "Cartas/carta.txt", "Cartas/backup.7z", "Cartas/solo.7z")
	dev := usb.Info().Dev
	e.sfs.Unmount(dev)

	e.rec.Reset()
	check := e.newCheck("casa", false, q...)
	if err := e.run(context.Background(), check, "casa", 0, &checkRuntime{}); err != nil {
		t.Fatal(err)
	}
	if c := e.check(check); c.state != checkReady {
		t.Fatalf("the check ended %+v; want ready", c)
	}
	recs := recordsByPath(e.files(check))
	for path, want := range map[string]string{q[0]: verdictCopyOffline, q[1]: verdictCopyOffline, q[2]: verdictOpaque} {
		if got := recs[path]; got.verdict != want || got.copySource != "" || got.copyPath != "" {
			t.Errorf("%q: %s with copy %q:%q; want %s and no copy", path, got.verdict, got.copySource, got.copyPath, want)
		}
	}
	if got := recs[q[2]].class; got != classUncertain {
		t.Errorf("the unopened archive with no copy is %q; want uncertain", got)
	}
	if opens, _ := e.opened("usb"); len(opens) != 0 {
		t.Errorf("the offline disk was read: %v", opens)
	}

	e.sfs.Mount(dev, "/usb")
	again := e.newCheck("casa", false, q...)
	if err := e.run(context.Background(), again, "casa", 0, &checkRuntime{}); err != nil {
		t.Fatal(err)
	}
	recs = recordsByPath(e.files(again))
	for path, want := range map[string][2]string{
		q[0]: {verdictSafe, "Copia/carta.txt"}, q[1]: {verdictOpaque, "Copia/backup.7z"}, q[2]: {verdictOpaque, ""},
	} {
		got := recs[path]
		wantSource := "usb"
		if want[1] == "" {
			wantSource = ""
		}
		if got.verdict != want[0] || got.copyPath != want[1] || got.copySource != wantSource {
			t.Errorf("%q back online: %s with copy %q:%q; want %s with %q", path, got.verdict, got.copySource,
				got.copyPath, want[0], want[1])
		}
	}
	if c := recs[q[0]]; c.copySize.Int64 != int64(len(carta)) || c.copyIno.Int64 != int64(e.lstat("usb", "Copia/carta.txt").Ino) {
		t.Errorf("the copy's identity: %+v", c)
	}
}

// An item holding a folder the scan could not read is unreadable: its
// purge_check_items row is not readable, every entry of it is recorded
// unreadable with the index's identity, and nothing in it is read. The
// other items are checked as usual, and the check ends ready.
func TestCheckItemHoldingAnUnreadableFolder(t *testing.T) {
	e := newCheckEnv(t)
	e.disk("casa", "/casa", func(r *synthfs.Node) {
		v := r.Dir("Velho")
		v.File("a.txt", 0, mtime).Content([]byte("a velha carta"))
		p := v.Dir("privado")
		p.File("diario.txt", 0, mtime).Content([]byte("querido diário"))
		p.Unreadable()
		r.Dir("Novo").File("b.txt", 0, mtime).Content([]byte("um bilhete novo"))
	})
	q := e.quarantine("casa", "Velho", "Novo/b.txt")
	e.rec.Reset()
	check := e.newCheck("casa", false, q...)
	rt := &checkRuntime{}
	if err := e.run(context.Background(), check, "casa", 0, rt); err != nil {
		t.Fatal(err)
	}
	if c := e.check(check); c.state != checkReady {
		t.Fatalf("the check ended %+v; want ready", c)
	}
	for path, want := range map[string]int64{q[0]: 0, q[1]: 1} {
		if got := e.count(`SELECT readable FROM purge_check_items WHERE check_id = ? AND path = ?`, check,
			[]byte(path)); got != want {
			t.Errorf("%q: readable %d; want %d", path, got, want)
		}
	}
	recs := recordsByPath(e.files(check))
	for _, p := range []string{q[0], q[0] + "/a.txt", q[0] + "/privado"} {
		r, ok := recs[p]
		if !ok || r.verdict != verdictUnreadable || r.sha != nil || r.copyPath != "" {
			t.Errorf("%q: %+v (%v); want unreadable", p, r, ok)
			continue
		}
		var ino sql.NullInt64
		if err := e.st.Reader().QueryRow(`SELECT ino FROM entries WHERE id = ?`, r.entry).Scan(&ino); err != nil {
			t.Fatal(err)
		}
		if r.ino != ino {
			t.Errorf("%q: inode %v; want the index's %v", p, r.ino, ino)
		}
	}
	if len(recs) != 4 || recs[q[1]].verdict != verdictUnique {
		t.Errorf("records %v; want the unreadable item's three and the unique file", recs)
	}
	opens, _ := e.opened("casa")
	for p := range opens {
		if strings.HasPrefix(p, q[0]+"/") {
			t.Errorf("%q in the unreadable item was opened", p)
		}
	}
	if rt.progress["of_files"] != 1 || rt.progress["files"] != 1 {
		t.Errorf("progress %v; want the one readable file", rt.progress)
	}
}

// A check marked stale while it runs (a decision or a move on what it
// read, through MarkStale), or whose item is restored before it ends,
// ends stale, not ready (I9): a late result never wins.
func TestCheckStaleWhileRunning(t *testing.T) {
	setup := func(t *testing.T) (*checkEnv, []string) {
		e := newCheckEnv(t)
		e.disk("casa", "/casa", func(r *synthfs.Node) {
			r.Dir("A").File("a.txt", 0, mtime).Content([]byte("primeiro"))
			r.Dir("B").File("b.txt", 0, mtime).Content([]byte("segundo"))
		})
		return e, e.quarantine("casa", "A", "B")
	}
	write := func(e *checkEnv, fn func(tx *sql.Tx) error) {
		e.t.Helper()
		if err := e.st.Write(context.Background(), fn); err != nil {
			e.t.Fatal(err)
		}
	}

	t.Run("marked stale", func(t *testing.T) {
		e, q := setup(t)
		check := e.newCheck("casa", false, q...)
		rt := &checkRuntime{onYield: func(n int) {
			if n == 2 { // reading the second item, the first one recorded
				write(e, func(tx *sql.Tx) error { return stale.MarkStale(context.Background(), tx, "casa", []byte(q[0])) })
			}
		}}
		if err := e.run(context.Background(), check, "casa", 0, rt); err != nil {
			t.Fatal(err)
		}
		if c := e.check(check); c.state != checkStale || c.reason != stale.ReasonIndexChanged || !c.finished {
			t.Errorf("the check ended %+v; want stale", c)
		}
	})

	t.Run("item restored", func(t *testing.T) {
		e, q := setup(t)
		check := e.newCheck("casa", false, q...)
		rt := &checkRuntime{onYield: func(n int) {
			if n == 2 { // the first item is recorded: it goes back home, with no MarkStale
				e.move("casa", q[0], "")
			}
		}}
		if err := e.run(context.Background(), check, "casa", 0, rt); err != nil {
			t.Fatal(err)
		}
		if c := e.check(check); c.state != checkStale || c.reason != stale.ReasonIndexChanged || !c.finished {
			t.Errorf("the check ended %+v; want stale", c)
		}
		if e.has("casa", q[0]) || !e.has("casa", "A/a.txt") {
			t.Fatal("the item did not go back")
		}
	})

	t.Run("item moved before the start", func(t *testing.T) {
		e, q := setup(t)
		check := e.newCheck("casa", false, q...)
		e.move("casa", q[1], "")
		e.rec.Reset()
		if err := e.run(context.Background(), check, "casa", 0, &checkRuntime{}); err != nil {
			t.Fatal(err)
		}
		if c := e.check(check); c.state != checkStale || !c.finished {
			t.Errorf("the check ended %+v; want stale", c)
		}
		if opens, _ := e.opened("casa"); len(opens) != 0 {
			t.Errorf("a stale check read %v", opens)
		}
	})
}

// The job's life: it defers while an organize job of its source is queued;
// a cancel request fails the check; a check already stale only records its
// end; settleChecks fails a running check whose job ended without it; and
// a new attempt starts over, without the records of the last one.
func TestCheckJobLifecycle(t *testing.T) {
	e := newCheckEnv(t)
	e.disk("casa", "/casa", func(r *synthfs.Node) {
		r.Dir("A").File("a.txt", 0, mtime).Content([]byte("primeiro"))
		r.Dir("B").File("b.txt", 0, mtime).Content([]byte("segundo"))
	})
	q := e.quarantine("casa", "A", "B")
	ctx := context.Background()

	// Deferred while organizing.
	check := e.newCheck("casa", false, q...)
	var organize int64
	// Queued for later, so the runner, which has no organize handler here,
	// never claims it.
	if err := e.st.Writer().QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
		max_attempts, available_at, created_at, updated_at) VALUES ('organize', 1, '{}', 'casa', 'queued', 0, 3, ?, 0, 0)
		RETURNING id`, time.Now().Add(time.Hour).UnixMilli()).Scan(&organize); err != nil {
		t.Fatal(err)
	}
	err := e.run(ctx, check, "casa", 0, &checkRuntime{})
	var d *jobs.Defer
	if !errors.As(err, &d) || d.Reason != index.DeferOrganizing {
		t.Fatalf("the check while organizing: %v; want a deferral", err)
	}
	if c := e.check(check); c.state != checkRunning || e.count(`SELECT count(*) FROM purge_check_files`) != 0 {
		t.Fatalf("a deferred check is %+v", c)
	}
	e.exec(`UPDATE jobs SET state = 'cancelled' WHERE id = ?`, organize)

	// An attempt starts over.
	if err := e.run(ctx, check, "casa", 0, &checkRuntime{}); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE purge_checks SET state = 'running', finished_at = NULL WHERE id = ?`, check)
	if err := e.run(ctx, check, "casa", 0, &checkRuntime{}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM purge_check_files WHERE check_id = ?`, check); n != 4 {
		t.Errorf("%d records after a second attempt; want 4", n)
	}

	// A cancel request fails it.
	cancelled := e.newCheck("casa", false, q...)
	var job int64
	if err := e.st.Writer().QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
		max_attempts, available_at, created_at, updated_at, cancel_requested) VALUES ('purge_check_direct', 1, '{}',
		'casa', 'running', 1, 3, 0, 0, 0, 1) RETURNING id`).Scan(&job); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	err = e.run(cctx, cancelled, "casa", job, &checkRuntime{onYield: func(int) { cancel() }})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled check: %v", err)
	}
	if c := e.check(cancelled); c.state != checkFailed || !c.finished {
		t.Errorf("a cancelled check is %+v; want failed", c)
	}
	e.exec(`UPDATE jobs SET state = 'cancelled' WHERE id = ?`, job)

	// A stop without a cancel request leaves it running for its next attempt.
	stopped := e.newCheck("casa", false, q...)
	sctx, stop := context.WithCancel(ctx)
	if err := e.run(sctx, stopped, "casa", 0, &checkRuntime{onYield: func(int) { stop() }}); !errors.Is(err, context.Canceled) {
		t.Errorf("a stopped check: %v", err)
	}
	if c := e.check(stopped); c.state != checkRunning {
		t.Errorf("a stopped check is %+v; want running", c)
	}

	// Already stale: only its end is recorded.
	e.exec(`UPDATE purge_checks SET state = 'stale', stale_reason = 'index_changed' WHERE id = ?`, stopped)
	if err := e.run(ctx, stopped, "casa", 0, &checkRuntime{}); err != nil {
		t.Fatal(err)
	}
	if c := e.check(stopped); c.state != checkStale || !c.finished {
		t.Errorf("a stale check is %+v", c)
	}

	// A check whose job was cancelled before it ran is settled as failed;
	// one whose job is queued is left alone.
	orphan := e.newCheck("casa", false, q...)
	var dead int64
	if err := e.st.Writer().QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
		max_attempts, available_at, created_at, updated_at) VALUES ('purge_check', 1, '{}', 'casa', 'cancelled', 0, 3,
		0, 0, 0) RETURNING id`).Scan(&dead); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE purge_checks SET job_id = ? WHERE id = ?`, dead, orphan)
	var queued int64
	if err := e.r.Write(ctx, func(tx *jobs.Tx) error {
		if err := tx.SQL().QueryRow(`INSERT INTO purge_checks (source_id, state, created_at) VALUES ('casa', 'running', 0)
			RETURNING id`).Scan(&queued); err != nil {
			return err
		}
		if _, err := tx.SQL().Exec(`INSERT INTO purge_check_items (check_id, entry_id, path, readable) VALUES (?, ?, ?, 1)`,
			queued, e.id("casa", q[0]), []byte(q[0])); err != nil {
			return err
		}
		_, err := enqueueCheck(tx, "casa", queued)
		if err != nil {
			return err
		}
		if err := settleChecks(ctx, tx.SQL(), time.Now()); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if c := e.check(orphan); c.state != checkFailed || !c.finished {
		t.Errorf("a check whose job was cancelled is %+v; want failed", c)
	}
	e.idle()
	if c := e.check(queued); c.state != checkReady {
		t.Errorf("a queued check is %+v; want ready once run", c)
	}
}

// The check's queries read by indexes: the subtree by (source_id, path),
// the copies by the digest's contents row and then file_content and
// archive_members by content, the same-size files by file_content's
// (source_id, state, size); the quarantine test is a residual.
func TestCheckQueryPlans(t *testing.T) {
	e := newCheckEnv(t)
	sum := make([]byte, 32)
	for _, c := range []struct {
		name  string
		query string
		args  []any
		uses  []string
	}{
		{"subtree", subtreePageSQL, []any{"casa", []byte("q/1/1/a/"), []byte("q/1/1/a0"), 256},
			[]string{"sqlite_autoindex_entries_1"}},
		{"blind", blindSQL, []any{"casa", []byte("a"), []byte("a/"), []byte("a0")}, []string{"sqlite_autoindex_entries_1"}},
		{"to read", toReadSQL, []any{"casa", []byte("a"), []byte("a/"), []byte("a0")}, []string{"sqlite_autoindex_entries_1"}},
		{"copies", copyFilesSQL, []any{sum}, []string{"sqlite_autoindex_contents_1", "file_content_by_content"}},
		{"members", copyMembersSQL, []any{sum}, []string{"sqlite_autoindex_contents_1", "archive_members_content"}},
		{"same size", sameSizeSQL, []any{"casa", 10, 10}, []string{"file_content_by_source"}},
	} {
		rows, err := e.st.Reader().Query(`EXPLAIN QUERY PLAN `+c.query, c.args...)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		all := strings.Join(plan, "\n")
		for _, idx := range c.uses {
			if !strings.Contains(all, idx) {
				t.Errorf("%s: the plan does not use %s:\n%s", c.name, idx, all)
			}
		}
		for _, line := range plan {
			if strings.HasPrefix(line, "SCAN ") && line != "SCAN CONSTANT ROW" {
				t.Errorf("%s: the plan scans a table:\n%s", c.name, all)
			}
		}
	}
}

func mustB64(t *testing.T, s string) string {
	t.Helper()
	e := corpus.Entry{PathB64: s}
	raw, err := e.RawPath()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func indexOf(t *testing.T, truth corpus.GroundTruth, path string) int {
	t.Helper()
	for i, en := range truth.Entries {
		if mustB64(t, en.PathB64) == path {
			return i
		}
	}
	t.Fatalf("%q is not in the ground truth", path)
	return -1
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func mapsEqual(a, b map[string]verdictTotals) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
