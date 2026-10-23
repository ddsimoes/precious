# Proposal

## Why

On 2026-10-23, right after the test server restarted, the owner could not sign in for a few minutes. Every request that writes, such as sign-in, a session refresh, or a decision, waited and then failed.

The cause is older than R5c (r5c design Addendum A20):
- Each hashing job's plan ends with `recomputeCoverage`, which rewrites `content_coverage` from an aggregate over every file and archive member.
- On the reference server that aggregate reads about 4.6 million rows and takes about 43 s.
- It runs inside a write transaction, so the single writer is held for about a minute.
- At startup, one hashing job per online source runs its plan, so the server stalls about a minute per source. The same happens after a scan, and whenever archive listing adds members.

The owner chose to fix this in a short step after R5c ("A", 2026-10-23). This change is milestone **R5d**.

## What Changes

- The coverage aggregate is computed in a read transaction, which does not hold the writer.
- A short write transaction then corrects `content_coverage` by the drift the aggregate found: the computed figures minus the stored figures, both read in the same snapshot.
  - Coverage changes committed in the meantime (hashing deltas) are kept, not overwritten.
  - The result equals what a full recomputation at that moment would give.
- Nothing else changes: when coverage is recomputed, what it counts, and what is shown.

## Acceptance

R2.1 and the coverage scenarios keep passing. The new scenarios in the delta spec are checked by:
- **unit tests**, through a test seam that holds the aggregate open while another write commits;
- **the reference server:** after a restart that starts hashing jobs on three sources, sign-in answers within 2 s throughout.

The owner's sign-off follows.

Not in scope:
- making the aggregate itself faster;
- the hours a startup hashing job can spend in its candidate small-file pass (r5c A19);
- removing a large source, which holds the writer for minutes (r5c A18).

## Capabilities

### New Capabilities
None.

### Modified Capabilities
- `file-index`: coverage is recomputed without holding the writer for the aggregate.

## Impact

- **Go:** `internal/content/plan.go`, which splits `recomputeCoverage` into compute (read) and apply (write), plus its tests.
- **Docs:** `docs/operator.md`, in the Coverage section (one paragraph).
- **No migration, API change, or UI change.**
