# Spec Delta

## ADDED Requirements

### Requirement: Category overrides and group marks are owner intent
The owner SHALL set an entry's category with `set-category` and a folder's group flag with `set-group`.
- **Targets.** Each command names its targets like `set-decision`: one `entry_id`, 1 to 1,000 `entry_ids`, or a `selection_id`.
- **Values.**
  - `set-category` takes one of the sixteen fixed categories, or `rules` to remove the override.
  - `set-group` takes `true`, `false`, or `rules`. `set-group` on a file is HTTP 400 `invalid_request`.
- **Members.** Archive members cannot be targets (HTTP 400 `invalid_request`): they are decided and described with their archive.
- **Survival.** A rescan, a rules change, and a model result SHALL never change an override or a mark. An entry that goes missing and returns keeps them (I4).
- **Effect.** The entry itself SHALL read its new category or group flag at once. Folder figures, notable entries, review lists, and Gems SHALL follow after the scan of its source that the command starts, or after the next scan when one is already running.
- **Controls.** The detail panel SHALL offer both controls, and SHALL show which values the owner set.

#### Scenario: Override survives a rescan and a rules change
- **WHEN** the owner sets `Downloads/emule-0.47c` to `application_installation`, and the source is rescanned with a newer rules version
- **THEN** its category is still `application_installation` set by the owner

#### Scenario: A member cannot be overridden
- **WHEN** a `set-category` request names a member of `Downloads/eMule0.47c-Installer.zip`
- **THEN** the response is HTTP 400 `invalid_request`, and nothing changes

#### Scenario: Figures follow after the scan
- **WHEN** the owner marks `Fotos/2006/Praia` as a group while no scan runs
- **THEN** the folder reads `group` true at once, a scan of its source starts, and when that scan ends the composition of `Fotos` counts `Praia` whole

## MODIFIED Requirements

### Requirement: Owner decisions are audited
Each accepted `set-decision`, `set-tags`, `create-tag`, `rename-tag`, `delete-tag`, `set-category`, or `set-group` request SHALL write one audit event in the same transaction. The event records the time, the client address, the target (the entry IDs or the selection, with counts of applied and skipped entries), and the old and new values. Audit events SHALL NOT contain file content, passwords, or session tokens.

#### Scenario: Protection change audited
- **WHEN** the owner changes `Fotos` from its own decision `keep` to `discard` with an individual request
- **THEN** a `decision_set` audit event records the entry `Fotos`, the old value `keep`, the new value `discard`, the time, and the client address

#### Scenario: Bulk decision audited once
- **WHEN** the owner applies `discard` to a selection of 40 entries, and 2 are skipped as kept
- **THEN** one `decision_set` audit event records the selection, the new value `discard`, 38 applied and 2 skipped, the time, and the client address

#### Scenario: Tag rename audited
- **WHEN** the owner renames `familia` to `família`
- **THEN** a `tag_renamed` audit event records the tag, the old and new names, the time, and the client address

#### Scenario: Category override audited
- **WHEN** the owner sets the category of `Projetos/site_antigo` from the rules' `source_project` to `documents`
- **THEN** a `category_set` audit event records the entry, the old value (none), the new value `documents`, the time, and the client address

#### Scenario: Rejected request not audited
- **WHEN** a `set-decision` request fails with HTTP 404 `not_found`
- **THEN** no audit event is written
