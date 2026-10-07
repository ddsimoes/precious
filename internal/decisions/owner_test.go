package decisions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/commands"
	"precious/internal/config"
	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/index/indextest"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/web/clientip"
)

// Corpus paths of the owner scenarios.
const (
	siteAntigo = "Projetos/site_antigo"
	praia      = "Fotos/2006/Praia"
	thumbs     = "Fotos/2006/Praia/Thumbs.db"
	winamp     = "Backup_PC_2004/C/Arquivos de programas/Winamp"
	programs   = "Backup_PC_2004/C/Arquivos de programas"
	emuleZip   = "Downloads/eMule0.47c-Installer.zip"
)

// ownerWorld is the regression corpus on a synthfs source "corpus", scanned
// by the real scanner under a job runner, with this package's commands
// served over HTTP. With manual set, the runner is never started: the test
// runs each scan pass itself (scanPass).
type ownerWorld struct {
	*api
	runner  *jobs.Runner
	scanner *index.Handler
	s       *indextest.Seeded
}

func newOwnerWorld(t *testing.T, manual bool) *ownerWorld {
	t.Helper()
	e := newEnv(t)
	sfs := synthfs.New()
	root, _ := corpus.BuildSynth(sfs, "/corpus", corpus.Corpus())
	addSynthSource(t, e, sfs, "corpus", "/corpus", root)
	srcs, err := sources.New(e.st, sfs, config.Sources{AllowedRoots: []string{t.TempDir()}}, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := jobs.NewRunner(jobs.Options{Store: e.st, Registry: srcs, Config: config.Defaults().Jobs, Logger: logger,
		TickInterval: 5 * time.Millisecond, ProgressInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	scanner := index.NewHandler(e.st, srcs, rules.Default(), nil, config.Scan{})
	scanner.Register(r)
	h := commands.New(commands.Options{Store: e.st, Jobs: r, Logger: logger})
	RegisterCommands(h, e.svc)
	mux := http.NewServeMux()
	mux.Handle("POST /api/commands/{name}", h)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(clientip.With(r.Context(), clientip.Info{Addr: clientAddr, Scheme: "https"})))
	}))
	t.Cleanup(srv.Close)
	w := &ownerWorld{api: &api{env: e, url: srv.URL}, runner: r, scanner: scanner}
	if manual {
		if err := w.scanPass(t, w.queueScan(t), nil); err != nil {
			t.Fatal(err)
		}
		w.finishJob(t, "succeeded")
	} else {
		if err := r.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Stop(context.Background()) })
		w.waitJob(t, w.queueScan(t).String())
	}
	w.s = indextest.Attach(t, e.st, "corpus")
	return w
}

// addSynthSource adds the synthfs root built at path as source id, on its
// own volume, root entry included, as add-source does.
func addSynthSource(t *testing.T, e *env, sfs *synthfs.FS, id domain.SourceID, path string, root *synthfs.Node) {
	t.Helper()
	caps := fsaccess.Capabilities{Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true,
		HardLinks: true, TimeResolution: time.Nanosecond}
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-" + string(id), FSType: "ext4",
		DeviceKey: "dev:" + string(id), Strong: true}
	sfs.SetVolume(root.Info().Dev, vol)
	sfs.SetCapabilities(root.Info().Dev, caps)
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
}

// queueScan starts a scan of the corpus as start-scan does.
func (w *ownerWorld) queueScan(t *testing.T) domain.JobID {
	t.Helper()
	var acc jobs.Accepted
	err := w.runner.Write(context.Background(), func(tx *jobs.Tx) error {
		var err error
		acc, err = index.StartScan(context.Background(), tx, "corpus")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := domain.ParseJobID(acc.JobID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// scanPass runs one pass of the scan job id as a runner's attempt would,
// calling yield at the scan's first yield.
func (w *ownerWorld) scanPass(t *testing.T, id domain.JobID, yield func()) error {
	t.Helper()
	w.exec(t, `UPDATE jobs SET state = 'running' WHERE id = ?`, int64(id))
	return w.scanner.Run(context.Background(), jobs.Job{ID: id, Kind: jobs.KindScan, PayloadVersion: 1,
		SourceID: "corpus", Attempt: 1}, &passRuntime{yield: yield})
}

// finishJob records the outcome of the manually run scan job.
func (w *ownerWorld) finishJob(t *testing.T, state string) {
	t.Helper()
	w.exec(t, `UPDATE jobs SET state = ? WHERE kind = 'scan' AND state = 'running'`, state)
}

func (w *ownerWorld) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := w.st.Writer().Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// waitJob waits until the job with the API ref id is terminal and fails
// unless it succeeded.
func (w *ownerWorld) waitJob(t *testing.T, ref string) {
	t.Helper()
	id, err := domain.ParseJobID(ref)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		rec, err := w.runner.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if rec.State.Terminal() {
			if rec.State != domain.JobSucceeded {
				t.Fatalf("scan job %s: %s %s %s", ref, rec.State, rec.TerminalCode, rec.TerminalDetail)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan job %s still %s", ref, rec.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// passRuntime is a jobs.Runtime that calls yield once, at the first Yield.
type passRuntime struct{ yield func() }

func (r *passRuntime) Progress(map[string]int64) {}
func (r *passRuntime) FSCall(string) func()      { return func() {} }
func (r *passRuntime) Yield(ctx context.Context) error {
	if f := r.yield; f != nil {
		r.yield = nil
		f()
	}
	return ctx.Err()
}
func (r *passRuntime) UseSource(context.Context, domain.SourceID) error { return nil }

// classRow is one entry's effective classification and its folder figures.
type classRow struct {
	Category, Family, Triage sql.NullString
	Group, Veto              bool
	ByFamily, Inside         sql.NullString
}

func (w *ownerWorld) rows(t *testing.T) map[string]classRow {
	t.Helper()
	rs, err := w.st.Reader().Query(`SELECT e.path, e.category, e.family, e.triage, e.is_group, e.veto, d.by_family, d.inside
		FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id WHERE e.source_id = 'corpus'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	out := map[string]classRow{}
	for rs.Next() {
		var (
			path []byte
			r    classRow
		)
		if err := rs.Scan(&path, &r.Category, &r.Family, &r.Triage, &r.Group, &r.Veto, &r.ByFamily, &r.Inside); err != nil {
			t.Fatal(err)
		}
		out[string(path)] = r
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (w *ownerWorld) row(t *testing.T, path string) classRow {
	t.Helper()
	r, ok := w.rows(t)[path]
	if !ok {
		t.Fatalf("no entry at %q", path)
	}
	return r
}

// family returns the bytes of family in a by_family column.
func family(t *testing.T, r classRow, f domain.Family) int64 {
	t.Helper()
	var m map[domain.Family]struct{ Bytes int64 }
	if err := json.Unmarshal([]byte(r.ByFamily.String), &m); err != nil {
		t.Fatal(err)
	}
	return m[f].Bytes
}

func (w *ownerWorld) ref(path string) string { return strconv.Quote(w.s.ID(path).String()) }

// overrides counts the entry_overrides rows.
func (w *ownerWorld) overrides(t *testing.T) int {
	t.Helper()
	var n int
	if err := w.st.Reader().QueryRow(`SELECT count(*) FROM entry_overrides`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// lastAudit returns the newest audit event.
func lastAudit(t *testing.T, w *ownerWorld) auditEvent {
	t.Helper()
	evs := audits(t, w.st)
	if len(evs) == 0 {
		t.Fatal("no audit event")
	}
	return evs[len(evs)-1]
}

// scanOf returns the response's scan job ref, failing unless a scan is
// reported with coalesced as wanted.
func scanOf(t *testing.T, r reply, coalesced bool) string {
	t.Helper()
	scan, ok := r.body["scan"].(map[string]any)
	if !ok || strings.Join(keys(scan), ",") != "coalesced,job_id" || scan["coalesced"] != coalesced {
		t.Fatalf("response %s, want a scan with coalesced %v", r.raw, coalesced)
	}
	return scan["job_id"].(string)
}

// classification: the owner corrects a folder's category; owner intent: the
// category override is audited, and back to the rules leaves nothing of the
// override.
func TestSetCategoryCommand(t *testing.T) {
	w := newOwnerWorld(t, false)
	before := w.rows(t)
	site := before[siteAntigo]
	if site.Category.String != "source_project" || !site.Group {
		t.Fatalf("site_antigo before: %+v", site)
	}

	r := w.want(t, CommandSetCategory, `{"entry_id":`+w.ref(siteAntigo)+`,"category":"documents"}`, http.StatusOK, "")
	if strings.Join(keys(r.body), ",") != "applied,scan" || r.body["applied"] != float64(1) {
		t.Fatalf("response %s", r.raw)
	}
	job := scanOf(t, r, false)
	// The entry reads its new classification at once.
	if got := w.row(t, siteAntigo); got.Category.String != "documents" || got.Family.String != "personal" ||
		got.Triage.String != "keep" || got.Group || got.Veto {
		t.Errorf("site_antigo at once: %+v", got)
	}
	ev := lastAudit(t, w)
	if ev.kind != AuditCategorySet || ev.addr.String != clientAddr.String() || ev.at != start.UnixMilli() ||
		ev.detail["entry_id"] != w.s.ID(siteAntigo).String() || ev.detail["path"] != siteAntigo ||
		ev.detail["old"] != "rules" || ev.detail["new"] != "documents" || ev.detail["applied"] != float64(1) {
		t.Errorf("audit %+v", ev)
	}

	// After the scan, the folders above count it as a documents folder: no
	// longer whole under personal as a source-project group, but by its own
	// composition, its files under their own families (design D2: documents
	// is no group category).
	w.waitJob(t, job)
	after := w.rows(t)
	if got := after[siteAntigo]; got.Category.String != "documents" || got.ByFamily != site.ByFamily {
		t.Errorf("site_antigo after the scan: %+v", got)
	}
	var total int64
	for _, f := range domain.Families {
		total += family(t, site, f)
	}
	for _, p := range []string{"Projetos", ""} {
		for _, f := range domain.Families {
			want := family(t, before[p], f) + family(t, site, f)
			if f == domain.FamilyPersonal {
				want -= total
			}
			if got := family(t, after[p], f); got != want {
				t.Errorf("%q %s: %d bytes, want %d", p, f, got, want)
			}
		}
	}
	if family(t, site, domain.FamilyPersonal) == 0 {
		t.Error("site_antigo holds no personal bytes")
	}

	// Back to the rules: the entry at once, and after the scan every row as
	// before the override; the override row is gone.
	r = w.want(t, CommandSetCategory, `{"entry_id":`+w.ref(siteAntigo)+`,"category":"rules"}`, http.StatusOK, "")
	if got := w.row(t, siteAntigo); got.Category.String != "source_project" || !got.Group {
		t.Errorf("site_antigo back to the rules at once: %+v", got)
	}
	w.waitJob(t, scanOf(t, r, false))
	if n := w.overrides(t); n != 0 {
		t.Errorf("%d override rows after back to the rules", n)
	}
	now := w.rows(t)
	for p, b := range before {
		if a := now[p]; a != b {
			t.Errorf("%q after back to the rules: %+v, before %+v", p, a, b)
		}
	}
	if ev := lastAudit(t, w); ev.kind != AuditCategorySet || ev.detail["old"] != "documents" || ev.detail["new"] != "rules" {
		t.Errorf("back-to-the-rules audit %+v", ev)
	}
}

// owner intent: figures follow after the scan: a group mark reads at once,
// starts a scan, and when it ends the composition above counts the folder
// whole. Unmarking a rule group counts its files one by one, and a row with
// both values back to the rules is deleted.
func TestSetGroupCommand(t *testing.T) {
	w := newOwnerWorld(t, false)
	before := w.rows(t)

	r := w.want(t, CommandSetGroup, `{"entry_id":`+w.ref(praia)+`,"group":true}`, http.StatusOK, "")
	job := scanOf(t, r, false)
	if got := w.row(t, praia); !got.Group || got.Category.String != "personal_media" {
		t.Errorf("Praia at once: %+v", got)
	}
	w.waitJob(t, job)
	after := w.rows(t)
	moved := family(t, before["Fotos"], domain.FamilyDisposable) - family(t, after["Fotos"], domain.FamilyDisposable)
	if moved <= 0 || family(t, after["Fotos"], domain.FamilyPersonal) != family(t, before["Fotos"], domain.FamilyPersonal)+moved ||
		family(t, after[praia], domain.FamilyDisposable) != moved {
		t.Errorf("Fotos does not count Praia whole as personal: %s -> %s", before["Fotos"].ByFamily.String,
			after["Fotos"].ByFamily.String)
	}

	r = w.want(t, CommandSetGroup, `{"entry_ids":[`+w.ref(winamp)+`,`+w.ref(praia)+`],"group":false}`, http.StatusOK, "")
	if r.body["applied"] != float64(2) {
		t.Errorf("bulk response %s", r.raw)
	}
	w.waitJob(t, scanOf(t, r, false))
	after = w.rows(t)
	if after[winamp].Group || after[praia].Group {
		t.Errorf("unmarked: Winamp group %v, Praia group %v", after[winamp].Group, after[praia].Group)
	}
	if got, was := family(t, after[programs], domain.FamilyPrograms), family(t, before[programs], domain.FamilyPrograms); got >= was {
		t.Errorf("%s still counts Winamp whole as programs: %d -> %d", programs, was, got)
	}
	ev := lastAudit(t, w)
	old, _ := ev.detail["old"].(map[string]any)
	ids, _ := ev.detail["entry_ids"].([]any)
	if ev.kind != AuditGroupSet || ev.detail["new"] != false || old["true"] != float64(1) || old["rules"] != float64(1) ||
		len(ids) != 2 {
		t.Errorf("bulk group audit %+v", ev)
	}

	w.want(t, CommandSetGroup, `{"entry_ids":[`+w.ref(winamp)+`,`+w.ref(praia)+`],"group":"rules"}`, http.StatusOK, "")
	if n := w.overrides(t); n != 0 {
		t.Errorf("%d override rows after both back to the rules", n)
	}
	if got := w.row(t, winamp); !got.Group {
		t.Error("Winamp is no group after its mark went back to the rules")
	}
}

// owner intent: a member cannot be overridden, set-group on a file is
// invalid, and so is any malformed request; nothing changes and nothing is
// audited. An unknown entry is not_found.
func TestOwnerCommandsRejected(t *testing.T) {
	w := newOwnerWorld(t, true)
	zip := w.s.SeedArchive(w.st, emuleZip, indextest.Archive{Format: domain.ArchiveZip,
		Members: []indextest.Member{{Path: "emule.exe", Size: 600, Content: indextest.Content{State: domain.ContentUniqueSize}}}})
	member := strconv.Quote(domain.Ref{Entry: zip.ID, Member: zip.Member("emule.exe")}.String())
	var symlink int64
	if err := w.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = 'corpus' AND kind = 'symlink' LIMIT 1`).
		Scan(&symlink); err != nil {
		t.Fatal(err)
	}
	before, events := w.rows(t), len(audits(t, w.st))
	for _, c := range []struct {
		name, body string
		status     int
		code       string
	}{
		{CommandSetCategory, `{"entry_id":` + member + `,"category":"application_installation"}`, http.StatusBadRequest, "invalid_request"},
		{CommandSetCategory, `{"entry_ids":[` + w.ref(siteAntigo) + `,` + member + `],"category":"documents"}`, http.StatusBadRequest, "invalid_request"},
		{CommandSetGroup, `{"entry_id":` + member + `,"group":true}`, http.StatusBadRequest, "invalid_request"},
		{CommandSetGroup, `{"entry_id":` + w.ref(thumbs) + `,"group":true}`, http.StatusBadRequest, "invalid_request"},
		{CommandSetGroup, `{"entry_ids":[` + w.ref(praia) + `,` + w.ref(thumbs) + `],"group":true}`, http.StatusBadRequest, "invalid_request"},
		{CommandSetCategory, `{"entry_id":"` + strconv.FormatInt(symlink, 10) + `","category":"documents"}`, http.StatusBadRequest, "invalid_request"},
		{CommandSetCategory, `{"entry_id":` + w.ref(siteAntigo) + `,"category":"photos"}`, http.StatusBadRequest, "invalid_request"},
		{CommandSetCategory, `{"entry_id":` + w.ref(siteAntigo) + `}`, http.StatusBadRequest, "invalid_request"},
		{CommandSetGroup, `{"entry_id":` + w.ref(praia) + `,"group":"yes"}`, http.StatusBadRequest, "invalid_request"},
		{CommandSetGroup, `{"entry_id":` + w.ref(praia) + `}`, http.StatusBadRequest, "invalid_request"},
		{CommandSetGroup, `{"entry_id":` + w.ref(praia) + `,"entry_ids":[` + w.ref(praia) + `],"group":true}`, http.StatusBadRequest, "invalid_request"},
		{CommandSetCategory, `{"entry_id":"999999","category":"documents"}`, http.StatusNotFound, "not_found"},
	} {
		w.want(t, c.name, c.body, c.status, c.code)
	}
	ids := make([]string, MaxEntryIDs+1)
	for i := range ids {
		ids[i] = w.ref(siteAntigo)
	}
	w.want(t, CommandSetCategory, `{"entry_ids":[`+strings.Join(ids, ",")+`],"category":"documents"}`, http.StatusBadRequest, "invalid_request")

	if n := w.overrides(t); n != 0 {
		t.Errorf("rejected requests wrote %d overrides", n)
	}
	if n := len(audits(t, w.st)); n != events {
		t.Errorf("rejected requests wrote %d audit events", n-events)
	}
	now := w.rows(t)
	for p, b := range before {
		if a := now[p]; a != b {
			t.Errorf("%q changed: %+v -> %+v", p, b, a)
		}
	}
}

// owner intent: an offline source keeps the override, the entry reads it at
// once, and no scan starts; its next scan applies it. The targets here are
// a selection.
func TestOverrideOnOfflineSource(t *testing.T) {
	w := newOwnerWorld(t, true)
	sel, count := w.selectionOf(t, "Projetos", "site_antigo")
	w.exec(t, `UPDATE sources SET state = 'offline' WHERE id = 'corpus'`)
	r := w.want(t, CommandSetCategory, `{"selection_id":"`+sel+`","category":"documents"}`, http.StatusOK, "")
	if want := `{"applied":` + strconv.Itoa(count) + `,"scan":null}`; r.raw != want {
		t.Errorf("offline response %s, want %s", r.raw, want)
	}
	var active int
	if err := w.st.Reader().QueryRow(`SELECT count(*) FROM jobs WHERE state IN ('queued', 'running')`).Scan(&active); err != nil || active != 0 {
		t.Errorf("%d active jobs (%v)", active, err)
	}
	if got := w.row(t, siteAntigo); got.Category.String != "documents" || w.overrides(t) != count {
		t.Errorf("offline override: %+v, %d rows", got, w.overrides(t))
	}
	if ev := lastAudit(t, w); ev.kind != AuditCategorySet || ev.detail["selection_id"] != sel || ev.detail["applied"] != float64(count) {
		t.Errorf("selection audit %+v", ev)
	}

	w.exec(t, `UPDATE sources SET state = 'online' WHERE id = 'corpus'`)
	before := w.row(t, "Projetos")
	if err := w.scanPass(t, w.queueScan(t), nil); err != nil {
		t.Fatal(err)
	}
	w.finishJob(t, "succeeded")
	if got := w.row(t, "Projetos"); family(t, got, domain.FamilyPersonal) <= family(t, before, domain.FamilyPersonal) {
		t.Errorf("the next scan did not apply the override: %s -> %s", before.ByFamily.String, got.ByFamily.String)
	}
}

// selectionOf creates a selection of the folders named name within the
// folder at path, and returns its ID and count.
func (w *ownerWorld) selectionOf(t *testing.T, path, name string) (string, int) {
	t.Helper()
	r := w.want(t, CommandCreateSelection, `{"query":{"within":`+w.ref(path)+`,"name":"`+name+`"}}`, http.StatusCreated, "")
	id, _ := r.body["selection_id"].(string)
	count, _ := r.body["count"].(float64)
	if id == "" || count < 1 {
		t.Fatalf("selection of %q in %q: %s", name, path, r.raw)
	}
	return id, int(count)
}

// owner intent: an override set while its source's scan runs joins that
// scan and requests one more pass; the pass that ends runs the job again
// instead of finishing, and the next pass converges.
func TestOverrideDuringActiveScanConverges(t *testing.T) {
	w := newOwnerWorld(t, true)
	before := w.rows(t)
	id := w.queueScan(t)
	var r reply
	err := w.scanPass(t, id, func() {
		r = w.want(t, CommandSetGroup, `{"entry_id":`+w.ref(praia)+`,"group":true}`, http.StatusOK, "")
	})
	if got := scanOf(t, r, true); got != id.String() {
		t.Errorf("the override joined scan %s, want the running %s", got, id)
	}
	var d *jobs.Defer
	if !errors.As(err, &d) || d.Reason != index.DeferRescanRequested {
		t.Fatalf("the pass that ended with the request returned %v", err)
	}
	if err := w.scanPass(t, id, nil); err != nil {
		t.Fatal(err)
	}
	w.finishJob(t, "succeeded")

	after := w.rows(t)
	moved := family(t, before["Fotos"], domain.FamilyDisposable) - family(t, after["Fotos"], domain.FamilyDisposable)
	if !after[praia].Group || moved <= 0 || family(t, after[praia], domain.FamilyDisposable) != moved {
		t.Errorf("not converged: Praia group %v, Fotos %s -> %s", after[praia].Group, before["Fotos"].ByFamily.String,
			after["Fotos"].ByFamily.String)
	}
	var requested bool
	if err := w.st.Reader().QueryRow(`SELECT rescan_requested FROM sources WHERE id = 'corpus'`).Scan(&requested); err != nil || requested {
		t.Errorf("rescan_requested %v (%v)", requested, err)
	}
}
