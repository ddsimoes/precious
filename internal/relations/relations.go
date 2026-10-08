// Package relations computes how folders relate by content across every
// source (R2 design D9–D11): the snapshot of the index reduced to compact
// arrays, the partner search, maximal results, and wrapper lifting restored
// from curator-m4b's relate.go; the relate job that stores a generation of
// relations and the folder duplication figures (dir_dups); RelationsOf for
// the detail panel; and Compare, which splits two folders' files into
// buckets on request.
//
// Imports go one way: content → relations ← review. This package reads the
// rows that content writes (file_content, contents, archives,
// archive_members) and never imports content.
package relations

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"precious/internal/domain"
	"precious/internal/jobs"
)

// KindRelate is the relate job's kind (design D5): pool "relate",
// capacity 1, single flight by scope "relate", plus at most one follow-up
// by scope "relate:next" queued while a "relate" job runs (design Addendum
// G1). A job enqueued by RequestRefresh or Startup runs a pass only while
// review_state.dirty is set.
const KindRelate jobs.Kind = "relate"

// relateScope is the EnqueueOnce scope of the relate job, and
// relateNextScope the scope of its follow-up, enqueued when the "relate"
// job is already running.
const (
	relateScope     = "relate"
	relateNextScope = "relate:next"
)

// ifDirtyPayload is the payload of the relate jobs that RequestRefresh and
// Startup enqueue: the job runs a pass only while the relations are dirty.
var ifDirtyPayload = json.RawMessage(`{"if_dirty":true}`)

// relatePool is the runner pool of the relate job.
const relatePool = "relate"

// overlapPercent is the share of one side's bytes an overlap needs (design
// D9, D18: "a large share"), and candidatePercent the provisional share at
// which Candidates proposes a pair (design D4).
const (
	overlapPercent   = 50
	candidatePercent = 50
)

// largeFileBytes is the bound at and above which hashing reads files before
// the small ones (design D4, D18: 1 MiB). In a provisional snapshot a
// hashed file or member below it is keyed by its size, like the unread
// small files it could match.
const largeFileBytes = 1 << 20

// Range is the half-open range [From, To) of the paths of Source's entries
// below one side of a candidate pair (design D4):
//   - a folder at path p: the paths below p, p+"/" up to p+"0"; the source
//     root (p empty): every non-empty path, From = "\x00" and To nil
//     (unbounded);
//   - an archive, or a folder inside one: the archive file's own path, from
//     p up to p+"\x00", whose members hashing reads as the archive's.
type Range struct {
	Source   domain.SourceID
	From, To []byte
}

// Contains reports whether path lies in r (on r's source).
func (r Range) Contains(path []byte) bool {
	return string(path) >= string(r.From) && (r.To == nil || string(path) < string(r.To))
}

// descendants is the Range of the paths below the folder at p.
func descendants(src domain.SourceID, p []byte) Range {
	if len(p) == 0 {
		return Range{Source: src, From: []byte{0}}
	}
	return Range{Source: src, From: append(p[:len(p):len(p)], '/'), To: append(p[:len(p):len(p)], '0')}
}

// itself is the Range holding only path p.
func itself(src domain.SourceID, p []byte) Range {
	return Range{Source: src, From: p[:len(p):len(p)], To: append(p[:len(p):len(p)], 0)}
}

// RequestRefresh marks the relations dirty and enqueues the relate job
// once, inside the caller's transaction (design D5). A run already going
// sees the flag at its flip and runs again. A refresh arriving after that
// read, while the run prunes or ends, finds the "relate" job running, so a
// follow-up job is enqueued too (scope "relate:next", design Addendum G1);
// it runs a pass if the flag is still set when it starts. A refresh is
// never lost.
func RequestRefresh(tx *jobs.Tx) error {
	if err := setDirty(tx.SQL()); err != nil {
		return err
	}
	return enqueueRelate(tx)
}

// enqueueRelate enqueues the relate job once by scope "relate" and, when
// that job is already running, its follow-up once by scope "relate:next".
// The pool runs one relate job at a time, so the two scopes cover a
// running job and the one that must run after it.
func enqueueRelate(tx *jobs.Tx) error {
	rec, _, err := tx.EnqueueOnce(jobs.Spec{Kind: KindRelate, ScopeKey: relateScope, Payload: ifDirtyPayload})
	if err != nil {
		return fmt.Errorf("relations: enqueue relate: %w", err)
	}
	if rec.State != domain.JobRunning {
		return nil
	}
	if _, _, err := tx.EnqueueOnce(jobs.Spec{Kind: KindRelate, ScopeKey: relateNextScope, Payload: ifDirtyPayload}); err != nil {
		return fmt.Errorf("relations: enqueue relate follow-up: %w", err)
	}
	return nil
}

func setDirty(tx *sql.Tx) error {
	if _, err := tx.Exec(`UPDATE review_state SET dirty = 1 WHERE id = 1`); err != nil {
		return fmt.Errorf("relations: mark dirty: %w", err)
	}
	return nil
}
