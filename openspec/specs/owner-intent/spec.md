# owner-intent Specification

## Purpose

Records the owner's decisions (category overrides, dispositions, and protection pins) as authoritative assertions. They survive rescans, policy changes, boundary changes, and late results, and every change is guarded by an expected revision and written to the audit trail.

## Requirements

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

### Requirement: Own and effective decisions
Every entry SHALL have an own decision, which is inherit (none), `undecided`, `keep`, `discard`, or `later`, and an effective decision. The effective decision SHALL be the entry's own decision when it has one, otherwise the own decision of its nearest ancestor that has one, otherwise `undecided`. Entry reads SHALL show the own decision, the effective decision, and the entry it comes from, and every read after an accepted change SHALL reflect it (§6.7).

#### Scenario: R1.7 Deciding a folder decides its subtree
- **WHEN** Home shows the source's undecided and decided bytes, `Backup_PC_2004/Meus documentos` has its own decision `keep`, and the owner sets `discard` on the folder `Backup_PC_2004`
- **THEN** every other entry inside `Backup_PC_2004` reads effective decision `discard`, coming from `Backup_PC_2004`, while `Meus documentos` and everything inside it stay `keep`
- **AND** Home's `discard` bytes grow, and its `undecided` bytes shrink, by the bytes of the present files inside `Backup_PC_2004` outside `Meus documentos`

#### Scenario: Nearest own decision wins
- **WHEN** `Fotos` has its own decision `keep` and `Fotos/2006/rejeitadas` has its own decision `discard`
- **THEN** `Fotos/2006` reads `keep` coming from `Fotos`, and `Fotos/2006/rejeitadas/IMG_0001.JPG` reads `discard` coming from `Fotos/2006/rejeitadas`

#### Scenario: Default is undecided
- **WHEN** neither an entry nor any of its ancestors has an own decision
- **THEN** the entry reads own decision none and effective decision `undecided`, with no entry it comes from

### Requirement: Inherit clears an own decision
`set-decision` with `"decision": "inherit"` SHALL remove the target's own decision. The target and every descendant that has no own decision of its own SHALL then take the effective decision of the target's nearest decided ancestor, or `undecided` when there is none.

#### Scenario: Clearing a folder's discard
- **WHEN** `Backup_PC_2004` has its own decision `discard`, its parent `HD antigo` has its own decision `later`, and the owner sets `inherit` on `Backup_PC_2004`
- **THEN** `Backup_PC_2004` reads own decision none and effective decision `later` coming from `HD antigo`, and so do its descendants without an own decision

#### Scenario: Clearing with no decided ancestor
- **WHEN** the owner sets `inherit` on `Microsoft Office`, whose own decision is `discard` and which has no decided ancestor
- **THEN** `Microsoft Office` and its descendants without an own decision read effective decision `undecided`

### Requirement: Individual and bulk decision requests
`set-decision` SHALL name its targets either by a single `entry_id` (an individual request) or by `entry_ids` (1 to 1,000 IDs) or a `selection_id` (a bulk request). An individual request SHALL apply its value to that entry even when its effective decision is `keep`, explicit or inherited. A request with both or neither form, or with more than 1,000 IDs, SHALL fail with HTTP 400 `invalid_request`. An unknown ID SHALL fail the whole request with HTTP 404 `not_found`.

#### Scenario: Individual action changes an inherited keep
- **WHEN** `Fotos` has its own decision `keep` and the owner sets `discard` on `Fotos/2006/borrada.jpg` alone with `entry_id`
- **THEN** the response is `{"applied":1,"skipped_count":0,"skipped":[]}`, `borrada.jpg` reads `discard`, and `Fotos` and its other entries stay `keep`

#### Scenario: Individual action changes an explicit keep
- **WHEN** `Microsoft Office` has its own decision `keep` and the owner sets `discard` on it with `entry_id`
- **THEN** `Microsoft Office` and its descendants without an own decision read `discard`

#### Scenario: Both target forms
- **WHEN** a `set-decision` request carries both `entry_id` and `entry_ids`, or neither of them nor `selection_id`
- **THEN** the response is HTTP 400 `invalid_request`, and no decision changes

#### Scenario: Too many IDs
- **WHEN** a `set-decision` request carries 1,001 `entry_ids`
- **THEN** the response is HTTP 400 `invalid_request`, and no decision changes

#### Scenario: One unknown ID fails the whole request
- **WHEN** a bulk request names 50 indexed entries and one ID that names no entry
- **THEN** the response is HTTP 404 `not_found`, and none of the 50 entries changes

### Requirement: Bulk actions never change a keep
A bulk `set-decision` with any value other than `keep` SHALL skip every target whose effective decision is `keep`, explicit or inherited, and apply to the others. The response SHALL report `applied`, the full `skipped_count`, and up to 100 skipped entries with their `entry_id`, `path`, and `path_b64`. A bulk `keep` SHALL apply to every target (I5, §6.7).

#### Scenario: R1.11 Bulk discard skips kept entries
- **WHEN** a selection holds 40 files, including `Fotos/IMG_0042.JPG` with its own decision `keep` and `Backup_PC_2004/Meus documentos/carta.doc` inside the kept folder `Meus documentos`, and the owner applies `discard` to the selection
- **THEN** the response has `applied` 38 and `skipped_count` 2, and `skipped` lists both files with their paths
- **AND** both files still read effective decision `keep`, and the other 38 read `discard`

#### Scenario: Bulk inherit does not clear a keep
- **WHEN** a bulk request sets `inherit` on `entry_ids` that include an entry whose own decision is `keep`
- **THEN** that entry is skipped and keeps its own decision `keep`

#### Scenario: Skipped list is capped
- **WHEN** a bulk discard skips 250 kept entries
- **THEN** `skipped_count` is 250 and `skipped` lists 100 of them

#### Scenario: Bulk keep applies to all
- **WHEN** the owner applies `keep` to `entry_ids` naming one `discard` file, one `undecided` file, and one already kept file
- **THEN** `applied` is 3, `skipped_count` is 0, and all three read effective decision `keep`

#### Scenario: A keep set just before is honored
- **WHEN** an individual `keep` on `Fotos` commits just before a bulk discard that names `Fotos/2006/IMG_0001.JPG`
- **THEN** the bulk request skips `IMG_0001.JPG` and lists it as skipped

### Requirement: Decisions and tags survive rescans
A rescan SHALL NOT change any entry's own decision or own tags. An entry that goes missing and later returns at the same path SHALL keep its ID, its own decision, and its own tags (I4).

#### Scenario: R1.6 Rescan keeps decisions and tags
- **WHEN** `Backup_PC_2004` is decided `discard`, `Fotos/2006` is tagged `familia`, files inside both folders are changed, added, and removed on disk, and the source is rescanned
- **THEN** `Backup_PC_2004` still reads own decision `discard` and `Fotos/2006` still carries its own tag `familia`, and each remaining entry inside them reads the same effective decision and tags as before

#### Scenario: R1.6 Missing entry returns with its intent
- **WHEN** `Fotos/2006/natal.jpg`, with its own decision `keep` and own tag `dudu`, is absent from one rescan and present again in the next
- **THEN** after the second rescan it has the same entry ID, own decision `keep`, and own tag `dudu`

### Requirement: New entries inherit their folder's decision
An entry that a scan indexes for the first time SHALL have no own decision and SHALL read the effective decision of its nearest decided ancestor, or `undecided` when there is none.

#### Scenario: New file under a discarded folder
- **WHEN** `Backup_PC_2004` has its own decision `discard` and a rescan finds the new file `Backup_PC_2004/novo.tmp`
- **THEN** `novo.tmp` reads own decision none and effective decision `discard` coming from `Backup_PC_2004`

#### Scenario: Decision set during a scan
- **WHEN** the owner discards `Downloads` while a scan is still adding entries inside it
- **THEN** after the scan finishes, every entry inside `Downloads` without an own decision reads `discard`

### Requirement: Owner tags
The owner SHALL be able to create, rename, and delete tags with `create-tag`, `rename-tag`, and `delete-tag`. A tag name SHALL be 1 to 64 characters and unique ignoring case; an empty or longer name SHALL fail with HTTP 400 `invalid_request`, and a duplicate with HTTP 409 `tag_exists`. Deleting a tag SHALL remove it from every entry. `set-tags` SHALL add and remove own tags on bulk targets (§6.9).

#### Scenario: Duplicate name ignoring case
- **WHEN** the tag `familia` exists and the owner creates `Familia`
- **THEN** the response is HTTP 409 `tag_exists`, and no tag is created

#### Scenario: Rename keeps assignments
- **WHEN** the owner renames `familia` to `família`
- **THEN** every entry that carried `familia` now carries `família`

#### Scenario: Delete removes everywhere
- **WHEN** `livro` is on 12 entries and the owner deletes it
- **THEN** no entry carries `livro`, it is gone from `GET /api/tags`, and a `set-tags` naming its ID fails with HTTP 404

#### Scenario: Tagging a selection
- **WHEN** the owner adds `scan` to a selection of 30 entries
- **THEN** the response is `{"applied":30}`, and each of the 30 entries lists `scan` as an own tag

### Requirement: Tags are inherited as a union
An entry's effective tags SHALL be its own tags plus every tag on any of its ancestors. Entry reads SHALL show each effective tag as the entry's own or name the folder it comes from, and a search filtered by a tag SHALL find the entries that carry it, own or inherited (§6.9).

#### Scenario: R1.12 A folder tag applies to everything inside
- **WHEN** the owner adds `familia` to `Fotos/2006`, and `Fotos/2006/natal/IMG_0001.JPG` has its own tag `dudu`
- **THEN** every entry inside `Fotos/2006` shows `familia` as inherited from `Fotos/2006`, and `IMG_0001.JPG` also shows `dudu` as its own
- **AND** a search filtered by `familia` finds `Fotos/2006` and the entries inside it

#### Scenario: Union of nested tags
- **WHEN** `Fotos` carries `familia` and `Fotos/2006` carries `familia` and `natal`
- **THEN** `Fotos/2006/IMG_0001.JPG` shows `familia` and `natal` once each, every tag naming the folder it comes from

### Requirement: Inherited tags are removed at their folder
Removing a tag from an entry SHALL remove only that entry's own tag. A tag an entry inherits SHALL stay effective on it until it is removed from the folder that carries it.

#### Scenario: Removing an inherited tag on a descendant
- **WHEN** `Fotos/2006` carries `familia` and the owner removes `familia` from `Fotos/2006/natal`
- **THEN** `Fotos/2006/natal` still shows `familia` inherited from `Fotos/2006`

#### Scenario: Removing at the source folder
- **WHEN** the owner removes `familia` from `Fotos/2006`
- **THEN** no entry inside `Fotos/2006` shows `familia` unless it or another ancestor carries it

### Requirement: Selections resolve to explicit entries
`create-selection` SHALL resolve a search query to explicit entry IDs when it is created and return its `selection_id`, `count`, `bytes`, the `kept` count and bytes, and `expires_at`. Entries indexed later SHALL NOT join it. A selection SHALL expire one hour after creation; a request naming an expired selection SHALL fail with HTTP 409 `selection_expired` and change nothing (§11.3).

#### Scenario: Confirmation counts
- **WHEN** the owner selects all results of a search for name `.tmp` within `Backup_PC_2004`, which match 1,200 entries of which 3 are kept
- **THEN** the response has `count` 1,200, their `bytes`, and `kept` with count 3 and those entries' bytes

#### Scenario: Later entries are not included
- **WHEN** a scan indexes a new `Backup_PC_2004/z.tmp` after the selection was created, and the owner then discards the selection
- **THEN** `z.tmp` has no own decision from that request

#### Scenario: Expired selection
- **WHEN** the owner applies `discard` to a selection created more than one hour earlier
- **THEN** the response is HTTP 409 `selection_expired`, and no decision changes

### Requirement: Classification never writes decisions
No scan, rule, or job SHALL set or change an own decision or an own tag. A suggested triage SHALL only be shown as a suggestion (I4, I5).

#### Scenario: Discard triage is not a decision
- **WHEN** a scan classifies `Temp/cache` as `cache` with triage `discard`
- **THEN** its own decision stays none, its effective decision is inherited or `undecided`, and its tags are unchanged

### Requirement: A copy is decided like any other entry
A decision on one copy of a duplicate group or on one side of a relation SHALL change only that entry and its subtree, exactly as any `set-decision` does. It SHALL NOT change any other copy's decision, suggestion, or tags. No duplicate or relation SHALL ever set a decision, a suggestion, or a tag by itself (§6.7, I4, I5).

#### Scenario: R2.4 Deciding one copy changes nothing on the others
- **WHEN** the owner discards `Documentos/curriculo (1).doc` from its duplicate group, where `Documentos/curriculo.doc` carries the own tag `documento`
- **THEN** `curriculo (1).doc` reads discard, and every other copy keeps its decision, triage, and tags, `documento` included, and gains none

#### Scenario: R2.4 Deciding a side in Compare
- **WHEN** the owner keeps `Fotos` from Compare against `Fotos - Copia`
- **THEN** `Fotos` reads keep, and `Fotos - Copia` and its files keep their decisions and suggestions

#### Scenario: Hashing writes no decision
- **WHEN** hashing finds that every file of `Fotos - Copia` but one has a copy in `Fotos`
- **THEN** no entry's own decision, triage suggestion, or tag changes

### Requirement: Archive members are decided with their archive
An archive member SHALL have no decision or tags of its own; its effective decision SHALL be the archive's. A `set-decision` or `set-tags` that names a member SHALL fail with HTTP 400 `invalid_request` and change nothing (§6.4).

#### Scenario: A member follows its archive
- **WHEN** the owner discards `Downloads/fotos_2005_do_pendrive.zip`
- **THEN** each of its members reads effective decision discard, coming from the archive

#### Scenario: Deciding a member is refused
- **WHEN** a `set-decision` request names a member of a zip
- **THEN** the response is HTTP 400 `invalid_request`, and no decision changes

### Requirement: Category overrides and group marks are owner intent
The owner SHALL set an entry's category with `set-category` and a folder's group flag with `set-group`.
- **Targets.** Each command names its targets like `set-decision`: one `entry_id`, 1 to 1,000 `entry_ids`, or a `selection_id`.
- **Values.**
  - `set-category` takes one of the sixteen fixed categories, or `rules` to remove the override.
  - `set-group` takes `true`, `false`, or `rules`. `set-group` on a file is HTTP 400 `invalid_request`.
- **Members.** Archive members cannot be targets (HTTP 400 `invalid_request`): they are decided and described with their archive.
- **Survival.** A rescan, a rules change, and a model result SHALL never change an override or a mark. An entry that goes missing and returns keeps them (I4).
- **Effect.** The entry itself SHALL read its new category or group flag at once. Folder figures, notable entries, and review lists SHALL follow after the scan of its source that the command starts, or after the next scan when one is already running.
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
