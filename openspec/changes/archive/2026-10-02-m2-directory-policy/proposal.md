# Proposal

## Why

M1 catalogs every source one level deep. Every directory below the root stays an undecided atomic row, so the owner cannot tell an old installation from a photo archive, keep user data safe, split a mixed backup, or learn how big an opaque unit is. This change is milestone **M2, directory policy** (§14). It turns the M1 inventory into a set of reviewable units. It makes acceptance scenarios **A2, A3, A4, A7, A8, and A9** pass, keeps A1, A5, A11, A15, A17, and A20 passing, and still creates no hidden full-tree catalog.

## What Changes

- **Deterministic classification (§4.2, §4.3, §5.3):**
  - A versioned rule set (`policies/rules/`) assigns one primary category from the §4.3 taxonomy and any number of orthogonal traits, each citing the descriptor evidence it matched.
  - An extended observation policy (`markers-v2`) adds user-data, cache, temporary, generated-output, OS-image, media, and container-name signals. It also adds name-derived file-kind counts.
  - Precedence is: protection, then owner override, then rule, then model suggestion, then `unknown`. Equally ranked rules that disagree produce `unknown` plus review, never an arbitrary winner.
  - Suggestions are kept as append-only history with the revisions they observed. A result computed against an older owner intent or evidence revision can never become effective (A7).
- **Policy-driven boundaries (§5.3):**
  - A coherent unit stays atomic.
  - `mixed` and `download_collection` directories are expanded automatically, one level at a time, within the node budget and the automatic refinement depth (default 6).
  - An `unknown` directory whose listing was truncated gets one escalated probe, then becomes an atomic review item.
  - Policy may expand a directory but never collapses one.
- **Owner refinement:** `refine-node` expands one directory one level and classifies each child independently (A3). `collapse-node` makes an expanded directory atomic again. Collapsing deactivates the cataloged descendants but keeps them as history. Re-expanding reactivates the same node IDs together with their owner decisions.
- **Owner intent (§4.2, §7.6):**
  - Category overrides that survive rescans and late results.
  - Dispositions: `unreviewed`, `preserve`, `cleanup_candidate`, `review`.
  - Path-scoped protection pins. A pin is effective on its own path, inherited by every path below it, and reported on every ancestor as "contains protected".
  - Every owner command carries an expected intent revision. A mismatch returns HTTP 409 `revision_conflict`.
  - Owner decisions are written to the audit trail.
- **Preservation risk (A4):** user-data, database, credential, or save indicators veto any low-risk suggestion and route the unit to review. Protected units are never suggested as cleanup candidates.
- **Inspection (§5.2):**
  - **Peek (A8):** a bounded, live, read-only listing inside an atomic unit. It descends through server-signed tokens, so the browser never sends a path, and it stores nothing.
  - **Aggregate walk (A2):** an explicit, cancellable `inspect-node` job. It records counts, logical and allocated bytes, hardlinked files, mount boundaries, errors, file-kind counts, and bounded examples of preservation indicators. It never creates descendant nodes.
  - Aggregates go stale after a configured validity period. Indicators found by a walk can add preservation traits but never remove them.
- **Accounting (§9.2, A9):**
  - Totals use the non-overlapping active frontier: complete current measurements, lower bounds from partial measurements, stale measurements, and units of unknown size are kept separate.
  - Selections are normalized so that a selected ancestor absorbs its selected descendants.
  - Totals are identical before an expansion and after the matching collapse.
- **Review queue (§9.1):** three queues: likely cleanup candidates, units to preserve or protect, and ambiguous containers. Bulk disposition applies to an exact list of node IDs from the page. Protected items are excluded and listed. There is no "select all matching".
- **UI and API:**
  - The overview shows queue counts and units per category.
  - The explorer adds category, disposition, protection, preservation risk, and measured size.
  - The inspector adds the classification explanation, history, owner controls, aggregate, and peek.
  - New commands: `refine-node`, `collapse-node`, `inspect-node`, `set-classification`, `set-disposition`, and `set-protection`.
  - New reads: `GET /api/review`, `GET /api/nodes/{id}/peek`, and `GET /api/selection-summary`.
  - New error codes: `protected`, `invalid_node_state`, and `stale_evidence`.
- **BREAKING (internal):** schema migration `0002` adds the classification, override, protection, and aggregate tables plus new node columns. Nodes discovered by M1 are shown as "not yet classified" until the next scan.

Deferred, with owning milestone:
- Owner-written scoped rules, and corrections turned into rules or evaluation examples → **M3**.
- Model classifiers, provider routing, and the classification cache → **M3**. M2 history already records model-kind suggestions, so A7 can be tested.
- Dirty scopes, change-triggered aggregate invalidation, scheduled shallow refresh and re-measurement, and verified missing entries → **M2a**. In M2, aggregates go stale only by age or a source-epoch change.
- The inbox role, and refusing to collapse inbox ancestors (A40) → **M3a**.
- Priority classes and fairness between long aggregate walks and interactive work (A36) → **M3a**.
- Plans, collapsing overlapping plan selections, the `quarantined` disposition, and the reproducibility dimension → **M5**. M5 re-verifies A9 for plans with the M2 selection normalizer.
- Content hashing, duplicate claims, and reclaimable-space estimates → **M4** and **M5**.
- Index health screen → **M2a**. Classifier settings screen → **M3**.

## Capabilities

### New Capabilities
- `directory-classification`: category taxonomy, traits, deterministic versioned rules, precedence, suggestion history with revision checks, preservation risk, suggested triage, and evidence-based explanations.
- `owner-intent`: category overrides, dispositions, path-scoped protection pins with inheritance, expected-revision checks, and auditing of owner decisions.
- `inventory-boundaries`: owner refine and collapse, deactivation and reactivation semantics, and the rule that owner-chosen inventory modes stick.
- `directory-inspection`: peek inside atomic units and aggregate walks, with their budgets, coverage, and freshness.
- `inventory-accounting`: non-overlapping frontier totals, measured, lower-bound, stale, and unknown size classes, and selection normalization.
- `review-queue`: queue membership, exact bulk selection, protected exclusions, and bulk disposition.

### Modified Capabilities
- `directory-discovery`: the frontier now runs at any depth; probed directories are decided by policy, which replaces "Undecided directories stay atomic"; automatic expansion and an escalated probe are added; the observation policy moves to `markers-v2` with file-kind counts; loose files get name-derived kind hints; rescans re-list every active expanded directory.
- `inventory-explorer`: the overview, explorer rows, inspector, and JSON API show the new dimensions and controls. The A20 read-only workflow now includes classification and review.
- `job-runner`: refine requests join the source's single active discovery job; there is at most one active aggregate walk per node; new machine-readable error codes are added.

## Impact

- **Packages:**
  - New: `internal/classify` (rules, precedence, history, triage), `internal/inspection` (peek, aggregate walk), and `internal/intent` (overrides, pins, dispositions).
  - Extended: `internal/discovery`, `internal/inventory`, `internal/commands`, `internal/jobs`, `internal/web/explorer`, `internal/domain`, `internal/config`, and `internal/fsaccess` (allocated blocks in `EntryInfo`).
- **Data:** `migrations/0002_directory_policy.sql`; new policy files `policies/markers/v2.toml` and `policies/rules/v1.toml`.
- **Configuration:** new `[discovery]` keys `auto_refine_depth` and `escalated_entry_budget`; a new `[inspection]` section for peek and aggregate budgets and the aggregate stale-after period.
- **UI:** a new review page; templates and `web/static/app.js` change.
- **Docs:** `docs/operator.md` gains sections on classification, owner decisions, inspection, review, and upgrading from M1.
- **Dependencies:** none new. No network egress at runtime.
