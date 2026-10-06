# Spec Delta

## MODIFIED Requirements

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

## ADDED Requirements

### Requirement: Expanded directories are listed one level
Every active expanded directory other than the source root SHALL be listed one level in bounded batches, exactly as the root is. Each listing SHALL be recorded as a run with its source epoch, generation, entries seen, completion state, and errors. A listing SHALL catalog nothing beneath the immediate entries it observes.

#### Scenario: Listing a policy-expanded directory
- **WHEN** policy expands `Downloads` during a scan
- **THEN** its immediate entries become active nodes, its child directories enter the frontier, and a complete listing run is recorded for `Downloads`

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
`start-scan` SHALL re-list the source root and every active expanded directory, and re-probe and re-classify every active child directory. A policy-decided atomic directory MAY be expanded by the new decision. No directory SHALL be collapsed and no node deactivated by a rescan.

#### Scenario: Rescan reclassifies with new rules
- **WHEN** a rescan runs after an upgrade to a newer rule set
- **THEN** every active directory gets a new rule suggestion recorded with the new rule-set version, and owner overrides and owner-set modes are unchanged

## REMOVED Requirements

### Requirement: Undecided directories stay atomic
**Reason**: M2 adds the category policy. Probed directories are now decided by "Policy decides each probed directory". The atomic-boundary guarantee is now stated by `inventory-boundaries` "No hidden catalog beneath atomic units".
**Migration**: Directories decided with reason `no_policy_retain_atomic` keep that reason, and stay atomic, until the next scan re-probes and re-decides them. The UI labels them "not yet classified".
