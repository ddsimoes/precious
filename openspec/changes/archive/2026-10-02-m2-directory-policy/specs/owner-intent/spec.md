# Spec Delta

## Purpose

Records the owner's decisions (category overrides, dispositions, and protection pins) as authoritative assertions. They survive rescans, policy changes, boundary changes, and late results, and every change is guarded by an expected revision and written to the audit trail.

## ADDED Requirements

### Requirement: Category override
`set-classification` SHALL set or clear the owner category of one directory node. A set override SHALL take effect immediately, SHALL record the descriptor digest current at that moment, and SHALL persist across rescans, rule-set changes, and the collapse and re-expansion of an ancestor. Clearing it SHALL restore the precedence result.

#### Scenario: Override survives rescan
- **WHEN** the owner sets `personal_media` on a directory and a later scan suggests `cache` for it
- **THEN** its effective category is still `personal_media` with source `owner`

#### Scenario: Clearing an override
- **WHEN** the owner clears the override on a directory whose current rule suggestion is `cache`
- **THEN** its effective category becomes `cache` with source `rule`

### Requirement: Changed evidence flags an override
When a later descriptor of an overridden directory has a different digest from the one recorded with the override, the override SHALL stay effective and SHALL be flagged for owner review. It SHALL NOT be replaced or cleared automatically.

#### Scenario: Directory contents changed after override
- **WHEN** a directory overridden as `documents` is probed again and its examined entries now differ
- **THEN** it is still `documents` with source `owner`, it is flagged "evidence changed since your decision", and it appears in the ambiguous review queue

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
A node that is explicitly or inheritedly protected, or that contains a protected path, SHALL NOT receive disposition `cleanup_candidate`. A single-node request SHALL fail with HTTP 409 code `protected`. In a bulk request the node SHALL be excluded and reported, without blocking the other items.

#### Scenario: Ancestor of a protected path
- **WHEN** the owner marks `Backup` as `cleanup_candidate` while `Backup/Photos` is pinned
- **THEN** the response is HTTP 409 with code `protected` naming the pinned path, and `Backup`'s disposition is unchanged

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
