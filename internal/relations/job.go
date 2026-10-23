package relations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/store"
)

// The relate job (design D5, D6, D10). Each pass:
//  1. clears review_state.dirty and reads the visible generation g;
//  2. loads the snapshot of every source in one read transaction;
//  3. relates it and writes generation g+1 of relations in batches, each
//     row inserted only while its entries and members exist;
//  4. upserts dir_dups and the places tables (entry_places, archive_places,
//     member_places) in place, writing only the rows that changed;
//  5. calls the after hook with g+1 (the review lists write their rows);
//  6. flips review_state.gen to g+1 in one transaction, so readers never
//     see two generations, and reads dirty: a refresh requested during the
//     pass up to here makes the job run another pass;
//  7. deletes generation g (relations and review rows) and the contents
//     no row references, in batches. A refresh requested from here until
//     the job ends is left to the follow-up job RequestRefresh enqueues
//     (design Addendum G1).

// Batch sizes: rows per write transaction.
const (
	insertBatch = 500
	deleteBatch = 5000
	pruneBatch  = 5000
)

// Progress phases of the relate job.
const (
	phaseLoad = iota + 1
	phaseRelate
	phaseWrite
	phaseReview
	phasePrune
)

// Handler runs the relate job.
type Handler struct {
	st       *store.Store
	clk      clock.Clock
	cfg      config.Duplicates
	after    func(ctx context.Context, gen int64) error
	classify classifier

	// stage, when set (tests), is called at each step of a pass: "begin",
	// "loaded", "written" (relations, dir_dups, and the places tables of the
	// new generation), "flipped".
	stage func(ctx context.Context, name string, gen int64)
}

// NewHandler returns the relate job's handler. after, when not nil, runs
// with each new generation before it becomes visible (the review lists'
// Refresh); an error fails the pass, and the generation is never shown.
// cfg is the [duplicates] section: its refresh interval paces the hashing
// jobs' requests, not this job.
func NewHandler(st *store.Store, clk clock.Clock, cfg config.Duplicates, after func(ctx context.Context, gen int64) error) *Handler {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Handler{st: st, clk: clk, cfg: cfg, after: after}
}

// Register installs the relate job in pool "relate" of capacity 1.
func (h *Handler) Register(r *jobs.Runner) { r.RegisterPool(KindRelate, h, relatePool, 1) }

// SetClassifier gives the relate pass the member classification of D14
// (wired in cmd/precious/serve.go with the server's policy); without it no
// member is disposable. Call it before the runner starts.
func (h *Handler) SetClassifier(f func(name []byte, size int64) (domain.Category, domain.FileKind, domain.Triage)) {
	h.classify = f
}

// Startup enqueues the relate job when the relations are dirty (design D5:
// a refresh that a stopped server never ran, or a new database).
func (h *Handler) Startup(ctx context.Context, r *jobs.Runner) error {
	return r.Write(ctx, func(tx *jobs.Tx) error {
		var dirty bool
		if err := tx.SQL().QueryRowContext(ctx, `SELECT dirty FROM review_state WHERE id = 1`).Scan(&dirty); err != nil {
			return fmt.Errorf("relations: startup: %w", err)
		}
		if !dirty {
			return nil
		}
		return enqueueRelate(tx)
	})
}

// Run runs passes until no refresh was requested during the last one. A
// first attempt whose payload has if_dirty (every job RequestRefresh and
// Startup enqueue) runs none when the relations are clean: a pass that
// began after the last refresh, in the job that ran before, covered it
// (design Addendum G1). A retry after a lost worker, or a job without the
// flag, always runs a pass. A pass that fails marks the relations dirty
// again: it cleared the flag at its start, and its generation may never
// show, so the follow-up job or the next Startup runs the pass again.
func (h *Handler) Run(ctx context.Context, job jobs.Job, rt jobs.Runtime) error {
	if job.Attempt <= 1 && len(job.Payload) > 0 {
		var p struct {
			IfDirty bool `json:"if_dirty"`
		}
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("relations: relate payload: %w", err)
		}
		if p.IfDirty {
			var dirty bool
			err := h.st.Read(ctx, func(tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, `SELECT dirty FROM review_state WHERE id = 1`).Scan(&dirty)
			})
			if err != nil {
				return fmt.Errorf("relations: read dirty: %w", err)
			}
			if !dirty {
				return nil
			}
		}
	}
	for {
		again, err := h.pass(ctx, rt)
		if err != nil {
			if derr := h.st.Write(context.WithoutCancel(ctx), setDirty); derr != nil {
				return errors.Join(err, derr)
			}
			return err
		}
		if !again {
			return nil
		}
	}
}

func (h *Handler) mark(ctx context.Context, name string, gen int64) {
	if h.stage != nil {
		h.stage(ctx, name, gen)
	}
}

// pass computes and shows one generation; it reports whether a refresh was
// requested meanwhile.
func (h *Handler) pass(ctx context.Context, rt jobs.Runtime) (again bool, err error) {
	var cur int64
	err = h.st.Write(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT gen FROM review_state WHERE id = 1`).Scan(&cur); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE review_state SET dirty = 0 WHERE id = 1`)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("relations: begin: %w", err)
	}
	gen := cur + 1
	h.mark(ctx, "begin", gen)
	rt.Progress(map[string]int64{"phase": phaseLoad})

	// Rows of an earlier attempt at this generation.
	if err := h.deleteGen(ctx, rt, `gen > ?`, cur); err != nil {
		return false, err
	}

	var s *Snapshot
	err = h.st.Read(ctx, func(tx *sql.Tx) error {
		var err error
		s, err = LoadSnapshot(ctx, tx, h.classify)
		return err
	})
	if err != nil {
		return false, err
	}
	h.mark(ctx, "loaded", gen)
	if err := rt.Yield(ctx); err != nil {
		return false, err
	}
	rt.Progress(map[string]int64{"phase": phaseRelate, "folders": int64(len(s.parent))})
	rels := s.Relate()
	if err := rt.Yield(ctx); err != nil {
		return false, err
	}

	rt.Progress(map[string]int64{"phase": phaseWrite})
	if err := h.insertRelations(ctx, rt, gen, rels); err != nil {
		return false, err
	}
	rels = nil
	if err := h.writeDirDups(ctx, rt, s.dirDups()); err != nil {
		return false, err
	}
	if err := h.writePlaces(ctx, rt, s.placeRows()); err != nil {
		return false, err
	}
	s = nil
	h.mark(ctx, "written", gen)

	rt.Progress(map[string]int64{"phase": phaseReview})
	if h.after != nil {
		if err := h.after(ctx, gen); err != nil {
			return false, fmt.Errorf("relations: after generation %d: %w", gen, err)
		}
	}

	err = h.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE review_state SET gen = ?, computed_at = ? WHERE id = 1`,
			gen, clock.Millis(h.clk.Now())); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT dirty FROM review_state WHERE id = 1`).Scan(&again)
	})
	if err != nil {
		return false, fmt.Errorf("relations: show generation %d: %w", gen, err)
	}
	h.mark(ctx, "flipped", gen)

	rt.Progress(map[string]int64{"phase": phasePrune})
	if err := h.deleteGen(ctx, rt, `gen < ?`, gen); err != nil {
		return false, err
	}
	if err := h.pruneContents(ctx, rt); err != nil {
		return false, err
	}
	return again, nil
}

// deleteGen deletes the relations and review rows of the generations
// matching cond (with arg), in batches.
func (h *Handler) deleteGen(ctx context.Context, rt jobs.Runtime, cond string, arg int64) error {
	for _, table := range []string{"review_rows", "relations"} {
		q := `DELETE FROM ` + table + ` WHERE id IN (SELECT id FROM ` + table + ` WHERE ` + cond + ` LIMIT ?)`
		for {
			var n int64
			err := h.st.Write(ctx, func(tx *sql.Tx) error {
				res, err := tx.ExecContext(ctx, q, arg, deleteBatch)
				if err != nil {
					return err
				}
				n, err = res.RowsAffected()
				return err
			})
			if err != nil {
				return fmt.Errorf("relations: delete %s: %w", table, err)
			}
			if err := rt.Yield(ctx); err != nil {
				return err
			}
			if n < deleteBatch {
				break
			}
		}
	}
	return nil
}

// insertRelations writes rels as generation gen, each row only while its
// entries and members exist (a source removed or a rescan meanwhile).
func (h *Handler) insertRelations(ctx context.Context, rt jobs.Runtime, gen int64, rels []result) error {
	for i := 0; i < len(rels); i += insertBatch {
		batch := rels[i:min(i+insertBatch, len(rels))]
		err := h.st.Write(ctx, func(tx *sql.Tx) error {
			stmt, err := tx.PrepareContext(ctx, `INSERT INTO relations (gen, kind, a_entry, a_member, b_entry, b_member,
					matched_bytes, redundant_bytes, a_bytes, a_files, b_bytes, b_files,
					a_only_files, a_only_bytes, b_only_files, b_only_bytes)
				SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16
				WHERE EXISTS (SELECT 1 FROM entries WHERE id = ?3) AND EXISTS (SELECT 1 FROM entries WHERE id = ?5)
				AND (?4 IS NULL OR EXISTS (SELECT 1 FROM archive_members WHERE id = ?4 AND archive_id = ?3))
				AND (?6 IS NULL OR EXISTS (SELECT 1 FROM archive_members WHERE id = ?6 AND archive_id = ?5))`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, r := range batch {
				if _, err := stmt.ExecContext(ctx, gen, r.Kind, int64(r.A.Entry), optMember(int64(r.A.Member)),
					int64(r.B.Entry), optMember(int64(r.B.Member)), r.MatchedBytes, r.Redundant,
					r.ABytes, r.AFiles, r.BBytes, r.BFiles, r.AOnlyFiles, r.AOnlyBytes, r.BOnlyFiles, r.BOnlyBytes); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("relations: write generation %d: %w", gen, err)
		}
		if err := rt.Yield(ctx); err != nil {
			return err
		}
	}
	return nil
}

func optMember(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// DirDups is one folder's duplication figures (design D10).
type DirDups struct {
	CandidateBytes, CheckedBytes, DuplicatedBytes, DuplicatedFiles int64
}

// dirDups computes every entry folder's figures bottom-up: candidate and
// checked bytes over its subtree's file_content rows (D8), and the files
// with another copy anywhere (members of complete archives and offline
// sources included; one hard-link set is one copy). Archives count as
// their file, never by their members.
func (s *Snapshot) dirDups() map[int64]DirDups {
	dd := s.dups
	n := len(s.parent)
	agg := make([]DirDups, n)
	for d := range n {
		if s.arc[d] >= 0 {
			continue
		}
		a := &agg[d]
		a.CandidateBytes, a.CheckedBytes = dd.own[d].candidate, dd.own[d].checked
		for e := s.first[d]; e < s.first[d+1]; e++ {
			if k := s.eKey[e]; s.kKind[k] == keyContent && dd.copies[k] >= 2 {
				a.DuplicatedBytes += s.eSize[e]
				a.DuplicatedFiles++
			}
		}
	}
	for _, f := range dd.arcFiles {
		if f.content == 0 {
			continue
		}
		copies := dd.arcExtra[f.content]
		if k := dd.arcKeys[f.content]; k >= 0 {
			copies = dd.copies[k]
		}
		if copies >= 2 {
			agg[f.dir].DuplicatedBytes += f.size
			agg[f.dir].DuplicatedFiles++
		}
	}
	out := make(map[int64]DirDups, n)
	for d := n - 1; d >= 0; d-- {
		if s.arc[d] >= 0 {
			continue
		}
		if p := s.parent[d]; p >= 0 {
			q := &agg[p]
			q.CandidateBytes += agg[d].CandidateBytes
			q.CheckedBytes += agg[d].CheckedBytes
			q.DuplicatedBytes += agg[d].DuplicatedBytes
			q.DuplicatedFiles += agg[d].DuplicatedFiles
		}
		out[s.id[d]] = agg[d]
	}
	return out
}

// figureTable is a table of figures per entry or member that each pass
// keeps in step with its snapshot (dir_dups and the places tables): a key
// column into the owner table and int64 columns.
type figureTable[T comparable] struct {
	name   string
	key    string // the key column
	owner  string // the table the key's IDs are rows of
	cols   []string
	fields func(v *T) []*int64 // v's columns, in the order of cols
}

var dirDupsTable = figureTable[DirDups]{
	name:  "dir_dups",
	key:   "entry_id",
	owner: "entries",
	cols:  []string{"candidate_bytes", "checked_bytes", "duplicated_bytes", "duplicated_files"},
	fields: func(d *DirDups) []*int64 {
		return []*int64{&d.CandidateBytes, &d.CheckedBytes, &d.DuplicatedBytes, &d.DuplicatedFiles}
	},
}

// writeDirDups upserts the rows of want that differ from the stored ones,
// each only while its folder exists, and deletes the rows of folders no
// longer in the snapshot.
func (h *Handler) writeDirDups(ctx context.Context, rt jobs.Runtime, want map[int64]DirDups) error {
	return writeFigures(ctx, h, rt, dirDupsTable, want)
}

// writeFigures upserts the rows of want that differ from the rows of table
// t, each only while its entry or member exists, and deletes the rows of
// keys not in want, insertBatch changes per transaction.
func writeFigures[T comparable](ctx context.Context, h *Handler, rt jobs.Runtime, t figureTable[T], want map[int64]T) error {
	have := make(map[int64]T, len(want))
	err := h.st.Read(ctx, func(tx *sql.Tx) error {
		var id int64
		var v T
		dest := []any{&id}
		for _, p := range t.fields(&v) {
			dest = append(dest, p)
		}
		return scanAll(ctx, tx, `SELECT `+t.key+`, `+strings.Join(t.cols, ", ")+` FROM `+t.name, nil,
			func(r *sql.Rows) error {
				if err := r.Scan(dest...); err != nil {
					return err
				}
				have[id] = v
				return nil
			})
	})
	if err != nil {
		return fmt.Errorf("relations: read %s: %w", t.name, err)
	}
	type change struct {
		id   int64
		v    T
		gone bool
	}
	var changes []change
	for id, v := range want {
		if old, ok := have[id]; !ok || old != v {
			changes = append(changes, change{id: id, v: v})
		}
	}
	for id := range have {
		if _, ok := want[id]; !ok {
			changes = append(changes, change{id: id, gone: true})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	params, set := make([]string, len(t.cols)), make([]string, len(t.cols))
	for i, c := range t.cols {
		params[i] = "?" + strconv.Itoa(i+2)
		set[i] = c + " = excluded." + c
	}
	upsert := `INSERT INTO ` + t.name + ` (` + t.key + `, ` + strings.Join(t.cols, ", ") + `)
		SELECT ?1, ` + strings.Join(params, ", ") + ` WHERE EXISTS (SELECT 1 FROM ` + t.owner + ` WHERE id = ?1)
		ON CONFLICT (` + t.key + `) DO UPDATE SET ` + strings.Join(set, ", ")
	args := make([]any, 1+len(t.cols))
	for i := 0; i < len(changes); i += insertBatch {
		batch := changes[i:min(i+insertBatch, len(changes))]
		err := h.st.Write(ctx, func(tx *sql.Tx) error {
			up, err := tx.PrepareContext(ctx, upsert)
			if err != nil {
				return err
			}
			defer up.Close()
			for _, c := range batch {
				if c.gone {
					if _, err := tx.ExecContext(ctx, `DELETE FROM `+t.name+` WHERE `+t.key+` = ?`, c.id); err != nil {
						return err
					}
					continue
				}
				args[0] = c.id
				for j, p := range t.fields(&c.v) {
					args[j+1] = *p
				}
				if _, err := up.ExecContext(ctx, args...); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("relations: write %s: %w", t.name, err)
		}
		if err := rt.Yield(ctx); err != nil {
			return err
		}
	}
	return nil
}

// pruneContents deletes the contents rows no file, member, or review row
// references, pruneBatch candidates per transaction (design Interfaces).
func (h *Handler) pruneContents(ctx context.Context, rt jobs.Runtime) error {
	after := int64(0)
	for {
		var last int64
		var n int
		err := h.st.Write(ctx, func(tx *sql.Tx) error {
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0), COUNT(*) FROM
				(SELECT id FROM contents WHERE id > ? ORDER BY id LIMIT ?)`, after, pruneBatch).Scan(&last, &n); err != nil {
				return err
			}
			if n == 0 {
				return nil
			}
			_, err := tx.ExecContext(ctx, `DELETE FROM contents WHERE id > ?1 AND id <= ?2
				AND NOT EXISTS (SELECT 1 FROM file_content f WHERE f.content_id = contents.id)
				AND NOT EXISTS (SELECT 1 FROM archive_members m WHERE m.content_id = contents.id)
				AND NOT EXISTS (SELECT 1 FROM review_rows r WHERE r.content_id = contents.id)`, after, last)
			return err
		})
		if err != nil {
			return fmt.Errorf("relations: prune contents: %w", err)
		}
		if n == 0 {
			return nil
		}
		if err := rt.Yield(ctx); err != nil {
			return err
		}
		if n < pruneBatch {
			return nil
		}
		after = last
	}
}
