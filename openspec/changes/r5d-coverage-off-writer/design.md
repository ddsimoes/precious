# Design

## Context

`content_coverage` holds one row per source with its candidate, checked, unchecked, and unreadable files and bytes (§7, I7). Two paths write it:

- **Deltas.** Hashing commits and archive listing apply deltas (`deltas.apply`), adding to the stored figures in the same write transaction as the rows they change.
- **Full recomputation.** `recomputeCoverage` (`plan.go`) deletes every row, then inserts an aggregate over `coverageRows`: every present file outside the quarantine, and every file member of a complete, present archive. It runs once per plan, inside the write transaction of the plan's last batch.
  - **Why it is needed:** the plan's state changes, the rows `insertRows` adds, and scans that mark entries missing all change the figures without deltas, so the recomputation is what makes them exact.
  - **The cost:** on the reference server the aggregate reads about 4.6 million rows in about 43 s, while the writer waits.

`plan` holds `planMu` throughout. Since r5c (A11), `insertRows` runs before `planMu` is taken, and the state writes come after it.

## Goals / Non-Goals

**Goals:**
- No write transaction runs the aggregate.
- Coverage stays exact: equal to a full recomputation once the plan ends.
- Concurrent deltas are never lost.

**Non-Goals:**
- A faster aggregate.
- Incremental coverage for scans, `insertRows`, or the plan's state changes.

## Decisions

### D1. The aggregate is computed in a read transaction, and the drift is applied in a write transaction (§7, I7)

`recomputeCoverage(ctx, tx, now)` is replaced by two steps, still run by the plan after its last state batch and while it holds `planMu`:

1. **`coverageDrift(ctx, st)`.** In one read transaction, so on one snapshot S, it reads:
   - the aggregate per source, `computed(S)` (the same SQL as today);
   - the stored rows of `content_coverage`, `stored(S)`.

   It returns `drift = computed(S) − stored(S)` per source, all 8 figures. A source with a row but no rows of its own drifts to zero. A source with rows but no stored row starts from zero.
2. **`applyDrift(ctx, tx, drift, now)`.** In one short write transaction it adds the drift to the stored rows: `UPDATE … SET x = x + drift`, inserting a missing row first. It skips sources that no longer exist, and leaves sources with zero drift untouched.

**Why it is exact:** a commit after S that changes coverage does so through deltas. The stored figures then become `stored(S) + Δ`, and the result is `computed(S) + Δ`: the full recomputation at write time, provided every change after S carries deltas.

**Changes after S that carry no deltas:**
- another job's `insertRows`, which runs outside `planMu`;
- a scan marking entries missing.

They stay out of the figures, as they do today when they commit after the recomputation. The next plan repairs them: that job's plan, or the plan that runs after the scan.

**`updated_at`** is set to `now` on the corrected rows.

Rejected:
- **Recompute in a read transaction, then overwrite the rows in a write transaction:** this loses the deltas committed in between.
- **Overwrite only if nothing changed in between, with a version counter:** it needs a migration and can starve under constant hashing.
- **Make every path incremental, with no recomputation:** scans and `insertRows` would each need deltas. That is a larger change, and the drift repair would still be needed.

### D2. A test seam

`Service` gets an unexported hook, `coverageRead func()`, that runs while the read transaction of `coverageDrift` is open. It is nil in production. Tests use it to commit a write meanwhile and to check that it does not wait.

## Interfaces

```go
// internal/content/plan.go (unexported)
type coverageFigures [8]int64 // candidate files/bytes, checked, unchecked, unreadable
func coverageDrift(ctx context.Context, st *store.Store) (map[string]coverageFigures, error)
func applyDrift(ctx context.Context, tx *sql.Tx, drift map[string]coverageFigures, now int64) error
```

`recomputeCoverage` is removed from `plan.go`. The test helper `indextest.recomputeCoverage` is a separate function and stays.

## Concurrency

- **The read transaction** holds no lock that blocks writers (WAL).
- **The write transaction** checks that each source exists (`INSERT … SELECT … WHERE EXISTS (SELECT 1 FROM sources …)`). A source removed after S is skipped, and its row is gone by cascade.
- **A hashing commit between S and the write** applies its deltas; D1 keeps them.
- **Another plan** cannot run in between, because `planMu` is held.
- **Another job's `insertRows`** in between is repaired by that job's own plan (D1).

## Risks / Trade-offs

- **[A change after S without deltas]** It stays out of the figures until the next plan, as today when it commits after the recomputation. → No regression.
- **[The plan holds `planMu` about 43 s longer than its writes]** That blocks only other plans, not HTTP or other writes. → Accepted.

## Migration Plan

Deploy over r5c with no migration: back up the database, keep the previous binary, and restart.
