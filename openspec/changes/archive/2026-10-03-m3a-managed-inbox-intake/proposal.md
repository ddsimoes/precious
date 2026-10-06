# Proposal

## Why

After M3, a file copied into the archive is noticed only at its directory's next scheduled pass, which runs every 15 minutes or later. Nothing tracks whether the copy has finished, and nothing suggests where the arrival belongs. All work on a device also runs in arrival order, so one long scan or aggregate walk delays everything else. This change is milestone **M3a, managed inbox intake** (§14, §4.5, §9.4). It makes **A32, A33, A36, A37, A39, A40, and A41** pass. It also covers A27's notification-overflow and watch-limit cases, which ADR 0002 moved here. Nothing is moved or written on disk: organization arrives with M5. All earlier acceptance scenarios keep passing.

## What Changes

- **Inbox role (§4.5, §9.4.5, A40):**
  - The owner sets the `inbox` role on an active expanded directory with `set-inbox-role`. On activation, the owner chooses whether to enroll the current children or leave them as cataloged items.
  - The role is refused inside an atomic ancestor, inside another inbox, or around one.
  - An inbox and its ancestors cannot be collapsed or marked `cleanup_candidate` until the role is removed.
- **Intake lifecycle (§4.5, §9.4.1, A32, A41):**
  - Each new immediate child is one durable intake item, kept open until it ends.
  - Repeated events for the same occurrence update that open item. A replacement at the same name starts a new item.
  - The item's generation advances only when observed evidence changes.
  - Owner commands: `pause-inbox`, `mark-intake-ready`, `retry-intake`, and `ignore-intake`.
- **Readiness (§9.4.2, A32, A33):**
  - `heuristic_stable` requires the configured quiet interval: two unchanged observations at least 5 s apart and no change for 10 s.
  - Names that match the temporary-name patterns never become ready on their own.
  - A quiet top level of a directory allows only provisional triage; nested writes are never ruled out.
  - `handoff_confirmed` comes only from `mark-intake-ready`. A change clears it, and the item settles again.
- **Express triage (§9.4.3, §6, §7.5):**
  - One intake job per source polls its inboxes every 5 s and probes arrivals within the shallow budget. It runs the rules once per settled generation, and arrivals are never expanded automatically.
  - Inbox arrivals are routed by grants on the inbox itself, one per task (`directory_category`, `file_kind`), set with `set-classifier-policy`.
  - A loose-file arrival is sent as a `file_kind` request only when its name hint is `unknown`. An in-flight result for an older generation becomes history only.
  - New arrivals are never suggested for cleanup; they default to review.
- **Destination proposals (§9.4.4, A37):**
  - The owner names destination collections (`set-routing-destination`) and deterministic routing rules over category, file kind, and inbox (`set-routing-rule`).
  - A proposal names the destination parent, the basename, the rule, and the revisions it relies on.
  - A missing match, a conflicting rule, a same-name conflict, or a destination that is stale, atomic, another inbox, or in another source is shown as an explicit blocker.
- **Priority and fairness (§8.4, A36):** jobs on one device are ranked by class: inbox intake, then reconciliation, then bulk walks. Each class gets a weighted share of the device, and waiting work ages into a higher class. Long jobs give up the device between bounded work units, so neither inbox work nor a bulk job starves.
- **Notifications (§5.5.1, §5.5.3, A27):**
  - Linux inotify watches are added through `fsnotify` within a watch budget, inbox boundaries first.
  - Events are coalesced over 500 ms, with a 2 s maximum delay, into durable dirty scopes.
  - A queue overflow opens a coverage gap. A watch budget or OS limit degrades to visible polling.
  - Polling stays the baseline, and inboxes are polled even when no notification arrives.
- **UI and API (§9.1, §9.3, A39):**
  - An inbox page grouped by intake state, with readiness basis, generation, evidence limits, profile, proposal, blockers, and controls. Organization is shown as unavailable.
  - Inbox counts on the overview and intake state in the node inspector. Index health reports watcher state.
  - New read endpoints `GET /api/inboxes` and `GET /api/inboxes/{id}/items`.
- **BREAKING (internal):** migration `0005`. Jobs on a device are no longer claimed in plain arrival order.

Deferred, with owning milestone:
- Approving a proposal into a move plan, the states `planned`, `organizing`, and `organized`, and A34, A35, A38, and A44 → **M5**.
- The cooperative staged-producer handoff protocol and its staging-area exclusion → **M5**, with A34, its only consumer.
- Splitting an intake package into separate items → **M5**. Refining an arrival is allowed, and the arrival stays one item.
- Owner-written classification rules, rules created from corrections, owner tags, and routing-rule previews from corrections → **M5**.
- Watching selected atomic boundaries, and duplicate reports for arrivals → **M4**.
- Model classification of loose files outside inboxes is not planned (§5.2, §6).

## Capabilities

### New Capabilities
- `inbox-intake`: the inbox role and its lock, enrollment, the intake lifecycle and generations, readiness, the intake job, and express triage of arrivals.
- `inbox-routing`: destination collections, routing rules, and destination proposals with their blockers.

### Modified Capabilities
- `incremental-reconciliation`: inbox scopes are polled on their own interval by the intake job, and notifications become low-latency hints with a watch budget, coalescing, and degradation on overflow or OS limits.
- `job-runner`: priority classes, weighted fair sharing of each device, aging, and yielding between work units.
- `inventory-boundaries`: collapse is refused for an inbox and its ancestors.
- `owner-intent`: `cleanup_candidate` is refused for an inbox and its ancestors.
- `classifier-routing`: inbox grants per task, automatic classification of ready arrivals (including `file_kind` for loose files), dispatch rechecks under the inbox grant, and results bound to the item generation.
- `inventory-explorer`: watcher state in index health, the inbox page, the inbox read API, overview counts, and intake state in the inspector.
- `server-config`: `[inbox]` and `[watch]` settings are validated.

## Impact

- **New package:** `internal/inbox` (intake lifecycle, readiness, routing destinations, rules, and proposals).
- **Changed packages:**
  - `jobs` (classes, fair device arbiter, `Runtime.Yield`, `Tx.WakeOnce`);
  - `watch` (inotify backend, coalescer, watch budget);
  - `discovery` (intake job, enrollment hook, arrival decisions);
  - `classify` (arrival triage, role lock), `classify/inference` and `classify/permission` (inbox grants, file requests);
  - `intent`, `commands`, `inventory`, `web/explorer`, `config`, `domain`, and `cmd/curator`.
- **Configuration:** new `[inbox]` and `[watch]` sections.
- **Database:** migration `0005`.
- **Docs:** `docs/operator.md`, with a new managed-inbox section and an M3 to M3a upgrade note.
- **Dependencies:** `github.com/fsnotify/fsnotify`, the library §5.5.1 names; it needs only `golang.org/x/sys`, which is already required.
