# classification Specification

## Purpose
Deterministic, versioned rules classify every file and folder from names and subtree aggregates into categories, families, traits, triage suggestions, and groups, each explained. R6 adds model suggestions.

## Requirements

### Requirement: Folder rules see the whole subtree
Folder rules SHALL match the folder's own name, signals among its immediate children, signals anywhere in its subtree with a minimum count, and the shares of each file kind in its subtree by file count and by bytes (§8). A folder's classification SHALL never depend only on its first level.

#### Scenario: Photos in year subfolders
- **WHEN** a folder `Fotos` holds only subfolders `2003`, `2004`, and `2005`, each full of JPEG files
- **THEN** `Fotos` is classified `personal_media`, although it holds no file directly

#### Scenario: Marker deep in the subtree
- **WHEN** a folder holds a project whose `.git` folder sits two levels below it
- **THEN** the folder's classification sees the `.git` marker through its subtree signals

### Requirement: Files are classified by name rules
Every file SHALL be classified by name rules when it is indexed, and its file kind SHALL be inferred from its name. Name rules SHALL recognize at least system junk, temporary and lock files, partial downloads, installers, and disk images; `.bin` counts as a disk image only next to a `.cue` file of the same stem.

#### Scenario: File name rules
- **WHEN** a source holds `Thumbs.db`, `Setup.exe`, `filme.avi.part`, and `office.iso`
- **THEN** `Thumbs.db` is `system_junk`, `Setup.exe` and `office.iso` are `installer_download`, and `filme.avi.part` is `temporary_data`

#### Scenario: A bin file is a disk image only beside its cue
- **WHEN** a folder holds `jogo.bin` and `jogo.cue`, and another folder holds only `dados.bin`
- **THEN** `jogo.bin` has file kind `archive` and category `installer_download`
- **AND** `dados.bin` is not classified as a disk image

### Requirement: Fixed categories in four families
Every classified entry SHALL carry one of sixteen fixed categories and the family that category belongs to, as in the §6.6 table: `personal` (personal_media, documents, source_project, application_user_data), `programs` (application_installation, application_configuration, os_installation, installer_download), `disposable` (system_junk, cache, temporary_data, generated_artifacts), and `containers` (download_collection, backup, mixed, unknown).

#### Scenario: Family follows the category
- **WHEN** an entry is classified `generated_artifacts`
- **THEN** its `family` is `disposable` in every API response that shows it

#### Scenario: No other category
- **WHEN** any entry of the regression corpus is read through the API
- **THEN** its `category` is one of the sixteen fixed categories

### Requirement: Folder bytes are broken down by family
Each folder SHALL carry its composition: its bytes and file counts by family, computed from its content. A child group whose category is outside the `containers` family SHALL count whole under its own family. Every other child folder SHALL contribute its own composition. Each file SHALL count under its file family: its category's family, or, when its category is `unknown`, by file kind:
- image, video, audio, document, and source as `personal`;
- installer and executable as `programs`;
- system as `disposable`;
- archive and other as `containers`.

A folder's own category SHALL NOT override its composition.

#### Scenario: A mixed folder splits by family
- **WHEN** a `mixed` folder holds a `personal_media` subfolder of 10 MB, a `cache` subfolder of 2 MB, and a loose 1 MB `.exe` file
- **THEN** its family breakdown shows 10 MB `personal`, 2 MB `disposable`, and 1 MB `programs`

#### Scenario: A photo folder keeps its other contents visible
- **WHEN** a `personal_media` folder holds 400 GB of photo folders, a `downloads` folder with a 4 GB disk image, and a 2 GB application installation group
- **THEN** its composition shows 400 GB `personal` and 6 GB `programs`, and Home counts those bytes the same way

#### Scenario: A loose photo counts as personal
- **WHEN** a JPEG that no rule matches sits in a folder
- **THEN** its category is `unknown`, and it counts as `personal` in the composition of every folder above it

#### Scenario: Home shows the breakdown
- **WHEN** Home is read for a scanned source
- **THEN** `by_family` sums to the source's total bytes

### Requirement: Folders list what is notable inside them
Each folder SHALL list up to ten notable entries below it, largest first:
- groups;
- folders with less than half of their bytes in the folder's dominant family, which is the family holding the most of its bytes;
- files whose file family differs from that dominant family.

Nothing inside a notable entry SHALL be listed again for the same folder.

#### Scenario: Clues from the top
- **WHEN** a source's root holds `local`, which is mostly photo folders but also holds a `downloads` folder with a disk image and the installed program `homeplanner`
- **THEN** the notable list of `local`, and of every folder above it, includes `downloads` and `homeplanner` with their sizes and categories

#### Scenario: A nested group appears once
- **WHEN** a folder holds a group that holds another group
- **THEN** the folder's notable list includes only the outer group

### Requirement: Traits are independent of the category
Entries SHALL carry traits independent of their category: `contains_user_material`, `contains_credentials`, `contains_database`, `contains_vcs`, and `possible_generated_content`. Traits SHALL be the union over all matching rules, whatever rule wins the category.

#### Scenario: Project with credentials and a database
- **WHEN** a folder with a `.git` folder and source files also holds `id_rsa` and an SQL dump
- **THEN** its category is `source_project` and its traits include `contains_vcs`, `contains_credentials`, and `contains_database`

### Requirement: Triage suggestion follows the category
Each classified entry SHALL carry a triage suggestion: `keep` for personal_media, documents, source_project, and application_user_data; `discard` for system_junk, cache, temporary_data, generated_artifacts, installer_download, and application_installation; `review` for every other category. Recovery material (`*.CHK` files and `found.000`) SHALL be `system_junk` with triage `review`.

#### Scenario: Recovery files are reviewed, never discarded
- **WHEN** the corpus's `temp` folder holds `FILE0000.CHK` and a folder `found.000` exists
- **THEN** both are `system_junk` with triage `review`

#### Scenario: Generated artifacts are discard
- **WHEN** a project holds `node_modules`
- **THEN** `node_modules` is `generated_artifacts` with triage `discard`

#### Scenario: Unknown is reviewed
- **WHEN** no rule matches a folder
- **THEN** its category is `unknown` and its triage is `review`

### Requirement: Groups never hide their contents
A folder classified application_installation, os_installation, source_project, application_user_data, cache, generated_artifacts, or backup SHALL be marked as a group (`group` true). Every entry inside a group SHALL still be indexed, listed by its parent, counted in totals, and found by search (§6.5).

#### Scenario: An application is a group and stays browsable
- **WHEN** `Arquivos de programas/WinZip` is classified `application_installation`
- **THEN** it has `group` true
- **AND** `GET /api/entries/{id}/children` lists its files, and a search by one file's name finds it

### Requirement: Conflicting rules give unknown
The highest-priority matching rule SHALL set the category. When rules with different categories match at the top priority, the category SHALL be `unknown` and the classification SHALL list the conflicting rules with their explanations. When no rule matches, the category SHALL be `unknown` with no rule listed.

#### Scenario: Two rules disagree at the same priority
- **WHEN** a folder matches a `cache` rule and a `documents` rule of the same, highest priority
- **THEN** its category is `unknown` and `classification.rules` in `GET /api/entries/{id}` lists both rules

#### Scenario: Higher priority wins
- **WHEN** a folder matches a `generated_artifacts` rule and a lower-priority `source_project` rule
- **THEN** its category is `generated_artifacts`

### Requirement: Every classification is explained
`GET /api/entries/{id}` SHALL return, under `classification.rules`, each rule that produced the entry's category or traits with its explanation sentence citing the names that matched. Explanations SHALL carry no coverage disclaimer, because the index is complete.

#### Scenario: Explanation cites the marker
- **WHEN** a folder is classified `source_project` because it holds `.git`
- **THEN** its classification lists that rule with an explanation naming `.git`
- **AND** no explanation says that part of the folder was not looked at

### Requirement: Rules recognize the minimum set
The rules SHALL recognize at least everything §8 lists: system junk, temporary data and caches, partial downloads, installers and disk images, application installations (one group per application) and operating-system copies, generated artifacts, personal media, documents, source projects, application user data, and credentials.

#### Scenario: R1.4 Rules classify the corpus as its ground truth says
- **WHEN** the regression corpus is scanned
- **THEN** every entry for which the ground truth gives a category, triage, or file kind has exactly that value
- **AND** in particular `RECYCLER`, `System Volume Information`, and `Thumbs.db` are `system_junk`; the `Downloads` installers are `installer_download`; each application folder under `Arquivos de programas` is an `application_installation` group; the `WINDOWS` copy is `os_installation`; `Fotos` is `personal_media`; the documents are `documents`; the projects with `.git` are `source_project`; the game saves are `application_user_data`; and `node_modules` and the Java `bin` output are `generated_artifacts`

### Requirement: User material vetoes discard
A folder whose triage would be `discard` and whose subtree holds user-material indicators (office documents, camera-named photos, saves, profiles, mail stores, credentials, databases) SHALL get triage `review` and `veto` true, and its classification SHALL list up to 20 indicator entries. Icons and images that programs ship SHALL not be indicators.

#### Scenario: R1.5 The spreadsheet in Microsoft Office vetoes discard
- **WHEN** the regression corpus is scanned, where `Arquivos de programas/Microsoft Office` would be `discard` as an application installation and its subtree holds `OFFICE11/Meu orcamento casamento.xls`
- **THEN** `Microsoft Office` has triage `review` and `veto` true
- **AND** `classification.indicators` in its `GET /api/entries/{id}` lists `OFFICE11/Meu orcamento casamento.xls`

#### Scenario: Program icons do not veto
- **WHEN** an application folder holds only program files plus `.ico`, `.bmp`, and `.png` images that the program ships
- **THEN** its triage stays `discard` and `veto` is false

### Requirement: Classification never changes owner decisions or tags
Classification and triage SHALL be suggestions only. A scan or rescan SHALL never set, change, or clear an entry's own decision, effective decision, or tags, whatever its category or triage.

#### Scenario: Discard triage does not decide
- **WHEN** a `generated_artifacts` folder with triage `discard` is indexed
- **THEN** its `eff_decision` stays `undecided` until the owner decides

#### Scenario: Owner keep survives a discard suggestion
- **WHEN** the owner keeps a folder whose triage is `discard` and the source is rescanned
- **THEN** the folder's decision is still `keep`
