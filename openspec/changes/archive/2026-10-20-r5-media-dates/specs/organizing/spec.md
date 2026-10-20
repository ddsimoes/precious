# Spec Delta

## ADDED Requirements

### Requirement: Set file dates is an organizing action
`plan-set-mtime` SHALL plan one `set_mtime` item per photo or video among its targets whose freshly derived effective date the filesystem can tell apart from its modification time, to that date rounded to the filesystem's resolution (§10.7.4). Files already at their date SHALL be counted, not listed. Refusals: `date_too_coarse` (coarser than a second), `not_dated_yet` (metadata not read yet), and `hard_link`. It needs the source's write permission.

#### Scenario: R5.4 Writing back a modification time changes no file content
- **WHEN** the owner sets the file dates of the photos in `Viagens/2008-03 Ouro Preto` and runs the action
- **THEN** `DSCN0001.JPG`–`DSCN0003.JPG` have their capture instants as modification times, with bytes and digests unchanged and no hashing read of them, `DSCN0004.JPG` (dated only to the month) is refused `date_too_coarse`, and History lists the action done

#### Scenario: A date known only to the year
- **WHEN** the targets include a photo whose effective date is the year 2006
- **THEN** its item is refused `date_too_coarse`, and the others are planned

#### Scenario: A photo not read yet
- **WHEN** the targets include a JPEG the `media` job has not read yet
- **THEN** its item is refused `not_dated_yet`, and nothing is written for it

### Requirement: Undo restores each previous modification time
`plan-undo` of a set-file-dates action SHALL plan, for each done item, a `set_mtime` back to the modification time the step found on disk just before writing. An item whose file's modification time is no longer the one written SHALL end `changed` with nothing written.

#### Scenario: R5.4 Undo restores the previous time
- **WHEN** the owner undoes the set-file-dates action
- **THEN** each photo's modification time is the one it had before, within the filesystem's resolution, and its digest is unchanged

#### Scenario: R5.4 A file changed since is skipped
- **WHEN** one of the photos is modified on disk after the write-back and before the undo runs
- **THEN** that item ends `changed` with nothing written, and the others are restored

### Requirement: Organize by date moves media into a template
`plan-date-organize` SHALL plan moving the photos and videos among its targets into folders below a destination, named by a template of `{year}`, `{month}`, `{day}`, and `{event}` (default `{year}/{month}`) from each freshly derived effective date, creating missing folders first. With `rename`, each file SHALL become `{date}_{time}_{name}` unless already so named. It SHALL refuse `date_too_coarse` and `not_dated_yet`, warn of siblings left behind, and apply bulk move rules, quarantine refusals, and the 10,000-item cap.

#### Scenario: R5.5 Organizing by Fotos/{year}/{month}/ previews every move
- **WHEN** the owner plans `{year}/{month}` into `Fotos` for `Viagens` and `celular_2011`
- **THEN** the preview lists one folder creation for each missing year and month folder and one move for each photo and video into the folder of its effective date, and nothing moves until the owner runs it

#### Scenario: The event token keeps the event name
- **WHEN** the template is `{year}/{month} {event}` and a photo of 2010-07 sits in `2010-07 Bahia`
- **THEN** it is planned into `2010/07 Bahia`, and a photo whose folder name is only a date goes into `2010/07`

#### Scenario: A RAW file beside its JPEG
- **WHEN** `IMG_0001.JPG` and `IMG_0001.CR2` share a folder and only the JPEG has a date fine enough for the template
- **THEN** the JPEG is planned, the CR2 is refused `date_too_coarse`, and the preview warns that one file leaves its sibling behind

### Requirement: Organize by date never overwrites, and offers copies for discard
A file whose destination name is taken, by an entry there or an earlier item of the plan, SHALL be refused `identical_copy`, naming the copy, when both have the same digest. Otherwise it SHALL take the first free name `name (1).ext`, `name (2).ext`, … under the source's name rules. The preview SHALL offer to discard the identical copies, and SHALL recommend deduplicating first when targets have copies among themselves or below the destination.

#### Scenario: R5.5 Same-content collisions are offered for discard
- **WHEN** two copies of `IMG-20110416-WA0003.jpg` with the same content land in `Fotos/2011/04`
- **THEN** the first is planned, the second is refused `identical_copy` naming the first, and the preview offers "Discard these copies", which sets their decision to discard only when the owner confirms

#### Scenario: R5.5 Same-name different-content files get a suffix
- **WHEN** two different photos named `IMG_0102.JPG` land in `Fotos/2010/07`
- **THEN** the later one in path order is planned as `IMG_0102 (1).JPG`

#### Scenario: R5.5 A name taken after the preview overwrites nothing
- **WHEN** a file appears at a planned destination name after the preview and before its move
- **THEN** that item ends `conflict`, both files are unchanged, and the other items run

### Requirement: The index follows a written modification time
When a `set_mtime` step is done, the index SHALL show the new modification and change times in the same transaction, the folders' newest, oldest, and by-year figures SHALL equal what a scan would compute, and the file's digest, archive listing, and media metadata SHALL be kept.

#### Scenario: A rescan after writing back
- **WHEN** the owner writes back a photo's date and then rescans the source
- **THEN** the rescan marks nothing changed, and the photo keeps its digest and metadata

### Requirement: History names the date actions
History SHALL title "Set file dates" and "Organize by date into …" actions, show each `set_mtime` item with its old and new times, and offer Undo for both. The CSV export SHALL name a `set_mtime` item's operation `set_mtime`, and an organize-by-date rename `move`.

#### Scenario: A write-back in History
- **WHEN** the owner opens History after setting file dates
- **THEN** the action is listed first with its title, each item shows the old and new times, and Undo and Export CSV are offered
