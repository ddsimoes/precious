# Spec Delta

## MODIFIED Requirements

### Requirement: Expanded directories are listed one level
Every active expanded directory other than the source root SHALL be listed one level in bounded batches, exactly as the root is. Each listing SHALL be recorded as a run with its source epoch, generation, entries seen, completion state, and errors. A complete listing that changes nothing SHALL instead record a confirmation time on the directory's previous complete run. A listing SHALL catalog nothing beneath the immediate entries it observes.

#### Scenario: Listing a policy-expanded directory
- **WHEN** policy expands `Downloads` during a scan
- **THEN** its immediate entries become active nodes, its child directories enter the frontier, and a complete listing run is recorded for `Downloads`

#### Scenario: A26 unchanged listing adds no run
- **WHEN** a scheduled pass lists an expanded directory whose entries are all unchanged since its last complete listing
- **THEN** no run row and no node row is written, and the previous complete run records the new confirmation time

### Requirement: Rescans revisit the active frontier
`start-scan` SHALL re-list the source root and every active expanded directory, and re-probe and re-classify every active child directory. A policy-decided atomic directory MAY be expanded by the new decision. No directory SHALL be collapsed by a rescan, and a rescan SHALL deactivate a node only as a verified missing entry or a replaced occurrence.

#### Scenario: Rescan reclassifies with new rules
- **WHEN** a rescan runs after an upgrade to a newer rule set
- **THEN** every active directory gets a new rule suggestion recorded with the new rule-set version, and owner overrides and owner-set modes are unchanged

## ADDED Requirements

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

## REMOVED Requirements

### Requirement: Scans start only on explicit request
**Reason**: M2a adds scheduled reconciliation, which observes scanned sources without a command.
**Migration**: The first scan still needs `start-scan`; see "First discovery starts only on request". Sources scanned before the upgrade are reconciled on schedule.

### Requirement: Rescans never infer absence
**Reason**: M2a tombstones entries, but only after verified absence (§5.5.4).
**Migration**: See "Missing entries need verified absence". After the upgrade, an entry absent from disk is tombstoned when its parent is next listed completely. Inactive rows deactivated before the upgrade record reason `unrecorded`.
