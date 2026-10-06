# directory-inspection Specification

## Purpose

Provides owner-requested inspections of atomic units: bounded live peeks and explicit aggregate walks. They improve what is known about a unit without cataloging its descendants, changing its inventory mode, or presenting partial or old measurements as current.

## Requirements

### Requirement: Peek is bounded and stores nothing
`GET /api/nodes/{id}/peek` on an active atomic directory SHALL list, live, at most the configured peek limit (default 100) of one directory's entries inside the unit. Each entry includes its kind, file size, mount-boundary flag, link text, and protection state. A peek SHALL write nothing to the inventory and SHALL state "first N entries in directory order, not a sample" and whether the listing was complete.

#### Scenario: A8 peek inside a large atomic unit
- **WHEN** the owner peeks into atomic `App`, which holds 100,000 nested entries
- **THEN** the response holds at most 100 of `App`'s immediate entries, the instrumented filesystem shows only `App` being listed, and the node, descriptor, aggregate, and issue counts and `App`'s inventory mode and intent revision are unchanged

#### Scenario: A8 drill-down stores nothing
- **WHEN** the owner follows a subdirectory token from a peek, two levels below `App`
- **THEN** that subdirectory's entries are returned, no inventory row is created or changed, and a later explorer listing of `App`'s parent is identical to the one before the peek

### Requirement: Peek addresses directories only by signed token
Below the unit, peek SHALL address directories only through server-issued tokens. Each token is signed, expires, and is bound to the unit's node ID, the source epoch, and the identity observed for each step. The request SHALL accept no client path. A forged, expired, or foreign token, or one whose epoch or identity no longer matches, SHALL fail with HTTP 409 `stale_evidence` and SHALL NOT be followed.

#### Scenario: Forged token rejected
- **WHEN** a client alters any byte of a peek token
- **THEN** the response is HTTP 409 `stale_evidence`, and no directory is opened

#### Scenario: Directory replaced by a symlink
- **WHEN** a peeked subdirectory is replaced by a symlink before its token is used
- **THEN** the response is HTTP 409 `stale_evidence`, and the link is not followed

### Requirement: Peek respects the filesystem boundary
Peek SHALL list symlinks with their link text and SHALL NOT follow them. It SHALL list special files by kind and SHALL NOT open them. It SHALL list mount boundaries without issuing a token for them. A peek SHALL be refused with `source_unavailable` while the source is unavailable, unresponsive, or awaiting reconfirmation. A peek of a node that is not an active atomic directory SHALL be refused with `invalid_node_state`.

#### Scenario: Nested mount inside a unit
- **WHEN** a peeked directory contains a nested mount point
- **THEN** the entry is shown as a mount boundary with no token, and nothing beneath it is read

### Requirement: Aggregate walk is an explicit job
`inspect-node` with operation `aggregate` on an active atomic directory SHALL enqueue a cancellable job and return HTTP 202. The walk SHALL traverse descendants without following symlinks, opening special files, crossing mounts, or reading file content. It SHALL create no inventory node. No page or API read SHALL start a walk.

#### Scenario: A2 aggregate the same atomic unit
- **WHEN** the owner aggregates atomic `App`, which holds 100,000 nested entries
- **THEN** the measurement records 100,000 entries with their file count and logical bytes, `App` stays atomic with zero active descendants, and the total node count is unchanged

#### Scenario: Size is never a hidden prerequisite
- **WHEN** the explorer and inspector show an atomic directory that has no measurement
- **THEN** its size is shown as unknown, and no walk is started

### Requirement: Aggregate measurement contents
A measurement SHALL record: counts of files, directories, symlinks, and special entries; logical bytes of regular files and allocated bytes where available; count and bytes of multiply-linked files; deepest level reached; mount boundaries not entered; unreadable directories; bounded error examples; name-derived file-kind counts; bounded preservation-indicator examples; entries examined, budget, stop reason, coverage state, and start and finish times.

#### Scenario: Hard links stay visible
- **WHEN** a measured unit holds two names hard-linked to one 1 GiB file
- **THEN** logical bytes include 2 GiB, the measurement reports 2 files with multiple links totalling 2 GiB, and no space saving is claimed

### Requirement: Partial measurements stay partial
A walk that skips a mount boundary, meets an unreadable directory, reaches its entry budget, is cancelled, or fails SHALL record coverage `partial` or `error` with the reason. Its bytes SHALL be reported only as an observed lower bound, never as the unit's size.

#### Scenario: Nested mount inside a measured unit
- **WHEN** a walk meets a nested mount point
- **THEN** the measurement is `partial` and names the boundary, and its bytes are labeled as a lower bound

#### Scenario: Cancelled walk keeps a lower bound
- **WHEN** the owner cancels a walk after 30,000 entries
- **THEN** the job ends `cancelled`, and a `partial` measurement with stop reason `cancelled` records those 30,000 entries as a lower bound

### Requirement: Aggregate freshness
A measurement SHALL be current only while its source epoch is unchanged, the unit's observation revision is the one the walk started with, and it is younger than the configured stale-after period (default 24 h). After that it SHALL be shown as stale, with its time, and excluded from current totals. A shallow probe, a scheduled boundary refresh, or a rescan SHALL NOT renew it. No measurement SHALL be retaken automatically.

#### Scenario: Measurement goes stale
- **WHEN** 25 hours pass after a complete walk, with a 24 h stale-after period
- **THEN** the inspector shows the measurement as stale with its time, and source totals count the unit as stale rather than measured

#### Scenario: Source epoch change
- **WHEN** a source is reconfirmed after an identity change
- **THEN** every earlier measurement in that source is stale

#### Scenario: Boundary change makes a measurement stale
- **WHEN** a measured atomic unit gains a new top-level entry, and its parent's next listing observes the unit's changed metadata
- **THEN** the unit's measurement is stale from that listing on, though it is younger than the stale-after period

#### Scenario: A29 deep internal edit without boundary evidence
- **WHEN** a file five levels inside a measured atomic unit changes, while the unit's own metadata and immediate entries stay the same, and its boundary refresh runs
- **THEN** the refresh records no new evidence, the measurement stays current until the stale-after period ends and is then stale, and no descendant node, recursive watch, or access below the immediate entries occurs

### Requirement: Walk indicators can only add risk
Preservation indicators found by a walk SHALL be passed to the classification rules and MAY add traits, with the walk's examples as evidence. A walk that finds no indicator SHALL NOT remove any trait or preservation risk derived from shallow evidence. Walk indicators SHALL keep applying after their measurement goes stale, until a newer complete walk in the same source epoch finds none; a partial walk SHALL NOT remove them. The inspector SHALL state that only names were checked.

#### Scenario: A4 nested save found by a walk
- **WHEN** a walk of an `application_installation` unit finds `Saves/slot1.sav` three levels down
- **THEN** the unit gains `contains_user_material` and preservation risk citing that example, and no node is created for it

#### Scenario: Risk outlives the measurement's freshness
- **WHEN** a walk found `Saves/slot1.sav` and 25 hours later the unit's boundary refresh runs with a 24 h stale-after period
- **THEN** the unit keeps `contains_user_material` and preservation risk, and the inspector says the evidence comes from a stale measurement taken at that time

#### Scenario: Partial walk does not remove risk
- **WHEN** a later walk of the same unit is cancelled before reaching `Saves`
- **THEN** the unit keeps the trait and its risk; only a complete walk that finds no indicator removes them

### Requirement: Walk results bind to the state they observed
A walk's measurement SHALL become the unit's measurement, and feed its classification, only if at commit the source epoch, the unit's observation revision, its active atomic state, and its scope's dirty version are all unchanged since the walk started. Otherwise the measurement SHALL be kept as history only, and the unit's scope SHALL stay due.

#### Scenario: A30 boundary change during a walk
- **WHEN** the owner requests `refresh-scope` for a unit while its aggregate walk is running, and the walk then finishes
- **THEN** the measurement is stored as history, the unit's measurement and classification are unchanged by it, and the unit is probed again

#### Scenario: A30 ancestor replaced during a walk
- **WHEN** an ancestor of a unit is replaced on disk and the ancestor's parent listing records the replacement while the unit's walk is running
- **THEN** the unit becomes inactive with reason `ancestor_gone`, and the walk's measurement is stored as history only
