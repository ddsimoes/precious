# Proposal

## Why

The owner can now decide what to discard (R1, R2) and organize safely (R3), but nothing frees space. R4 (§14) turns discard decisions into cleanup plans on the R3 executor:
- a reversible quarantine;
- a pre-delete check that proves which files have a verified copy elsewhere;
- a purge gated by that check (§10.1–§10.4, §10.6, §11.9).

## What Changes

- **Cleanup plans** (§10.1).
  - **Drafting.** A plan is drafted from the explicit discard decisions of a source, or from the discarded rows of one review list. Each item is a topmost explicitly discarded entry.
  - **Blocked items.** An item whose subtree holds a kept entry is blocked and lists the kept entries that block it, while the rest proceeds.
  - **Approval.** It shows totals, warnings, and a summary of copies before approval, which freezes it.
  - **Duplicates.** A plan from the duplicates list never quarantines every copy of a group or both sides of a relation (R4.4).
- **Quarantine** (§10.3).
  - **Where.** Each source gets a folder Precious controls, `.precious-quarantine` at its top, on the same filesystem as every entry of the source.
  - **Moving in.** Approved items move there through the executor, each with a small record of its origin written next to it.
  - **In the index.** Quarantined entries keep their IDs, decisions, and tags, but every view, total, copy count, and card leaves them out. Home shows the bytes in quarantine.
- **Restore** (§10.3, R4.3). An item goes back to its original path, or to a folder the owner chooses when that path is taken. It never merges and never overwrites.
- **Pre-delete check** (§10.6, R4.6–R4.8).
  - **The set.** The owner picks quarantined items to delete for good.
  - **Hashing.** A check job reads every file of that set in full, archive members included.
  - **Safe files.** A file is safe only with an identical copy outside the set and outside quarantine, re-read and verified now.
  - **The rest.** Every other file is unique, ranked by the rules as possibly valuable or likely junk. A copy on an offline source counts as unverified.
- **Purge** (§10.3, R4.5).
  - **The gate.** It runs only on a fresh check. Likely-junk unique files need one confirmation for their group; possibly valuable or uncertain ones are confirmed one by one, or restored.
  - **Deleting.** It deletes exactly the checked files, and stops when anything changed.
  - **The report.** It reports the space freed, noting that ZFS snapshots may keep it.
- **CSV export** (§10.4, R4.9). Every plan and action exports as CSV: path, size, operation, and reason for each item.
- **The executor** gains two primitives: an exclusive file creation for the origin records, and a file unlink for purges. Both work only inside the quarantine folder.
- **Screens** (§11.9). Cleanup in the main navigation offers:
  - plans, with the draft and approve flow and blocked items;
  - the quarantine browser, with restore;
  - the check report, with its confirmations;
  - purge and export.

  History shows live execution. Review lists offer "Draft a cleanup plan".

Acceptance scenarios this change must make pass: **R4.1, R4.2, R4.3, R4.4, R4.5, R4.6, R4.7, R4.8, R4.9**.

## Capabilities

### New Capabilities

- `cleanup`: cleanup plans, quarantine, restore, the pre-delete check, purge, and export.

### Modified Capabilities

- `source-writes`: the executor may also create origin records and delete files and folders, only inside a source's quarantine folder.
- `organizing`: organizing refuses quarantined entries and the quarantine folder's name.
- `file-index`: scans skip the quarantine folder and leave its index rows as they are.
- `inventory-explorer`: views, totals, and search leave quarantine out, and Home shows bytes in quarantine.
- `duplicates`: a quarantined copy is not a copy elsewhere.
- `review-lists`: cards and lists leave quarantined entries out, and a list's discarded rows can start a cleanup plan.

## Deferred

- The model layer of the pre-delete check (probabilities from Jev): R6. R4 ranks by rules only.
- Moves across filesystems (copy, verify, then quarantine the original): out of scope (§10.4, §13).
- Quarantine on macOS and Windows: R8, with their no-replace rename.
- Automatic purge or retention timers: out of scope (§13, "any change the owner did not explicitly request").
