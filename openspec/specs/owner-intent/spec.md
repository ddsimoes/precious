# owner-intent Specification

## Purpose

Records the owner's decisions (category overrides, dispositions, and protection pins) as authoritative assertions. They survive rescans, policy changes, boundary changes, and late results, and every change is guarded by an expected revision and written to the audit trail.

## Requirements

### Requirement: Category override
`set-classification` SHALL set or clear the owner category of one directory node. A set override SHALL take effect immediately, SHALL record the descriptor digest current at that moment, and SHALL persist across rescans, rule-set changes, and the collapse and re-expansion of an ancestor. Clearing it SHALL restore the precedence result.

#### Scenario: Override survives rescan
- **WHEN** the owner sets `personal_media` on a directory and a later scan suggests `cache` for it
- **THEN** its effective category is still `personal_media` with source `owner`

#### Scenario: Clearing an override
- **WHEN** the owner clears the override on a directory whose current rule suggestion is `cache`
- **THEN** its effective category becomes `cache` with source `rule`

### Requirement: Changed evidence flags an override
When a later descriptor of an overridden directory, probed with the same entry budget as the descriptor recorded with the override, has a different digest, the override SHALL stay effective and SHALL be flagged for owner review. A descriptor from a probe with a different budget, such as the escalated probe, SHALL NOT flag it. The override SHALL NOT be replaced or cleared automatically.

#### Scenario: Directory contents changed after override
- **WHEN** a directory overridden as `documents` is probed again and its examined entries now differ
- **THEN** it is still `documents` with source `owner`, it is flagged "evidence changed since your decision", and it appears in the ambiguous review queue

#### Scenario: Escalated probe does not flag
- **WHEN** an `unknown` directory is overridden after its escalated probe, and its next probe uses the normal budget and examines the same entries as the normal-budget probe before the override
- **THEN** the override is not flagged

### Requirement: Owner-only disposition
`set-disposition` SHALL set a disposition of `unreviewed`, `preserve`, `cleanup_candidate`, or `review` on each listed node. New nodes SHALL start `unreviewed`. No scan, rule, aggregate, or other job SHALL change a disposition. A request for `quarantined` SHALL be rejected with `invalid_request`, because quarantine does not exist yet.

#### Scenario: Classification never sets disposition
- **WHEN** a scan classifies a directory as `cache` with suggested triage `cleanup_candidate`
- **THEN** its disposition remains `unreviewed`

#### Scenario: Quarantined disposition rejected
- **WHEN** `set-disposition` requests `quarantined`
- **THEN** the response is HTTP 400 with code `invalid_request`, and nothing changes

### Requirement: Path-scoped protection pins
`set-protection` SHALL add or remove a pin on the path of a cataloged node, or on a path inside an atomic unit identified by a peek token. A pin SHALL protect its path and every path beneath it, whether cataloged or not. It SHALL stay at that path when nodes there are collapsed, deactivated, or reactivated. Pinning SHALL create no inventory node.

#### Scenario: Pin inside an atomic unit
- **WHEN** the owner pins `Saves` through a peek token inside the atomic directory `Game`
- **THEN** no node is created for `Saves`, `Game` reports that it contains a protected path, and `Game`'s inventory mode is unchanged

#### Scenario: Pin survives collapse and re-expansion
- **WHEN** a pinned directory's parent is collapsed and later refined again
- **THEN** the reactivated directory is still explicitly protected

### Requirement: Effective protection states
Each node SHALL report its protection as `explicit` (pinned at its own path), `inherited` (a pin at an ancestor path), or `none`. Separately, it SHALL report whether a pinned path exists beneath it. Both states SHALL reflect a pin change in every read that follows it.

#### Scenario: Inherited and contained protection
- **WHEN** `Backup/Photos` is pinned
- **THEN** `Backup/Photos` is `explicit`, `Backup/Photos/2004` is `inherited`, and `Backup` is `none` and reports that it contains a protected path

### Requirement: Protection blocks cleanup designation
A node that is explicitly or inheritedly protected, that contains a protected path, or that is an inbox or lies above one, SHALL NOT receive disposition `cleanup_candidate`. A single-node request SHALL fail with HTTP 409 code `protected`. In a bulk request the node SHALL be excluded and reported, without blocking the other items.

#### Scenario: Ancestor of a protected path
- **WHEN** the owner marks `Backup` as `cleanup_candidate` while `Backup/Photos` is pinned
- **THEN** the response is HTTP 409 with code `protected` naming the pinned path, and `Backup`'s disposition is unchanged

#### Scenario: A40 inbox cannot become a cleanup candidate
- **WHEN** the owner marks the inbox `data/Incoming`, or `data`, as `cleanup_candidate`
- **THEN** the response is HTTP 409 with code `protected` naming the inbox, and no disposition changes

### Requirement: Expected intent revision
Every node-targeted owner command SHALL carry the node's expected intent revision. A mismatch SHALL return HTTP 409 `revision_conflict` and change nothing. Each accepted override, disposition, or inventory-mode change SHALL increment the node's intent revision. Adding or removing a pin SHALL increment the intent revision of the cataloged node at that path, if one exists.

#### Scenario: Concurrent edits from two tabs
- **WHEN** two browser tabs load a node at intent revision 4 and both submit a disposition change
- **THEN** the first is applied and moves the node to revision 5, and the second receives HTTP 409 `revision_conflict`

### Requirement: Owner decisions are audited
Each accepted override, disposition, protection, refine, or collapse change SHALL write an audit event. The event records the time, the client address, the target node or pinned path, and the old and new values. Audit events SHALL NOT contain file content, passwords, or session tokens.

#### Scenario: Protection change audited
- **WHEN** the owner removes a pin
- **THEN** an audit event records the removal, the source and pinned path, the time, and the client address

### Requirement: Cleanup marks can carry copy evidence
An item of `set-disposition` with disposition `cleanup_candidate` MAY name a copy result. The result SHALL name that node as its copy: the inside folder or archive, either side of a `same` result, or either file of a file result, and that side SHALL NOT lie inside an archive. The result SHALL be `current`. The evidence SHALL stay attached until the node's disposition is next set. Protection and inbox locks SHALL refuse the mark as without evidence.

#### Scenario: Mark a copy with its evidence
- **WHEN** the owner marks `fotos-b` as `cleanup_candidate` naming the current result "`fotos-b` inside `fotos`"
- **THEN** the disposition is set, the inspector shows "inside `fotos`" with the matched size and the search time, and the audit event names the result

#### Scenario: Mark an archive with its evidence
- **WHEN** the owner marks the file `bkp.tar.gz` as `cleanup_candidate` naming the current result "`bkp.tar.gz` same as `bkp`"
- **THEN** the disposition is set with that evidence, and `bkp.tar.gz` appears in the `marked` queue with "same as `bkp`"

#### Scenario: Stale evidence refused
- **WHEN** the named result reads `changed`
- **THEN** a single-item request fails with HTTP 409 code `stale_evidence`, and nothing changes

#### Scenario: Stale evidence in a bulk request
- **WHEN** a bulk request names three current results and one `changed` result
- **THEN** three items are applied, and the fourth is reported with outcome `stale_evidence`

#### Scenario: Protection still refuses
- **WHEN** `fotos-b` is pinned and the owner marks it with a current result
- **THEN** the response is HTTP 409 code `protected` naming the pin, and no evidence is stored

#### Scenario: Evidence for the wrong node or disposition
- **WHEN** a request names a result for a node the result does not name as its copy, or names a result with disposition `preserve`
- **THEN** the response is HTTP 400 code `invalid_request`, and nothing changes

#### Scenario: A copy inside an atomic unit
- **WHEN** the owner names the result "`Elements/bkp-old-laptop` inside `bkp`" for the atomic unit `Elements`
- **THEN** the response is HTTP 400 code `invalid_request` stating that the copy lies inside `Elements` and must be refined before it can be marked

#### Scenario: A copy inside an archive
- **WHEN** the owner marks `bkp.tar.gz` naming the result "`home/fotos` in `bkp.tar.gz` inside `fotos`"
- **THEN** the response is HTTP 400 code `invalid_request` stating that the copy is a part of `bkp.tar.gz` and cannot be marked on its own

#### Scenario: Evidence ends with the mark
- **WHEN** the owner later sets `fotos-b` to `preserve`
- **THEN** its copy evidence is no longer shown as attached
