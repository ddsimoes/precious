package cleanup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/config"
	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/organize"
	"precious/internal/relations"
	"precious/internal/review"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// checkPosix is a local POSIX filesystem with the no-replace rename (ext4).
var checkPosix = fsaccess.Capabilities{
	Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true,
	TimeResolution: time.Nanosecond, NoReplaceRename: true,
}

// checkClock is the real time shifted by an offset the test moves.
type checkClock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *checkClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

// checkEnv is the server's stack for a check, on a synthfs seen through an
// instrument recorder: the store, the source registry, a started job runner
// with the scan, hashing, relate, and purge_check handlers, organize's
// index adapter (which quarantines entries as the executor's outcomes do),
// and the cleanup service.
type checkEnv struct {
	t       *testing.T
	st      *store.Store
	sfs     *synthfs.FS
	rec     *instrument.Recorder
	srcs    *sources.Service
	r       *jobs.Runner
	clk     *checkClock
	hashing *content.Service
	svc     *Service
	org     *organize.Service
	roots   map[domain.SourceID]string
	seq     int
}

func newCheckEnv(t *testing.T) *checkEnv {
	t.Helper()
	cfg := config.Defaults()
	e := &checkEnv{t: t, st: storetest.Open(t), sfs: synthfs.New(), clk: &checkClock{}, roots: map[domain.SourceID]string{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.rec = instrument.Wrap(e.sfs)
	srcs, err := sources.New(e.st, e.rec, config.Sources{AllowedRoots: []string{t.TempDir()}, AllowWrites: true}, e.clk)
	if err != nil {
		t.Fatal(err)
	}
	e.srcs = srcs
	r, err := jobs.NewRunner(jobs.Options{Store: e.st, Registry: srcs, Config: cfg.Jobs, Clock: e.clk, Logger: log,
		TickInterval: 20 * time.Millisecond, ProgressInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	e.r = r
	pol := rules.Default()
	scanner := index.NewHandler(e.st, srcs, pol, e.clk, cfg.Scan)
	e.hashing = content.NewService(e.st, srcs, e.clk, cfg.Hashing, cfg.Archives, cfg.Duplicates)
	e.hashing.Register(r)
	relations.NewHandler(e.st, e.clk, cfg.Duplicates, func(ctx context.Context, gen int64) error {
		return review.Refresh(ctx, e.st, gen)
	}).Register(r)
	scanner.OnScanDone(func(ctx context.Context, src domain.SourceID) {
		e.hashing.AfterScan(ctx, src)
		_ = r.Write(ctx, relations.RequestRefresh)
	})
	scanner.Register(r)
	e.org = organize.New(organize.Options{Store: e.st, Policy: pol, AllowWrites: true, Clock: e.clk, Logger: log})
	e.svc = New(Options{Store: e.st, Runner: r, Sources: srcs, Content: e.hashing, Policy: pol, AllowWrites: true,
		Clock: e.clk, Logger: log})
	e.svc.Register(r)
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		if err := r.Stop(sctx); err != nil {
			t.Errorf("stop the runner: %v", err)
		}
		cancel()
	})
	return e
}

// disk builds the synthfs root at path on its own device, adds it as source
// id, and scans and hashes it.
func (e *checkEnv) disk(id domain.SourceID, path string, build func(root *synthfs.Node)) *synthfs.Node {
	e.t.Helper()
	root := e.sfs.Root(path)
	if build != nil {
		build(root)
	}
	e.add(id, path, root)
	return root
}

// add adds the synthfs root built at path as source id, on its own volume,
// and scans and hashes it.
func (e *checkEnv) add(id domain.SourceID, path string, root *synthfs.Node) {
	e.t.Helper()
	dev := root.Info().Dev
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-" + string(id), FSType: "ext4",
		DeviceKey: "dev:" + string(id), Strong: true}
	e.sfs.SetVolume(dev, vol)
	e.sfs.SetCapabilities(dev, checkPosix)
	caps, err := json.Marshal(checkPosix)
	if err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root, device_key, capabilities,
		state, mount_point, created_at, write_enabled) VALUES (?, ?, 'uuid', ?, 'ext4', 1, X'', ?, ?, 'online', ?, 0, 1)`,
		string(id), string(id), vol.ID, vol.DeviceKey, string(caps), []byte(path))
	e.exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		VALUES (?, NULL, X'', X'', 'directory', 'present', 0, 0, 0)`, string(id))
	e.roots[id] = path
	e.scan(id)
}

// scan scans src through the runner, which then hashes and relates, and
// waits for every job to end.
func (e *checkEnv) scan(src domain.SourceID) {
	e.t.Helper()
	if _, err := e.r.Enqueue(context.Background(), jobs.Spec{Kind: jobs.KindScan, SourceID: src}); err != nil {
		e.t.Fatal(err)
	}
	e.idle()
}

// idle waits until no job is queued or running.
func (e *checkEnv) idle() {
	e.t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for e.count(`SELECT count(*) FROM jobs WHERE state IN ('queued', 'running')`) > 0 {
		if time.Now().After(deadline) {
			e.t.Fatalf("jobs still active: %d", e.count(`SELECT count(*) FROM jobs WHERE state IN ('queued', 'running')`))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *checkEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(query, args...); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

func (e *checkEnv) count(query string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.st.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// id is the entry ID of a path of src.
func (e *checkEnv) id(src domain.SourceID, path string) int64 {
	e.t.Helper()
	var id int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = ?`, string(src),
		[]byte(path)).Scan(&id); err != nil {
		e.t.Fatalf("entry %s:%q: %v", src, path, err)
	}
	return id
}

func splitItemPath(p string) (dir, name string) {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

func joinItemPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// open opens the folder at the path p of src through rooted handles.
func (e *checkEnv) open(src domain.SourceID, p string) fsaccess.Dir {
	e.t.Helper()
	dir, err := e.sfs.OpenRoot(e.roots[src])
	if err != nil {
		e.t.Fatal(err)
	}
	if p == "" {
		return dir
	}
	for _, name := range strings.Split(p, "/") {
		info, err := dir.Lstat([]byte(name))
		if err != nil {
			e.t.Fatalf("lstat %q in %q: %v", name, p, err)
		}
		next, err := dir.OpenDir([]byte(name), info)
		dir.Close()
		if err != nil {
			e.t.Fatalf("open %q in %q: %v", name, p, err)
		}
		dir = next
	}
	return dir
}

// lstat lstats the entry at the path p of src.
func (e *checkEnv) lstat(src domain.SourceID, p string) fsaccess.EntryInfo {
	e.t.Helper()
	if p == "" {
		dir := e.open(src, "")
		defer dir.Close()
		return dir.Self()
	}
	parent, name := splitItemPath(p)
	dir := e.open(src, parent)
	defer dir.Close()
	info, err := dir.Lstat([]byte(name))
	if err != nil {
		e.t.Fatalf("lstat %q: %v", p, err)
	}
	return info
}

// facts are the post-step facts of the entry at p, as the executor
// confirms a step.
func (e *checkEnv) facts(src domain.SourceID, p string) index.PostFacts {
	info := e.lstat(src, p)
	f := index.PostFacts{Dev: info.Dev, Ino: info.Ino, MtimeNs: info.ModTime.UnixNano()}
	if !info.Ctime.IsZero() {
		f.CtimeNs = info.Ctime.UnixNano()
	}
	return f
}

func (e *checkEnv) has(src domain.SourceID, p string) bool {
	return e.count(`SELECT count(*) FROM entries WHERE source_id = ? AND path = ?`, string(src), []byte(p)) > 0
}

// mkdir makes the folder name in the folder parent of src, on disk and in
// the index, as the executor's mkdir outcome does.
func (e *checkEnv) mkdir(src domain.SourceID, parent, name string) {
	e.t.Helper()
	dir := e.open(src, parent)
	w, _ := fsaccess.AsWriter(dir)
	if err := w.Mkdir([]byte(name)); err != nil {
		e.t.Fatal(err)
	}
	dir.Close()
	nf := index.NewFolder{Source: src, Parent: domain.EntryID(e.id(src, parent)), Name: []byte(name),
		Facts: e.facts(src, joinItemPath(parent, name)), ParentFacts: e.facts(src, parent)}
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := e.org.Index().ApplyMkdir(context.Background(), tx, nf)
		return err
	})
	if err != nil {
		e.t.Fatalf("mkdir %q: %v", joinItemPath(parent, name), err)
	}
}

// move moves the entry at from into the folder to of src, on disk and in
// the index, as the executor's rename outcome does: it keeps its ID, its
// digests, and its archive listing.
func (e *checkEnv) move(src domain.SourceID, from, to string) string {
	e.t.Helper()
	parent, name := splitItemPath(from)
	sd, dd := e.open(src, parent), e.open(src, to)
	w, _ := fsaccess.AsWriter(sd)
	err := w.RenameNoReplace([]byte(name), dd, []byte(name))
	sd.Close()
	dd.Close()
	if err != nil {
		e.t.Fatalf("rename %q into %q: %v", from, to, err)
	}
	mv := index.Move{Source: src, Entry: domain.EntryID(e.id(src, from)), NewParent: domain.EntryID(e.id(src, to)),
		NewName: []byte(name), Facts: e.facts(src, joinItemPath(to, name)), OldParentFacts: e.facts(src, parent),
		NewParentFacts: e.facts(src, to)}
	err = e.st.Write(context.Background(), func(tx *sql.Tx) error {
		return e.org.Index().ApplyRename(context.Background(), tx, mv)
	})
	if err != nil {
		e.t.Fatalf("move %q into %q: %v", from, to, err)
	}
	return joinItemPath(to, name)
}

// quarantine moves each entry at paths of src into its own item folder of
// the quarantine's plan 1, as a cleanup's mkdir and rename steps leave it,
// and returns their quarantine paths.
func (e *checkEnv) quarantine(src domain.SourceID, paths ...string) []string {
	e.t.Helper()
	q := index.QuarantineName
	if !e.has(src, q) {
		e.mkdir(src, "", q)
	}
	plan := q + "/1"
	if !e.has(src, plan) {
		e.mkdir(src, q, "1")
	}
	var out []string
	for _, p := range paths {
		e.seq++
		seq := strconv.Itoa(e.seq)
		e.mkdir(src, plan, seq)
		out = append(out, e.move(src, p, plan+"/"+seq))
	}
	return out
}

// newCheck inserts a running check of src over the quarantined items at
// paths, as check-purge does, and returns its ID. With enqueue it also
// starts its job.
func (e *checkEnv) newCheck(src domain.SourceID, enqueue bool, paths ...string) int64 {
	e.t.Helper()
	var id int64
	err := e.r.Write(context.Background(), func(tx *jobs.Tx) error {
		if err := tx.SQL().QueryRow(`INSERT INTO purge_checks (source_id, state, created_at)
			VALUES (?, 'running', 0) RETURNING id`, string(src)).Scan(&id); err != nil {
			return err
		}
		for _, p := range paths {
			if _, err := tx.SQL().Exec(`INSERT INTO purge_check_items (check_id, entry_id, path, readable)
				VALUES (?, ?, ?, 1)`, id, e.id(src, p), []byte(p)); err != nil {
				return err
			}
		}
		if enqueue {
			_, err := enqueueCheck(tx, src, id)
			return err
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// checkRuntime records progress; Yield runs onYield before it reports the
// context's end.
type checkRuntime struct {
	mu       sync.Mutex
	progress map[string]int64
	yields   int
	onYield  func(n int)
}

func (r *checkRuntime) Progress(c map[string]int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.progress == nil {
		r.progress = map[string]int64{}
	}
	for k, v := range c {
		r.progress[k] = v
	}
}

func (r *checkRuntime) FSCall(string) func() { return func() {} }

func (r *checkRuntime) Yield(ctx context.Context) error {
	r.mu.Lock()
	r.yields++
	n, hook := r.yields, r.onYield
	r.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return ctx.Err()
}

func (r *checkRuntime) UseSource(context.Context, domain.SourceID) error { return nil }

// run runs one attempt of the check's job directly, as the runner would,
// with rt, under the job row job (0 for none).
func (e *checkEnv) run(ctx context.Context, check int64, src domain.SourceID, job int64, rt jobs.Runtime) error {
	e.t.Helper()
	payload := fmt.Sprintf(`{"check_id":"%d"}`, check)
	return (&checkHandler{s: e.svc}).Run(ctx, jobs.Job{ID: domain.JobID(job), Kind: KindPurgeCheck, PayloadVersion: 1,
		Payload: json.RawMessage(payload), SourceID: src, Attempt: 1}, rt)
}

// checkState is a check's state, stale reason, and whether it ended.
type checkState struct {
	state, reason string
	finished      bool
}

func (e *checkEnv) check(id int64) checkState {
	e.t.Helper()
	var c checkState
	var reason sql.NullString
	var finished sql.NullInt64
	if err := e.st.Reader().QueryRow(`SELECT state, stale_reason, finished_at FROM purge_checks WHERE id = ?`, id).
		Scan(&c.state, &reason, &finished); err != nil {
		e.t.Fatal(err)
	}
	c.reason, c.finished = reason.String, finished.Valid
	return c
}

// checkFile is a purge_check_files row as the tests read it.
type checkFile struct {
	item, entry, member    int64
	kind, path             string
	size                   int64
	mtime, ctime, ino, dev sql.NullInt64
	nlink, alloc           sql.NullInt64
	sha                    []byte
	verdict, class         string
	copySource, copyPath   string
	copyEntry, copyMember  sql.NullInt64
	copySize, copyMtime    sql.NullInt64
	copyCtime, copyIno     sql.NullInt64
	copyDev                sql.NullInt64
	hardLink               bool
}

// files reads the check's records, by path then member.
func (e *checkEnv) files(check int64) []checkFile {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT item_id, coalesce(entry_id, 0), coalesce(member_id, 0), kind, path, size,
		mtime_ns, ctime_ns, ino, dev, nlink, alloc, sha256, verdict, coalesce(class, ''), coalesce(copy_source, ''),
		coalesce(copy_path, X''), copy_entry, copy_member, copy_size, copy_mtime_ns, copy_ctime_ns, copy_ino, copy_dev,
		copy_hard_link FROM purge_check_files WHERE check_id = ? ORDER BY path, member_id`, check)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []checkFile
	for rows.Next() {
		var f checkFile
		var path, copyPath []byte
		if err := rows.Scan(&f.item, &f.entry, &f.member, &f.kind, &path, &f.size, &f.mtime, &f.ctime, &f.ino, &f.dev,
			&f.nlink, &f.alloc, &f.sha, &f.verdict, &f.class, &f.copySource, &copyPath, &f.copyEntry, &f.copyMember,
			&f.copySize, &f.copyMtime, &f.copyCtime, &f.copyIno, &f.copyDev, &f.hardLink); err != nil {
			e.t.Fatal(err)
		}
		f.path, f.copyPath = string(path), string(copyPath)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// recordsByPath returns the check's entry records (not members) by path.
func recordsByPath(files []checkFile) map[string]checkFile {
	out := map[string]checkFile{}
	for _, f := range files {
		if f.member == 0 {
			out[f.path] = f
		}
	}
	return out
}

// opened counts the successful OpenFile calls of each path below the root
// of src since the recorder was last reset; readBytes the content bytes
// read from each.
func (e *checkEnv) opened(src domain.SourceID) (opens map[string]int, readBytes map[string]int64) {
	opens, readBytes = map[string]int{}, map[string]int64{}
	prefix := e.roots[src] + "/"
	for _, c := range e.rec.Calls() {
		p, ok := strings.CutPrefix(c.FullPath(), prefix)
		if !ok {
			continue
		}
		switch {
		case c.Op == instrument.OpOpenFile && c.Err == nil:
			opens[p]++
		case c.Op == instrument.OpReadAt:
			readBytes[p] += int64(c.Bytes)
		}
	}
	return opens, readBytes
}

// writes counts the calls since the last reset that change a disk.
func (e *checkEnv) writes() int {
	n := 0
	for _, op := range []instrument.Op{instrument.OpMkdir, instrument.OpRmdir, instrument.OpRename,
		instrument.OpCreate, instrument.OpUnlink} {
		n += e.rec.Count(op)
	}
	return n
}

// sameRecordIdentity fails unless the record's identity is info's.
func sameRecordIdentity(t *testing.T, what string, f checkFile, info fsaccess.EntryInfo) {
	t.Helper()
	want := identityOf(info)
	if f.mtime != want.mtime || f.ctime != want.ctime || f.ino != want.ino || f.dev != want.dev ||
		f.nlink != want.nlink || f.alloc != want.alloc {
		t.Errorf("%s: recorded identity mtime %v ctime %v ino %v dev %v nlink %v alloc %v; want %+v", what,
			f.mtime, f.ctime, f.ino, f.dev, f.nlink, f.alloc, want)
	}
}
