package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// testNow is the scans' clock.
var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// posix is the capability set of a local POSIX filesystem.
var posix = fsaccess.Capabilities{
	Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true,
	TimeResolution: time.Nanosecond,
}

// fat is the capability set of a FAT memory card (design D3).
var fat = fsaccess.Capabilities{Known: true, LocalTime: true, TimeResolution: 2 * time.Second}

// env is a store, a synthfs, the source registry over both, and the scan
// handler.
type env struct {
	t   *testing.T
	st  *store.Store
	fs  fsaccess.FS
	sfs *synthfs.FS
	src *sources.Service
	cfg config.Scan
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, st: storetest.Open(t), sfs: synthfs.New(), cfg: config.Defaults().Scan}
	e.fs = e.sfs
	e.services()
	return e
}

// services (re)builds the registry over e.fs, as wrapping it requires.
func (e *env) services() {
	e.t.Helper()
	svc, err := sources.New(e.st, e.fs, config.Sources{AllowedRoots: []string{e.t.TempDir()}}, fixedClock{testNow})
	if err != nil {
		e.t.Fatal(err)
	}
	e.src = svc
}

func (e *env) handler() *Handler {
	return NewHandler(e.st, e.src, rules.Default(), fixedClock{testNow}, e.cfg)
}

// disk creates the synthfs root at path on its own device with caps and
// adds it as source id, root entry included, as add-source does.
func (e *env) disk(id domain.SourceID, path string, caps fsaccess.Capabilities) *synthfs.Node {
	e.t.Helper()
	root := e.sfs.Root(path)
	e.addSource(id, path, root, caps)
	return root
}

// addSource adds the synthfs root built at path as source id, on its own
// volume with caps, root entry included, as add-source does.
func (e *env) addSource(id domain.SourceID, path string, root *synthfs.Node, caps fsaccess.Capabilities) {
	e.t.Helper()
	dev := root.Info().Dev
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-" + string(id), FSType: "ext4",
		DeviceKey: "dev:" + string(id), Strong: true}
	e.sfs.SetVolume(dev, vol)
	e.sfs.SetCapabilities(dev, caps)
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		e.t.Fatal(err)
	}
	err = e.st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root,
			device_key, capabilities, state, mount_point, created_at)
			VALUES (?, ?, 'uuid', ?, 'ext4', 1, X'', ?, ?, 'online', ?, 0)`,
			string(id), string(id), vol.ID, vol.DeviceKey, string(capsJSON), []byte(path)); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen,
			last_seen, scan_gen) VALUES (?, NULL, X'', X'', 'directory', 'present', 0, 0, 0)`, string(id))
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// fakeRuntime records progress; Yield reports the context's end.
type fakeRuntime struct {
	mu       sync.Mutex
	progress map[string]int64
	yields   int
	onYield  func(n int)
}

func (r *fakeRuntime) Progress(c map[string]int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.progress == nil {
		r.progress = map[string]int64{}
	}
	for k, v := range c {
		r.progress[k] = v
	}
}

func (r *fakeRuntime) FSCall(string) func() { return func() {} }

func (r *fakeRuntime) Yield(ctx context.Context) error {
	r.mu.Lock()
	r.yields++
	n, hook := r.yields, r.onYield
	r.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return ctx.Err()
}

func (r *fakeRuntime) UseSource(context.Context, domain.SourceID) error { return nil }

// scanWith runs one scan attempt of src under a running job row, as the
// runner would, and records the job's outcome.
func (e *env) scanWith(ctx context.Context, src domain.SourceID, rt jobs.Runtime) error {
	e.t.Helper()
	var id int64
	err := e.st.Writer().QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
		max_attempts, available_at, created_at, updated_at) VALUES ('scan', 1, '{}', ?, 'running', 0, 3, 0, 0, 0)
		RETURNING id`, string(src)).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	job := jobs.Job{ID: domain.JobID(id), Kind: jobs.KindScan, PayloadVersion: 1, SourceID: src, Attempt: 1}
	runErr := e.handler().Run(ctx, job, rt)
	state := "succeeded"
	if runErr != nil {
		state = "failed"
	}
	if _, err := e.st.Writer().Exec(`UPDATE jobs SET state = ? WHERE id = ?`, state, id); err != nil {
		e.t.Fatal(err)
	}
	return runErr
}

// scan runs a complete scan of src and fails the test on any error.
func (e *env) scan(src domain.SourceID) *fakeRuntime {
	e.t.Helper()
	rt := &fakeRuntime{}
	if err := e.scanWith(context.Background(), src, rt); err != nil {
		e.t.Fatalf("scan %s: %v", src, err)
	}
	return rt
}

// entry is one entries row with its dir_stats, as tests compare them.
type entry struct {
	ID                                        int64
	Parent                                    sql.NullInt64
	Name, Path, Kind                          string
	Special, Link                             sql.NullString
	Size, TotalBytes, TotalFiles              int64
	MTime, Newest, Oldest, Dev, Ino, Alloc    sql.NullInt64
	Ext, FileKind, MainKind                   sql.NullString
	Category, Family, Traits, Triage, RuleIDs sql.NullString
	Group, Veto, Partial, Boundary            bool
	State                                     string
	ScanGen                                   int64
	MissingSince                              sql.NullInt64
	Decision                                  sql.NullString
	EffDecision                               string
	EffFrom                                   sql.NullInt64
	HasStats                                  bool
	Dirs, Files, Symlinks, Specials           sql.NullInt64
	Unreadable, Mounts                        sql.NullInt64
	ByKind, ByYear, ByFamily                  sql.NullString
	Signals, Indicators, Inside               sql.NullString
	// FTS counts the entry's entry_names rows (the table is contentless).
	FTS int
}

// entries returns every row of src by raw path.
func (e *env) entries(src domain.SourceID) map[string]entry {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT e.id, e.parent_id, e.name, e.path, e.kind, e.special_kind,
		e.link_text, e.size, e.total_bytes, e.total_files, e.mtime_ns, e.newest_ns, e.oldest_ns, e.dev, e.ino,
		e.alloc, e.ext, e.file_kind, e.main_kind, e.category, e.family, e.traits, e.triage, e.rule_ids,
		e.is_group, e.veto, e.partial, e.mount_boundary, e.state, e.scan_gen, e.missing_since, e.decision,
		e.eff_decision, e.eff_from, d.entry_id IS NOT NULL, d.dirs, d.files, d.symlinks, d.specials,
		d.unreadable, d.mount_boundaries, d.by_kind, d.by_year, d.by_family, d.signals, d.indicators,
		d.inside, (SELECT count(*) FROM entry_names n WHERE n.rowid = e.id)
		FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id WHERE e.source_id = ?`, string(src))
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]entry{}
	for rows.Next() {
		var r entry
		var name, path []byte
		if err := rows.Scan(&r.ID, &r.Parent, &name, &path, &r.Kind, &r.Special, &r.Link, &r.Size,
			&r.TotalBytes, &r.TotalFiles, &r.MTime, &r.Newest, &r.Oldest, &r.Dev, &r.Ino, &r.Alloc, &r.Ext,
			&r.FileKind, &r.MainKind, &r.Category, &r.Family, &r.Traits, &r.Triage, &r.RuleIDs, &r.Group,
			&r.Veto, &r.Partial, &r.Boundary, &r.State, &r.ScanGen, &r.MissingSince, &r.Decision,
			&r.EffDecision, &r.EffFrom, &r.HasStats, &r.Dirs, &r.Files, &r.Symlinks, &r.Specials,
			&r.Unreadable, &r.Mounts, &r.ByKind, &r.ByYear, &r.ByFamily, &r.Signals, &r.Indicators,
			&r.Inside, &r.FTS); err != nil {
			e.t.Fatal(err)
		}
		r.Name, r.Path = string(name), string(path)
		out[r.Path] = r
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// get returns the row at path, failing the test when there is none.
func get(t *testing.T, rows map[string]entry, path string) entry {
	t.Helper()
	r, ok := rows[path]
	if !ok {
		t.Fatalf("no entry at %q", path)
	}
	return r
}

// writes counts the inserts, updates, and deletes of entries and dir_stats
// rows made on the writer connection after it is called, through temporary
// triggers.
func (e *env) writes() func() int {
	e.t.Helper()
	db := e.st.Writer()
	stmts := []string{`CREATE TEMP TABLE IF NOT EXISTS writes (n INTEGER)`, `DELETE FROM temp.writes`}
	for _, table := range []string{"entries", "dir_stats"} {
		for _, ev := range []string{"INSERT", "UPDATE", "DELETE"} {
			stmts = append(stmts, `CREATE TEMP TRIGGER IF NOT EXISTS count_`+table+`_`+ev+` AFTER `+ev+` ON main.`+table+
				` BEGIN INSERT INTO temp.writes VALUES (1); END`)
		}
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			e.t.Fatal(err)
		}
	}
	return func() int {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM temp.writes`).Scan(&n); err != nil {
			e.t.Fatal(err)
		}
		return n
	}
}

// sourceGen returns the source's scan generation.
func (e *env) sourceGen(src domain.SourceID) int64 {
	e.t.Helper()
	var gen int64
	if err := e.st.Reader().QueryRow(`SELECT scan_gen FROM sources WHERE id = ?`, string(src)).Scan(&gen); err != nil {
		e.t.Fatal(err)
	}
	return gen
}
