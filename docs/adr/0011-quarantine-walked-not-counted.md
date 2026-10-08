# ADR 0011: The quarantine is walked by scans, but never counted

- Status: accepted
- Date: 2026-10-08
- Context: `precious-spec-v0.3.md` §10.3, §5 I4 and I7; OpenSpec change `r4-cleanup`, design D1 and D2.

## Context

§10.3 describes the quarantine as "a folder that Precious controls on the same filesystem as the item, excluded from scans". R4 keeps quarantined entries in the index, so that their IDs, decisions, tags, and digests survive a restore (I4).

A plan review found a flaw in reading "excluded from scans" as "never walked". The executor journals each step and updates the index when the step's outcome is recorded. Suppose a crash, or a failed index update, lands between a rename into the quarantine and its outcome. The file is then in the quarantine with no index row for it, and the owner's "I fixed it" starts a scan, which could never see it. That file could neither be restored nor purged, and it would count nowhere (I7).

## Decision

Scans walk `.precious-quarantine` at the top of a source like any folder, so its index rows always match the disk. Everything a scan counts or reports leaves it out:
- the top folder's fold does not absorb it, so source, Home, and folder totals exclude it;
- hashing does not enrol or read it;
- size groups, coverage, review lists, relations, copies, the Map, and Search all exclude it by path.

The quarantine folder's own row still folds its bytes, which Home shows as "In quarantine".

"Excluded from scans" is read as excluded from what a scan counts and reports, not from its walk.

## Alternatives rejected

- **Skipping the quarantine in the walk.** Crash recovery would then need its own quarantine listing, apart from resolve-recovery's scan. A file left there by a crash, or by hand, would stay unknown.
- **Removing quarantined entries from the index.** Restore would bring files back under new IDs, without decisions or tags (I4).

## Consequences

- Scans read the metadata of quarantined entries. They never read their content.
- Every reader that counts or shows the disk carries one residual path predicate. Unit and corpus tests check it, and so do the existing query-plan guards.
- Origin records are indexed as files under `.precious-quarantine/<plan>/` once a scan walks them, and are never shown.
