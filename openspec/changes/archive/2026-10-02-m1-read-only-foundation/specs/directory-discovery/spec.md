# Spec Delta

## Purpose

Discovers a source directory-first. It expands only what policy allows, collects bounded shallow evidence for each directory before any recursive decision, and keeps undecided directories atomic, so discovery work never grows with hidden descendant counts.

## ADDED Requirements

### Requirement: Scans start only on explicit request
Discovery of a source SHALL start only through the `start-scan` command for a configured, available source. Server startup and page views SHALL never trigger a scan.

#### Scenario: Startup does not scan
- **WHEN** the server starts with configured sources and no queued jobs
- **THEN** no source directory is listed until `start-scan` is issued

### Requirement: Source root is expanded one level
A scan SHALL treat the source root as an expanded directory. It SHALL inventory the root's immediate entries in bounded batches, creating nodes for directories, regular files, and symlinks. Each loose file SHALL get occurrence metadata only (size, modification time, mode, device and inode as evidence), with no hashing and no content reads.

#### Scenario: Loose file metadata only
- **WHEN** the root contains `notes.txt`
- **THEN** a file node records its size and modification time, and no content bytes are read

### Requirement: Durable breadth-first frontier
Every discovered child directory SHALL enter a durable frontier and receive a shallow probe before any decision about its descendants. The frontier SHALL survive restarts, so an interrupted scan resumes with the directories not yet probed. Directories already probed in the same scan SHALL NOT be probed again.

#### Scenario: Scan resumes after crash
- **WHEN** the process is killed after 40 of 100 root child directories were probed and is then restarted
- **THEN** the scan job resumes, probes only the remaining 60, and finishes with each directory probed once in that scan

### Requirement: Shallow probe stays within budget
A shallow probe SHALL examine at most the configured entry budget of immediate entries (default 256) and perform at most the configured number of targeted marker lookups (default 16). It SHALL use descendant depth 0, read 0 bytes of file content, and create no inventory nodes for the probed directory's entries.

#### Scenario: A1 large application unit stays one record
- **WHEN** discovery probes an application directory that holds 100,000 nested entries, with its markers among the immediate entries
- **THEN** the directory is one active node with zero active descendant nodes, and the instrumented filesystem shows at most 256 entries examined, at most 16 marker lookups, and no access below the directory's immediate entries

#### Scenario: A1 cost independent of hidden size
- **WHEN** the same probe runs against fixtures with 1,000 and 100,000 nested entries that share the same immediate entries
- **THEN** both runs issue the same number of filesystem calls

### Requirement: Undecided directories stay atomic
Until a category policy exists, every probed non-root directory SHALL be recorded in `atomic` inventory mode, with decision reason `no_policy_retain_atomic`. An atomic directory SHALL have no active descendant inventory records. Evidence about its contents SHALL exist only inside its bounded descriptor.

#### Scenario: Atomic directory has no descendant rows
- **WHEN** a scan completes
- **THEN** the inventory holds no active node whose parent is an atomic directory

#### Scenario: Deeper content stays unknown
- **WHEN** an atomic directory's descriptor examined only its immediate entries
- **THEN** its coverage states that descendants were not inspected, and nothing marks its subtree as verified or empty

### Requirement: Versioned directory descriptors
Each probe SHALL persist a descriptor holding: schema version; node revision; observation time; source label, ancestor names, and name; coverage (scope, entries examined, listing complete, descendants inspected, content bytes read, applied budget, stop reason); observed entries with stable evidence IDs; marker results; name-derived signals; errors; and recursive bytes as null. It SHALL also record a digest of its canonical form.

#### Scenario: Truncated listing descriptor
- **WHEN** a directory with 1,000 immediate entries is probed under a 256-entry budget
- **THEN** its descriptor records 256 entries examined, `listing_complete: false`, and stop reason `entry_budget`

#### Scenario: Marker result distinguishes outcomes
- **WHEN** marker lookups find one marker present, one absent, and one permission-denied
- **THEN** the descriptor records `present`, `absent`, and `unreadable` respectively

### Requirement: Signals are observations, not categories
Name-derived signals (for example `executable_present` or `vcs_marker_present`) SHALL be computed by a versioned rule set from observed names and marker results only. A descriptor SHALL record the rule-set version. Signals SHALL NOT assign a category, disposition, or protection.

#### Scenario: Executable name observed
- **WHEN** a probed directory's examined entries include `setup.exe`
- **THEN** the descriptor lists `executable_present`, citing the evidence ID of that entry, and the node has no category

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

### Requirement: Rescans never infer absence
A rescan SHALL add and update observed entries but SHALL NOT tombstone, delete, or hide any previously observed node because it was not seen. Nodes not observed again SHALL keep their last observation time.

#### Scenario: A5 entry missing on rescan
- **WHEN** an entry seen in the first scan is absent from a second scan's listing
- **THEN** its node remains current with its original last-observed time, and no deletion is recorded

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
