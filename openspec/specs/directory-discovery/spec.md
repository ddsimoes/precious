# directory-discovery Specification

## Purpose

Discovers a source directory-first. It expands only what policy allows, collects bounded shallow evidence for each directory before any recursive decision, and keeps undecided directories atomic, so discovery work never grows with hidden descendant counts.

## Requirements

### Requirement: Source root is expanded one level
A scan SHALL treat the source root as an expanded directory. It SHALL inventory the root's immediate entries in bounded batches, creating nodes for directories, regular files, and symlinks. Each loose file SHALL get occurrence metadata only (size, modification time, mode, device and inode as evidence) plus a name-derived file-kind hint, with no hashing and no content reads.

#### Scenario: Loose file metadata only
- **WHEN** the root contains `notes.txt`
- **THEN** a file node records its size and modification time, and no content bytes are read

#### Scenario: Loose file kind hint
- **WHEN** the root contains `GetRight-setup.exe` and `IMG_0001.JPG`
- **THEN** the first is hinted `installer` and the second `image`, each recorded as a name-derived hint, and neither file is opened

### Requirement: Durable breadth-first frontier
Every discovered directory, at any depth, SHALL enter a durable frontier and receive a shallow probe and a policy decision before any of its descendants is cataloged. Expanded directories awaiting their listing SHALL belong to the same frontier. Work SHALL proceed breadth-first and SHALL survive restarts, so an interrupted scan resumes with what remains. No directory SHALL be probed twice in one scan, except for its single escalated probe.

#### Scenario: Scan resumes after crash
- **WHEN** the process is killed after 40 of 100 root child directories were probed and is then restarted
- **THEN** the scan job resumes, probes only the remaining 60, and finishes with each directory probed once in that scan

#### Scenario: Shallower directories decided first
- **WHEN** a scan expands a `mixed` directory at depth 1 while other depth-1 directories are still unprobed
- **THEN** every depth-1 directory is probed before any directory at depth 2

### Requirement: Shallow probe stays within budget
A shallow probe SHALL examine at most the configured entry budget of immediate entries (default 256) and perform at most the configured number of targeted marker lookups (default 16). The single escalated probe of an `unknown` directory SHALL use the escalated entry budget instead (default 1024). Every probe SHALL use descendant depth 0, read 0 bytes of file content, and create no inventory nodes for the probed directory's entries.

#### Scenario: A1 large application unit stays one record
- **WHEN** discovery probes an application directory that holds 100,000 nested entries, with its markers among the immediate entries
- **THEN** the directory is one active node with zero active descendant nodes, and the instrumented filesystem shows at most 256 entries examined, at most 16 marker lookups, and no access below the directory's immediate entries

#### Scenario: A1 cost independent of hidden size
- **WHEN** the same probe runs against fixtures with 1,000 and 100,000 nested entries that share the same immediate entries
- **THEN** both runs issue the same number of filesystem calls

### Requirement: Versioned directory descriptors
Each probe SHALL persist a descriptor holding: schema version; node revision; observation time; source label, ancestor names, and name; coverage (scope, entries examined, listing complete, descendants inspected, content bytes read, applied budget, stop reason); observed entries with stable evidence IDs; name-derived file-kind counts of the examined regular files; marker results; name-derived signals; errors; and recursive bytes as null. It SHALL also record a digest of its canonical form.

#### Scenario: Truncated listing descriptor
- **WHEN** a directory with 1,000 immediate entries is probed under a 256-entry budget
- **THEN** its descriptor records 256 entries examined, `listing_complete: false`, and stop reason `entry_budget`

#### Scenario: Marker result distinguishes outcomes
- **WHEN** marker lookups find one marker present, one absent, and one permission-denied
- **THEN** the descriptor records `present`, `absent`, and `unreadable` respectively

#### Scenario: File-kind counts
- **WHEN** a probed directory's examined entries include 40 `.jpg` files, 2 `.mov` files, and 1 `desktop.ini`
- **THEN** its descriptor counts 40 `image`, 2 `video`, and 1 `unknown` among examined regular files

#### Scenario: Ancestor context at depth
- **WHEN** `Backup2003/Program Files/GetRight` is probed
- **THEN** its descriptor names `Backup2003` and `Program Files` as ancestors, in order, as raw names

### Requirement: Signals are observations, not categories
Name-derived signals (for example `executable_present` or `vcs_marker_present`) SHALL be computed by a versioned observation policy from observed names and marker results only. A descriptor SHALL record the policy version. Signals SHALL NOT themselves assign a category, disposition, or protection. Only the versioned classification rules MAY use them, as cited evidence.

#### Scenario: Executable name observed
- **WHEN** a probed directory's examined entries include `setup.exe`
- **THEN** the descriptor lists `executable_present`, citing the evidence ID of that entry, and any category the node receives comes from a classification rule that cites that signal

### Requirement: Budgets pause work, never drop entries
When a scan reaches the initial discovery node budget (default 50,000), it SHALL pause with reason `node_budget_reached` and keep its frontier. It SHALL report how many entries remain unobserved, as known so far. Re-issuing `start-scan` SHALL continue from the frontier, and no entry SHALL be silently skipped.

#### Scenario: Node budget reached
- **WHEN** a root has 60,000 immediate entries and the node budget is 50,000
- **THEN** the job pauses with `node_budget_reached`, the root listing is marked incomplete, and a later `start-scan` continues past the first 50,000 entries

### Requirement: Incomplete observation stays explicit
A listing interrupted by an error SHALL keep the entries already observed and mark the listing incomplete with the error. An unreadable or unavailable directory SHALL record that outcome. If the source becomes unavailable mid-scan, the scan SHALL stop and mark the source unavailable. In none of these cases SHALL a directory be reported as empty.

#### Scenario: A5 partial listing
- **WHEN** reading a root listing fails with an I/O error after 300 entries
- **THEN** those 300 entries are recorded, the root listing is `partial` with the error, and the job ends in a state that names the error

#### Scenario: A5 unreadable child
- **WHEN** a child directory denies permission
- **THEN** its node records coverage `error` with outcome `unreadable`, and the UI does not show it as empty

#### Scenario: A5 source vanishes mid-scan
- **WHEN** the source root disappears while the frontier still has directories
- **THEN** the job stops with `source_unavailable`, the source is marked unavailable, and already-recorded nodes keep their last observations

### Requirement: Recursive sizes are never extrapolated
A directory's recursive size SHALL be reported as unknown unless a completed traversal measured it. No discovery step SHALL derive a recursive size from directory metadata or from probed entries.

#### Scenario: Probed directory size
- **WHEN** a probed directory's examined entries include files totalling 5 MB
- **THEN** its recursive size is reported as unknown, not as 5 MB

### Requirement: Lossless node identity
Each node SHALL have a stable application ID that is independent of its path. Nodes SHALL store their parent ID and exact raw name bytes, and SHALL derive relative paths from the parent chain. Clients SHALL address nodes only by ID.

#### Scenario: A17 non-UTF-8 and case-distinct names discovered
- **WHEN** a root contains `Report.txt`, `report.txt`, and a name with invalid UTF-8 bytes
- **THEN** three distinct nodes exist, and each stored name round-trips byte-for-byte

### Requirement: Expanded directories are listed one level
Every active expanded directory other than the source root SHALL be listed one level in bounded batches, exactly as the root is. Each listing SHALL be recorded as a run with its source epoch, generation, entries seen, completion state, and errors. A complete listing that changes nothing SHALL instead record a confirmation time on the directory's previous complete run. A listing SHALL catalog nothing beneath the immediate entries it observes.

#### Scenario: Listing a policy-expanded directory
- **WHEN** policy expands `Downloads` during a scan
- **THEN** its immediate entries become active nodes, its child directories enter the frontier, and a complete listing run is recorded for `Downloads`

#### Scenario: A26 unchanged listing adds no run
- **WHEN** a scheduled pass lists an expanded directory whose entries are all unchanged since its last complete listing
- **THEN** no run row and no node row is written, and the previous complete run records the new confirmation time

### Requirement: Policy decides each probed directory
After each probe, the effective classification SHALL decide the directory's inventory mode:
- a coherent unit (any category but `mixed`, `download_collection`, `unknown`) stays atomic, reason `policy_coherent_unit`;
- `mixed` and `download_collection` are expanded, reason `policy_expand`, within the automatic-refinement limits;
- `unknown` becomes an atomic review item.

An owner-set mode SHALL take precedence, and an already expanded directory SHALL stay expanded.

#### Scenario: Coherent unit stays atomic
- **WHEN** a probed directory is classified `application_installation`
- **THEN** it is atomic with reason `policy_coherent_unit`, it has no active descendants, and its coverage states that descendants were not inspected

#### Scenario: Mixed container expanded
- **WHEN** a probed directory named `Program Files` at depth 1 is classified `mixed`
- **THEN** it becomes expanded with reason `policy_expand`, its immediate entries are cataloged, and each child directory is probed and decided independently

#### Scenario: Owner mode wins over policy
- **WHEN** the owner collapsed a directory that is probed again and classified `mixed`
- **THEN** it stays atomic, and its reason remains `owner_collapsed`

### Requirement: Automatic refinement is bounded
Policy expansion SHALL stop at the configured automatic refinement depth (default 6, counted from the source root). A `mixed` or `download_collection` directory at that depth SHALL stay atomic with reason `refinement_depth_reached` and SHALL appear in the ambiguous review queue. Nodes created by policy expansion SHALL count toward the scan's node budget. Owner refinement is not depth-limited.

#### Scenario: Depth limit reached
- **WHEN** nested `mixed` directories reach depth 6
- **THEN** the depth-6 directory is atomic with reason `refinement_depth_reached`, nothing beneath it is cataloged, and it is listed in the ambiguous queue

### Requirement: One escalated probe for unknown directories
An `unknown` directory whose probe stopped at the entry budget SHALL receive exactly one further probe in that scan, with the escalated entry budget, before its decision is final. A directory still `unknown` afterwards, or whose first listing was complete, SHALL stay atomic with reason `insufficient_evidence`. Escalation SHALL never read content or descend.

#### Scenario: Escalation then review
- **WHEN** a directory with 5,000 immediate entries matches no rule within the first 256 entries
- **THEN** it is probed once more examining up to 1,024 entries, and if still `unknown` it is atomic with reason `insufficient_evidence` and appears in the ambiguous queue

#### Scenario: Complete listing is not escalated
- **WHEN** a directory with 12 immediate entries matches no rule
- **THEN** it is not probed again, and it is atomic with reason `insufficient_evidence`

### Requirement: Rescans revisit the active frontier
`start-scan` SHALL re-list the source root and every active expanded directory, and re-probe and re-classify every active child directory. A policy-decided atomic directory MAY be expanded by the new decision. No directory SHALL be collapsed by a rescan, and a rescan SHALL deactivate a node only as a verified missing entry or a replaced occurrence.

#### Scenario: Rescan reclassifies with new rules
- **WHEN** a rescan runs after an upgrade to a newer rule set
- **THEN** every active directory gets a new rule suggestion recorded with the new rule-set version, and owner overrides and owner-set modes are unchanged

### Requirement: First discovery starts only on request
The first scan of a source SHALL start only through the `start-scan` command for a configured, available source. Server startup and page views SHALL never start a source's first scan. After it, scheduled reconciliation observes the source without a command.

#### Scenario: Startup does not scan a new source
- **WHEN** the server starts with a configured source that has never been scanned
- **THEN** none of its directories is listed until `start-scan` is issued, and no scheduled pass is dispatched for it

### Requirement: Missing entries need verified absence
A child SHALL be tombstoned (made inactive with reason `missing`, kept as history) only after all of the following:
- its parent's listing read to the end, in the same source epoch and with the same directory identity;
- no hint reached the parent's scope during that pass;
- a rooted recheck reported the name absent.

A missing directory's active descendants SHALL become inactive with reason `ancestor_gone`, without a missing record of their own.

#### Scenario: Verified missing file
- **WHEN** a file seen by an earlier listing is deleted, and its parent is listed completely and the recheck reports it absent
- **THEN** its node is inactive with reason `missing` and the tombstone time, and it keeps its ID, metadata, and owner assertions as history

#### Scenario: Missing directory
- **WHEN** an expanded directory with 30 active descendants is deleted
- **THEN** it is tombstoned as `missing`, and its 30 descendants become inactive with reason `ancestor_gone`

#### Scenario: A28 incomplete parent listing
- **WHEN** a listing fails with an I/O error after 300 of 500 entries, and 50 earlier-seen children were not among the 300
- **THEN** no child is tombstoned, all 50 stay active, and the parent's scope stays due

#### Scenario: A28 offline disk during a missing-entry check
- **WHEN** a complete listing leaves candidates, and the source's disk goes offline before their recheck
- **THEN** no candidate is tombstoned, the source is marked unavailable, and its nodes stay browsable as history

#### Scenario: Entry seen only by the recheck
- **WHEN** a child is absent from a complete listing, but the recheck finds it present
- **THEN** it stays active and is not tombstoned

### Requirement: Occurrence identity at a name
An active node SHALL continue only while the entry at its parent and raw name has the same kind and inode, and, for a mount boundary, the same device. Otherwise the old node SHALL be tombstoned with reason `replaced` (its active descendants `ancestor_gone`), and a new node SHALL inherit no owner assertion except path-scoped protection. Hardlinked names SHALL stay separate nodes. A device-number change of a whole source with an unchanged identity SHALL NOT count as replacement.

#### Scenario: A31 replacement at the same name
- **WHEN** an active, overridden, `preserve` directory `Photos` is replaced on disk by a different directory with the same name, while a pin covers `Photos`
- **THEN** the old node is inactive with reason `replaced`, a new node has no override and disposition `unreviewed`, and the pin protects the new node

#### Scenario: A31 hardlinks stay separate
- **WHEN** two names in an expanded directory are hard links to one file, and one name is later deleted
- **THEN** each name had its own node, and only the deleted name's node is tombstoned

#### Scenario: A31 inode reuse under another name
- **WHEN** file `a` is deleted and a new file `b` in the same directory reuses `a`'s inode
- **THEN** `a` is tombstoned, `b` is a new node, and nothing of `a` is carried over to `b`

#### Scenario: A31 external rename
- **WHEN** an expanded directory's child `Photos`, which has a disposition and a pin, is renamed to `Fotos` outside curator
- **THEN** `Photos` is tombstoned after verified absence, `Fotos` is a new node with disposition `unreviewed`, and the pin still names `Photos` and does not protect `Fotos`

#### Scenario: Same filesystem, new device number
- **WHEN** a source's disk is reattached and the root reports a different device number but the same filesystem identity
- **THEN** no node is tombstoned or replaced, and reconciliation continues

### Requirement: Unchanged evidence adds no history
A probe whose descriptor has the same digest as the node's latest descriptor of the same budget, with an unchanged node revision and rule set, SHALL write no descriptor or classification row and SHALL NOT escalate. It records only a confirmation time. A scheduled or refresh listing SHALL probe only new, reactivated, or changed child directories, and SHALL NOT rewrite unchanged children.

#### Scenario: A26 unchanged items are not reclassified
- **WHEN** scheduled passes list an expanded directory whose 40 child directories are unchanged, and refresh 10 unchanged atomic units
- **THEN** the listing probes none of the 40, the 10 probes add no descriptor or classification row, and no filesystem access goes below any directory's immediate entries

### Requirement: Probe results bind to the state they observed
A probe's rule suggestion SHALL become current only if the source epoch, the node's observation and intent revisions, and its scope's dirty version are all unchanged since the probe started. Otherwise its descriptor SHALL be kept, its suggestion SHALL be stored as `stale`, and the scope SHALL stay due.

#### Scenario: A30 refresh hint during a probe
- **WHEN** the owner requests `refresh-scope` for an atomic unit while that unit's probe is in flight
- **THEN** the probe's suggestion is stored as `stale`, the unit's effective classification does not come from it, and the unit is probed again in a later pass
