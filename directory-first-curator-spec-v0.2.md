# Directory-First Archive Curator
## Initial implementation specification - revised

**Version:** 0.2  
**Date:** 2026-10-01  
**Status:** Superseded by `precious-spec-v0.3.md` ([ADR 0008](docs/adr/0008-product-reset.md)); kept as history  
**Implementation language:** Go  
**Deployment:** Self-hosted, remotely accessible web application  
**Working executable name:** `curator` (not a product-name commitment)

This document defines product behavior, architectural boundaries, implementation order, and acceptance tests. MUST denotes a required invariant; SHOULD allows a documented implementation tradeoff. Numeric limits below are initial configuration defaults, not measured performance claims.

## 1. Product objective

Help an owner understand and curate a personal filesystem accumulated over decades: old installations, application profiles, caches, source projects, photographs, documents, downloads, backups, archives, duplicate copies, and divergent revisions.

The first useful outcome is **a manageable inventory of meaningful directory-level objects**, not an exhaustive table of every file. The owner should identify obvious cleanup candidates, preserve potentially important material, and progressively subdivide mixed directories.

The primary optimization target is **time to a useful, evidence-backed human decision**. Initial discovery MUST NOT depend on hashing the entire collection, recursively cataloging every directory, or having an AI API key.

### Changes in version 0.2

This revision integrates three first-release requirements throughout the architecture, persistence model, web interface, and tests:

- Provider-neutral classification with interchangeable local or remote backends, explicit capability differences, and comparable evaluation/cost reporting. Jev is one adapter, not the domain contract.
- Incremental indexing of a changing filesystem, using durable dirty scopes, scheduled reconciliation, and optional filesystem notifications without breaking atomic directory boundaries.
- Managed inbox directories for continuous intake, fast classification, review, and explicitly approved organization through the existing action engine.

This is a complete replacement for version 0.1, not an addendum. The existing directory-first and mutation-safety requirements remain in force.

### Core workflow

```text
Register source roots
    -> shallow directory discovery
    -> evidence collection and classification
    -> choose semantic boundaries
    -> review whole units
    -> quarantine obvious unwanted units
    -> refine remaining mixed containers
    -> selectively inventory valuable content
    -> compare duplicates, archives, and revisions
    -> organize retained material
    -> reconcile changes continuously at the chosen inventory boundaries

Drop new files or directories into a configured inbox
    -> observe arrival and copy readiness
    -> run prioritized, bounded classification
    -> suggest a configured destination
    -> owner reviews and approves an organization plan
    -> move the intact item and reconcile both locations
```

### Guiding distinction

An installed application can be represented by one inventory object even when it contains 100,000 physical entries. An application profile can also be one object, but protected rather than disposable. **Atomicity is a cataloging decision, not a judgment of value.**

## 2. Scope and delivery boundaries

The first usable release, delivered through milestones M1-M5 including M2a and M3a below, includes authenticated remote access; multiple configured local source roots; directory-first discovery; atomic units; interchangeable optional classifiers; evidence and manual overrides; progressive refinement; incremental reconciliation; managed inboxes with prioritized intake; durable jobs; basic selective file hashing; and explicitly approved same-filesystem quarantine, restore, and reorganization.

Dynamic storage is a supported indexing use case, not an exceptional error. Indexing is eventually consistent within declared observation scopes. Mutation is a separate capability with stronger, item-scoped quiescence and validation requirements.

Permanent purge is a separately gated capability delivered only after quarantine/recovery tests pass. It is never a prerequisite for using the inventory.

Later milestones add archive-to-directory comparisons, directory overlap analysis, document families, richer previews, and semantic search. Their data-model constraints are specified now, but they MUST NOT delay the directory-first workflow.

Initial supported server platforms are Linux amd64 and arm64. Historical Windows directory layouts are data to inspect, not a requirement for a Windows server implementation. Mounted filesystems may be inventoried when readable; writes require an explicitly tested filesystem capability profile. Network mounts are read-only in the first release.

Non-goals: cloud storage hosting, filesystem synchronization, antivirus scanning, executing recovered programs, modifying active application profiles, distributed scanning, multiple tenants, automatic file merges, automatic deletion, and an exhaustive global content index.

## 3. Non-negotiable invariants

1. **Directory first.** Classification happens before recursive inventory decisions.
2. **Real atomicity.** An atomic directory has no active descendant inventory records. Do not index everything and merely hide it in the UI. Bounded evidence and aggregate statistics are allowed.
3. **Observation is not mutation.** Discovery, classification, summarization, and hashing never intentionally alter source content. Read-only mounts are the strongest default deployment boundary.
4. **Unknown is not empty or safe.** Partial listings, unreadable entries, unsampled descendants, and unavailable volumes remain explicit.
5. **Classification is not authorization.** Rules and model outputs may change labels, inspection priorities, and suggestions; they never authorize filesystem mutations.
6. **Human intent survives rescans.** Protection pins and explicit corrections cannot be silently overwritten by jobs or model updates.
7. **No global completeness fiction.** Duplicate and uniqueness statements name their inspected scope. Opaque subtrees cannot be claimed to contain no unique files.
8. **No replacement by accident.** Moves and restores never overwrite an existing destination. A failed or ambiguous operation stops for review.
9. **Provider independence.** Domain services, UI, policies, and persistent effective classifications do not depend on Jev primitives or any vendor SDK. Unsupported measurements remain absent, never fabricated.
10. **Change-aware, scope-honest indexing.** Filesystem events are hints; reconciliation establishes observations. An atomic node never implies recursively monitored or currently verified contents.
11. **Inbox is a role, not a content category.** Automatic intake may observe and suggest; moving or deleting an arrival still requires explicit authorization and current preconditions.
12. **Late work cannot win.** Every asynchronous result is conditional on the source epoch, item generation, relevant evidence, and owner-intent revisions it observed.

## 4. Domain model

### 4.1 Sources, observations, and inventory objects

A `Source` is an administrator-configured directory accessible to the server process. The browser selects a source ID; it cannot register arbitrary absolute filesystem paths.

Store a source's configured root, label, availability, observed filesystem identity, read/write mode, privacy policy, inspection budgets, quarantine configuration, reconciliation schedule, watcher health, and source epoch. Record filesystem and mount identity when available. A newly mounted filesystem at an old path requires reconfirmation; it is not automatically the same source.

An `InventoryNode` represents an observed file, directory, or symlink. Its stable application ID is distinct from its path, content digest, and operating-system identity. Device/inode information is useful evidence, not a globally permanent identifier.

Store parent IDs and lossless names; derive full relative paths. Preserve raw filename bytes in the database and maintain a separately escaped display name. Do not lowercase or Unicode-normalize identity paths. Browser operations use node IDs, not display strings converted back into paths.

A file's content identity is established only after a complete successful hash. Multiple occurrences can reference the same `Content` record. An opaque directory has no content hash unless an explicit whole-tree analysis has actually computed one.

### 4.2 Keep these dimensions separate

| Dimension | Initial values | Meaning |
|---|---|---|
| Physical kind | `directory`, `file`, `symlink` | What was observed on disk |
| Directory role | `normal`, `inbox` | Operator-assigned workflow purpose, not an inferred category |
| Primary directory category | Section 4.3 | What a directory appears to represent |
| Loose-file kind | Section 6 | Format/purpose hints for an individually cataloged file; distinct from directory categories |
| Inventory mode | `atomic`, `expanded` | Whether descendants belong to the active catalog |
| Inspection capabilities achieved | shallow listing, aggregate walk, hashes, text extraction | Independent facts; not one misleading ordered level |
| Disposition | `unreviewed`, `preserve`, `cleanup_candidate`, `review`, `quarantined` | Owner-facing workflow state |
| Protection | explicit pin plus inherited protection | A hard policy barrier to destructive operations |
| Evidence completeness | `unknown`, `partial`, `complete`, `error` | Completeness relative to a named observation scope |
| Observation freshness | `never_observed`, `current`, `dirty`, `stale`, `unavailable` | Freshness of that scope, never a claim about hidden descendants |

An expanded directory is a container whose immediate entries may be inventoried. Each child directory independently becomes atomic or expanded. A source project can remain one protected atomic unit during initial triage, then be expanded later to separate source files from generated output.

Previously indexed descendants of a newly atomic node may be retained as historical observations, but MUST become inactive. They must not appear as current search results, current duplicate evidence, or additive dashboard totals.

### 4.3 Initial category taxonomy

| Category | Definition | Default triage behavior |
|---|---|---|
| `personal_media` | Apparently personal photograph, video, or audio collection | Preserve; hold as a unit until refinement is requested |
| `source_project` | Source project or source collection | Preserve; selective refinement later |
| `documents` | User-facing documents and authored material | Preserve; selective refinement later |
| `application_installation` | Installed program payload and its supporting resources | Atomic; possible cleanup candidate |
| `application_user_data` | Profiles, databases, mail stores, saves, or other application-held user material | Atomic and preserve |
| `application_configuration` | Settings and customization | Atomic; preserve or review |
| `cache` | Apparently cached derivative data | Atomic; possible cleanup candidate |
| `temporary_data` | Apparently temporary working material | Atomic; possible cleanup candidate |
| `generated_artifacts` | Apparently generated build output or derived assets | Atomic; possible cleanup candidate |
| `os_installation` | Copied operating-system installation material | Atomic; review before cleanup |
| `download_collection` | Collection of downloaded items rather than one application | Expand incrementally |
| `mixed` | Evidence of distinct semantic units requiring separate treatment | Expand incrementally |
| `unknown` | Insufficient or conflicting evidence | Bounded additional probe, then review |

`mixed` and `unknown` are different: the first identifies a container to subdivide; the second identifies an information gap. A large `Program Files` collection may need expansion into individual applications rather than one irreversible classification for the entire collection.

Add orthogonal traits such as `backup_container`, `contains_vcs`, `contains_database`, `contains_credentials`, `contains_user_material`, and `possible_generated_content`. These are not proof of ownership or recoverability.

Keep reproducibility separate: `unknown`, `owner_declared_unneeded`, `regeneration_verified`, or `retained_copy_verified`. Names such as `cache`, `target`, `node_modules`, or `temp` do not prove that rebuilding or redownloading remains possible.

### 4.4 Preservation examples

The regression corpus MUST contain an installed-program folder with a nested personal document; an application profile; a game directory with saves; an old source project with private libraries; and a temporary directory containing recovery material.

A user-data indication vetoes an automatic *low-risk suggestion*. It does not require cataloging the whole subtree: the application can keep the directory atomic, protected, and pending review.

### 4.5 Inbox configuration and intake identity

An `Inbox` assigns the `inbox` role to an existing directory node within a configured source, or to the source root itself. It is not a second source and does not bypass the prohibition on overlapping source roots. An inbox remains expanded so immediate arrivals are visible; each arriving directory can independently remain atomic.

Store inbox ID, directory node ID, configuration revision, intake enabled/paused state, readiness policy, classifier routing profile, inspection budgets, permitted destination IDs, and polling schedule. The inbox role can only be set by the owner, never by a classifier. Support multiple inboxes, but reject nested inboxes in the first release.

Reject an inbox inside quarantine, application state, an excluded mount, or a currently atomic ancestor. The UI can offer an explicit path-only refinement to expose the intended inbox; it must not secretly expand every sibling. Refuse collapsing or moving an inbox or its ancestor until its role is removed and pending work is resolved.

An `IntakeItem` references an ordinary inventory node. It does not duplicate the filesystem catalog. One immediate child is one arrival: a directory is a package, a loose regular file is an individual item, and an archive is an opaque file. Symlinks are review-only; unsupported special entries receive a visible issue rather than being opened.

Persist arrival time, original relative location, node/occurrence identity, intake lifecycle ID, content/observation generation, readiness basis, review state, suggestions, and eventual action outcome. Repeated events for the same current occurrence update one open intake item. A replacement at the same name is not the old intake item merely because its path matches. A genuinely returned item may start a new lifecycle only after its prior departure has been established.

### 4.6 Revision and freshness model

Do not use one counter indiscriminately for every kind of change. Track:

| Field | Purpose |
|---|---|
| `source_epoch` | Changes when source identity changes or continuity cannot be trusted |
| `observation_revision` | Changes when relevant observed identity/metadata/listing evidence changes |
| `location_revision` | Changes on a verified rename, move, or ancestor-context change |
| `intent_revision` | Changes with owner labels, protection, inventory mode, or workflow decisions |
| `dirty_version` | Monotonic invalidation counter for a scope, including hints not yet reconciled |
| `descriptor_digest` | Digest of canonical semantic evidence used for classification |
| `last_observed_at`, `last_reconciled_at` | Observation timing, not proof of subtree integrity |

Unchanged observations may refresh timestamps without incrementing semantic evidence revisions. Jobs carry only the applicable revision dependencies; a newly observed arrival elsewhere must not invalidate every unrelated job in the source. An epoch change invalidates all source-bound assumptions. Record descriptor dependencies, including relevant ancestor context and explicitly selected child evidence.

A freshness status describes a named scope, such as `immediate_listing`, `shallow_descriptor`, or `selected_hash_scope`. Keep completeness and freshness separate: a complete listing from last week can be stale, and a freshly taken bounded sample is still partial.

## 5. Adaptive discovery and inspection

### 5.1 Jobs and budgets

Use breadth-first discovery so top-level results appear early. Maintain a durable frontier of directories awaiting a decision. Do not call an unrestricted recursive walker before classifying the current directory.

Initial configurable budgets:

| Setting | Default |
|---|---:|
| Concurrent filesystem workers per source device | 1 |
| Entries examined for a shallow descriptor | 256 |
| Targeted marker lookups per shallow descriptor | 16 |
| Descendant depth during a shallow probe | 0 |
| File-content bytes read during shallow discovery | 0 |
| Initial discovery node budget | 50,000 |
| Automatic refinement depth from the source root | 6 |
| UI children page size | 100 |

Budgets pause or defer work; they must not silently drop entries. Each result records the budget, entries examined, traversal depth, and why observation stopped. A first-N directory listing is not a statistically representative sample and must not be described as one.

### 5.2 Inspection operations

**Shallow probe:** inspect the node, a bounded immediate listing, and optional known marker names. Produce a descriptor without creating descendant inventory records. Marker lookup failures distinguish absent, unreadable, and unavailable.

**Expand one level:** enumerate immediate entries into the active inventory using bounded batches. Create child directory candidates, then classify each before descending. Loose files receive basic occurrence metadata, not automatic hashes or content extraction. They may receive format tags such as installer or archive; they do not automatically inherit a personal-content or disposable verdict from their parent.

**Aggregate walk:** optionally traverse descendants and retain only summary statistics, bounded examples, and errors. No descendant node records. This may still involve substantial filesystem I/O; it must run as an explicit, cancellable job, never as a hidden prerequisite for displaying a directory size.

**Selective analysis:** hash or inspect files only within explicitly selected expanded scopes. Atomic nodes remain excluded until the user changes their inventory mode or requests a specific analysis.

Exact recursive size is unavailable until an appropriate traversal completes. Display `unknown`, a clearly identified observed lower bound, or a completed measurement. Do not extrapolate a trustworthy recursive size from directory metadata or a tiny sample.

### 5.3 Decision policy

```text
Load explicit protection, classification overrides, and directory role.
If role is inbox:
    keep the boundary expanded; reconcile/enroll immediate children;
    classify each arrival under the inbox policy; never collapse the inbox.
For other nodes, collect a bounded shallow descriptor.
Apply deterministic evidence rules.
If eligible and permitted, ask the configured classifier.
Combine observations and predictions with deterministic policy.
Persist the decision, evidence, and coverage.

If the node is a coherent semantic unit:
    keep it atomic; stop recursive cataloging.
Else if it is mixed or a download collection:
    enqueue one-level expansion within the current budget.
Else if evidence is insufficient and a richer probe is allowed:
    enqueue that probe, with a fixed maximum escalation count.
Else:
    retain an atomic review item; do not recurse indefinitely.
```

Classification precedence is: protection barriers, explicit owner override, applicable user rule, deterministic detector, model suggestion, unknown. Keep disagreements in the evidence record. Rule priorities and conflicts must be deterministic; conflicting equally ranked rules produce review rather than an arbitrary winner.

A heuristic match is a rule match, not a fabricated probability such as `0.9999`.

### 5.4 Filesystem behavior

Do not follow symlinks during discovery. Record them as links. Do not open FIFOs, sockets, devices, or other special files. Nested mounts are visible boundaries and are not crossed by default. Detect same-filesystem bind mounts separately where the platform supports it.

Reject overlapping configured roots in the first release. Independently mounted aliases of the same data should be flagged when detectable, not treated as independent backups.

Use bounded directory reads, not APIs that materialize an entire huge directory before returning. Cancellation must stop new work promptly; already blocked filesystem calls may take longer to return. Report an unresponsive source rather than promising that every system call can be interrupted.

### 5.5 Incremental indexing and reconciliation

#### 5.5.1 One change pipeline

All initial discovery, manual refreshes, scheduled refreshes, watcher hints, and application-owned moves feed the same durable reconciliation services. Do not build an independent inbox scanner with different identity, coverage, or mutation rules.

```text
filesystem notification / periodic deadline / manual request / action journal
    -> coalesce a durable dirty scope with reason and dirty_version
    -> observe the smallest useful scope
    -> compare observations and update inventory transactionally
    -> invalidate only affected derived evidence
    -> enqueue classification, aggregates, or selected hashing as needed
```

The baseline MUST work with notifications entirely disabled. Implement polling/reconciliation first. Add `fsnotify` as an optional low-latency optimization, not as the sole source of truth. The library documents non-recursive watches and notification limitations on network filesystems; it does not provide the required polling fallback itself. [I1]

Linux inotify can lose events on queue overflow, and an event's named path may no longer exist when processed. Recovery therefore re-observes the filesystem rather than treating the event stream as a replayable filesystem journal. [I2]

#### 5.5.2 Observation frontier and atomic directories

For expanded directories, periodically enumerate immediate children, compare their relevant metadata, and discover new direct entries. Visit each active expanded container on its own schedule; never infer unchanged descendants from the source root's timestamp.

For an atomic directory, perform at most the scheduled bounded shallow probe. Do not add recursive watches, descendant inventory rows, or an implicit full-tree walk. A watch on that directory may reveal immediate changes but cannot certify its hidden descendants. Show `boundary-only monitoring; nested changes may be unobserved` in the inspector.

An aggregate measurement of an atomic subtree becomes stale after a known relevant change or its configured validity period. Absence of a watcher event does not extend that validity. A scheduled shallow refresh cannot renew a recursive measurement. Deeper periodic verification is a separate, explicitly enabled inspection job; it retains aggregates/evidence, not hidden descendant inventory rows.

Changes inside an uninspected atomic subtree that do not affect its bounded evidence need not be detected by the baseline index. This is an intentional scope limitation, not a defect to solve by silently cataloging the subtree. Mutation validation has its own stronger requirements.

#### 5.5.3 Initial scheduling policy

These are configurable starting defaults, not detection or latency guarantees:

| Setting | Default |
|---|---:|
| Watcher backend | `auto`, with explicit polling fallback and visible degraded status |
| Watcher coalescing window / maximum scheduling delay | 500 ms / 2 seconds |
| Expanded-container reconciliation target interval | 15 minutes |
| Atomic shallow-refresh target interval | 24 hours |
| Inbox immediate-listing polling target interval | 5 seconds |
| Recursive aggregate stale-after interval | 24 hours |

Distribute periodic work over the interval instead of rescanning all sources simultaneously. Large listings use bounded batches and explicit progress. A target interval can be exceeded under load; display actual lag and missed deadlines. Back off unavailable sources without blocking other sources.

Watch only configured roots, active expanded directories, inbox boundaries, and selected atomic boundaries within a configurable watch budget. Register a watch before the associated reconciliation when possible, then reconcile any events observed during that pass. Reaching OS watch limits degrades to polling; it does not silently remove monitoring coverage.

Coalescing stores the latest dirty version and a set of reasons, not an unbounded row for every event. If an in-memory notification cannot be persisted, mark the affected source for reconciliation. After process restart, watcher recreation, overflow, or a coverage gap, reconcile the affected active frontier; do not assume events during downtime were retained.

#### 5.5.4 Safe enumeration and missing entries

Each listing run has a generation and completion marker. Upsert observed children in batches. Only consider previously present children missing after a successful complete enumeration of that parent, with the same source epoch and directory identity, and no known invalidation during the pass.

Before committing a missing result, recheck the candidate's absence through rooted access. A permission error, partial listing, failed batch, unavailable volume, or changed mount is not evidence of absence. If new invalidations arrive, keep/requeue the dirty scope rather than marking it clean.

A live directory listing is not a snapshot. Concurrent activity can still require another pass; the correctness contract is convergence after changes settle, not a point-in-time complete filesystem view. Mark missing occurrences as tombstoned history, not hard-deleted database records. A missing directory makes dependent descendants unavailable as current evidence without issuing fabricated independent deletion observations for each one.

Re-enumerate an interrupted directory on recovery; do not persist operating-system listing offsets as durable portable cursors. A continuously changing scope may remain dirty and must not starve other work.

#### 5.5.5 Incremental invalidation

| Observed change | Required consequences |
|---|---|
| New child in expanded container | Create/update its node; shallow-classify a directory before descending; create intake item if it is an inbox arrival |
| Changed file identity or content metadata | Invalidate its content association and dependent comparisons; schedule hashing only if that scope has opted in |
| Changed directory evidence | Refresh its bounded descriptor; rerun rules and, when inputs changed, the selected classifier |
| Rename or move | Refresh location/ancestor context; invalidate path-dependent classifications, routing, manifests, and plan preconditions |
| Removed occurrence | Tombstone only after verified absence; invalidate relationships and proposed keepers referring to it |
| Change to an atomic boundary or known descendant signal | Mark the boundary and dependent aggregates stale; do not expand it automatically |
| Manual category/protection change | Preserve the assertion; recompute policy and affected suggestions without calling a model unnecessarily |
| Task/model/prompt/privacy configuration change | Mark affected model evidence superseded; reclassify only the selected scope or when next due, not the whole disk automatically |

Increment dirty versions immediately on a relevant hint so an in-flight model or hash result cannot become current while reconciliation is pending. A worker applies results with a compare-and-swap against its source epoch, dirty version, relevant observation/context revisions, and owner intent. Stale results can remain in history but cannot change the effective category, create a current duplicate claim, or authorize a plan.

Propagate aggregate/relationship invalidation through recorded dependencies and ancestor dirty markers, not by synchronously rewriting every descendant for every event. UI queries must honor those markers while refresh jobs are pending.

#### 5.5.6 Identity, content reuse, and application-owned changes

A path is not an enduring occurrence identity. Recognize application-owned moves through the operation journal and preserve the node ID. External renames may preserve identity only with sufficiently unambiguous evidence, such as a correlated move event plus matching source/OS identity and confirmed old-path absence. Otherwise model disappearance and arrival separately, with an optional uncertain relationship. Never transfer owner assertions on the basis of a content hash alone. Explicit path-scoped protection remains effective at that path, including after replacement.

Device/inode reuse and hardlinks must not merge distinct paths into one occurrence. Keep content identity separate. For a confirmed unchanged occurrence, cached hashes may remain observational evidence, but metadata equality is not a new byte verification. Destructive duplicate claims still require the validation in Section 11.

Application actions update catalog identity and journal correlation, then dirty both source and destination parents. Process resulting watcher events idempotently; do not ignore all events in a timed suppression window, because an unrelated writer may also have changed the directory.

#### 5.5.7 Freshness in the UI

Show last observation, reconciliation lag, dirty status, scope completeness, watcher health, and pending refresh. Queries and dashboards distinguish current observations, stale evidence, and unavailable-source history. An offline disk remains browsable as historical inventory, never as verified current duplicate protection.

Explicit refresh options are `refresh boundary`, `reconcile expanded subtree`, and `verify selected contents`. The second traverses the active inventory frontier and respects atomic boundaries. The third is an explicitly costlier content operation; it must not be hidden behind a generic Refresh button.

## 6. Evidence descriptors and explainability

A `DirectoryDescriptor` MUST identify its schema version, node revision, observation time, coverage, source-relative context, observed entries, marker evidence, metadata summaries, errors, and truncation.

An example descriptor shape follows. These are illustrative observations, not evidence from a real disk:

```json
{
  "schema_version": 1,
  "node_id": "node-123",
  "node_revision": 4,
  "context": {
    "source_label": "old-disk",
    "ancestor_names": ["Backup2003", "Program Files"],
    "name": "GetRight"
  },
  "coverage": {
    "scope": "immediate_children",
    "entries_examined": 4,
    "listing_complete": false,
    "descendants_inspected": false,
    "content_bytes_read": 0
  },
  "observed_entries": [
    {"name": "GetRight.exe", "kind": "file"},
    {"name": "uninstall.exe", "kind": "file"},
    {"name": "GetRight.hlp", "kind": "file"},
    {"name": "plugins", "kind": "directory"}
  ],
  "observed_signals": ["executable_present", "uninstaller_name_present"],
  "recursive_bytes": null,
  "errors": []
}
```

Use separate evidence records for filesystem facts, rule matches, model predictions, and owner assertions. Each item of bounded evidence has a stable identifier within its descriptor, so a provider may refer to observed evidence without inventing a filesystem fact.

For loose inbox files, use a `FileDescriptor` containing observed filename, source-relative parent context, regular-file metadata, format hints, readiness basis, coverage, and errors. The first release does not require content extraction. A `.jpg` extension is a format hint, not proof that an image is personal; a `.zip` does not identify its contents.

Both descriptors fit a versioned `ItemDescriptor` union. Classification tasks are separate: `directory_category` uses Section 4.3, while `file_kind` starts with `image`, `video`, `audio`, `document`, `source_file`, `archive`, `installer`, `executable`, and `unknown`. Preserve uncertainty between filename-derived hints and verified format evidence. File kinds and possible-purpose traits assist routing; they do not automatically establish ownership or disposability.

Generate a canonical **semantic outbound representation** for classifier caching. Exclude node IDs, revisions, scan timestamps, request IDs, and other bookkeeping that cannot affect the answer; retain relevant context, evidence coverage, readiness limitations, and all material evidence. Sort maps and observed-entry samples deterministically without normalizing lossless identity paths. Keep observation provenance outside the cached semantic payload. The UI may explain a decision with templates such as:

> Suggested category: application installation. Observed an executable and an uninstaller-like name. Only four immediate entries were examined. Nested user data has not been ruled out.

Do not generate nonexistent facts such as "available to download again", "contains no personal files", or "no unique content" from a category alone. File timestamps are observed filesystem metadata, not necessarily original creation dates. Future timelines must preserve that distinction.

## 7. Provider-neutral classification

### 7.1 Architectural boundary

The application asks domain questions about an item, not vendor primitives. Jev, a structured-output generative model, a local classifier, and a test fake must fit the same boundary without modifying discovery, inbox workflows, effective classification storage, or the web UI.

```text
versioned item descriptor + task/taxonomy definition
    -> deterministic rules and owner policy
    -> provider routing, privacy transformation, cache, and budget checks
    -> selected adapter
    -> validated domain result + typed optional measurements + provenance
    -> deterministic application policy
    -> suggestion / non-destructive label / review
```

Keep transport, authentication, request encoding, provider prompts, response parsing, and provider-specific error mapping inside adapters. Do not make all providers imitate Jev's `Choice`, `Noul`, or `Score`, and do not force Jev through a chat-completion API.

Use ordinary Go dependency injection and a static adapter registry, not Go shared-object plugins or a general agent framework. Switching among shipped adapters, endpoints, and models is configuration-only, optionally requiring a server restart; it must not require recompilation or a schema migration. Adding a genuinely new wire protocol requires one adapter package, registration, and contract fixtures, not domain changes.

### 7.2 Go-facing contract

The following is an interface sketch. Supporting types are required domain types, not vendor SDK types:

```go
type Classifier interface {
    Capabilities() ClassifierCapabilities
    Classify(ctx context.Context, req ClassificationRequest) (ClassificationResult, error)
}

type ClassificationRequest struct {
    Task       ClassificationTask // ID, revision, labels, definitions, semantic questions
    Descriptor ItemDescriptor     // already scoped/redacted; no filesystem access
}

type ClassificationResult struct {
    Status       ResultStatus       // answered, abstained, or refused
    CategoryID   string             // from Task labels; empty only when not answered
    Assessments  []Assessment       // domain question IDs, not vendor primitive names
    Measures     []Measure          // optional; preserve the meaning of each number
    EvidenceRefs []string           // references to supplied observations only
    Provenance   ProviderProvenance // actual backend/model/adapter/task revisions
    Usage        Usage              // known dimensions plus explicit unknowns
}

type Measure struct {
    Target    string       // e.g. category:source_project or signal:preservation
    Kind      MeasureKind  // native_probability, derived_confidence,
                           // self_reported_score, or uncalibrated_score
    Value     float64
    ScaleID   string       // named, versioned semantics and range
}
```

`ClassificationTask` owns the taxonomy and semantic questions; adapters translate that task into their native request. Task definitions and provider-specific prompt templates have separate version IDs. `Assessment` carries a domain question ID and an optional categorical answer such as `supported`, `unsupported`, or `unknown`. A probability-only answer is legal without a categorical verdict; policy may interpret it, but the adapter must not silently threshold it into a fact.

`ClassifierCapabilities` describes the configured adapter/model profile: supported task IDs, input types, structured-output mode, whether native distributions are available, request limits, and usage dimensions. Treat declared capabilities as claims to test, not proof of classification quality. A missing capability must cause explicit review/routing or a configuration error, not fabricated output.

`ProviderProvenance` records profile ID and revision, adapter version, requested and returned model IDs, endpoint identity without credentials, task revision, prompt/template revision, descriptor schema/digest, privacy-transform revision, timing, and optional provider request ID. Some backends do not return an immutable model revision; record that uncertainty and scope caches/evaluations to an operator-controlled deployment revision.

The orchestration layer, not the adapter, associates a result with node revisions, saves history, applies policy, and creates jobs. Adapters cannot read arbitrary files, update inventory records, or create actions. Use bounded `net/http` clients rather than shelling out to provider CLIs.

### 7.3 Required adapters and capability differences

Ship these implementations by M3:

| Adapter | Required behavior |
|---|---|
| `disabled` | Skip model work explicitly; deterministic rules and manual review still work |
| `fake` | Deterministic fixtures including abstention, missing measurements, errors, and delayed/stale results |
| `typesafe` | Native Jev HTTP protocol; map typed decisions into domain labels and measures |
| `structured_chat` | Tested subset of an OpenAI-compatible chat-completion protocol; schema-constrained domain labels/assessments where supported |

The `structured_chat` adapter accepts configured endpoint, model, secret reference, prompt revision, output limit, and an explicit capability profile. Implement schema-constrained and JSON-only modes with strict local validation; plain free-form prose parsing is not required. Refusal, truncation, invalid labels, malformed JSON, absent required answers, unsupported schema features, and transport errors are explicit outcomes. Any bounded retry/repair consumes time and cost budget; never loop until valid output appears.

At least one non-Jev endpoint must pass the adapter contract tests. A local Ollama deployment is a suitable reference integration: its documentation describes schema-constrained output and a subset of the compatible chat API. Compatibility is endpoint/model-specific; it is not a promise that every provider with a similarly named API supports the same fields. Ollama currently distinguishes local structured-output support from its cloud service. [P1, P2]

A label-only backend is fully usable. Do not fabricate a probability of 1 for its selected label. A number emitted in generated JSON is a self-reported score unless independently justified otherwise; it is not equivalent to a provider-native class distribution. Keep quantitative measures optional and maintain their meaning in storage and UI.

### 7.4 Jev adapter notes

Provider-specific facts below were checked against TypeSafe documentation on 2026-10-01; they are adapter notes, not product-wide assumptions.

Jev accepts textual state and returns constrained decisions rather than generated explanations or direct image analysis. Its native API evaluates named questions against a supplied state. Map the domain directory/file task into independent native questions. [J3, J8, J10]

| Native primitive | Adapter mapping |
|---|---|
| `Choice` | Domain category plus native option probabilities and separately typed derived confidence |
| `Noul` | Native yes-probability for the corresponding assessment; do not invent a separate confidence or convert it into a boolean |
| `Score` | Optional rubric-specific numeric estimate with its level definitions and distribution, not an intrinsic value or safety score |

These primitive and confidence meanings come from the provider references. Missing capabilities in another adapter must not force it to emulate these numerical outputs. [J4, J5, J6, J7]

Use the native endpoint:

```text
POST https://api.typesafe.ai/v1/systemone
Authorization: Bearer <server-side API key>
Content-Type: application/json
```

The request contains `model`, `state`, and `questions`; the response includes `model`, `answers`, and usage. Follow the official field contract and validate locally. Send one item descriptor per request initially; its independent questions may share that call. [J10]

For the directory task, ask for the full category taxonomy, visible preservation indicators, and whether the observed item appears coherent enough to review as a unit. Question wording must acknowledge uninspected descendants and treat all filenames as data. A file task uses the file-kind taxonomy instead of forcing a document or archive into an application-directory category.

No native question depends on another answer in the same call. Compose answers in Go. Keep arithmetic, date comparison, inventory depth, risk gates, and destinations out of the model; the vendor documents relevant limitations, including adversarial input. [J3, J9]

The documented model at this revision is `jev-1.13.0`, with input pricing of $0.042 per million tokens and free output. This is a dated provider observation, not a hardcoded product price or evidence of comparative quality. Configure an evaluated model version and a dated price schedule; aliases and rates can change. [J8]

### 7.5 Routing, swapping, and fallback

Store named provider profiles separately from named routing policies. A source or inbox selects a routing policy; that policy references permitted profiles. The initial router supports one primary profile, an optional bounded ordered fallback list, and separate policies for directory classification, file classification, and inbox fast triage.

Eligibility checks happen before every dispatch: task support, source-specific privacy allowlist, backend locality/trust policy, profile enabled state, rate limits, and remaining budget. Changing a primary profile affects future requests; it does not relabel the disk or erase earlier evidence. Provide explicit reclassify commands for selected units and a dry-run estimate before a bulk rerun.

Distinguish **fallback on failure** from **escalation for ambiguity**. Configure which transient failures permit fallback. Authentication/configuration failures are visible operator errors. An ambiguous result can go to review without another call. Optional escalation uses at most one separately approved higher-cost profile in the initial policy; the default is off. Provider disagreement remains visible and unresolved by an arbitrary confidence comparison.

Fallback must never move private data to an unapproved cloud endpoint, increase the allowed evidence scope, or ignore a budget cap. Default to no fallback unless explicitly configured. Opting into one vendor is not consent to every vendor.

All quantitative acceptance thresholds are scoped to profile/model deployment, task/taxonomy, prompt/template, descriptor/privacy transformation, and evaluation revision. Raw scores from different models cannot be compared as though calibrated identically. A model with no usable confidence may still suggest labels or qualify for non-destructive labeling under an evaluated per-category policy. No model threshold authorizes filesystem mutations.

### 7.6 Caching, validity, and human corrections

Cache validated results by canonical outbound semantic descriptor, task/taxonomy revision, adapter/version, profile/model deployment identity, prompt/template revision, generation settings, privacy transform, and owner/source privacy isolation scope. Do not include scan timestamps or node revisions that would invalidate an otherwise identical semantic request on every poll.

Store node-specific applicability outside the cache. Applying a cache hit still checks the current source epoch, dirty version, location/context dependencies, evidence revision, and owner intent. A cache entry is not permission to ignore changed coverage or new preservation evidence. Updating only a price schedule does not invalidate semantic classifications.

Changing a model alias without a verifiable deployment identity requires invalidating the affected cache or using a bounded alias-cache lifetime and explicit uncertain provenance. Do not silently carry evaluated confidence thresholds across an untracked model change.

Persist append-only suggestions from all providers, their measures and provenance, and the separate effective owner/policy decision. Validate finite numbers, declared ranges, label membership, evidence references, and complete native distributions when supplied. Missing measurements remain missing, not zero. Explanations come from recorded evidence and templates; optional model prose is not filesystem evidence and is not required.

Manual corrections remain authoritative across rescans, model changes, and delayed responses. Corrections can create explicit scoped rules or evaluation examples, but are not automatically sent for vendor training or fine-tuning. A change to underlying evidence can flag the owner assertion for review without silently replacing it.

### 7.7 Reliability, costs, and privacy

Enforce request deadlines, response-size limits, bounded concurrency, cancellation, retry budgets, jittered backoff, and adapter-specific status mapping. Respect `Retry-After` when supported. For example, the Jev adapter maps documented 429/529 responses as transient; it must not export those codes as universal provider semantics. [J10]

Pricing belongs to versioned configuration, not constants inside a classifier. Support input/output/cached-token rates and request charges; retain other usage dimensions as extensible data. Store known usage, estimates, actual provider-reported charges when available, currency, price revision, and unknown cost distinctly. Use fixed-point monetary storage. A local model has no assumed provider token bill, but latency/compute usage is not the same as zero operating cost.

Reserve a conservative job/global budget before each outbound attempt and reconcile after the response. Reservations include allowed fallback, output limits, retries, and optional escalation. If reliable spend bounds cannot be estimated, require an explicit per-call reserve plus a request-count cap or refuse metered automation. Unknown usage/charges remain reserved or conservatively accounted for; they do not become zero. Provider billing may differ from estimates, so do not promise an exact hard bill ceiling without provider-side limits.

Require per-job and daily caps before metered remote calls. Report calls, error/abstention rates, latency, input/output usage, estimates, provider charges where available, and cache savings by profile/task. Do not automatically choose the cheapest model without an owner-defined quality/privacy policy.

Remote classification is disabled by default. Per-source permission names permitted profiles and evidence scopes and shows the exact outbound payload preview. Local network endpoints also require explicit trust configuration; a localhost proxy is not proof that processing stays local. No silent local-to-cloud fallback.

The default evidence scope is metadata only, including potentially private names. Redact absolute roots and honor source/inbox exclusions. File content, images, document excerpts, and secrets require separate explicit consent and are outside initial model input. Apply privacy policy both when enqueueing and immediately before dispatch. Revocation cancels pending requests and prevents further sends, but cannot recall data already transmitted.

Secrets remain server-side. Restrict endpoints to operator-configured URLs, allowlist redirects or disable them, and never forward credentials to a different origin. New endpoints cannot be supplied by a filename, browser classification request, or model result. Store raw request/response bodies only under an explicitly enabled, access-controlled diagnostic retention policy; normalized evidence is the default.

### 7.8 Evaluation and operational model comparison

Provide a local evaluation command using frozen descriptor fixtures and owner-reviewed labels. Run the same semantic tasks through selected profiles and report per-category precision/recall, preservation-risk false negatives, review/abstention rate, response validity, latency distribution, and estimated/observed cost per 1,000 completed decisions. Include retries, escalation, and provider failures rather than measuring successful calls only.

Record corpus, prompt, task, descriptor, privacy, model/deployment, pricing, and adapter revisions. Split source families between tuning and evaluation. Report calibration only for supported measurement types and adequate labeled samples. A label-only provider must participate without invented confidence values. Ordinary CI uses saved/fake responses, never paid calls.

Optionally shadow a capped, explicitly consented sample through a second provider without changing live labels. Shadowing is off by default, charged to budget, and subject to the same privacy restrictions. Switching providers is an owner decision backed by these reports, not a hidden runtime marketplace.

## 8. Go architecture and persistence

### 8.1 Technology choices

Build a single Go server with embedded assets. Use Go 1.27 and pin a current patch release in the build configuration; the release page listed Go 1.27.1 when researched. [G1]

Use `net/http`, `html/template`, `embed`, `database/sql`, `log/slog`, and `crypto/sha256`. Use SQLite through the CGO-free `modernc.org/sqlite` driver; pin and test the dependency graph. [G2]

Use server-rendered HTML plus a small self-hosted JavaScript layer for selection, pagination, asynchronous commands, and job events. No Node.js runtime, CDN, external frontend service, or network dependency is required to use the deployed UI. A complex SPA is not required for the initial workflow.

Keep the database on local server storage outside source roots. Use WAL, foreign keys, migrations, bounded write transactions, a serialized writer, and a small read pool. Configure busy timeouts and checkpointing. SQLite documents the same-host constraint for normal WAL shared-memory operation; do not place this database on an NFS/SMB share. [G3]

Use durable SQLite jobs rather than Redis, a separate queue service, or an in-memory-only work list.

### 8.2 Package layout

```text
cmd/curator/                 CLI bootstrap and server entry point
internal/domain/            Domain types and invariants
internal/store/             SQLite queries, migrations, transactions
internal/fsaccess/          Rooted filesystem access and capabilities
internal/discovery/         Shallow probes and one-level expansion
internal/inspection/        Aggregate walks and optional metadata extractors
internal/classify/          Domain tasks, rules, descriptors, policy, routing
internal/classify/providers/registry/    Statically registered adapter factories
internal/classify/providers/typesafe/    Native Jev adapter
internal/classify/providers/structuredchat/ Compatible structured-output HTTP adapter
internal/classify/providers/fake/        Deterministic contract fixtures
internal/reconcile/         Dirty scopes, generations, dependency invalidation
internal/watch/             Optional notifications and polling coordination
internal/inbox/             Intake lifecycle, readiness, routing suggestions
internal/jobs/              Durable scheduling, leases, progress, recovery
internal/compare/           Selective hashes and relationship analysis
internal/actions/           Plans, journal, quarantine, restore, move, purge
internal/web/               Authentication, handlers, templates, events
web/static/                 Embedded CSS and JavaScript
migrations/                 Versioned SQL migrations
policies/                   Versioned taxonomy, rules, and question sets
```

Keep mutation logic behind `internal/actions`; HTTP handlers, classifiers, and discovery workers cannot call deletion or rename operations directly. Use explicit dependency injection and ordinary Go types, not a plugin framework or generalized workflow language.

### 8.3 Required persistence entities

| Entity | Key responsibilities |
|---|---|
| `sources` | Root configuration, availability, identity/epoch, permissions, refresh schedule |
| `inboxes` | Directory role, configuration revision, readiness/routing policy, allowed destinations |
| `intake_items` | Node reference, arrival lifecycle, generation, readiness, review/action outcome |
| `routing_destinations`, `routing_rules` | Owner-approved destination node IDs, labels, matching policy |
| `dirty_scopes` | Coalesced scope, reason set, dirty version, first/last hint, retry deadline |
| `reconciliation_runs` | Source epoch, listing generation, coverage, completion and errors |
| `evidence_dependencies` | Revisions/scopes on which derived evidence relies |
| `provider_profiles`, `classifier_policies` | Versioned adapter/model settings, eligibility and routing |
| `classification_cache` | Semantic request digest, profile/deployment identity, result, validity |
| `inference_attempts`, `usage_ledger` | Request lifecycle, reservations, retries, usage and estimated charges |
| `nodes` | Parent, lossless name, kind, role, revisions, active/tombstone state, effective classification |
| `observations` | Scan generation, metadata, coverage, errors, staleness |
| `descriptors` | Versioned bounded evidence snapshots and digests |
| `classifications` | Rule/model suggestions and provenance; append-only history |
| `overrides` | Owner categories, protection pins, scoped policies |
| `aggregates` | Counts/bytes with coverage, scope, and measurement time |
| `contents` | Algorithm, full digest, logical size |
| `occurrence_content` | Observed occurrence-to-content association and validation state |
| `relationships` | Typed comparison evidence, scope, and algorithm version |
| `jobs` | Kind, payload version, status, lease, attempts, progress, cancellation |
| `plans`, `plan_items` | Immutable approved actions and explicit preconditions |
| `operation_journal` | Durable intent, filesystem outcome, reconciliation state |
| `quarantine_items` | Original identity/path, stored path, retention, restoration status |
| `users`, `sessions` | Single-owner authentication and session lifecycle |
| `audit_events` | Security-sensitive actions and owner decisions |

Use database constraints for uniqueness and referential integrity. Enforce at most one current occurrence per active parent/name, one open intake lifecycle per inbox/occurrence, one coalesced dirty scope per scope key, and unique operation idempotency keys. Do not treat hardlinked occurrences as duplicate primary keys. Keep common filters in typed columns; bounded provider/evidence payloads may use JSON. Index parent/name, source/activity, category/disposition, job status, and content digest. Never allocate one record per uninspected descendant of an atomic unit.

### 8.4 Job semantics

Job states are `queued`, `running`, `paused`, `succeeded`, `failed`, and `cancelled`, with durable leases for crash recovery. Use idempotent job keys and bounded retries. Persist progress in batches, not per-byte events.

Separate metadata, aggregate, hashing, and AI work budgets. Scanning and UI reads must remain usable while a provider is slow. Each source supports pause/resume and coordinated mutation reservations over the affected nodes, subtrees, and parents. Overlapping application plans cannot execute concurrently. An application lock does not lock out unrelated programs accessing the filesystem.

Use bounded priority classes: interactive/inbox metadata, ordinary reconciliation, and bulk/deep analysis. Schedule weighted fairly and age delayed work. Inbox traffic must not starve normal reconciliation, and a bulk hash job must yield between bounded work units. Keep the conservative per-device I/O budget; priority does not imply launching many concurrent random reads on a spinning disk. Providers have separate queues and rate limits.

Persist deadlines and readiness state so inbox items and polling work resume after restart without an open browser. If an item changes while a classification is running, invalidate that generation and enqueue at most one successor after settling. Reuse unchanged semantic cache entries; an event storm must not produce one billed request per event.

Jobs publish monotonically numbered events. The UI reconnects using its last event ID or reloads a fresh snapshot when history has expired. No job depends on an open browser tab.

## 9. Web interface and command surface

### 9.1 Required screens

**Overview:** source availability, review queues, candidate units, preserved units, mixed/unknown containers, inspection coverage, running jobs, and API spend. Unknown sizes remain visible; do not present a sum of known sizes as the disk's complete inventory.

**Semantic explorer:** paginated tree of inventory objects with category, atomic/expanded state, disposition, protection, evidence coverage, and known size. The real source path remains available even when the display groups semantic units.

**Node inspector:** observed evidence, model/rule provenance, alternate predictions, manual corrections, protection control, inspection history, and bounded internal peek. Peeking inside an atomic object MUST NOT silently create descendant inventory records or change its inventory mode.

**Review queue:** bulk review of likely installations/caches, protected user-data units, and ambiguous containers. Bulk selection shows the exact objects, selection scope, and excluded protected items. "Select all" must not silently mean every item matching a changing live query.

**Plan and quarantine:** immutable planned changes, preflight failures, execution journal, restore controls, and separately gated purge.

**Inbox:** arrivals grouped into copying/settling, queued/classifying, needs-review, proposed destinations, and completed intake. Show readiness basis, last change, generation, evidence limits, classifier profile, suggested destination, blockers, and conflicts. Provide pause/resume intake, mark-ready, retry classification, split/refine a directory, ignore-for-now, and create-organization-plan commands. The item inspector is shared with the ordinary inventory.

**Index health and classifier settings:** source lag and watcher/polling coverage, dirty scopes, latest errors, active provider/routing profiles, privacy permissions, spend, and evaluation summaries. Model comparisons never imply that numerical confidence scales are interchangeable.

### 9.2 Aggregate accounting

Use a non-overlapping inventory frontier for totals: an atomic unit contributes at its boundary; an expanded node contributes direct loose files and descendant frontier units. Never add a parent's inclusive size to its children's sizes.

Distinguish logical bytes, measured allocated bytes when available, and estimated reclaimable space. Hardlinks, compression, sparse files, and snapshots must not produce a fabricated guaranteed saving. Same-filesystem quarantine reports data relocated, not disk space reclaimed.

### 9.3 HTTP surface

Use read endpoints for queries and explicit command endpoints for state changes. These names define the initial contract; handlers share domain services rather than embedding business logic.

```text
GET  /api/sources
GET  /api/nodes/{id}
GET  /api/nodes/{id}/children?cursor=...
GET  /api/nodes/{id}/evidence
GET  /api/review?category=...&cursor=...
GET  /api/inboxes
GET  /api/inboxes/{id}/items?state=...&cursor=...
GET  /api/sources/{id}/index-health
GET  /api/classifier-profiles
GET  /api/classifier-usage
GET  /api/jobs/{id}
GET  /api/events

POST /api/commands/start-scan
POST /api/commands/refresh-scope
POST /api/commands/set-inbox-role
POST /api/commands/pause-inbox
POST /api/commands/mark-intake-ready
POST /api/commands/retry-intake
POST /api/commands/ignore-intake
POST /api/commands/set-routing-rule
POST /api/commands/set-classifier-policy
POST /api/commands/reclassify-scope
POST /api/commands/refine-node
POST /api/commands/inspect-node
POST /api/commands/set-classification
POST /api/commands/set-protection
POST /api/commands/create-plan
POST /api/commands/validate-plan
POST /api/commands/approve-plan
POST /api/commands/execute-plan
POST /api/commands/restore-item
POST /api/commands/purge-item
POST /api/commands/cancel-job
```

Commands include an idempotency key and expected revision where relevant. Long-running commands return a durable job ID and HTTP 202. Revision conflicts return HTTP 409 with a machine-readable code. Errors distinguish unavailable sources, permissions, stale evidence, protected nodes, unsupported filesystem capabilities, and exhausted budgets.

Never accept an arbitrary shell command, unrestricted filesystem path, or provider-supplied action through this API.

### 9.4 Managed inbox workflow

#### 9.4.1 Intake contract

An inbox is a normal filesystem directory exposed by the operator through existing mechanisms such as a local file manager or a separately managed file transfer service. The application observes it; implementing file transfer protocols or browser uploads is not required. It MUST discover arrivals by polling even if notifications never arrive.

On inbox activation, enumerate current immediate children and let the owner choose whether to enroll them as pending intake or leave them as already cataloged items. Future new occurrences enroll automatically. Keep enrollment independent of whether the current category is known.

```text
observed -> settling -> queued -> classifying -> needs_review
                                               -> proposed -> planned -> organizing -> organized
```

`needs_review` may later become `proposed`. Items may also become `ignored`, `missing`, `failed`, or `superseded`. Readiness is a separate recorded basis, not a claim implied by the workflow state. Reclassification and replanning operate on new item generations when relevant evidence changes. A failure does not delete or move the arrival.

#### 9.4.2 Copy readiness

Do not assume that a create event, a close-write event, a final-looking filename, or an unchanged directory timestamp proves a transfer is complete. The application cannot prove that an arbitrary external process will not resume writing.

Provide two readiness levels:

| Readiness basis | Permitted behavior |
|---|---|
| `heuristic_stable` | Bounded read-only metadata classification and destination suggestions, visibly provisional |
| `handoff_confirmed` | Eligible for owner-approved organization, still subject to current action validation and external-writer coordination |

For ordinary regular files, the initial heuristic requires two unchanged identity/size/mtime observations at least 5 seconds apart and no detected change for 10 seconds. Exclude configurable temporary-name patterns such as `.part`, `.partial`, `.tmp`, `.crdownload`, and an operator-configured staging area from automatic readiness. These are hints, not a universal format guarantee; the owner can explicitly inspect an item with such a name.

For directories, the same quiet interval over a **bounded shallow descriptor** allows only provisional classification; it does not prove that nested copying stopped. Do not enumerate an entire incoming installation just to add it to the intake queue. A whole-tree stability pass can be requested separately and remains evidence of observed quiet, not a lock against future writes.

`handoff_confirmed` comes from an explicit owner Mark ready command, or from a configured cooperative producer protocol: copy into an excluded same-filesystem staging directory, stop all writes, and atomically rename the completed item into the inbox under its final name. Recognize protocol membership through operator configuration, not arbitrary move events. A producer that keeps open writable handles violates that protocol; the application cannot make it safe by assertion alone. An owner may combine Mark ready with plan approval after the UI clearly states the handoff requirement.

The completion signal applies to the current occurrence and observed generation. New evidence of a change clears it, invalidates pending classification/organization suggestions as appropriate, and returns the item to settling. A source disconnect or permission error blocks new readiness; a partial listing never establishes transfer completion. Bounded provisional classification remains distinct from confirmed handoff. Surface long-stalled arrivals rather than repeatedly paying for speculative classification.

#### 9.4.3 Express classification

Enroll immediate arrivals promptly, then run bounded metadata probes and deterministic rules using the inbox's priority queue and configured classifier policy. Do not wait for a whole-source scan, full hash, recursive aggregate, or archive extraction. An installation directory with 100,000 descendants must still be one fast-triage item.

A completed first-pass result contains category/file kind, evidence coverage, preservation indicators, optional typed measures, and possible destinations. It can be useful even when the result is `unknown`, sizes are incomplete, a model is offline, or no destination is known. Expensive inspection and selected duplicate comparisons are separate optional jobs and must not block initial review.

Changing an intake item while a provider request is in flight makes the result historical-only. Classify the latest settled generation once, subject to budgets. No cloud call is justified solely because a polling timestamp changed.

#### 9.4.4 Organization suggestions

Maintain owner-configured destination collections, each identified by a source/node ID and label, for example `Personal photos`, `Source projects`, `Documents`, or `Needs manual sorting`. Destination parents must be active expanded directories within the same configured source/filesystem in the first release; an atomic destination must be explicitly refined before it accepts cataloged children. Reject destinations inside the originating inbox, inside any other inbox, quarantine/state, or the moving subtree.

Initially use deterministic routing rules over effective categories, file kinds, owner tags, and inbox identity. A rule can map `source_project -> Source projects` or `archive -> Needs manual sorting`. The classifier suggests semantic labels, not arbitrary paths. An owner correction may offer a preview of a reusable routing rule; it must not silently alter future routing.

One category can have several valid destinations. No match, conflicting rules, stale destination identity, or insufficient evidence leaves the item in review. Do not invent a directory taxonomy or a guessed date-based destination from unreliable timestamps.

An organization proposal references the current intake generation, explicit destination parent ID, validated basename, reason, collision policy, and relevant revisions. A changed or unavailable destination invalidates the proposal. Existing same-name content is a conflict, not permission to overwrite or merge; the owner may choose another name or destination.

Approving a proposal creates an ordinary immutable move plan in `internal/actions`. Inbox code cannot rename files directly. Keep incoming directories and archives intact unless the owner explicitly refines/splits the intake package. Mark the item `organized` only after the journal confirms the move and catalog reconciliation establishes the destination. Preserve arrival provenance independently of the new path and maintain an audit trail.

A manually moved or externally removed arrival becomes missing or externally relocated only when verified; do not claim that the application's proposed organization succeeded. An application-owned move must not reenroll its own event echoes as new arrivals. Repeated commands and restarts must not move one item twice.

#### 9.4.5 Scope and safety defaults

First-release automation covers observation, classification, and proposing destinations, not unattended filesystem moves. Bulk approval is allowed for a concrete reviewed list. Later rule-authorized auto-filing requires a separately specified authorization policy; it is not implied by a high model confidence or a handoff marker.

Inbox mode works with source mounts read-only: catalog and propose, but show organization as disabled. Network mounts remain read-only under the initial filesystem capability policy. Recommend placing inbox and destination collections under the same configured source to use initial same-filesystem organization; cross-source copying is still deferred.

The inbox itself is protected infrastructure and cannot become a cleanup candidate. Enforce this as a role lock on the boundary and its ancestors, not as a recursively inherited preservation pin that would prevent approved moves of its children. Explicit owner protection pins still apply independently. Category-based cleanup suggestions for new arrivals default to review; exact-duplicate reports do not silently delete incoming files. Dropping an item into the inbox expresses a desire to catalog it, not consent to discard it.

Examples that MUST work:

```text
/data/                          configured source
    Incoming/                   role=inbox, expanded
        Vacation 2026/          one provisional personal-media package
        OldProject/             one source-project package
        manual.pdf              one file-kind=document item
        download.zip.part       waiting for completion
    Photos/                     expanded destination collection
    Projects/                   expanded destination collection
    Documents/                  expanded destination collection
```

After explicit handoff and approval, move `OldProject/` as one unit to `Projects/OldProject/`; retain its atomic state and do not catalog its internal files as a side effect of the move.

## 10. Cleanup and reorganization

### 10.1 Plans are explicit and immutable after approval

A plan contains concrete node IDs, observed identities, evidence revisions, requested actions, reasons, protection checks, destination policies, coverage warnings, and explicit keeper references for duplicate removal. It does not contain an unevaluated query such as "delete everything currently labeled cache".

Plan lifecycle:

```text
draft -> validated -> approved -> executing -> completed
                                  |          -> partially_completed
                                  |          -> failed
                                  -> invalidated / cancelled
```

Editing an approved plan creates a new revision requiring approval. Collapse overlapping ancestor/descendant selections into a non-overlapping set; never act on both independently. Check all protection pins, including configured protected paths inside uncataloged subtrees. Selecting an ancestor does not bypass a descendant's protection.

Distinguish two bases for cleanup: **owner-declared unwanted unit** and **verified redundant content**. The first is an explicit human choice; it is not a claim that all bytes exist elsewhere. The second requires the stronger comparison preconditions in Section 11.

### 10.2 Write-mode preconditions

Writes are disabled by default and enabled per source by the operator. Continuous read-only indexing is allowed on dynamic storage. Mutation support remains limited to quiescent archive units and explicitly handed-off inbox arrivals, not actively used application profiles.

The owner or cooperative producer must stop writing the selected item/subtree and coordinate operations on the affected parents and destinations. Unrelated activity elsewhere in the same source does not by itself prohibit organizing a completed inbox item. Refuse mutation when the affected scope is still changing, readiness is only heuristic, the source is unavailable, the mount lacks required capabilities, or identity is ambiguous. Do not demand that a whole multi-terabyte source stop receiving unrelated files just to move one completed arrival. Unrelated sibling arrivals, including in the same inbox, must not invalidate a handed-off item solely because a parent timestamp changed; validate parent identity/access and the exact source/destination entries instead of requiring an unchanged entire child listing.

Before execution, refresh observed identity, the relevant evidence, permissions, protection, destination availability, source epoch, and any intake handoff/generation precondition. Relevant changes invalidate approval; unrelated source activity does not. A directory's own timestamp is not a subtree snapshot; bounded probes cannot prove that all descendants remained unchanged. Explain that limit explicitly for opaque units, and offer deeper inspection instead of asserting perfect change detection.

For an opaque unit, confirmation MUST say that the operation includes its entire subtree, including unindexed descendants. A fresh safety probe may identify preservation risks, but it must never claim absence of risks from a partial inspection.

An application-level lock cannot eliminate races with external processes. Rooted path access and identity checks are still required, but do not market this as transactional snapshot semantics over an arbitrary live filesystem.

### 10.3 Quarantine

Move whole units to a service-controlled quarantine directory on the **same filesystem**. The quarantine directory is excluded from discovery and cannot be selected as a normal source. Refuse quarantining a source root, nested mount, protected ancestor, or the application's own state directory.

Use unique item IDs and a no-replace rename primitive. Do not emulate no-replace behavior with only "check destination, then rename". On supported Linux filesystems, `renameat2` with `RENAME_NOREPLACE` provides the needed destination behavior; refuse write mode if the required capability is unavailable. [G5]

Persist operation intent before the filesystem step, write a durable item sidecar, perform the rename, sync required directory metadata, and then commit the observed outcome. The database and filesystem do not share one transaction. On restart, reconcile intent against both paths and expected identity before retrying anything. Ambiguous states become `needs_manual_recovery`; never blindly execute the same move twice.

Quarantining an atomic unit does not catalog its descendants or hash every byte. It preserves the unit as a whole. Same-filesystem quarantine does not reclaim its storage.

### 10.4 Restore and reorganize

Restore to the original location only when it is free; otherwise request a new destination. Never overwrite or silently merge directories. Same-filesystem rename/move is supported through the same plan and journal machinery.

A proposed destination inside a source is specified as a destination parent node ID and a validated new basename. Cross-source/cross-filesystem moves and directory merges are deferred. Later cross-filesystem support must copy, verify content and required metadata, and only then quarantine the original; it cannot silently degrade to copy-and-delete.

### 10.5 Purge

Permanent removal is possible only from quarantine, never directly from a normal inventory node. Require a new confirmation, recent authentication, a fresh complete streaming preflight, and an enabled server-side purge capability. A complete traversal here is acceptable: it is an explicit destructive-stage cost, not part of initial inventory.

Preflight creates a versioned deletion manifest covering entries, identities, metadata, links, and any errors without adding descendant inventory nodes. Quarantine must be service-controlled and quiescent. Reject mount crossings, unexpected special files, unresolved errors, or changes since confirmation. Do not follow symlinks while deleting. For redundancy-based cleanup, revalidate that designated retained content is still available.

Default retention is 30 days. Expiration only makes an item eligible for manual purge; it does not trigger deletion. Retention is configurable before approval. Once purge begins it is not reversible; failures can leave a partly removed item and must be reported accurately. Maintain item-level completion and recovery records.

The application is not a backup system. Quarantine on the same disk does not protect against disk failure.

## 11. Selective deduplication and later analysis

### 11.1 First comparison capability

Hash only owner-selected expanded scopes. Group regular-file candidates by size, then compute full SHA-256 hashes with bounded workers. Optional sampled hashes may reject candidates, but never prove equality. Check file metadata and identity before and after reading; changing files receive an unstable result rather than a reusable digest.

Keep byte-identical content separate from equivalent metadata or interchangeable context. A duplicated file may still be required in several intact project or application trees. Default to reporting duplication, not removing arbitrary occurrences.

Before approving redundancy-based quarantine, choose explicit retained occurrences, verify they are accessible, compare full contents, and ensure the plan cannot remove every occurrence in a group. Revalidate keepers before purge. Hardlinked paths are not independent backup copies or automatically reclaimable duplicate bytes.

The UI says "one known occurrence within the selected, fully hashed scope", not "the only copy on this disk", while opaque or unscanned areas remain.

### 11.2 Directory relationships

Later, support `same_content_tree`, `same_content_multiset`, `contained_in`, and `similar_tree` as different relations. A path-aware content manifest maps lossless relative paths and entry kinds to content digests, preserving duplicate multiplicities and relevant empty directories. A path-independent multiset ignores placement but not repeated content counts.

Neither content relation automatically proves metadata equivalence. Store comparison algorithm, included/excluded scope, normalization policy, completeness, and generation. Partial manifests cannot prove full containment. Use indexed candidates rather than all-pairs comparison across the disk.

### 11.3 Archives

Initially treat ZIP, TAR, and other containers as opaque files. Later expose supported archive entries as virtual nodes only on request. Compare decompressed contents, not archive compression bytes, when establishing archive-to-directory relations.

Start with read-only ZIP inspection. Impose entry-count, uncompressed-byte, expansion-ratio, time, and nesting budgets. Reject traversal paths and normalization collisions; distinguish encrypted, corrupt, unsupported, and partial containers. Never execute members or extract untrusted archives into source roots. Never claim full equivalence from member names, sizes, or CRC values alone.

### 11.4 Divergent revisions and discoveries

Different content with related names is a candidate family, not a proven version history. Use token/text similarity and contextual evidence later, preserve alternatives, and do not infer authoritative ancestry from timestamps or filenames such as `final`.

Discovery views may rank old, apparently authored, sparsely duplicated material for review. Keep every ranking explainable and scope-aware. LLM-generated summaries, embeddings, image analysis, and office-document extraction are later optional capabilities, not dependencies for the core product.

## 12. Remote access, security, and deployment

### 12.1 Authentication and browser protection

Support one administrator initially, created or reset using a local CLI command with interactive password entry. No default password, public bootstrap endpoint, or unauthenticated inventory access. Use a maintained Argon2id implementation with a random salt and versioned work parameters; never use the file-deduplication hash for passwords. Follow the OWASP password-storage guidance. [S1]

Use opaque server-side sessions, rotate them on login, and expire/revoke them. Cookies use `HttpOnly`, `Secure` for remote HTTPS operation, and an appropriate `SameSite` setting. Add CSRF tokens and origin validation for mutations, login throttling, request-size limits, and recent-authentication checks for purge. Follow the referenced authentication, session, and CSRF guidance. [S2, S3, S4]

Escape filenames, extracted text, and all model-derived values. Do not render recovered HTML, SVG, or scripts in the application's authenticated origin. Rich previews are deferred; initial downloads, if implemented, use authenticated attachment responses. Use a restrictive Content Security Policy and no permissive cross-origin API policy.

### 12.2 Filesystem boundary

Open configured source roots through Go's rooted filesystem APIs. Plain path-prefix checks and `filepath.Join` are insufficient as the only boundary. `os.Root` addresses path escape but does not itself prohibit mount crossings or special files; implement those policies separately. It also permits some internal symlinks, so enforce the application's stricter no-follow policy explicitly. [G4]

Keep the process unprivileged, with access only to configured sources and state. Keep source mounts read-only until writes are intentionally enabled. Never invoke old binaries, installers, scripts, or shell-built file commands. External metadata extractors, if added, require isolation, timeouts, resource limits, and no network access.

### 12.3 Deployment contract

Default native bind address: `127.0.0.1:8080`. For remote use, deploy behind an HTTPS reverse proxy with an explicit external origin and trusted-proxy allowlist. Never trust forwarded identity or scheme headers from arbitrary clients. Direct HTTP on a local-only development mode is a distinct, explicitly enabled configuration.

Provide a multi-stage container image, Docker Compose example, and systemd unit. In containers, the application may bind to `0.0.0.0` inside the container only under explicit configuration; publish it only on an intended private/proxy network or host loopback. Do not expose the unaudited port broadly by default.

Persist database/state on local storage, mount sources independently, and keep secrets outside the image. Create private state/quarantine directories with owner-only permissions and restrict database, sidecar, and secret files to the service account. Document configured UID/GID ownership, read-only mode, write-mode changes, backups, upgrades, and restoration. Do not copy a live SQLite database file casually; implement a consistent backup command or documented stopped-service backup procedure.

Configuration must cover sources, privacy, bind/external origin, trusted proxies, worker budgets, named classifier profiles and routing policies, versioned prices, spending caps, reconciliation schedules, watcher budgets, inbox roles/readiness, destination allowlists, retention, and write/purge enablement. Reject invalid or unsafe combinations at startup.

Ship commented examples for: read-only/no-model operation; a local structured-output endpoint; explicit Jev opt-in; switching classifier profiles; polling-only network storage; and an inbox with same-source destination collections. Store secrets as references to environment variables or protected files, never in exported browser configuration. Classification and inbox policy changes have revisions and audited diffs. Configuration-only provider switching must preserve existing inventory and owner decisions.

## 13. Tests, observability, and acceptance criteria

### 13.1 Test strategy

Use synthetic filesystem fixtures, an instrumented filesystem abstraction, fake clocks/watchers, at least two distinct classifier contract fixtures, HTTP contract tests, and crash injection. Ordinary CI must neither use private files nor call a paid model. Run unit/integration tests and the Go race detector. Make a disposable-filesystem end-to-end suite available for actual rename, sync, permission, and recovery behavior.

Mandatory scenarios:

| ID | Scenario | Required result |
|---|---|---|
| A1 | Recognizable application unit containing 100,000 nested entries | One active unit record; no descendant inventory; shallow work stays within budget |
| A2 | Aggregate the same atomic unit | Summary may improve; active descendant count remains zero |
| A3 | Refine a mixed backup with applications, photos, and projects | One-level expansion; child units follow independent policies |
| A4 | Profile/database/save indicators inside an apparent installation | Preservation risk appears; no automatic low-risk recommendation |
| A5 | Partial listing, unreadable directory, or disappearing source | Incomplete/unavailable status; no empty-directory inference or false tombstones |
| A6 | Model unavailable, malformed response, 429, 529, or budget exhaustion | Bounded retries; rules/UI remain usable; no mutation |
| A7 | Owner override followed by a stale model response | Override remains authoritative |
| A8 | Peek inside an atomic object | No persistent expansion or global indexing |
| A9 | Tree totals after expansion/collapse and overlapping plan selections | No double counting or duplicate operations |
| A10 | Duplicate candidates with sampled-hash collision or changing contents | No equality claim until full verification; unstable files blocked |
| A11 | Source/path traversal, symlink escape, special file, or nested mount | No escape, execution, unintended read, or unsafe mutation |
| A12 | Crash before/after rename and before/after DB commit | Correct reconciliation; no repeated move or lost recovery location |
| A13 | Restore/move destination already exists | No overwrite; explicit conflict |
| A14 | Protected descendant, unavailable keeper, or changed source identity | Mutation refused and plan invalidated |
| A15 | Unauthenticated request, CSRF, malicious filename, forged proxy headers | Request rejected or value safely rendered |
| A16 | Duplicate IDs/idempotency keys and lost browser connection | At most one effective command; job survives client disconnect |
| A17 | Non-UTF-8 names and case-distinct files | Lossless identities and correct actions |
| A18 | Quarantine retention expires | No automatic purge |
| A19 | Purge error partway through | Honest partial state; no claim of rollback or successful full removal |
| A20 | Source mounted read-only with cloud disabled | Complete directory-first review workflow still works |
| A21 | Switch TypeSafe to structured-chat using configuration only | No schema/domain/UI change; old predictions retain provenance and owner labels remain |
| A22 | Non-Jev backend returns valid labels without probabilities | Classification works; no fabricated confidence; evaluated policy or review decides applicability |
| A23 | Different provider score semantics, bad labels, invalid distributions, refusal, or truncation | Preserve typed measures; reject invalid output; bounded retry/review, never a fabricated zero |
| A24 | Primary fails but only a disallowed/private or over-budget fallback exists | No data sent there; visible failure/review with rules still usable |
| A25 | Unchanged semantic evidence across repeated polls; then model/prompt/privacy change | Cache hit despite new scan timestamps; relevant version change prevents wrong cache reuse |
| A26 | New, changed, and deleted direct entries with watcher disabled | Scheduled reconciliation converges; unchanged items are not fully rehashed or repeatedly model-classified |
| A27 | Watch overflow, watch limit, process downtime, or notification coverage gap | Visible degradation; active-frontier reconciliation without unbounded descent into atomic nodes |
| A28 | Incomplete parent listing or offline disk during a missing-entry check | No false tombstones; historical inventory remains available |
| A29 | Deep internal edit in an atomic unit without boundary evidence | No promise of detection; recursive measurements expire/stale; no descendant indexing or hidden recursive watches |
| A30 | Item or ancestor context changes while a worker/model call is in flight | Old result cannot become effective; dirty scope survives and latest generation is reconsidered |
| A31 | External rename ambiguity, hardlinks, inode reuse, or replacement at the same name | No unproven identity merge or transfer of owner assertions; explicit path protection still applies |
| A32 | Slow file copy, repeated events, and temporary-name arrival | One intake lifecycle; no organization before handoff; metadata triage waits for configured quiet conditions |
| A33 | Incoming directory has quiet top level but nested writes continue | Classification is provisional and bounded; completion not inferred; no automatic move or descendant catalog |
| A34 | Cooperative staged handoff or owner Mark ready, followed by approval | One journaled same-filesystem move; readiness basis and intake provenance retained |
| A35 | Incoming item changes after handoff or after proposal approval | Handoff/proposal invalidated for the changed generation; no stale move |
| A36 | Inbox metadata work arrives during a bulk scan/hash job | Bounded priority/fairness works; no starvation, no need to finish a disk-wide job first |
| A37 | Destination conflicts, is moved/replaced, is atomic, is another inbox, or is on another source | Explicit blocker; no overwrite, merge, hidden expansion, or unapproved cross-source operation |
| A38 | Crash during inbox organization plus repeated notifications/commands | Journal reconciliation yields one outcome; no duplicate intake or double move |
| A39 | Inbox on read-only or polling-only storage, with models disabled | Arrivals cataloged and reviewable; suggestions allowed; unsupported mutation disabled |
| A40 | Inbox role set inside an atomic ancestor, then attempted collapse/deletion | Explicit refinement required; role boundary cannot be hidden or deleted accidentally |
| A41 | Rapid arrivals, readiness timers, and repeated dirty hints across restart | Durable/coalesced work; bounded reservations and billed calls; same open arrival is not duplicated |
| A42 | Evaluation of label-only and probabilistic providers on the same fixture corpus | Comparable quality/cost/latency report without pretending score scales are interchangeable |
| A43 | Classifier permission revoked after enqueue or fallback enabled for a new vendor | Dispatch rechecks policy; no unapproved send; already-sent data is not claimed recalled |
| A44 | Unrelated new file elsewhere while a handed-off inbox item is organized | Relevant item/parent checks still apply; no unnecessary whole-source write prohibition |
| A45 | Successful ordinary move into an expanded collection | Node identity, atomic boundary, history and totals preserved; both parent scopes reconciled |

The A1 fixture must make classification markers available to the bounded probe; recognition accuracy and bounded inspection are separate tests. Test that deeper unknown content is explicitly unknown, not implicitly verified safe.

### 13.2 Model evaluation

Implement the provider comparison harness in Section 7.8. Build a hand-reviewed descriptor corpus covering directory and loose-file tasks, Portuguese/English names, mixed directories, misleading application folders, unfinished inbox arrivals, and intentionally private metadata. Report per-category precision/recall, confusion matrix, review rate, and preservation-risk false negatives. Add probability calibration measurements with sample counts when the corpus supports them.

Split tuning and evaluation examples by source family so near-identical backups do not inflate results. Save profile, model/deployment, adapter, task/taxonomy, prompt/template, descriptor/privacy, and evaluation versions. Small-corpus scores are development signals, not proof of safe automatic deletion. All mutation authorization remains human-controlled regardless of classification performance.

### 13.3 Operational metrics

Expose authenticated job statistics: entries examined, nodes created, atomic boundaries, bytes read, observed errors, paused work, queue age, dirty-scope age, reconciliation lag, watcher drops/coverage, inbox arrival-to-first-suggestion latency, settling duration, cache hits, per-profile model calls/usage/cost, and action outcomes. Logs omit credentials and file content. Record private paths only where necessary for protected audit/recovery state.

Record performance benchmarks with storage type and corpus shape. The scalability invariant is that discovery of a recognized atomic unit does not perform work proportional to its hidden descendant count. Full aggregate walks and explicit comparisons are separately reported costs.

## 14. Implementation milestones

| Milestone | Deliverable | Exit condition |
|---|---|---|
| M1 - Read-only foundation | Single binary, authentication, SQLite migrations, configured roots, shallow discovery, semantic explorer | Useful inventory without AI; A1, A5, A11, A15, A17, A20 pass |
| M2 - Directory policy | Categories, atomic boundaries, one-level refinement, aggregates, overrides/protection, review queue | A2-A4, A7-A9 pass; no hidden full-tree catalog |
| M2a - Incremental indexing | Durable dirty scopes, source epochs, periodic active-frontier reconciliation, optional notifications, selective invalidation | A26-A31 pass; polling works alone; atomic coverage remains honest |
| M3 - Interchangeable classifiers | Domain contract, native Jev and structured-chat adapters, routing/privacy/budgets/cache, evaluation harness | A6, A21-A25, A42-A43 pass; provider swap needs configuration only |
| M3a - Managed inbox intake | Directory roles, durable arrivals/readiness, prioritized metadata classification, destination proposals | A32-A33, A36-A37, A39-A41 pass; no filesystem mutation required |
| M4 - Selective comparison | Explicit-scope file inventory/hash jobs, occurrence/content separation, duplicate report | A10 passes; incomplete coverage represented honestly |
| M5 - Controlled organization | Immutable plans, same-filesystem quarantine/restore/move, inbox organization, journal and recovery | A12-A14, A16, A34-A35, A38, A44-A45 pass; writes opt-in and item-scoped |
| M5b - Manual purge | Retention, complete preflight, recent authentication, partial-failure reporting | A18-A19 pass; no direct or automatic deletion |
| M6 - Rich relationships | Directory manifests, ZIP inspection, containment/overlap, candidate revision families | Completeness and archive safety tests added before enabling cleanup suggestions |

Every milestone must produce a runnable application and update operator documentation. Do not implement M6 infrastructure before the directory-first workflow is usable.

### Instructions to the coding agent

Implement M1 first, with tests that prove the atomic-boundary contract through instrumented filesystem calls. Add the remaining milestones in table order, including M2a and M3a. Prove polling-only reconciliation before adding notification optimizations, and prove both provider protocols through contract tests before treating the classifier boundary as interchangeable. Preserve the data/suggestion/policy/action boundaries even when mocking later capabilities.

Choose the simplest implementation satisfying this specification. Record material deviations in an architecture decision note. Do not replace directory-first discovery with an ordinary exhaustive file index, invent provider fields or probabilities, confuse notifications with complete change tracking, infer copy completion from quiet timestamps, let inbox code mutate files directly, assume confidence means safety, or build a distributed platform.

The defining product behavior is:

> Classify directories first. Treat coherent units atomically. Expand only when useful. Reconcile a changing archive at those boundaries. Make new arrivals easy to review. Keep classifiers replaceable and the owner in control of every filesystem change.

## 15. Primary references

Sources below were consulted on 2026-10-01. Provider facts and public APIs must be rechecked when implementation begins; the product requirements above are design decisions rather than claims made by these sources.

| ID | Source | Location |
|---|---|---|
| J1 | TypeSafe launch announcement, September 15, 2026 | `https://typesafe.ai/blog/introducing-system-one-models-and-jev` |
| J2 | TypeSafe homepage and access announcement | `https://typesafe.ai/` |
| J3 | TypeSafe introduction and parallel question behavior | `https://docs.typesafe.ai/introduction` |
| J4 | Choice primitive | `https://docs.typesafe.ai/primitives/choice` |
| J5 | Noul primitive | `https://docs.typesafe.ai/primitives/noul` |
| J6 | Score primitive | `https://docs.typesafe.ai/primitives/score` |
| J7 | Confidence semantics | `https://docs.typesafe.ai/confidence` |
| J8 | Models, pricing, limits, and aliases | `https://docs.typesafe.ai/models` |
| J9 | Jev 1.13 documented limitations | `https://docs.typesafe.ai/model-jaggedness/jev-1.13` |
| J10 | TypeSafe native HTTP API | `https://docs.typesafe.ai/api` |
| P1 | Ollama structured output modes and local/cloud distinction | `https://docs.ollama.com/capabilities/structured-outputs` |
| P2 | Ollama supported compatible chat API subset | `https://docs.ollama.com/api/openai-compatibility` |
| I1 | fsnotify API, non-recursive watches, limitations, and polling status | `https://pkg.go.dev/github.com/fsnotify/fsnotify` |
| I2 | Linux inotify event semantics and overflow limitations | `https://man7.org/linux/man-pages/man7/inotify.7.html` |
| G1 | Go releases | `https://go.dev/dl/` |
| G2 | modernc SQLite driver documentation | `https://pkg.go.dev/modernc.org/sqlite` |
| G3 | SQLite WAL documentation | `https://sqlite.org/wal.html` |
| G4 | Go rooted filesystem access and limitations | `https://go.dev/blog/osroot` and `https://go.dev/src/os/root.go` |
| G5 | Linux rename and no-replace semantics | `https://man7.org/linux/man-pages/man2/rename.2.html` |
| S1 | OWASP Password Storage Cheat Sheet | `https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html` |
| S2 | OWASP Authentication Cheat Sheet | `https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html` |
| S3 | OWASP Session Management Cheat Sheet | `https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html` |
| S4 | OWASP CSRF Prevention Cheat Sheet | `https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html` |
