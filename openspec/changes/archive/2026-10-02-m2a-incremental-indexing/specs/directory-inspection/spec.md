# Spec Delta

## MODIFIED Requirements

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

## ADDED Requirements

### Requirement: Walk results bind to the state they observed
A walk's measurement SHALL become the unit's measurement, and feed its classification, only if at commit the source epoch, the unit's observation revision, its active atomic state, and its scope's dirty version are all unchanged since the walk started. Otherwise the measurement SHALL be kept as history only, and the unit's scope SHALL stay due.

#### Scenario: A30 boundary change during a walk
- **WHEN** the owner requests `refresh-scope` for a unit while its aggregate walk is running, and the walk then finishes
- **THEN** the measurement is stored as history, the unit's measurement and classification are unchanged by it, and the unit is probed again

#### Scenario: A30 ancestor replaced during a walk
- **WHEN** an ancestor of a unit is replaced on disk and the ancestor's parent listing records the replacement while the unit's walk is running
- **THEN** the unit becomes inactive with reason `ancestor_gone`, and the walk's measurement is stored as history only
