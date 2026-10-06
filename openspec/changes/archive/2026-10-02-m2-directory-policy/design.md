# Design

## Context

M1 is merged on `master`: see `openspec/changes/archive/2026-10-02-m1-read-only-foundation/design.md` (D1–D17). This design reuses that work and changes it only where noted. The M1 facts that shape M2:

- **One migration, one decision point.**
  - `migrations/0001_init.sql` is the whole schema.
  - Every probed directory is decided by one hard-coded `UPDATE` in `internal/discovery/inventory.go` (`commitProbe`): `inventory_mode='atomic'`, `decision_reason='no_policy_retain_atomic'`.
  - Only the source root is `expanded`.
- **The scan is depth-1.**
  - `listRoot` lists only the root.
  - The frontier is `nodes.probe_state='pending' AND probe_scan_job_id=<job>`, processed in node-ID order.
  - `probeFrontier` rejects any node whose parent is not the root.
  - Probes open targets from the root handle.
- **Revisions.** Only `nodes.observation_revision`, `sources.source_epoch`, and `reconciliation_runs.generation` exist. There is no intent revision. `domain.CodeRevisionConflict` is defined and mapped to HTTP 409, but nothing returns it.
- **Accounting.** `inventory.summarize` already sums the non-overlapping frontier: every active non-root node except expanded directories. It does this without measurements.
- **Jobs.** The only job kind is `scan`, made unique per source by `jobs_one_active_scan`. A command is an `operation` (`canonical` / `prepare` / `apply(*jobs.Tx)`), and its idempotency row is written in the same transaction as its effects.
- **Filesystem access.** `fsaccess.Dir.OpenDir(name, expect)` descends by handle. It checks identity, refuses symlink swaps, and refuses mount boundaries. `EntryInfo` has no allocated-block count.
- **Test support.** `internal/fsaccess/instrument` counts calls per operation and per depth. `synthfs` builds lazy 100,000-entry trees.

## Goals / Non-Goals

**Goals:**
- Policy decisions stay cheap and local. Each decision reads one descriptor, at most one aggregate measurement, and the owner's intent for that node, inside the transaction that applies it.
- Every invariant change (atomic boundary, owner intent, protection state) commits in one transaction, so no reader or crash ever sees a half-applied boundary.
- Late or concurrent work is checked against revisions inside the writer transaction. No locking is held across filesystem calls.

**Non-Goals:**
- No classifier adapters, provider routing, or cache. Owner-written rules are also out (M3).
- No dirty scopes, scheduled refresh, tombstones, or watcher (M2a).
- No job priority classes (M3a).
- No plans, quarantine, or protection enforcement on mutations (M5).
- No full-text or content inspection of any kind.

## Decisions

### D1. Two policy files: observation and classification (§5.2, §5.3, §6)
- **Observation policy.** `policies/markers/v2.toml` (`markers-v2`) supersedes v1, in the same format and loader (`discovery.MarkerPolicy`).
  - It keeps the ≤16-lookup marker list. Lookups are spent on the markers that the listing cannot see cheaply: VCS directories, build manifests, `CACHEDIR.TAG`, `DCIM`, uninstallers, and profile and save markers.
  - It adds signals for save data, profile data, database files, mail stores, credential files, temporary names, generated-output directory names, and OS-image layouts.
  - It adds a `[file_kinds]` table that maps lower-case extensions to the §6 `file_kind` values.
  - The same table produces the descriptor's `file_kind_counts` and the hint on loose file nodes.
  - The patterns are deliberately narrow. For example, `contains_database` matches `*.sqlite`, `*.sqlite3`, `*.mdb`, `*.accdb`, and `*.fdb`, but not `*.db`, because `Thumbs.db` would put every photo folder at preservation risk.
- **Classification policy.** `policies/rules/v1.toml` (`rules-v1`) is a new file with a new loader in `internal/classify`. It is strictly validated: unknown keys are rejected, and every category, trait, and signal ID must be known. Each rule has:
  - `id` and `priority`;
  - an optional `category` and optional `traits`;
  - conditions, all of which must hold: `all_signals`, `any_signals`, `no_signals`, `name` (case-insensitive globs on the directory's own raw name), and `kind_share` (`kind`, `min_count`, `min_share` over examined regular files);
  - an `explain` sentence.

  A rule that sets only traits never competes for the category.
- Both versions are recorded:
  - the observation-policy version in each descriptor (as in M1);
  - the classification-policy version in each suggestion as `policy_version = "rules-v1+markers-v2"`.
- **Rejected:**
  - One merged file. Changing a rule would then change descriptor digests, which are a pure function of observations and are reused by M3 cache keys.
  - A general expression language. It cannot be validated statically, and §8.1 favors the simplest workable mechanism.

### D2. Precedence, conflicts, and the "late results cannot win" check (§3.12, §5.3, §5.5.5, §7.6)
- `classify.Evaluate(descriptor, aggregate *Aggregate) RuleResult` is pure.
  - It collects the matching rules and takes the highest priority among the category rules.
  - If two or more categories tie at that priority, the result is `unknown` with reason `conflicting_rules`, and the competing matches are listed.
  - If nothing matches, the result is `unknown` with reason `no_rule_matched`.
  - Traits are the union over all matching rules. Every match carries its cited evidence IDs: `e#` entries, `m#` markers, and `a#` aggregate examples.
- `classify.Apply(tx, Suggestion)` is the only writer of `classifications` and of the effective classification columns on `nodes`. A suggestion carries the source epoch, observation revision, and intent revision it observed. In one transaction, `Apply`:
  1. Reads the current values.
  2. On any mismatch, inserts the row with `status='stale'` and stops.
  3. Otherwise marks the previous current row of the same `source_kind` as `superseded`, and inserts the new row as `current`.
  4. Recomputes the effective state, in this order:
     - category: owner override, else current rule, else current model, else `unknown`;
     - traits: those of the current rule (model traits are left to M3);
     - preservation risk;
     - triage (D9).
- Discovery and aggregate commits evaluate the rules inside the commit transaction against values read in that same transaction, so their suggestions are never stale.
- **A7 is proven by the model-kind path.** `source_kind` admits `model` now. The A7 test injects a model-kind suggestion through `Apply`, carrying an older intent revision, after an owner override. M3 plugs its adapters into the same entry point.
- **Rejected:**
  - Overwriting a single "current classification" row. It loses alternatives and provenance (§7.6).
  - Stamping results with timestamps. Clocks don't order commits; revisions do.

### D3. Schema `0002_directory_policy.sql` (§8.3)
The migration only adds things. It uses `ALTER TABLE … ADD COLUMN` plus new tables, so `nodes` is never rebuilt: rebuilding would need `PRAGMA foreign_keys=OFF`, which migrations forbid (D6).

| Change | Contents |
|---|---|
| `nodes` + | `category` (CHECK over the 13 §4.3 values), `category_source` (`owner`, `rule`, `model`), `traits` (JSON array), `preservation_risk` (0/1), `suggested_triage` (`preserve`, `cleanup_candidate`, `review`, `expand`), `disposition` (NOT NULL, default `unreviewed`; CHECK includes `quarantined`, reserved for M5 and rejected by every M2 command), `intent_revision` (NOT NULL, default 1), `mode_set_by` (`policy`, `owner`), `protection` (`none`, `explicit`, `inherited`; default `none`), `contains_protected` (0/1), `listing_state` (`pending`), `listing_job_id` → jobs, `escalated_job_id` → jobs, `file_kind`, `latest_aggregate_id` → aggregates |
| `classifications` | Append-only suggestions: `node_id`, `source_kind`, `policy_version`, `descriptor_id`, `aggregate_id`, `category` (nullable for future abstentions), `reason`, `traits`, `matches` (JSON), `observed_epoch`, `observed_observation_revision`, `observed_intent_revision`, `status` (`current`, `superseded`, `stale`), `job_id`, `created_at`. Unique partial index: one `current` row per (`node_id`, `source_kind`). |
| `category_overrides` | `node_id` (PK), `category`, `descriptor_digest`, `evidence_changed`, `set_at` |
| `protection_pins` | `source_id`, `rel_path` BLOB, `created_at`; UNIQUE(`source_id`, `rel_path`) |
| `aggregates` | One measurement per row: `node_id`, `job_id`, `source_epoch`, `observation_revision`, `coverage_state`, `stop_reason`, the typed counters of the `directory-inspection` spec, `started_at`, `finished_at`, and `summary` JSON (file-kind counts, bounded examples with `a#` evidence IDs, budget) |
| `sources` + | `protection_revision` (NOT NULL, default 0) |
| `jobs` + | `scope_key`, plus a unique partial index on (`kind`, `scope_key`) while the job is queued, running, or paused |

- **Indexes:**
  - (`source_id`, `active`, `disposition`, `suggested_triage`) and (`source_id`, `active`, `category`) for queues and overview counts;
  - `listing_job_id` partial on `listing_state='pending'`;
  - `classifications(node_id, id)` and `aggregates(node_id, id)`.
- **Backfill:** `mode_set_by='policy'` wherever `inventory_mode` is set. M1 nodes keep `category` NULL, which displays as "not yet classified".
- The `quarantined` and `model` enum values are included now. Adding a value to a CHECK later would force a `nodes` rebuild, while an unused value costs nothing: commands reject `quarantined`, and only tests produce `model`.
- **Rejected:**
  - A separate `node_state` table. Every list query would need a join, for no benefit.
  - Dropping the M1 `probe_state` and `probe_scan_job_id` columns. SQLite cannot drop a column that has a foreign key or a partial index without a rebuild. Their meaning is unchanged, and D4 keeps using them.

### D4. One frontier at any depth (§5.1, §5.3, §5.5.1)
- **The frontier has two step types**, both keyed by the scan job ID:
  - probe-pending: `probe_state='pending'`, `probe_scan_job_id`;
  - listing-pending: `listing_state='pending'`, `listing_job_id`.

  The job always takes the pending item with the lowest node ID. IDs are assigned in discovery order, so this is breadth-first: a directory's children always get larger IDs than every directory at its own depth that was already known. Reactivated nodes keep their older IDs and so are processed early, which is harmless.
- **Job scopes.** The `scan` payload moves to version 2: `{source_id, scope: "source" | "frontier"}`; a version-1 payload reads as `source`.
  - A `source` scan marks the root and every active expanded directory listing-pending at `begin`.
  - A `frontier` scan processes only what is already pending.
  - Both are the same job kind, so `jobs_one_active_scan` still allows one discovery job per source.
- **Opening deep nodes.** A small opener walks from the root through the recorded parent chain with `OpenDir(name, expect{kind, dev, ino})`. It caches the most recently used chain, because consecutive frontier items usually share parents. An identity mismatch records the node's coverage as `error` with outcome `changed_during_observation`. Nothing beyond it is followed.
- **Listing any expanded directory** generalizes `listRoot` to `listDir(node)`: one `reconciliation_runs` row per listing, the same batch transaction, and the same node-budget accounting. Each batch transaction re-checks inside the transaction that the parent is still active, still `expanded`, and still `listing_job_id = job`. If it is not (for example after a collapse), the listing stops with run state `cancelled` and stop reason `boundary_changed`. That check is what keeps "no hidden catalog" true across a concurrent collapse.
- **Reactivation (§5.5.6).** Child upsert looks up the active row by (parent, name) as in M1. Failing that, it looks up the newest inactive row with the same name, kind, dev, and ino, and reactivates it. Any other entry becomes a new node.
- **Completion.** A job completes only from a transaction that finds no pending item for it. A refine committed just before that transaction is picked up. A refine committed after it finds no active job and starts a new one.
- **The depth-1 assumptions in M1** are removed: the parent-is-root check, the root-only upsert, and probing from the root handle.
- **Rejected:**
  - A separate frontier table. It would duplicate state that already lives on `nodes`, and it would need its own cleanup on collapse.
  - A stored depth column. Depth is needed only for the auto-refine limit, which takes one bounded recursive CTE per decision. A stored depth would go stale under M5 moves.

### D5. The policy decision (§5.3, §4.3)
`commitProbe` becomes `commitDecision`. In one transaction it:
1. inserts the descriptor;
2. flags `category_overrides.evidence_changed` if the digest differs from the override's;
3. evaluates the rules and calls `classify.Apply`;
4. decides the mode:
   - an owner-set mode is kept;
   - an already expanded directory stays expanded;
   - a coherent category → `atomic`, reason `policy_coherent_unit`;
   - `mixed` or `download_collection` → if the depth (root = 0) is below `auto_refine_depth`, `expanded` with reason `policy_expand`; otherwise `atomic` with reason `refinement_depth_reached`;
   - `unknown` → if this probe stopped at `entry_budget` and `escalated_job_id ≠ job`, the node stays probe-pending with `escalated_job_id = job` and is probed next with `escalated_entry_budget`; otherwise `atomic` with reason `insufficient_evidence`.

Whatever the reason, a directory whose resulting mode is `expanded` becomes listing-pending unless this job has already listed it (`listing_job_id = job`). That also covers expanded directories reactivated under a refined ancestor.

Policy never writes `atomic` over `expanded`. That is the whole of the "never collapses" rule.
- **Rejected:**
  - Letting policy collapse directories whose classification changed. That would silently deactivate cataloged rows the owner may be relying on. Collapse stays an explicit owner act.
  - Escalating by depth (probing grandchildren). §5.1 fixes probe depth at 0, and a larger first-N listing is the bounded "richer probe" that keeps the cost independent of hidden size.

### D6. Owner intent as one package (§4.2, §7.6, §9.3)
`internal/intent` owns every owner write: overrides, dispositions, pins, refine, and collapse. Each command in `internal/commands` is a thin `operation` that calls an `intent` function inside the command's transaction, so the idempotency row, the change, the audit event (via `auth.WriteAudit`), and any job enqueue commit together. Every node-targeted request carries `expected_intent_revision`; a mismatch returns `revision_conflict`.

The command set extends the §9.3 list:

| Command | Body | Result |
|---|---|---|
| `set-classification` | `{node_id, expected_intent_revision, category \| null}` | 200 |
| `set-disposition` | `{disposition, items: [{node_id, expected_intent_revision}]}`, ≤ page size | 200, per-item outcomes |
| `set-protection` | `{protected, target: {node_id, expected_intent_revision} \| {peek_token}}` | 200 |
| `refine-node` | `{node_id, expected_intent_revision}` | 202, job ID |
| `collapse-node` | `{node_id, expected_intent_revision}` | 200 |
| `inspect-node` | `{node_id, operation: "aggregate"}` | 202, job ID |

- `collapse-node` and `set-disposition` are not in §9.3's initial list. They are added because the spec requires both behaviors (§4.2 dispositions, A9 collapse) and no listed command fits without overloading its meaning.
- **Rejected:**
  - Overloading `refine-node {mode}` for collapse. Its audit and error semantics differ.
  - Folding disposition into `set-classification`. Disposition is workflow state, not classification (§4.2).

### D7. Collapse is one transaction (A9, §4.2)
Collapse:
1. verifies the expected revision and that the node is an expanded non-root directory;
2. sets it `atomic`, `mode_set_by='owner'`, reason `owner_collapsed`, increments `intent_revision`, and clears its `listing_state`;
3. deactivates every active descendant and clears their frontier states, in one recursive-CTE `UPDATE`.

Overrides, dispositions, and owner modes stay on the inactive rows. Pins are path-scoped and are not touched.

This deliberately exceeds M1 D10's ≤256-row write batch. The active catalog is bounded by the node budget (50,000 by default), and SQLite updates that many indexed rows in well under a second. A half-collapsed state would break the atomic-boundary invariant that M2 exists to keep.
- **Rejected:**
  - A batched collapse job. Until it finished, an atomic node would have active descendants, so readers would need ancestor checks everywhere.

### D8. Path-scoped pins with materialized protection state (§4.2, §5.5.6, §10.1)
- **Path keys.** A pin key is the raw relative path: names joined with byte `0x2F`, and the empty string for the root. Names never contain `/` or NUL, so the key is lossless. Descendant paths of `p` are found with the BLOB range `rel_path > p || '/' AND rel_path < p || '0'`.
- **Materialized state.** `nodes.protection` and `nodes.contains_protected` are stored, so queue and explorer queries can filter on them. They are kept exact by three writers, each in the same transaction as its cause:
  - **Pin change:** recompute the active nodes at, under, and above the pin path. The affected set is the active subtree of the deepest cataloged node at or above the path, plus that node's ancestor chain.
  - **Node insert or reactivation:** derive the state from the parent's state plus the source's pins at and under the child's path.
  - **Collapse:** has no effect, because inactive rows are not read.

  A property test recomputes the state from pins for random trees and pin sets, and compares it with the stored state.
- A pin change increments `sources.protection_revision`. M5 plans will snapshot it. It also increments `intent_revision` of the cataloged node at exactly that path, if there is one.
- **Pins inside atomic units** are created from peek tokens (D10). Their target path is the unit's path plus the token's steps.
- **Rejected:**
  - Node-ID pins. They would vanish with deactivation and could not express uncataloged paths.
  - Computing protection at read time only. Review-queue pagination would then need post-filtering, which breaks cursor stability.

### D9. Triage and preservation risk (§4.3, §4.4, A4)
- **Preservation risk** is set when the category is `application_user_data`, or when any of `contains_user_material`, `contains_database`, or `contains_credentials` is present.
- **Triage** starts from the §4.3 defaults:

  | Categories | Suggested triage |
  |---|---|
  | `personal_media`, `source_project`, `documents`, `application_user_data` | `preserve` |
  | `application_installation`, `cache`, `temporary_data`, `generated_artifacts` | `cleanup_candidate` |
  | `application_configuration`, `os_installation`, `unknown` | `review` |
  | `mixed`, `download_collection` | `expand` |

  A `cleanup_candidate` suggestion becomes `review` when preservation risk is set, when protection is not `none`, or when `contains_protected` is set.
- Triage is recomputed by `classify.Apply` and by every pin change (D8). Neither ever touches `disposition`.
- **The §4.4 regression corpus** is a synthfs fixture with expected outcomes:
  - installed program with a nested personal document;
  - application profile;
  - game with saves;
  - old source project with private libraries;
  - temporary directory with recovery material.

  A shallow probe catches the shallow cases. The nested document is caught only after an aggregate walk (D11), which is what §4.4 intends: the unit stays atomic and pending review.

### D10. Peek: live, bounded, token-addressed (§9.1, A8, §12.2)
- **Request and response.** `GET /api/nodes/{id}/peek[?at=<token>]` and the page `/nodes/{id}/peek` run in `internal/inspection`:
  1. open the source with `Registry.BeginInspection`. This is a new method with the same checks as `BeginScan`, but it does not touch `last_scan_at`; frontier-scope scans and aggregate walks also use it;
  2. walk to the unit through the recorded identities;
  3. follow the token's steps with `OpenDir(name, expect)`;
  4. read at most `peek_entry_limit` entries, lstat each, and readlink symlinks.

  Each entry carries its kind, size, mount-boundary flag, link text, protection derived from pins, and, for an enterable directory, a child token.
- **Tokens.** A token is `base64url(v1 | node_id | source_epoch | issued_at | steps[{name, dev, ino}]) || HMAC-SHA256`. The key is 32 random bytes generated at process start and never stored, so a restart invalidates every token. The token TTL is configurable, default 1 h, and a token holds at most 64 steps. Verification checks, in order:
  1. the MAC;
  2. the TTL;
  3. that the node is the same and still active and atomic;
  4. that the epoch is the same;
  5. the identity at each step.

  Any failure returns `stale_evidence`.
- **Concurrency and blocking.**
  - Peeks run inline, outside the job device slots, with at most one in-flight peek per source.
  - Every filesystem call runs under the `call_watchdog` deadline. A blocked call fails the request with `source_unavailable` and flags the source unresponsive, as a job would.
  - Peek writes nothing, not even an audit row.
- **Rejected:**
  - Accepting relative paths from the client. That would violate "the browser addresses nodes by ID, never by path" (§3, §4.1).
  - Persisting peek results as temporary nodes. A8 forbids persistent expansion.
  - Routing peeks through the job runner. A peek is an interactive read of at most one directory, and a 202-plus-polling round trip would make it unusable.

### D11. Aggregate walk job (§5.2, §5.5.2, A2)
- **Job.** The job kind is `aggregate` with payload `{node_id}`. Its `scope_key` is `node:<id>`, which enforces one active walk per node, and its device key comes from the source. It is cancellable through `cancel-job`.
- **Walk.** The walk is an iterative depth-first traversal with an explicit stack of open `Dir` handles, at most `aggregate_max_depth` deep (default 256).
  - Every regular file and directory is lstat'ed once, giving size, blocks, nlink, and the mount flag.
  - Symlinks and special entries are counted by `DirEntry.Kind`, with an lstat only when the kind is unknown. They are never read or opened.
  - Mount boundaries and directories beyond the depth limit are counted and not entered.
  - Each name is run through the observation policy's signal patterns, producing up to 20 indicator examples with relative raw paths, and through the file-kind table.
  - Every call goes through `rt.FSCall`. Progress is published in batches.
- **Stop reasons:** `complete`, `entry_budget`, `depth_limit`, `cancelled`, `error`, and `source_unavailable`. Coverage is `complete` only for `complete` with no unreadable directory, error, or skipped boundary.
- **Commit.** In one transaction, the commit:
  1. checks the source epoch. On a change, the measurement is discarded and the job fails with `source_epoch_changed`;
  2. inserts the `aggregates` row;
  3. if the node is still active and atomic, sets `latest_aggregate_id`, re-evaluates the rules with the descriptor plus the walk's indicator signals, and calls `classify.Apply`.

  Cancellation commits the partial measurement through the non-cancelled DB context, as M1 discovery does.
- **Crash recovery.** A walk keeps no traversal state durably, because §5.5.4 forbids durable listing offsets. A requeued attempt restarts from the unit's root.
- **New `fsaccess.EntryInfo.Blocks` field** carries `st_blocks`; allocated bytes are `Blocks × 512`. `synthfs` reports the size rounded up to 4 KiB unless a test sets it.
- **Freshness is computed at read time:** a measurement is current if its source epoch equals the source's epoch and `now < finished_at + aggregate_stale_after`. Nothing renews it. M2a will add change-triggered invalidation.
- **Rejected:**
  - Storing aggregates on descriptors. A descriptor is a shallow observation with its own digest; mixing in walk results would change digests on every walk.
  - Yielding the device slot in the middle of a walk. That needs resumable traversal state, which §5.5.4 rules out; fairness is M3a (A36). See Risks.

### D12. Accounting and selection (§9.2, A9)
- **Totals.** `inventory.Totals(scope)` sums the frontier, which is what M1 already does, now with size classes:
  - a file contributes its `size` as measured bytes;
  - a symlink contributes 0 bytes and is counted as a link;
  - an atomic directory contributes its latest measurement by class: `complete` and current → measured; `partial` or `error` and current → lower bound; not current → stale; none → unknown;
  - an undecided directory or a mount boundary is unknown.

  A source scope filters on `source_id`. An expanded-node scope takes the active descendants through a recursive CTE, which is bounded because atomic boundaries cap the active catalog.
- **Selections.** `inventory.NormalizeSelection(ids)` removes duplicate IDs, loads each node's ancestor chain, and marks nodes covered by a selected ancestor. It is shared by `GET /api/selection-summary` and `set-disposition`, which de-duplicates only: labels apply per node. M5 plan creation will reuse the covered-by logic.

### D13. Review queues (§9.1)
Each queue is a keyset-paginated query over `nodes`, filtered to active, atomic, classified directories, using the D3 indexes:
- `cleanup`: `suggested_triage='cleanup_candidate' AND disposition='unreviewed'`;
- `preserve`: (`preservation_risk=1`, `protection<>'none'`, `contains_protected=1`, or `suggested_triage='preserve'`) and `disposition='unreviewed'`;
- `ambiguous`: (`category='unknown'`, `category IN ('mixed','download_collection')`, or `evidence_changed=1` through a join on `category_overrides`) and `disposition IN ('unreviewed','review')`.

The cursor is the node ID. Bulk disposition reports per-item outcomes (D6). The page renders checkboxes for the visible rows only.

### D14. Error codes (§9.3)
New `domain` codes, each mapped to HTTP 409 in `apierr`:
- `protected`: carries the blocking pinned path's display form in `detail`;
- `invalid_node_state`: wrong kind, mode, or root, or a mount boundary;
- `stale_evidence`: an invalid peek token, or an identity changed since the token was issued.

### D15. Configuration (§5.1, §5.5.3)
| Key | Default | Range |
|---|---|---|
| `[discovery] auto_refine_depth` | 6 | 0–64; 0 disables automatic expansion |
| `[discovery] escalated_entry_budget` | 1024 | from `shallow_entry_budget` to 65,536 |
| `[inspection] peek_entry_limit` | 100 | 1–1000 |
| `[inspection] peek_token_ttl` | 1h | 1m–24h |
| `[inspection] aggregate_entry_budget` | 2,000,000 | ≥ 1 |
| `[inspection] aggregate_max_depth` | 256 | 1–4096 |
| `[inspection] aggregate_stale_after` | 24h | ≥ 1m |

`check-config` prints all of them. The example configuration documents them with comments.

### D16. Web layer (§9.1)
- **Explorer row.** The `row` partial and its JS twin `renderRow` gain these columns: category and source badge, disposition, protection and preservation-risk badges, and size class. File rows show the file-kind hint.
- **Inspector.** The inspector gains classification, alternatives and history, owner forms, measurement, and peek link sections. Owner forms post through `sendCommand` with `data-command`, `data-node-id`, and `data-intent-revision` attributes. A 409 `revision_conflict` reloads the node.
- **New pages:**
  - `review` (queue tabs, checkboxes, a preview panel fed by `GET /api/selection-summary`, and a confirm button);
  - `peek` (a server-rendered listing whose links carry tokens; Protect buttons post `set-protection` with the token).

  Everything stays under the M1 CSP: no inline script, and text nodes only in JS.

### D17. A9 split between M2 and M5 (§14)
M2 closes A9 for tree totals across expansion and collapse, and for overlapping review selections. "Overlapping plan selections" cannot exist before plans do. M5 re-verifies that half using `NormalizeSelection`. This is recorded as `docs/adr/0001-a9-split-across-m2-and-m5.md`, as the project rules require for deviations from a milestone exit condition.

## Risks / Trade-offs

- [A long aggregate walk holds the device's single filesystem worker, delaying scans and refines on that device] → It is cancellable and its progress is visible. The docs advise walking large units when no scan is needed. Priority classes arrive in M3a (A36).
- [Name heuristics misclassify, for example a `build` directory that holds hand-written files] → Generated-output names add only the trait `possible_generated_content` unless corroborated by a manifest or VCS sibling. Ambiguity resolves to `unknown` and review, never to cleanup. Owner overrides win.
- [Materialized protection state could drift from the pins] → Every writer is listed in D8 and runs in the same transaction as its cause. A property test compares stored state with state recomputed from pins.
- [A one-transaction collapse of a very large expanded subtree blocks other writers briefly] → The active catalog is bounded by the node budget. A test measures a 50,000-descendant collapse, and the docs state the cost.
- [The peek HMAC key is per process, so tokens die on restart] → This is intended. Peeks are interactive, and the page just reloads.
- [Upgraded M1 nodes have no category until rescanned] → They are labeled "not yet classified", and the upgrade docs say to run a scan per source.
- [Escalation reads up to 1,024 entries of a huge `unknown` directory once per scan] → The cost is still bounded and independent of hidden size, and the escalated budget is configurable.

## Migration Plan

1. Back up with `curator backup`; the docs already require this before upgrades.
2. On first start, migration `0002` runs: the new columns, tables, and indexes, plus the `mode_set_by` backfill. It is additive, so the M1 binary refuses the newer schema (`ErrSchemaTooNew`) instead of misreading it.
3. Run `start-scan` per source. Every directory is re-probed under `markers-v2` and classified under `rules-v1`; `mixed` containers expand within the depth and node budgets.
4. **Rollback:** stop the server, restore the pre-upgrade backup, and start the M1 binary. No source is ever written, so rollback is purely a database restore.

## Open Questions

- The exact contents of `markers-v2` and `rules-v1` (pattern lists, priorities, and kind-share thresholds) are tuned against the §4.4 corpus and the A3/A4 fixtures during implementation. Changing them does not alter the specs, the rule format, or the tasks.

## Addendum: decisions made during apply

Added after archiving (2026-10-02). These decisions were made or confirmed while M2 was implemented, and the code on `master` follows them. None deviates from `directory-first-curator-spec-v0.2.md`, so none needs an ADR. G2 below conflicts with §5.1, but it is a defect still to fix, not an accepted deviation.

### Jobs and discovery

**D18. A scan with pending work is requeued, not finished (D4).** A refine can commit between the scan handler's last "nothing pending" check and the runner marking the job succeeded. The refine then joins a job that is about to end, and the refined directory would never be listed. `jobs.PendingWork` is an optional handler interface. When `Run` returns nil, the runner calls `Pending` inside the transaction that would mark the job succeeded. If work is pending, the job is queued again without using an attempt. The scan handler reports any probe-pending or listing-pending node of the job.
- Rejected: having every later scan adopt directories stranded on finished jobs. The refine would wait until the next scan.
- Rejected: making refine detect finishing jobs. It cannot see the handler's in-memory state.
- The failure path is still open (G1).

**D19. The listing boundary check also compares the intent revision (D4).** Every listing batch re-checks, inside its transaction, that the parent is active, `expanded`, still listed by this job, and at the intent revision the listing started with. If not, the run ends `cancelled` with `boundary_changed` and creates nothing. The directory stays listing-pending, so the same job lists it again from the start.
- Rejected: D4's three conditions alone. They miss a collapse followed by a refine between two batches, which would leave the listing marked done while its earlier entries are inactive.

**D20. Listing state is durable.** A finished listing keeps `listing_job_id = job`, and a resumed attempt lists again only what it did not finish (paused or interrupted).
- Rejected: re-listing the root and every expanded directory on each attempt, as M1 did for the root. Repeating finished work grows with the size of the catalog.

**D21. The node budget counts reactivations as well as creations.** This keeps the active catalog bounded. The `nodes_created` counter still counts only creations.
- Rejected: counting creations only. An owner refine that reactivates history could then exceed the budget without limit.

**D22. Kind replacement at any depth deactivates the replaced node's active subtree,** clearing its frontier states.
- Rejected: deactivating only the node itself. A replaced expanded directory would leave active nodes beneath an inactive one.

**D23. Listing failures at depth end the job after the rest of the frontier.**
- A listing that fails at any depth ends the job with `permission_denied` or `observation_incomplete`, naming the first failed listing, once the rest of the frontier has been processed. This generalizes M1's root behavior for A5.
- An ancestor that can no longer be reached gets coverage `error` and an `access_error` issue. The job's pending items beneath it are dropped from this scan.
- Rejected: failing the job at the first failure. Unrelated directories would stay unprobed.

**D24. Frontier exceptions.**
- A reactivated directory is probed again even if this scan already probed it before an owner collapse and refine. That is the only exception to "probed once per scan", and it is what gets an expanded reactivated directory listed again.
- `RequestListing` refuses the source root, which only a source scan lists.
- A listing-pending directory that has since become a mount boundary is dropped from the frontier, not listed.
- Rejected: skipping the second probe. A reactivated expanded directory would stay without children.

**D25. A refine joins a scan that is being cancelled.** It answers 202 with the job in state `cancel_requested`, the same way `start-scan` does. The refined directory stays expanded with no entries until the next `start-scan` lists it.
- Rejected: refusing the refine, which needs an error code that does not exist yet.

### Classification

**D26. Walk indicators reach only trait rules.** Indicator signals found by an aggregate walk feed only rules that set traits. Rules that set a category see only the shallow descriptor.
- Rejected: letting every rule see walk signals. A vendored `.git` or `package.json` deep inside an installation would reclassify the unit as a source project. Walk evidence may add risk; it never changes what a unit is.

**D27. Name patterns before extensions.** `markers-v2` checks an ordered `[[file_kind_names]]` pattern list before the `[file_kinds]` extension table, and `FileKindOf` stays the single entry point.
- Rejected: classifying by extension only. It cannot recognise `GetRight-setup.exe` as an installer.

**D28. Callers supply `classify.Suggestion.CreatedAt` from their injected clock;** zero falls back to the wall clock. `Apply` takes no clock, and `classifications.created_at` is display-only; ordering uses IDs and revisions (D2).
- Rejected: adding a clock parameter to `Apply` or `Recompute`, which would change the frozen API that wave-2 slices coded against.

**D29. Writing `classifications.matches`.** The JSON is an array of objects with the keys `rule_id`, `priority`, `category`, `traits`, and `evidence`. A match on the directory's own name cites no evidence ID; the explanation names the directory instead.
- Rejected: inventing a pseudo evidence ID for the directory's name.

### Owner commands

**D30. Command responses (D6).**

| Command | Success response |
|---|---|
| `set-classification`, `collapse-node` | 200 `{node_id, intent_revision}` |
| `set-protection` | 200 `{source_id, pinned_path, protected, node_id\|null, intent_revision\|null}` |
| `set-disposition` | 200 `{items:[{node_id, outcome, intent_revision?, pinned_path?}]}`, one item per distinct node, in first-listed order |
| `refine-node`, `inspect-node` | 202 `Accepted` |

A 409 `protected` names the pinned path, as produced by `inventory.DisplayRelPath`, in `error.message`.
- Rejected: leaving response bodies to each slice. The UI needs fixed shapes.

**D31. A one-node `set-disposition` fails like a single-node command.** When the request lists exactly one distinct node, a non-applied outcome is returned as an error: 409 `revision_conflict`, 409 `protected`, 404 `not_found`, or 409 `invalid_node_state` for an inactive node. With two or more nodes the response is always 200 with per-item outcomes.
- Rejected: always answering 200 per item. The owner-intent scenario "Concurrent edits from two tabs" requires the second tab to get HTTP 409.
- Rejected: making only `protected` an error.

**D32. Owner command details.**
- **Check order:** for node-targeted commands, an unknown node is 404, then a revision mismatch is 409 `revision_conflict`, then a wrong state is 409 `invalid_node_state`.
- **Re-setting an override:** setting the same category again re-records the current descriptor digest and clears `evidence_changed`. This is how the owner confirms a flagged override.
- **Pin requests that change nothing:** a pin request for a state that already holds changes nothing, but it is still audited with `"changed": false`, so every accepted single-node command writes exactly one audit event.
- **Pins from peek tokens:** a pin given by peek token checks the unit's state and the source epoch, but never touches the filesystem.
- Rejected: re-checking each token step's identity on disk. A pin is a path assertion, and the check would need filesystem access inside a command.

### Inspection

**D33. Peek details (D10).**
- **Tokens and refusals:**
  - A token naming a unit that is no longer atomic is `stale_evidence`; without a token, the same node is `invalid_node_state`.
  - `needs_reconfirmation` is reported as `source_unavailable`.
  - The token epoch is checked before any `OpenDir`.
- **Reads:**
  - A peek never reads past its limit, so a directory with exactly `limit` entries reports `complete=false` with `entry_limit`.
  - Child tokens are issued for directories whose lstat succeeded; following one into an unreadable directory gives 403 `permission_denied`.
- **Blocking calls:** each call's deadline is watched from the request goroutine. On expiry the peek answers 503 and flags the source unresponsive. The blocked goroutine keeps the source's peek slot until its call returns.
- **Bookkeeping:** a peek writes no inventory row, but `BeginInspection` updates the source's availability bookkeeping.
- Rejected: reading one extra entry to detect the end of the listing. It would break the bound of N entries per peek.

**D34. Aggregate walk details (D11).**
- **Depth:** the unit's entries are level 1. Directories at `aggregate_max_depth` are counted but not entered, with stop reason `depth_limit`.
- **Failures:** individual failures are counted and the walk continues, with coverage `partial`. Stop reason `error` is used only when the unit's own listing fails.
- **Allocated bytes** are the sum of `st_blocks × 512` over regular files, the same scope as logical bytes.
- **Interruption:** shutdown or a lost lease commits the partial measurement as `cancelled`, and the requeued attempt starts over.
- **Late commit:** a measurement committed after the unit stopped being atomic is kept as history, but it is not linked to the unit and not classified.
- **Indicator examples** keep the first match of every signal before any repeats, at most 3 per signal and 20 in total. `markers-v2` defines 22 signals, so past 20 distinct signals a signal can be listed without evidence, and rules then treat it as absent.
- Rejected: raising `domain.MaxAggregateExamples`, which is a frozen contract.

### Accounting and UI

**D35. Accounting details (D12).**
- A file whose size was not recorded counts as a unit of unknown size.
- A mount boundary is always unknown; its measurement never counts.
- Symlinks count as links of 0 bytes.
- A covered node is reported against its outermost selected ancestor, the one whose totals include it.
- `Evidence` also carries the owner override and its evidence-changed flag, which the inspector needs.
- Rejected: counting unknown-size files as zero, which would make a lower bound look like a measurement.

**D36. The UI offers controls by node state.**

| Node | Controls offered |
|---|---|
| Atomic directory | override, disposition, protection, refine, aggregate, peek |
| Expanded non-root directory | override, disposition, protection, collapse |
| Undecided directory | override, disposition, protection |
| File, symlink, mount boundary | disposition, protection |
| Source root | protection |
| Inactive node | none |

Error pages map every domain code through `apierr.Status`.
- Rejected: offering an override on mount boundaries. The API accepts it, but boundaries are never counted in categories or queues, so the control would do nothing visible.

## Addendum: known gaps carried into M2a

- **G1. Refine race on the failure path.** `PendingWork` (D18) is consulted only when the handler succeeds. A refine that commits between a scan's final check and the runner marking the job *failed* leaves its directory listing-pending on a failed job until the next source scan.
  - Fix: also ask `Pending` when the handler returns a domain error, and requeue the job.
- **G2. Two jobs of one source can briefly run concurrently.** `Registry.DeviceKey` answers `source:<id>` until the source's identity is first recorded, and `dev:<n>` afterwards. A job enqueued before the first scan and one enqueued after it can run at the same time on different keys, bypassing §5.1's per-device worker limit for that window.
  - Fix: derive a stable key, or re-key queued jobs when the identity is recorded.
- **G3. False "evidence changed" flags.** Descriptor digests include the coverage budget. An override set while the latest descriptor came from an escalated probe (1,024 entries) is compared later against a normal probe (256 entries), so it is flagged as changed even though the entries are the same.
  - Fix: compare a digest that does not depend on the budget, or record the digest of the latest non-escalated descriptor.
- **G4. Walk-derived traits can drop out.** Rules use only the *current* measurement. A trait found by a walk disappears once that measurement goes stale, or when a later partial walk misses the indicator; traits from the shallow probe stay. M2a's invalidation work should decide whether walk-derived risk persists until it is disproved.
- **G5. Duplicated pin logic.** The "which pin blocks this node" logic exists twice, as `protection.Blocking` and as the inventory reader's `blockingPin`.
  - Fix: point the inventory reader at `protection.Blocking`.
