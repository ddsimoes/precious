# Tasks

## 1. Schema, domain types, and configuration

- [x] 1.1 Write `migrations/0002_directory_policy.sql` per design D3: the new `nodes` columns, the `classifications`, `category_overrides`, `protection_pins`, and `aggregates` tables, `sources.protection_revision`, `jobs.scope_key` with its active-scope unique index, the D3 indexes, and the `mode_set_by='policy'` backfill. Verify with store tests:
  - a fresh database migrates to version 2;
  - a seeded M1 database upgrades with every row kept, `mode_set_by` backfilled, and `category` NULL;
  - constraints reject a second `current` suggestion per node and kind, a duplicate pin per source and path, a second active aggregate job per scope key, and an unknown disposition.
- [x] 1.2 Add `internal/domain` types and constants:
  - categories (13), traits (6), dispositions (including reserved `quarantined`), triage values, protection states, `mode_set_by`, file kinds (9);
  - the M2 decision reasons (`policy_coherent_unit`, `policy_expand`, `refinement_depth_reached`, `insufficient_evidence`, `owner_refined`, `owner_collapsed`);
  - error codes `protected`, `invalid_node_state`, `stale_evidence`, mapped to HTTP 409 in `apierr`.

  Verify with parse/validate unit tests and an `apierr` status-mapping test.
- [x] 1.3 Add the D15 configuration keys (`[discovery] auto_refine_depth` and `escalated_entry_budget`; the `[inspection]` section) with defaults, range validation, and `check-config` output. Document them in `deploy/examples/read-only-no-model.toml`. Verify with table tests for each range rejection, and confirm that `check-config` accepts the updated example.
- [x] 1.4 Add `Blocks` to `fsaccess.EntryInfo`, filled from `st_blocks` in the OS backend. Make `synthfs` report allocation rounded up to 4 KiB unless a test sets it, and pass the field through `instrument`. Verify with a real-tempdir test (a sparse file reports fewer allocated bytes than logical bytes) and a `synthfs` unit test.
- [x] 1.5 Extend `internal/inventory/inventorytest` seeding and the discovery test helpers for the new columns. Verify that `go test -race ./...` passes unchanged.
- [x] 1.6 Update the configuration reference in `docs/operator.md` for the new keys. Verify that the existing documented-example decoding test covers them.

## 2. Classification

- [x] 2.1 Write `policies/markers/v2.toml` (`markers-v2`) per D1: at most 16 markers, the new user-data, cache, temporary, generated, OS, and container signals, and the `[file_kinds]` table. Extend `discovery.MarkerPolicy` to load file kinds. Raise the descriptor schema to version 2 with `file_kind_counts` in the payload and the canonical form. Verify with:
  - loader tests (unknown keys and more than 16 markers rejected);
  - the **File-kind counts** scenario test;
  - a digest test (same evidence gives the same digest);
  - a test that version-1 descriptors still render in the inventory reader.
- [x] 2.2 Write `policies/rules/v1.toml` (`rules-v1`) and a strict loader in `internal/classify` that rejects unknown keys, categories, traits, and signal IDs. Verify with loader table tests.
- [x] 2.3 Implement `classify.Evaluate` (D2): highest priority wins, ties give `unknown` with `conflicting_rules`, no match gives `no_rule_matched`, traits are unioned, and evidence IDs are cited. Verify with table tests for:
  - **Installation recognized**;
  - **Identical evidence gives identical result**;
  - **No rule matches**;
  - **Source project with version control**;
  - **Equal-priority conflict**;
  - **Priority resolves overlap**.
- [x] 2.4 Implement preservation risk and triage (D9) as pure functions. Verify with tests for **Cache suggested as a candidate** and **Protected cache is not a candidate**, and for risk turning cleanup into review.
- [x] 2.5 Verify **A4 saves inside an apparent installation** and **A4 profile database inside an installation** with probe-plus-classify tests over `synthfs`. Each asserts the trait, preservation risk with cited evidence, and triage `review`.
- [x] 2.6 Implement `classify.Apply` (D2): the revision check against epoch, observation revision, and intent revision; supersede of the previous current row; `stale` rows; and recomputation of effective category, traits, risk, and triage in precedence order. Verify with tests for **Owner override beats rule** and **Rescan appends history**.
- [x] 2.7 Verify **A7 owner override followed by a stale model response** and **A7 override outranks fresh suggestions**: inject model-kind suggestions through `classify.Apply` around an owner override, then run a rescan.
- [x] 2.8 Implement the explanation builder: rule `explain` sentences plus coverage sentences. Verify with the **Limited evidence explained** test, and with a test that no explanation produced over the corpus contains the forbidden claims (no personal files, available again, no unique content).
- [x] 2.9 Build the §4.4 regression corpus as a `synthfs` fixture: installed program with a nested personal document, application profile, game with saves, old source project with private libraries, and temporary directory with recovery material. Record the expected shallow classifications, traits, and risk. Verify that the corpus test passes.
- [x] 2.10 Write the `docs/operator.md` "Classification" section: categories, traits, rule and observation policy versions, precedence, preservation risk, suggested triage, and explanations. Update "Markers and signals". Verify the section's worked examples against the outputs of the 2.9 corpus test.

## 3. Discovery policy

- [x] 3.1 Generalize the frontier (D4): probe-pending and listing-pending steps taken in lowest-node-ID order; a parent-chain opener that checks identity and caches the last chain; removal of the depth-1 assumptions. Verify with tests for **Shallower directories decided first** and for crash-resume of a scan stopped at depth 2.
- [x] 3.2 Implement `listDir(node)` for any expanded directory: listing runs, batches, node budget, and the in-transaction parent check that ends with stop reason `boundary_changed`. Verify with the **Listing a policy-expanded directory** test, and with a hook test that collapses the parent between batches and finds no active node beneath it.
- [x] 3.3 Implement `commitDecision` (D5): descriptor insert, the override `evidence_changed` flag, `Evaluate` plus `Apply`, the mode decision, and the listing-pending rule. Verify with tests for **Coherent unit stays atomic**, **Mixed container expanded**, **Owner mode wins over policy**, and an already expanded directory staying expanded.
- [x] 3.4 Apply the automatic refinement depth and node budget to policy expansions. Verify with the **Depth limit reached** test and a node-budget pause/resume test across expansions.
- [x] 3.5 Implement the single escalated probe (D5). Verify with **Escalation then review** (the instrumented second probe examines at most 1,024 entries, and the directory is probed twice in total) and **Complete listing is not escalated**. Confirm that the existing A1 tests pass unchanged.
- [x] 3.6 Record name-derived file-kind hints on loose files during listing. Verify with the **Loose file kind hint** test, which also asserts that no file is opened.
- [x] 3.7 Add the version-2 `scan` payload (`source` or `frontier` scope; version 1 reads as `source`) and `Registry.BeginInspection`, which checks like `BeginScan` but leaves `last_scan_at` untouched. A source scan marks every active expanded directory for listing. Verify with tests for:
  - **Rescan reclassifies with new rules** (using a test rule-set version);
  - **Policy does not collapse**;
  - payload version-1 compatibility;
  - `last_scan_at` unchanged by a frontier scan.
- [x] 3.8 Fill descriptor ancestor names at depth. Verify with the **Ancestor context at depth** test.
- [x] 3.9 Update the `docs/operator.md` discovery sections: the frontier at depth, policy decisions and their reasons, automatic refinement depth, escalation, and rescans. Replace "Atomic directories until M2". Verify by running the documented decision examples against the 3.3 and 3.4 test fixtures.

## 4. Owner intent and inventory boundaries

- [x] 4.1 Implement `internal/intent` and the `set-classification` command (D6): set or clear an override with its descriptor digest, check the expected revision, and write an audit event in the command transaction. Verify with tests for **Override survives rescan**, **Clearing an override**, and **Concurrent edits from two tabs**.
- [x] 4.2 Verify **Directory contents changed after override** end to end: re-probe with changed entries, then check that the override stays effective, `evidence_changed` is set, and the node is in the ambiguous queue query.
- [x] 4.3 Implement `set-disposition`: exact item list up to the page size, de-duplication, per-item outcomes, rejection of `quarantined`, exclusion of protected items, and audit events. Verify with tests for:
  - **Classification never sets disposition**;
  - **Quarantined disposition rejected**;
  - **Mixed bulk outcome**;
  - **Duplicate IDs in a bulk command**;
  - idempotent replay.
- [x] 4.4 Implement the peek token codec (D10) in `internal/inspection`: HMAC-SHA256 with a per-process key, TTL, a 64-step cap, and verification of node, epoch, and step identities. Verify with codec unit tests for round-trip, tampered byte, expired token (fake clock), foreign node, and too many steps.
- [x] 4.5 Implement path-scoped pins and `set-protection` by node or peek token (D8). Keep `protection` and `contains_protected` up to date on pin change and on node insert or reactivation in discovery. Bump `protection_revision` and the exact node's `intent_revision`. Verify with tests for:
  - **Inherited and contained protection**;
  - **Pin inside an atomic unit**;
  - **Ancestor of a protected path** (409 `protected`);
  - a property test comparing stored states with states recomputed from pins over random trees and pin sets.
- [x] 4.6 Implement `refine-node`. It rejects files, symlinks, mount boundaries, and non-atomic directories with `invalid_node_state`; sets the node owner-expanded; marks it for listing; and joins the source's active scan, resumes a paused one, or creates a `frontier` scan. Verify with tests for **Refining a mount boundary**, **Refine joins a running scan**, and **Refine without an active scan** (the instrumented filesystem shows only the refined directory and its children touched).
- [x] 4.7 Verify **A3 refine a mixed backup** with the `Backup2003` fixture (exact immediate entries; `application_installation`, `personal_media`, and `source_project` children; all atomic; no descendants). Also verify **Children do not inherit the parent's verdict**.
- [x] 4.8 Implement `collapse-node` as one transaction (D7). Verify with tests for:
  - **Collapse leaves no active descendants**;
  - **Collapse of a source root** (409 `invalid_node_state`);
  - **Collapse during an in-flight expansion**;
  - a 50,000-descendant collapse whose duration is logged in the test output.
- [x] 4.9 Implement reactivation in child upsert by name, kind, dev, and ino; any other entry becomes a new node. Verify with tests for **Same identity after collapse and refine**, **Replacement at the same name**, and **Pin survives collapse and re-expansion**.
- [x] 4.10 Verify sticky owner modes with the **Owner-collapsed mixed directory stays atomic** test.
- [x] 4.11 Add an invariant property test: random sequences of scans, refines, collapses, and simulated process kills over `synthfs`, after which no active node has an atomic or inactive ancestor.
- [x] 4.12 Verify **Protection change audited**, and that every owner command writes exactly one audit event with no file content.
- [x] 4.13 Write the `docs/operator.md` "Owner decisions" section: overrides, evidence-changed flags, dispositions, protection pins (including pins inside atomic units), refine and collapse, revision conflicts, and audit event kinds. Verify that each documented command body is accepted by its decoder in a docs example test.

## 5. Inspection: peek and aggregate walks

- [x] 5.1 Implement the peek service and `GET /api/nodes/{id}/peek` (D10): open the parent chain and token steps, take a bounded listing with lstat and readlink, issue child tokens for enterable directories, compute per-entry protection, allow one in-flight peek per source, and apply the watchdog deadline. Verify with tests for:
  - **Nested mount inside a unit**;
  - symlinks listed with link text and not followed;
  - special files listed and not opened;
  - a non-atomic node giving `invalid_node_state`;
  - an unavailable source giving `source_unavailable`.
- [x] 5.2 Verify **A8 peek inside a large atomic unit**: a 100,000-entry `synthfs` unit, the instrumented call log shows only `App` listed, and node, descriptor, aggregate, and issue counts and `App`'s mode and intent revision are unchanged.
- [x] 5.3 Verify **A8 drill-down stores nothing**: follow tokens two levels down, then compare a checksum of every inventory table and the parent's explorer listing before and after.
- [x] 5.4 Verify the token rejection scenarios through the API: **Forged token rejected** (no directory opened) and **Directory replaced by a symlink** (a pre-open hook swaps the directory; the result is `stale_evidence` and nothing is read through the link).
- [x] 5.5 Implement the `aggregate` job (D11): `inspect-node` command, `scope_key` single-flight, iterative walk with budgets and stop reasons, epoch-checked commit, rule re-evaluation with indicator signals through `classify.Apply`, and partial commit on cancellation. Verify with tests for:
  - **Hard links stay visible**;
  - **Nested mount inside a measured unit**;
  - **Cancelled walk keeps a lower bound**;
  - **Repeated aggregate request**;
  - **Size is never a hidden prerequisite**;
  - an epoch change that discards the measurement.
- [x] 5.6 Verify **A2 aggregate the same atomic unit**: a 100,000-entry `synthfs` unit gives a measurement of 100,000 entries with logical bytes; `App` stays atomic with zero active descendants; and the total node count is unchanged.
- [x] 5.7 Implement measurement freshness at read time. Verify with tests for **Measurement goes stale** (fake clock) and **Source epoch change**, and with a test that a rescan does not renew a measurement.
- [x] 5.8 Verify **A4 nested save found by a walk**. Using the 2.9 corpus, also verify that the nested personal document raises preservation risk after a walk, and that a walk finding nothing removes no shallow trait.
- [x] 5.9 Extend the 4.11 invariant property test with aggregate walks and peeks. Verify that it passes under `-race`.
- [x] 5.10 Write the `docs/operator.md` "Inspecting atomic units" section: peek limits and tokens (nothing stored, restart invalidates tokens), aggregate walks, budgets, coverage, lower bounds, staleness, and the note that a walk holds the device's filesystem worker. Verify the documented limits against the D15 defaults printed by `check-config`.

## 6. Accounting and review queues

- [x] 6.1 Implement `inventory.Totals` with size classes (D12) for source and expanded-node scopes, and move the overview summary onto it. Verify with tests for **Measured directory later refined** and **Mixed size classes**, and confirm that the M1 **Unknown sizes stay visible** test still passes.
- [x] 6.2 Verify **A9 totals after expansion and collapse**: measure a unit, refine it, collapse it, and compare source totals at each step.
- [x] 6.3 Implement `inventory.NormalizeSelection` and `GET /api/selection-summary` (exact IDs up to the page size, covered-by-ancestor reporting, size-class totals, protected exclusions for a target disposition). Verify with **A9 overlapping selection** and **Preview lists excluded protected items**.
- [x] 6.4 Implement the review queues (D13) and `GET /api/review` with keyset paging. Verify with tests for:
  - **Installation in the cleanup queue**;
  - **Risky installation goes to preserve, not cleanup**;
  - **Decided units leave the queues**;
  - ambiguous membership for `refinement_depth_reached`, `insufficient_evidence`, `conflicting_rules`, and evidence-changed overrides.
- [x] 6.5 Verify **Live query changes after the page loads**: a unit added to the queue between the page read and the bulk command stays `unreviewed`.
- [x] 6.6 Verify **Review on a read-only source without models**: queues are filled by rules alone, bulk dispositions succeed, and the instrumented filesystem records zero mutating calls.
- [x] 6.7 Update `docs/operator.md` "Reading sizes and coverage honestly" (size classes, lower bounds, stale measurements) and add a "Review queues" section. Verify the documented queue rules against the 6.4 test fixtures.

## 7. Web interface

- [x] 7.1 Extend the node read model and JSON API: policy, intent, protection, measurement, and file-kind fields on nodes; suggestion history and the latest measurement in evidence. Verify with API tests for **Node carries policy and intent fields**, and confirm that the A17 lossless-name API test still passes.
- [x] 7.2 Extend the `row` partial and its JS twin `renderRow` with category and source, disposition, protection, risk, and size class. Verify with HTML tests for **Atomic row** and **Expanded row**, and confirm that **Cursor pagination** still passes.
- [x] 7.3 Extend the inspector: classification with explanation, alternatives and history, owner forms, measurement, and peek link. Verify with HTML tests for **Classification explained with provenance** and **Controls follow node state**.
- [x] 7.4 Extend the overview with category counts, queue counts with links, and size classes. Verify with the **Queue and category counts** HTML test.
- [x] 7.5 Add the review page: queue tabs, row checkboxes, a preview panel from `GET /api/selection-summary`, confirmation, and per-item results; there is no select-all-matching control. Verify with HTML tests and a headless-Chrome run under the production CSP that completes a bulk decision with no CSP violation.
- [x] 7.6 Add the peek page: server-rendered listing, token links, and Protect buttons. Verify with HTML tests, including the "first N entries in directory order, not a sample" wording.
- [x] 7.7 Extend `web/static/app.js` with the six owner commands (each carrying its expected revision) and reload the node on a 409 `revision_conflict`. Verify with a headless-Chrome run under the CSP that performs override, protect via peek, refine, collapse, and aggregate with no console errors.
- [x] 7.8 Verify **A15 malicious filename** on the new surfaces: names such as `<img src=x onerror=alert(1)>` in peek listings, review rows, explanations, and selection previews render as inert text in both HTML and JSON.
- [x] 7.9 Update the `docs/operator.md` "Using the web interface" section: pages, review flow, peek, owner controls, and what a revision conflict means. Verify that every page named there has a route in `explorer.New`.

## 8. Integration

- [x] 8.1 Wire `curator serve`: register the `aggregate` handler, pass intent, inspection, and policy dependencies to the commands and explorer, generate the peek key, and plumb the new configuration. Verify that `go build ./... && go vet ./...` succeed.
- [x] 8.2 Extend `TestA20ReadOnlyWorkflowWithoutModels` (**A20 read-only and cloud disabled**) to the M2 workflow over the real server stack: scan, review queues, override, protect through peek, refine, collapse, aggregate, and peek, with no write to the source and no outbound request.
- [x] 8.3 Write `docs/adr/0001-a9-split-across-m2-and-m5.md` (D17). Update the `docs/operator.md` "Upgrades" section for M1 → M2: back up, migration `0002`, rescan each source, and rollback by restore.
- [x] 8.4 Run `go vet ./...`, `go test -race ./...`, and `go test -race -tags e2e ./...`. Run the mount tests in a privileged `golang:1.27.1` container where user namespaces are blocked. Verify that everything passes, including the A1, A5, A11, A15, A17, and A20 regressions.
- [x] 8.5 Smoke-check the built binary:
  1. `check-config` on the updated example;
  2. `serve` on loopback against a fixture tree holding an installation with saves, a mixed backup, a cache, and a large unit;
  3. over HTTP, confirm that the queues are populated, the A4 risk is shown, totals match before refine and after collapse, a peek leaves database row counts unchanged, and an aggregate walk completes;
  4. `curator backup` passes the integrity check.
