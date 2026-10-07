// Package index is the scanner (§7, design D7): the "scan" job, which walks
// a source and writes one entries row per entry, the folder totals and
// dir_stats of every folder, the name index, and the rules' classification,
// in a single pass.
//
// The walk is depth-first with an explicit stack. It lists every folder in
// batches (scan.list_batch), Lstats every entry, never follows a symlink,
// never opens a special file, and records a mount boundary as a folder with
// no children. Row operations go to one writer goroutine through a queue of
// at most four batches, and the writer commits each batch of up to
// scan.batch_size operations in one transaction, so walking and writing
// overlap and a slow database slows the walk instead of filling memory.
// Operations name entries the scan inserted by path; the writer resolves
// them to the IDs it assigned.
//
// A folder's totals are final once its last child is done (post-order), so
// its row is written then: totals, the newest and oldest file times, the
// main kind, dir_stats (counts, breakdowns by kind, year, and family, name
// signals, and up to 20 indicator examples), and its classification.
//
// A rescan reads each stored folder's children before listing it and writes
// only what changed: an unchanged entry (same kind and size, a modification
// time and, when both are known, a change time within the filesystem's
// tolerance, design D8 and R2 D4, and on filesystems with stable identity
// the same object) is not written. A changed entry is updated, a new one
// inserted, and a stored one that a complete listing no longer shows goes
// missing with its subtree; it keeps its ID, decision, and
// tags, and returns under the same ID. A different kind at the same path is
// a different entry: the old row and its subtree are deleted. A folder whose
// listing fails is unreadable, keeps its stored children, and makes its
// ancestors partial. A cancelled scan keeps the rows it wrote and marks the
// folders it had not finished partial; the next scan starts again at the
// root. An update of a file whose own facts changed also deletes its
// file_content and archives rows in the same batch (R2 D4), and a scan that
// finishes successfully runs the OnScanDone hook.
//
// The owner's overrides (entry_overrides, r2b design D3) are read when a
// scan starts and applied through rules.ApplyOwner right after the rules
// classify an entry, so rows, compositions, and inside lists come out as
// for a rule result, and a rescan with unchanged overrides writes nothing.
// An override set while a scan runs sets sources.rescan_requested; the scan
// that ends with it set clears it and runs its job once more (jobs.Defer),
// and only the last pass runs the OnScanDone hook.
package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"

	"precious/internal/clock"
	"precious/internal/commands"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
)

// Handler runs scan jobs. It implements jobs.Handler.
type Handler struct {
	st  *store.Store
	src *sources.Service
	pol *rules.Policy
	clk clock.Clock
	cfg config.Scan
	// done, when set, runs after each successful scan (OnScanDone).
	done func(ctx context.Context, src domain.SourceID)
}

var _ jobs.Handler = (*Handler)(nil)

// NewHandler returns the scan handler. Zero batch sizes in cfg take the
// configuration defaults.
func NewHandler(st *store.Store, src *sources.Service, pol *rules.Policy, clk clock.Clock, cfg config.Scan) *Handler {
	d := config.Defaults().Scan
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = d.BatchSize
	}
	if cfg.ListBatch <= 0 {
		cfg.ListBatch = d.ListBatch
	}
	if clk == nil {
		clk = clock.Real{}
	}
	return &Handler{st: st, src: src, pol: pol, clk: clk, cfg: cfg}
}

// Register registers the scan kind with r, in the reconciliation class
// (design D15). Call it before r.Start.
func (h *Handler) Register(r *jobs.Runner) {
	r.RegisterClass(jobs.KindScan, h, jobs.ClassReconciliation)
}

// OnScanDone sets fn to run once after each scan that finishes successfully,
// never after a failed or cancelled one (R2 design D5: serve wires it to
// the hashing service, so index imports neither content nor relations).
// Call it before Register.
func (h *Handler) OnScanDone(fn func(ctx context.Context, src domain.SourceID)) {
	h.done = fn
}

// Run scans the job's source.
func (h *Handler) Run(ctx context.Context, job jobs.Job, rt jobs.Runtime) error {
	done := rt.FSCall("OpenRoot")
	opened, err := h.src.Open(ctx, job.SourceID)
	done()
	if err != nil {
		return err
	}
	defer opened.Root.Close()

	root, err := readRoot(ctx, h.st.Reader(), job.SourceID)
	if err != nil {
		return err
	}
	children, err := h.st.Reader().PrepareContext(ctx, childrenQuery)
	if err != nil {
		return err
	}
	defer children.Close()
	// The owner's overrides as they are now: one set later reaches this
	// scan's source through rescan_requested (r2b design D3).
	owner, err := readOverrides(ctx, h.st.Reader(), job.SourceID)
	if err != nil {
		return err
	}

	walkCtx, stopWalk := context.WithCancelCause(ctx)
	defer stopWalk(nil)
	gen, now := opened.Source.ScanGen+1, clock.Millis(h.clk.Now())
	w, err := newWriter(ctx, h.st, job, gen, now, stopWalk)
	if err != nil {
		return err
	}
	s := &walk{
		ctx: walkCtx, rt: rt, pol: h.pol, caps: opened.Source.Caps, w: w, children: children,
		listN: h.cfg.ListBatch, batchN: h.cfg.BatchSize, codec: newCodec(), progress: map[string]int64{},
		held: map[uint64]int32{}, owner: owner,
	}
	s.report()
	walkErr := s.run(opened.Root, []byte(filepath.Base(opened.AbsRoot)), root)
	if walkErr != nil {
		walkErr = cmpErr(walkErr, s.abort())
	}
	s.close()
	walkErr = cmpErr(walkErr, s.flush())
	close(w.in)
	<-w.done
	switch {
	case w.err != nil:
		return w.err
	case errors.Is(walkErr, errSourceGone):
		return domain.Wrap(domain.CodeSourceOffline, walkErr, "source %q became unavailable during its scan", job.SourceID)
	case walkErr != nil:
		return walkErr
	}
	s.report()
	s.progress[ProgressPhase] = PhaseFinishing
	rt.Progress(s.progress)
	again, err := w.finishScan(ctx, h.pol.Version())
	if err != nil {
		return err
	}
	if again {
		// An override arrived during this pass, which may have read the
		// overrides before it: one more pass converges (r2b design D3). The
		// job runs again at once; the after-scan hook waits for its end.
		return &jobs.Defer{Until: h.clk.Now(), Reason: DeferRescanRequested}
	}
	if h.done != nil {
		h.done(ctx, job.SourceID)
	}
	return nil
}

// DeferRescanRequested is the reason of a scan job that runs one more pass
// because the owner overrode a classification during the pass that ended.
const DeferRescanRequested = "rescan_requested"

// cmpErr returns the first non-nil error.
func cmpErr(first, then error) error {
	if first != nil {
		return first
	}
	return then
}

// StartScan queues a scan of src, or returns its active scan (single
// flight, design D15). An unknown source is unknown_source and one that is
// not online is source_offline.
func StartScan(ctx context.Context, tx *jobs.Tx, src domain.SourceID) (jobs.Accepted, error) {
	var state string
	err := tx.SQL().QueryRowContext(ctx, `SELECT state FROM sources WHERE id = ?`, string(src)).Scan(&state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return jobs.Accepted{}, domain.Errorf(domain.CodeUnknownSource, "unknown source %q", src)
		}
		return jobs.Accepted{}, err
	}
	if state != string(sources.StateOnline) {
		return jobs.Accepted{}, domain.Errorf(domain.CodeSourceOffline, "source %q is %s", src, state)
	}
	rec, coalesced, err := tx.StartScan(src)
	if err != nil {
		return jobs.Accepted{}, err
	}
	return rec.Accepted(coalesced), nil
}

// CommandStartScan is the name of the start-scan command.
const CommandStartScan = "start-scan"

// RegisterCommands registers start-scan {"source_id"} with h: 202
// {"job_id","state","coalesced"}; 404 unknown_source; 409 source_offline.
func RegisterCommands(h *commands.Handler) {
	h.Register(CommandStartScan, func(body []byte) (commands.Operation, error) {
		var req startScanRequest
		if err := commands.DecodeStrict(body, &req); err != nil {
			return nil, err
		}
		if req.SourceID == "" {
			return nil, domain.Errorf(domain.CodeInvalidRequest, "source_id is required")
		}
		return &startScanOp{req: req}, nil
	})
}

type startScanRequest struct {
	SourceID domain.SourceID `json:"source_id"`
}

type startScanOp struct{ req startScanRequest }

func (o *startScanOp) Canonical() []byte {
	b, err := json.Marshal(o.req)
	if err != nil {
		panic(err) // a struct of one string always marshals
	}
	return b
}

func (o *startScanOp) Prepare(context.Context) error { return nil }

func (o *startScanOp) Apply(ctx context.Context, tx *jobs.Tx) (int, any, error) {
	acc, err := StartScan(ctx, tx, o.req.SourceID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusAccepted, acc, nil
}
