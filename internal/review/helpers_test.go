package review

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/corpus"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/index/indextest"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// posix is the capability set of a local POSIX filesystem.
var posix = fsaccess.Capabilities{
	Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true,
	TimeResolution: time.Nanosecond,
}

// world is a seeded index with its review rows.
type world struct {
	t   *testing.T
	st  *store.Store
	gen int64
}

func newWorld(t *testing.T) *world {
	t.Helper()
	return &world{t: t, st: storetest.Open(t)}
}

func (w *world) exec(query string, args ...any) {
	w.t.Helper()
	if err := w.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	}); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
}

// refresh writes the next generation of review rows and makes it visible,
// as the relate job does.
func (w *world) refresh() {
	w.t.Helper()
	w.gen++
	if err := Refresh(context.Background(), w.st, w.gen); err != nil {
		w.t.Fatal(err)
	}
	w.exec(`UPDATE review_state SET gen = ?, dirty = 0`, w.gen)
	w.exec(`DELETE FROM review_rows WHERE gen < ?`, w.gen)
}

// relation inserts a relation of the next generation between two entries
// (member 0: the entry itself) and returns its ID. Its figures come from
// the sides' totals: matched and redundant bytes are a's.
func (w *world) relation(kind string, a, b domain.EntryID) int64 {
	w.t.Helper()
	var id int64
	err := w.st.Write(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`INSERT INTO relations (gen, kind, a_entry, b_entry, matched_bytes, redundant_bytes,
			a_bytes, a_files, b_bytes, b_files, a_only_files, a_only_bytes, b_only_files, b_only_bytes)
			SELECT ?, ?, ea.id, eb.id, ea.total_bytes, ea.total_bytes, ea.total_bytes, ea.total_files,
				eb.total_bytes, eb.total_files, 0, 0, 0, 0
			FROM entries ea, entries eb WHERE ea.id = ? AND eb.id = ? RETURNING id`, w.gen+1, kind, a, b).Scan(&id)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

// decide sets an own decision as set-decision does.
func (w *world) decide(id domain.EntryID, d domain.Decision) {
	w.t.Helper()
	svc := decisions.New(clock.Real{})
	err := w.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := svc.SetDecision(context.Background(), tx, decisions.SetDecision{EntryID: id, Decision: d})
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

// card returns list's card for src.
func (w *world) card(list List, src domain.SourceID) Card {
	w.t.Helper()
	cards, err := Cards(context.Background(), w.st.Reader(), src)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, c := range cards {
		if c.List == list {
			return c
		}
	}
	w.t.Fatalf("no card %s", list)
	return Card{}
}

// all reads every row of list for src through pages of limit rows.
func (w *world) all(list List, src domain.SourceID, decided bool, limit int) []Row {
	w.t.Helper()
	var out []Row
	cursor := ""
	for {
		p, err := Rows(context.Background(), w.st.Reader(), list, src, decided, cursor, limit)
		if err != nil {
			w.t.Fatal(err)
		}
		if len(p.Items) > limit {
			w.t.Fatalf("%s: page of %d rows, limit %d", list, len(p.Items), limit)
		}
		out = append(out, p.Items...)
		if p.NextCursor == "" {
			return out
		}
		cursor = p.NextCursor
	}
}

// path returns an entry's raw path.
func (w *world) path(id domain.EntryID) string {
	w.t.Helper()
	var p []byte
	if err := w.st.Reader().QueryRow(`SELECT path FROM entries WHERE id = ?`, id).Scan(&p); err != nil {
		w.t.Fatalf("entry %d: %v", id, err)
	}
	return string(p)
}

// digest is a test content digest.
func digest(s string) []byte {
	d := sha256.Sum256([]byte(s))
	return d[:]
}

// corpusWorld is the regression corpus scanned with the default rules as
// source "corpus", hashed to completion from its ground truth, with the
// ground truth's declared relations; plus source "pen", holding one more
// copy of a duplicated corpus file, a not-checked photo, and some junk.
type corpusWorld struct {
	*world
	gt     corpus.GroundTruth
	corpus *indextest.Seeded
	pen    *indextest.Seeded
	arcs   map[string]*indextest.SeededArchive
	// rels are the relation IDs by ground-truth index.
	rels []int64
	// sizes are the sizes of files and members ("archive!member"), by raw
	// path.
	sizes map[string]int64
	// penCopy is the corpus content pen copies.
	penCopy corpus.Duplicate
}

// The pen source's paths.
const (
	penCopyPath    = "DCIM/copia.jpg"
	penPendingPath = "DCIM/nao_checada.jpg"
	penJunkPath    = "Thumbs.db"
	penCachePath   = "cache"
)

func newCorpusWorld(t *testing.T) *corpusWorld {
	t.Helper()
	w := &corpusWorld{world: newWorld(t)}
	sfs := synthfs.New()
	root, gt := corpus.BuildSynth(sfs, "/corpus", corpus.Corpus())
	w.gt = gt
	w.scan(sfs, "corpus", "/corpus", root)

	w.sizes = map[string]int64{}
	for _, e := range gt.Entries {
		if e.Size != nil {
			w.sizes[raw(t, e.PathB64)] = *e.Size
		}
	}
	for _, a := range gt.Members {
		for _, m := range a.Members {
			if m.Size != nil {
				w.sizes[raw(t, a.PathB64)+"!"+raw(t, m.PathB64)] = *m.Size
			}
		}
	}
	// pen copies the first duplicated corpus file whose copies are all
	// files (no member), so the copy adds to a group the corpus has anyway.
	for _, d := range gt.Duplicates {
		if !slices.ContainsFunc(d.Copies, func(p corpus.Path) bool { return strings.Contains(p.Path, "!") }) {
			w.penCopy = d
			break
		}
	}
	w.pen = indextest.Seed(t, w.st, indextest.Tree{Source: "pen", CreateSource: true, MountPoint: "/media/pen",
		Nodes: []indextest.Node{
			{Path: "DCIM", Kind: domain.EntryDirectory},
			{Path: penCopyPath, Size: w.penCopy.Size, FileKind: domain.FileKindImage, MTime: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)},
			{Path: penPendingPath, Size: 777_777, FileKind: domain.FileKindImage, MTime: time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)},
			{Path: penJunkPath, Size: 4093, FileKind: domain.FileKindSystem, Category: domain.CategorySystemJunk, Triage: domain.TriageDiscard},
			{Path: penCachePath, Kind: domain.EntryDirectory, Category: domain.CategoryCache, Triage: domain.TriageDiscard},
			{Path: penCachePath + "/a.bin", Size: 1001},
			{Path: penCachePath + "/b.bin", Size: 2003},
		}})
	sum, err := hex.DecodeString(w.penCopy.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	w.pen.SetContent(w.st, penCopyPath, indextest.Content{State: domain.ContentHashed, SHA256: sum})
	w.pen.SetContent(w.st, penPendingPath, indextest.Content{State: domain.ContentPending})
	w.pen.SetContent(w.st, penJunkPath, indextest.Content{State: domain.ContentUniqueSize})
	w.pen.SetContent(w.st, penCachePath+"/a.bin", indextest.Content{State: domain.ContentUniqueSize})
	w.pen.SetContent(w.st, penCachePath+"/b.bin", indextest.Content{State: domain.ContentUniqueSize})

	w.corpus = indextest.Attach(t, w.st, "corpus")
	w.arcs = w.corpus.SeedContent(w.st, gt)
	indextest.RecomputeCoverage(t, w.st)
	w.seedRelations()
	w.refresh()
	return w
}

func raw(t *testing.T, b64 string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sideTotals are the files and bytes of a relation side: the files below a
// folder, or the file members of an archive.
func (w *corpusWorld) sideTotals(p string) (files, bytes int64) {
	for q, n := range w.sizes {
		if strings.HasPrefix(q, p+"/") || strings.HasPrefix(q, p+"!") {
			files++
			bytes += n
		}
	}
	return files, bytes
}

// seedRelations writes the ground truth's relations as generation 1 of
// relations, as relate would find them: matched bytes are the side's bytes
// less its only-here bytes, and redundant bytes a's matched bytes.
func (w *corpusWorld) seedRelations() {
	t := w.t
	for _, r := range w.gt.Relations {
		a, b := raw(t, r.A.PathB64), raw(t, r.B.PathB64)
		af, ab := w.sideTotals(a)
		bf, bb := w.sideTotals(b)
		var aof, aob, bof, bob int64
		for _, p := range r.AOnly {
			aof++
			aob += w.sizes[raw(t, p.PathB64)]
		}
		for _, p := range r.BOnly {
			bof++
			bob += w.sizes[raw(t, p.PathB64)]
		}
		var id int64
		err := w.st.Write(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`INSERT INTO relations (gen, kind, a_entry, b_entry, matched_bytes, redundant_bytes,
				a_bytes, a_files, b_bytes, b_files, a_only_files, a_only_bytes, b_only_files, b_only_bytes)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
				w.gen+1, r.Kind, w.corpus.ID(a), w.corpus.ID(b), ab-aob, ab-aob, ab, af, bb, bf, aof, aob, bof, bob).Scan(&id)
		})
		if err != nil {
			t.Fatal(err)
		}
		w.rels = append(w.rels, id)
	}
}

// scan adds the synthfs root built at path as source id and runs one
// complete scan of it with the default rules, as the runner would.
func (w *world) scan(sfs *synthfs.FS, id domain.SourceID, path string, root *synthfs.Node) {
	t := w.t
	t.Helper()
	ctx := context.Background()
	dev := root.Info().Dev
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-" + string(id), FSType: "ext4",
		DeviceKey: "dev:" + string(id), Strong: true}
	sfs.SetVolume(dev, vol)
	sfs.SetCapabilities(dev, posix)
	caps, err := json.Marshal(posix)
	if err != nil {
		t.Fatal(err)
	}
	var job int64
	err = w.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root,
			device_key, capabilities, state, mount_point, created_at)
			VALUES (?, ?, 'uuid', ?, 'ext4', 1, X'', ?, ?, 'online', ?, 0)`,
			string(id), string(id), vol.ID, vol.DeviceKey, string(caps), []byte(path)); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen,
			last_seen, scan_gen) VALUES (?, NULL, X'', X'', 'directory', 'present', 0, 0, 0)`, string(id)); err != nil {
			return err
		}
		return tx.QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
			max_attempts, available_at, created_at, updated_at) VALUES ('scan', 1, '{}', ?, 'running', 0, 3, 0, 0, 0)
			RETURNING id`, string(id)).Scan(&job)
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := sources.New(w.st, sfs, config.Sources{AllowedRoots: []string{t.TempDir()}}, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	h := index.NewHandler(w.st, svc, rules.Default(), clock.Real{}, config.Defaults().Scan)
	j := jobs.Job{ID: domain.JobID(job), Kind: jobs.KindScan, PayloadVersion: 1, SourceID: id, Attempt: 1}
	if err := h.Run(ctx, j, scanRuntime{}); err != nil {
		t.Fatalf("scan %s: %v", id, err)
	}
	w.exec(`UPDATE jobs SET state = 'succeeded' WHERE id = ?`, job)
}

// scanRuntime is a jobs.Runtime that never pauses the scan.
type scanRuntime struct{}

func (scanRuntime) Progress(map[string]int64)                        {}
func (scanRuntime) FSCall(string) func()                             { return func() {} }
func (scanRuntime) Yield(ctx context.Context) error                  { return ctx.Err() }
func (scanRuntime) UseSource(context.Context, domain.SourceID) error { return nil }
