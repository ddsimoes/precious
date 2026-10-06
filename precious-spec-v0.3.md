# Precious: product specification

**Version:** 0.3 (reviewed with the owner on 2026-10-05)
**Date:** 2026-10-05
**Supersedes:** `directory-first-curator-spec-v0.2.md` (see [ADR 0008](docs/adr/0008-product-reset.md))
**Implementation:** Go server and a React + TypeScript single-page app, shipped as one binary

MUST marks a required behavior. SHOULD allows a recorded tradeoff. Numbers marked *target* are goals to measure, not claims.

## 1. Problem

The owner has a disk that has been collecting files for over twenty years. It holds duplicated files and folders, zip files next to their unpacked copies, several copies of the same material with different edits, personal material mixed with everything else, 2000s application downloads that no longer make sense, and copied `Program Files` and `WINDOWS` trees. Separating what matters from what does not is hard by hand.

A metadata survey of the owner's real archive (ADR 0005, 0006, 0007) found:

- 1.17 TB in about 1.3 million files;
- about 210 GB of redundancy in three photo trees, rearranged and renamed;
- 356 GB in archives (30% of the bytes), including a 105 GB machine backup next to its unpacked copy;
- near-copies, such as one video at two sizes and a `.tgz` next to its `.old` revision;
- material that exists nowhere else.

## 2. Objective

Precious gives the owner a clear view of the disk, the problems on it, what to do first, and the means to act, safely.

The owner can answer these questions with the product:

1. Where is my space going, by folder, by kind of file, and by year?
2. What is obviously disposable, and how much space does it hold?
3. What is duplicated, and which copy should I keep?
4. Which copies have diverged, and how?
5. What is personal or valuable, and where is it hiding?
6. What have I decided so far, and what is left?
7. How do I carry out my decisions without losing anything by mistake?

The measures of success are time to a confident decision, bytes resolved per hour of the owner's attention, and personal material found that the owner did not know was there.

## 3. Users and deployment

- **Users.** Precious is built for anyone with an accumulated mess of files, not just its first owner. Each installation has one owner, with a login.
- **Where it runs.** On Linux, macOS, and Windows. It can run on a home or small-office file server, reached from other machines on the local network, or on the owner's own computer.
- **What it reads.** Internal disks, external disks, USB sticks and memory cards, and optical discs (CD, DVD, Blu-ray), on any filesystem the operating system can mount: ext4, ZFS, Btrfs, NTFS, APFS, HFS+, exFAT, FAT32, UDF, ISO 9660. Network mounts are read-only in this version.
- **Delivery.** The core is portable from R1, and Linux is delivered and tested first. macOS, Windows, and removable and optical media are completed in their own milestone (§14). The owner's reference server is Linux with OpenZFS (RAIDZ1).
- Deployment hardening (HTTPS, reverse proxy, remote access from outside the network) is a deployment concern, not a product milestone. The product MUST run over plain HTTP on a local network when configured to.

## 4. Principles

1. **Index everything, decide in groups.** Every file and folder is indexed with its metadata. Grouping (a folder treated as one item) helps the owner decide; it never hides data or limits what is indexed.
2. **Answer questions, not dump data.** Every screen exists to answer one of the questions in §2. Evidence and details are one click away, not in front of the owner.
3. **Sizes everywhere.** Every folder shows its total size and file count as soon as the scan has passed it.
4. **Decide fast, act safely.** Decisions are cheap and reversible. Removal goes through an explicit plan and a reversible quarantine. Organizing moves are explicit and can be undone.
5. **Assist, never authorize.** Rules and models classify and suggest. Only the owner decides, and only the owner's explicit actions touch the disk.
6. **Honest but quiet.** Incomplete data (scan in progress, hashing not finished, unreadable folders) is visible as a compact indicator with details on demand, never as paragraphs of disclaimers.
7. **Plain language.** The interface uses the owner's vocabulary (folder, copy, duplicate, keep, discard, quarantine), never internal terms.

## 5. Invariants

These hold in every milestone. A design that needs to break one stops and records an ADR.

- **I1 Observation never mutates.** Scanning, hashing, archive listing, classification, and previews never write to a source.
- **I2 Only explicit owner actions mutate.** The only code that renames, moves, or deletes in a source is one shared executor. It acts only for an approved cleanup plan (§10.1) or an organizing action the owner requested (§10.5), never on its own initiative.
- **I3 No overwrite.** A move or restore never replaces an existing destination. It uses the platform's no-replace primitive: `renameat2` with `RENAME_NOREPLACE` on Linux, `renamex_np` with `RENAME_EXCL` on macOS, and `MoveFileEx` without `MOVEFILE_REPLACE_EXISTING` on Windows. A filesystem without one is read-only to Precious. Intent is journaled before each filesystem step. An ambiguous outcome stops for manual recovery and is never retried blindly.
- **I4 Owner decisions survive.** Decisions, tags, and overrides survive rescans, rule changes, and model results. No job writes an owner decision.
- **I5 Classification is not authorization.** A suggestion never becomes a decision on its own. A kept entry is never removed by a plan, including when an ancestor is discarded, and no bulk action changes it.
- **I6 Lossless identity.** Names are stored exactly as the platform gives them (bytes on Linux and macOS, UTF-16 on Windows, including sequences that are not valid text), with no case folding or Unicode normalization. They are displayed with invisible or invalid parts escaped. The browser addresses entries by ID, never by path.
- **I7 Scope-honest claims.** "Duplicate" and "no other copy" are stated relative to the content that was actually hashed, and the interface shows how much was hashed. An unreadable folder is shown as unreadable, never as empty.
- **I8 Provider independence.** The domain, storage, and interface never depend on a vendor's types. Every model is behind the classifier contract (§9.3).
- **I9 Late results cannot win.** An asynchronous result (hash, classification, aggregate) is applied only if the entry it observed has not changed since.

## 6. Domain model

### 6.1 Source

A source is a directory tree the owner adds: a whole volume, or a folder on it. It has an ID, a label, a write permission (off by default), and a location made of a **volume identity** and a path inside that volume.

- **Adding sources.** The owner adds sources in the interface, with a picker of volumes and folders limited to **allowed roots**.
  - On a server, the configuration file lists the allowed roots (for example `/tank`, `/mnt`, `/media`).
  - On a personal computer, the default is the mounted volumes and the user's home folder.
  - The picker navigates by opaque handles, never by typed paths. The server refuses anything outside the allowed roots.
- **Write permission.** The owner turns on writes for a source in the interface, after a confirmation. The configuration can forbid writes entirely, for a read-only installation; the toggle is then unavailable.
- **Volume identity.** The filesystem UUID, serial number, or ZFS dataset GUID, as the platform reports it. The current mount point is only where the volume happens to be today. A USB stick that comes back as `/media/x`, `E:\`, or `/Volumes/X` is recognized as the same source.
- **Online and offline.** A source whose volume is not mounted is **offline**. Its index stays browsable and searchable, and its decisions and tags stay visible: "that file is on the blue USB stick". Nothing that needs the disk (scans, hashing, previews, moves) runs until it comes back. Its copies still count as copies, marked as offline.
- **Filesystem capabilities.** Detected per filesystem and recorded:
  - read-only, which optical discs always are;
  - case sensitivity and Unicode normalization of names;
  - stable file identity across mounts;
  - timestamp resolution and whether times are stored in local time (FAT);
  - hard links;
  - availability of a no-replace rename.

  Every comparison, collision check, and move follows them.
- **Nested mounts.** On ZFS, each child dataset is a nested mount. Nested mounts are not crossed unless the source opts in. Each filesystem under a source is separate for moves and quarantine (§10).

### 6.2 Entry

Every file, folder, and symlink under a source is an entry with:

- a stable ID, its parent ID, its raw name, and its kind;
- size, allocated size where the filesystem reports it, modification time, change time where the platform has one, and the file identity the platform gives (device and inode, or volume serial and file ID on Windows);
- extension and file kind (image, video, audio, document, source, archive, installer, executable, system, other), inferred from the name and labeled as inferred;
- first-seen and last-seen times, and a state: present, missing, or unreadable.

Symlinks are recorded and never followed. Special files (FIFOs, sockets, devices) are recorded and never opened.

### 6.3 Folder aggregates

Every folder carries aggregates over its whole subtree: total bytes, allocated bytes, file and folder counts, bytes and counts by file kind, bytes by modification year, oldest and newest modification times, hashed bytes, duplicated bytes, and decided bytes. Aggregates are computed bottom-up after a scan and updated by rescans.

An aggregate over a subtree with unreadable folders is marked partial.

### 6.4 Content and relations

- **Content.** A file's content identity is its SHA-256, set only by a complete read. Files with the same digest form a *duplicate group*.
- **Folder relations.** Computed from file digests, ignoring names and layout: `same` (each folder's files all exist in the other), `inside` (every file of one exists in the other), and `overlap` (a large share matches). Each relation records which files are only on one side.
- **Archives.** zip and the tar family (tar, tar.gz, tar.bz2, single-file gzip and bzip2) are listed and hashed member by member in memory, never extracted to disk (ADR 0007). Archive members are browsable as read-only virtual entries and take part in relations: an archive can be `same` as a folder. Only the whole archive can be decided.
- **Version families (R7).** Files with related names and different content, such as `curriculo.doc`, `curriculo_final.doc`, and `curriculo_final2.doc`, form a candidate family. A family is a suggestion, never a proven history; no file is assumed newest because of its name or date.

### 6.5 Groups

A *group* is a folder that the rules, a model, or the owner marks as one item for review: an installed application, a photo event, a source project, a game, a copied `WINDOWS` tree. Groups appear as one row in review lists. The map and search still reach every entry inside them. Groups can nest; the outermost group is the review row.

### 6.6 Classification

Every folder and file can carry:

- a **category**, from a fixed list that rules and models label into. Owners label with tags (§6.9), never with new categories. The interface groups the categories into four families for charts and colors, and shows the exact category in details and filters. A folder also carries its **composition**, its bytes by family computed from its content (only a group counts as one item), so a mostly-photo folder that holds downloads and programs shows all three.

  | Family | Categories |
  |---|---|
  | Personal and valuable | personal_media, documents, source_project, application_user_data |
  | Programs and system | application_installation, application_configuration, os_installation, installer_download (installers and disk images) |
  | Disposable | system_junk, cache, temporary_data, generated_artifacts |
  | Containers | download_collection, backup (a copy of a whole machine or disk), mixed, unknown |

- **traits**, independent of the category: contains_user_material, contains_credentials, contains_database, contains_vcs, possible_generated_content;
- a **triage suggestion**: keep, discard, or review;
- for each, its **source** (owner, rule, or model), the evidence or rule that produced it, and for models the native probability when the provider gives one.

Precedence: owner override, then rule, then model. A model fills in where the rules say unknown, and adds probabilities a rule cannot give.

### 6.7 Decisions

The owner's decision on an entry is one of: undecided (the default), keep, discard, or later. A decision on a folder applies to its whole subtree, except where a descendant has its own decision. The *effective decision* of an entry is the nearest explicit decision on it or an ancestor.

A *keep* is the owner's protection; there is no separate protection concept. Two rules make it safe:

- **Bulk actions never change a keep.** A bulk action (from a review list, search results, or a model threshold) skips every entry whose effective decision is keep, explicit or inherited, and reports what it skipped. Only an individual action on the entry itself changes a keep.
- **Plans never remove a keep.** A plan never quarantines a kept entry, even when an ancestor is discarded (§10.1).

Duplicates and folder relations are information, not decisions. The owner decides each copy like any other entry, one by one or in bulk, whenever they choose. Deciding one copy never changes another copy's decision, suggestion, or tags, and Precious never picks a copy to keep.

### 6.8 Cleanup plans and organizing

Removing and reorganizing are separate features that never mix.

- A **cleanup plan** is an immutable list of quarantine and restore operations on entry IDs, with the identity each entry had when the plan was drafted (§10).
- **Organizing** moves and renames entries to destinations: in bulk or one by one, interactively, often to many destinations (§10.5). Rescuing kept material out of a folder before discarding the folder is organizing.

### 6.9 Tags

Tags are the owner's free labels, such as `familia`, `dudu`, `livro`, `paper`, `scan`, or `documento`. An entry can carry any number of them, and they are independent of the category.

- **Inheritance.** A tag on a folder applies to everything inside it. An entry's *effective tags* are its own tags plus those inherited from its ancestors. An inherited tag cannot be removed from a single descendant; tag the subfolders instead. The interface always shows whether a tag is the entry's own or which folder it comes from.
- **Durability.** Tags belong to the entry. They survive rescans and follow the entry through moves and renames made by organizing. They are owner decisions (I4).
- **Uses.** Search and filters, Map coloring, bulk tagging from any list, and organizing templates (for example `Familia/{year}/…`). From R6, Jev can suggest tags from the owner's tag list, with probabilities (§9.1).

## 7. Indexing

- **Full scan.** A scan walks every folder of a source, recording metadata only. It reads no file content. It commits in batches, shows progress (folders, files, and bytes so far), can be paused and resumed, and survives a restart.
- **Indexing target.** A scan takes at most 1.5 times as long as a bare metadata walk of the same tree, one that only stats each entry (as `find -printf '%s %T@'` does), measured cold on the same machine. The disk sets the pace; Precious's own overhead, including database writes, is what this target bounds. R1 starts by measuring the bare walk on the reference server.
- **Rescan.** A rescan walks the source again and matches entries by file identity and path when the filesystem's identity is stable, and by path, size, and modification time otherwise (FAT and exFAT). Modification times are compared within the filesystem's resolution, and FAT local times allow for time-zone and daylight-saving shifts. Changed metadata updates the entry and invalidates its hash. Entries no longer found become missing, and their decisions are kept as history. Rescans run on demand and on an optional schedule. Change feeds (`inotify`, `zfs diff`, FSEvents, the NTFS change journal) are a later optimization.
- **Hashing.** A background job hashes the files whose size is shared with another file, largest first. Files of at least 16 MiB are compared first by three 64 KiB samples and read in full only when the samples match. Digests are cached by file identity, size, and modification and change times (with the same fallback as rescans), so unchanged files are never read twice. Archives are opened in the same pass. Hashing coverage (bytes hashed out of bytes that could have a copy) is always visible.
- **Folder relations** are recomputed when hashing advances, from the index, with no separate snapshot.

## 8. Rules

Rules are deterministic and versioned. They match names, paths, markers (such as `.git`, `unins000.exe`, `DCIM`, `CACHEDIR.TAG`), and folder aggregates (for example, at least 80% of a folder's bytes are images). Unlike v0.2, rules see the whole subtree through its aggregates, not just a folder's first level.

The rules MUST recognize at least:

- system junk: `$RECYCLE.BIN`, `RECYCLER`, `System Volume Information`, `Thumbs.db`, `desktop.ini`, `.DS_Store`, `.Trash-*`, `found.000` and `*.CHK` (as recovery material, so review, never discard);
- temporary data and caches: `Temp`, `tmp`, `Temporary Internet Files`, `cache`, `~$*` lock files, `*.tmp`;
- partial downloads: `*.part`, `*.partial`, `*.crdownload`;
- installers and old downloads: `setup*.exe`, `*install*.exe`, `*.msi`, and disk images (`*.iso`, `*.img`, `*.nrg`, `*.bin` with `*.cue`);
- application installations (installed `Program Files` trees, one group per application) and operating-system copies (`WINDOWS`, `system32`, `I386`);
- generated artifacts: `node_modules`, `__pycache__`, `bin` and `obj` next to sources, `target`, `build`;
- personal media (`DCIM`, folders that are mostly photos, video, or audio), documents, source projects, application user data (profiles, saves, mail stores), and credentials (`id_rsa`, `*.kdbx`, `*.pfx`).

A user-material indicator (documents, photos, saves, profiles, credentials) inside an otherwise disposable group vetoes the discard suggestion for that group, and the indicator is listed in the group's details. Example: `Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls`.

## 9. Classifier assistant

### 9.1 Role

Jev (TypeSafe API) is the first model provider. It classifies quickly and cheaply into predetermined labels, with calibrated probabilities. It does not generate text and does not analyze images. Precious uses it in two ways:

1. **Classification.** It assigns a category, a triage suggestion, a destination chosen from the owner's list, or tags chosen from the owner's tag list, to groups and files the rules leave unknown or weakly decided.
2. **Routing deeper analysis.** It decides which items deserve more expensive steps: full hashing, the owner's attention first, or, in future, a generative or image model.

Generative text models (summaries, explanations, natural-language search) and image recognition are out of scope for this version (§13).

### 9.2 Input and output

- **Input:** a text *state* built from the index. The state never includes the source root or an absolute path. It holds:
  - the entry's name and its ancestors' names;
  - its kind and size;
  - its aggregates (bytes by kind and by year, counts, date range);
  - a bounded sample of descendant names;
  - its markers and the rule results.
- **Text excerpts.** By default, no file content is sent. With a separate grant per source (§9.4), the state of a file also carries a text excerpt: up to about 4 KB of text from its beginning, never binary content.
  - Read natively: plain text, Markdown, source code, CSV, DOCX, and ODT.
  - Read only when the optional tools are installed: PDF (`pdftotext`) and old DOC (LibreOffice).
- **Questions:** typed and independent of one another:
  - *choice*, among a fixed label set (category, triage, destination);
  - *yes/no*, for example "do the names indicate personal material?";
  - *score*, on a defined rubric.
- **Output:** for each question, the answer and its probabilities as the provider reports them, stored with their scale and the model version. A provider that gives labels only is fully usable; Precious never invents a probability for it.
- **Thresholds:** the owner can sort and filter by probability and accept suggestions in bulk above a threshold. Thresholds are set per question and per model version, from an evaluation (§9.4).

### 9.3 Contract and providers

The classifier contract is provider-neutral: typed questions in, typed answers out, with explicit capability differences between providers. Adapters:

- `typesafe` (Jev): first and primary;
- `structured_chat`: any OpenAI-compatible endpoint with schema-constrained output, local (Ollama) or cloud, kept so that a second provider can be swapped in by configuration;
- `fake` and `disabled`: for tests and for running without a model.

Both local and cloud providers MUST work through the same contract. Configuration selects them; no code change is needed.

### 9.4 Cost, cache, privacy, and evaluation

- **Privacy.** Two grants per source, both off by default:
  - *metadata*: the state without content;
  - *text excerpts*, which requires the metadata grant.

  Nothing is sent to a provider without the grant it needs. A preview shows exactly what would be sent for any entry.
- **Spending.** Daily and per-job spending caps; spend is shown on the settings screen.
- **Cache.** Answers are cached by state digest, question revision, and model version, so an unchanged entry is never asked twice.
- **Evaluation.** An evaluation command compares providers and model versions on a frozen, owner-labeled corpus. It reports per-label precision and recall, and the false discards of material the owner kept.

## 10. Cleanup plans, quarantine, and organizing

### 10.1 From decisions to a plan

1. **Draft.** A plan is drafted from the explicit discard decisions (or from one review list). Each plan item is the topmost explicitly discarded entry; nested explicit discards are folded into it.
   - An item whose subtree contains a kept entry is **blocked**. It is not quarantined, in whole or in part. The plan lists it with the kept entries that block it, so the owner can move them out (§10.5) or change a decision.
   - The rest of the plan proceeds. Nothing is ever quarantined "around" a kept entry.
2. **Show before approval.** Before approval, the plan shows the operations, total bytes, file counts, and every warning. It also shows a light summary built only from data already computed, without reading any file: bytes with a known copy elsewhere, bytes with no known copy, and personal-material indicators.
3. **Approval** freezes the plan. Changing it creates a new plan.
4. **Duplicates are never removed together.** A plan never quarantines every copy of a duplicate group or both sides of a relation on the grounds that they are duplicates. Before quarantining a copy for that reason, the executor re-reads and verifies a copy that stays outside the plan.

### 10.2 Preflight and execution

- **Preflight.** Writes are allowed only on sources with write permission. Before each operation, the executor re-checks the entry's identity (device, inode, size, modification time), that its subtree has gained no kept entry since approval (otherwise the item becomes blocked), and, for a copy removed as a duplicate, that a copy outside the plan still holds the same content. A changed entry is skipped and reported; the rest of the plan continues.
- **Mechanics.** Each operation is journaled first, then performed with a no-overwrite rename, then confirmed. After a crash, Precious reconciles the journal against both paths before doing anything else. An ambiguous item is marked for manual recovery.

### 10.3 Quarantine, restore, and purge

- **Quarantine** is a folder that Precious controls on the same filesystem as the item (on ZFS, the same dataset; on a USB stick, the stick itself), excluded from scans. Read-only media such as optical discs have none. Each item keeps a sidecar record of its origin. Quarantining frees no space.
- **Restore** puts an item back at its original path when that path is free, or at a destination the owner chooses. It never merges and never overwrites.
- **Purge** empties quarantined items permanently, only from the quarantine screen, after a separate confirmation and only after the pre-delete check (§10.6). It reports the space it freed. On ZFS, space held by snapshots returns only when they expire, and the screen says so.

### 10.4 Export and moves across filesystems

- **Export.** Any plan can be exported as CSV (path, size, operation, reason).
- **Across filesystems.** A move to another filesystem (copy, verify, then quarantine the original) is out of scope for this version.

### 10.5 Organizing

Organizing moves and renames entries to destinations chosen by the owner. It is separate from cleanup: an organizing action never quarantines, and a cleanup plan never moves anything except into and out of quarantine.

- **Expected use.** Interactive work: rescuing gems out of old folders, building a new structure, sending some entries in bulk and others one by one, renaming as they go.
- **Destinations.** Folders inside a source on the same filesystem. Moves across filesystems are out of scope (§13).
- **Safety.** Every move and rename obeys I3 (no overwrite, journal first, recovery) and I6. The owner can undo it. Collisions follow the destination filesystem's capabilities: on a case-insensitive or normalization-insensitive filesystem, `Foto.jpg` and `foto.jpg` are the same name.
- **Folder copies.** Moving the files unique to one copy of a folder into another copy is an organizing action (§11.6).
- **Execution.** Moves and renames happen immediately, like in a file manager, and go into a history where each one can be undone. A bulk action first shows the list and any conflicts, then runs once confirmed.
- **Later.** A staged mode, where the owner drafts a whole new structure and applies it at once, may come later if needed (§13).
- **Milestone.** Organizing is delivered in R3, before cleanup (R4), which reuses its executor.

### 10.6 Pre-delete check

Before a purge, Precious checks the exact set of files about to be deleted, so the owner knows precisely what will be gone for good. Classification never guarantees that nothing of value is lost. The guarantee is the deterministic layer; the classification layer only orders the owner's review.

1. **Deterministic layer (the guarantee).**
   - Every file in the purge set is hashed in full, archive members included. At this destructive step, a complete read is an accepted cost.
   - A file is **safe** only when an identical copy exists outside the purge set and outside quarantine, and that copy is re-read and verified at check time.
   - A copy on an offline source cannot be verified. The file is reported as "copy offline, unverified" and gated like a unique file, unless the owner mounts that source and runs the check again.
   - Every other file is **unique**. The check reports their exact count and bytes.
2. **Classification layer (the priority).**
   - The rules flag unique files that look personal or valuable: photos, documents, saves, credentials, databases, and mail.
   - From R6, Jev adds probabilities ("looks like personal material", "looks like system junk or an installer") to rank them.
   - Generative and image models may join later (§13).
3. **Report and gate.**
   - **Safe:** may be purged.
   - **Unique, likely junk** (rules or a high model probability): may be purged after one confirmation for the group.
   - **Unique, possibly valuable,** or uncertain: blocks the purge until the owner restores each item, moves it out through organizing, or confirms it one by one.
4. **Freshness.** The check is valid only for the set it checked. A change in quarantine or in a verified copy before the purge invalidates it.

### 10.7 Media dates and organizing by date

The goal is to organize photos and videos into folders that reflect when they were taken, such as `Fotos/{year}/{month}/`, even when file dates are wrong.

1. **Read.** A background job reads the metadata of photos and videos, read-only (I1), from the start of each file only, and caches it by file identity. Date sources, from the most to the least trusted:
   - the photo's EXIF capture date, with its time-zone offset when present;
   - the GPS time, which is UTC;
   - the creation time recorded in MP4 and MOV files;
   - a date in the file name (`IMG_20150312_…`, `IMG-20150312-WA0001`, `Screenshot_2015-03-12…`);
   - a date in a folder name (`2006/Natal`, `2005-12`);
   - the file's modification time.

   Camera make, model, and serial number are read too, so that corrections can target one camera.
2. **Detect.** Precious flags:
   - files whose modification time disagrees with the capture date;
   - implausible capture dates, such as a camera's default `2000-01-01` or a date before the camera model existed;
   - consistent offsets per camera, when GPS time or another camera at the same event disagrees;
   - media with no date metadata at all.
3. **Consolidate.** Each photo or video gets an **effective date**, recording its source and how confident it is. The owner corrects dates in bulk ("shift camera X in this folder by +1 year 3 hours", "use the file-name date", "use the folder date", "set this date for these items"). Corrections are owner decisions (I4).
4. **Write back.** The effective date lives in Precious's database. As an optional organizing action, Precious can also set the file's modification time to it. That changes no content, so hashes and duplicate detection are unaffected. It goes through the shared executor, is journaled with the previous time, and can be undone. Writing EXIF into files, or XMP sidecar files, is future work (§13).
5. **Organize by template.** An organizing action moves media into a destination template such as `Fotos/{year}/{month}/`, optionally renaming them (`{date}_{time}_{original name}`), with a preview and undo (§10.5).
   - **Collisions.** Same name and same content means a duplicate, offered for discard. Same name and different content gets a suffix. Nothing is ever overwritten.
   - **Order.** The preview recommends deduplicating first, so that copies do not land together in the same month.
   - **Event names.** How the template keeps event names such as `Natal` (in the folder or in the file name) is settled when the template is detailed.

## 11. Screens

The interface is a single-page app. Every list is sorted, filtered, and paged on the server, and long lists are virtualized. Every user-facing string goes through translation keys. English ships first, then Brazilian Portuguese. Numbers, sizes, and dates follow the chosen locale.

### 11.1 Home: "How is my disk?"

- totals: bytes, files, folders, and scan and hashing progress;
- charts of bytes by category family (§6.6), by file kind, and by year;
- the opportunity cards (§11.4) with their bytes;
- decision progress: bytes kept, discarded, in quarantine, and undecided.

### 11.2 Map: "Where is my space?"

- A treemap and a table of the same folder, side by side and synchronized. The owner drills into any folder, including inside groups and archives.
- The treemap is colored by category, file kind, triage, duplication, age, decision, or tag.
- Table columns: name, size, files, kind or category, date range, % duplicated, triage, decision. Each folder row shows its composition by family, and the share of its main family when it is mixed.
- Selecting a row opens the detail panel (§11.8).

### 11.3 Search: "Where is it?"

- Filters: name (substring), extension, file kind, size range, year range, category, tag, triage, decision, duplicate state, and path prefix.
- Results support bulk decisions. "Select all results" shows the count and bytes, and is resolved to explicit entry IDs when confirmed.

### 11.4 Opportunities: "What should I do first?"

Ranked cards, each with bytes, count, and how sure the classification is. Each card opens a focused review list:

- exact duplicate folders and files;
- archives already unpacked elsewhere;
- system junk;
- old installers, disk images, and downloads;
- application installations and operating-system copies;
- caches, temporary data, and generated artifacts;
- partial downloads, empty folders, and zero-byte files.

### 11.5 Review list

- Each row is a group or a file, with its size, dates, its suggestion with source and probability, and a one-line evidence summary built from the index, for example "PHP project, 2006, 64 files, 417 KB, contains `.git` and an SQL dump".
- Keyboard: keep, discard, later, next.
- Bulk selection with a confirmation that shows exactly what is selected.

### 11.6 Compare: "Which copy do I keep?"

- **Two folders side by side** (or a folder and an archive), in four groups: only on the left, only on the right, identical, and same name with different content.
- **Actions:** decide either side or any file with the usual decision controls (§6.7), and move the files unique to one side into the other (organizing, §10.5).
- **Duplicate file groups:** each group lists every copy with its path and decision. The owner decides the copies with the usual controls, one by one or in bulk.

### 11.7 Gems: "What is valuable?"

- Personal media and documents with no other copy in the hashed content, oldest first.
- User material inside disposable groups (rescue candidates).
- Files that exist only on one side of an `overlap` relation. Example: the one edited photo found only in `Fotos - Copia`.
- Version families (R7).
- Actions: the usual decision controls, and move to a destination (organizing, §10.5).

### 11.8 Detail panel

- Path, size, dates, and breakdowns by kind and by year.
- For a folder: its composition by family, and what is notable inside it (groups, and areas or files of another family), largest first, each a link.
- Category and triage with their source and probability, plus the rule or question behind them.
- Duplicates and relations.
- Decision controls.
- A preview of the file (image, media, PDF, or the first lines of text), and the full viewer (§11.12).
- Technical details (inode, raw name bytes, hashes) in a collapsed section.

### 11.9 Plans and quarantine

The plan list; the draft, preflight, and approve flow, with blocked items and the kept entries that block them; live execution; the journal; the quarantine browser with restore; the pre-delete check report (§10.6) and purge; and CSV export.

### 11.10 Settings

Sources (added through the picker within the allowed roots, §6.1) and their write permission, the scan schedule, jobs, the classifier providers with their spend and grants, and the language.

### 11.11 Vocabulary

The interface MUST NOT show internal terms such as atomic, expanded, descriptor, epoch, intent revision, frontier, or coverage scope. A technical detail that the owner may need lives in the collapsed details section.

### 11.12 Viewer

The owner can open files directly in the interface, including members inside archives, without extracting anything to disk.

- **Native in the browser:**
  - images: JPEG, PNG, GIF, WebP, AVIF, BMP; SVG only as an image, never as a page;
  - video: MP4 and WebM, and MOV when the browser can decode it, with seeking (HTTP range requests);
  - audio: MP3, AAC/M4A, Ogg/Opus, WAV, FLAC;
  - PDF: through the browser's built-in viewer.
- **Rendered by Precious:**
  - plain text and source code, with syntax highlighting and encoding detection (UTF-8, UTF-16, Windows-1252, ISO-8859-1);
  - Markdown, sanitized;
  - archive member listings.
- **Optional tools, detected at startup:** when installed on the server, these convert old or non-browser formats on demand.
  - `ffmpeg`: thumbnails, and AVI, WMV, 3GP, MPEG, FLV, WMA, and AMR to browser formats;
  - `libvips` or ImageMagick: HEIC, TIFF, and camera RAW;
  - LibreOffice in headless mode: DOC, XLS, PPT, DOCX, ODT, and RTF to PDF.

  Without them, the viewer says which tool would enable the preview.
- **Safety.**
  - Viewing is read-only (I1), and conversions and thumbnails are cached in the state directory with a size cap, never in a source.
  - Raw file content is served with a sandboxing Content-Security-Policy, `nosniff`, and a type chosen by Precious, never one inferred from the file. Content from the disk can never run as part of the application.
  - Executables are never run.

## 12. Architecture

- **Server.** Go, pinned by `go.mod`. SQLite through modernc.org/sqlite (WAL, versioned migrations). Durable jobs in SQLite. One binary per platform: Linux, macOS, and Windows, on amd64 and arm64.
- **Platform layer.** Everything that differs by operating system or filesystem sits behind one internal interface, so the core never assumes Linux:
  - the no-replace rename;
  - file and volume identity;
  - name encoding and comparison;
  - filesystem capability detection;
  - mount discovery;
  - read flags such as `O_NOATIME`.
- **Optional external tools.** `ffmpeg`, `libvips` or ImageMagick, LibreOffice, `pdftotext`, and `exiftool` are never required. Precious detects them at startup and uses them only to extend previews, text excerpts, and metadata reading (§9.2, §10.7, §11.12). Media metadata is read natively in Go for EXIF (JPEG, TIFF, HEIC) and MP4/MOV.
- **Front end.** React, TypeScript, and Vite. The built assets are embedded in the binary. Node is a build-time dependency only. Nothing loads from a CDN, and the app works without internet access.
- **API.** JSON over `/api`: read endpoints with server-side sort, filter, and cursor paging; command endpoints for decisions, jobs, and plans; and a server-sent event stream for progress.
- **Authentication.** One owner account with a password and server-side sessions (the existing implementation). The existing request-forgery checks stay. Plain HTTP is allowed on any listen address when configured.
- **Reuse.** These are reused and adapted: the rooted, no-follow filesystem access (`internal/fsaccess`), the job runner (`internal/jobs`), authentication (`internal/auth`), and the command dispatcher. Code that R1 does not use is removed in R1 and restored from the git tag `curator-m4b` by the milestone that needs it:
  - the hashing and archive engine and the folder relations (`internal/compare`) in R2;
  - the classifier provider layer (`internal/classify`: contract, adapters, cache, ledger, and evaluation) in R6.
- **Removal.** The directory-first machinery (atomic boundaries, frontier, shallow descriptors, per-scope reconciliation), the managed inbox, the filesystem-notification watcher, and the server-rendered pages are removed in R1. The git history keeps them.
- **Fresh baseline.** R1 starts a new database schema. Databases created by v0.2 builds are not migrated.
- **Name.** R1 renames everything from `curator` to `precious`: the binary and command (`cmd/precious`), the configuration keys and examples, the state directory, and the documentation. The Go module is already `precious`.
- **Scale targets.**
  - 2 million entries per source.
  - List endpoints answer a page of 200 rows, sorted by size, in under 300 ms at the 95th percentile.
  - The Map's treemap loads one level in under 500 ms.
  - Both response times are measured with 2 million entries on the reference server.

## 13. Out of scope

Out of scope for this version:

- generative text models (summaries, explanations, natural-language search);
- image recognition;
- continuous intake (inbox) and automatic organization of new arrivals;
- change feeds (`inotify`, `zfs diff`);
- moves across filesystems;
- automatic snapshots;
- a staged organizing mode (§10.5);
- writing dates or tags into files (EXIF) or XMP sidecar files (§10.7);
- more than one user;
- cloud storage;
- backup;
- signed installers and app-store distribution;
- any change to the disk that the owner did not explicitly request.

## 14. Milestones

Each milestone ends with a running binary, its acceptance scenarios passing, an owner smoke test in a real browser on the regression corpus, and updated operator docs. The order follows the owner's process: see everything, find copies and gems, rescue and organize, isolate and delete, organize photos by date, then refine with the model.

### R1: Full index and explorer

The new schema; full scan and rescan; folder aggregates; rules v2 (§8); the platform layer with filesystem capabilities, volume identity, and offline sources, on Linux (§6.1, §12); builds for all three platforms; the SPA shell with login, Home (without opportunities), Map, Search, and the detail panel; decisions and tags; the viewer for browser-native formats and for text, source code, and Markdown (§11.12, without optional tools); English translations; and the removals and the rename to `precious` listed in §12.

- **R1.1** A scan of the regression corpus indexes every file and folder, and every folder's total equals the sum of its files' sizes.
- **R1.2** Sizes and file counts appear for every folder in the Map, including folders the rules treat as groups.
- **R1.3** Search by name, extension, size, year, and tag finds files anywhere, including inside groups.
- **R1.4** The rules classify the corpus's system junk, installers, `Program Files` applications, `WINDOWS` copy, personal photos, documents, projects, game saves, and generated artifacts as listed in the corpus's ground truth.
- **R1.5** The office spreadsheet inside `Microsoft Office` vetoes discard for its group and is listed in the group's details.
- **R1.6** A rescan after changes in the corpus updates sizes, marks removed entries missing, and keeps every decision and tag.
- **R1.7** Deciding a folder sets the effective decision of its subtree, except entries with their own decision, and Home shows decided and undecided bytes.
- **R1.8** Names that are not valid UTF-8 round-trip losslessly, and are displayed escaped.
- **R1.9** A scan never writes to the source (verified on a read-only mount).
- **R1.10** On a synthetic tree of 2 million entries, the Map table answers a page of 200 rows and the treemap loads one level within the §12 targets.
- **R1.11** A bulk discard over a selection skips every entry whose effective decision is keep, explicit or inherited, and lists what it skipped.
- **R1.12** A tag on a folder appears on every entry inside it, marked as inherited from that folder, and a search by tag finds them.
- **R1.13** The viewer shows the corpus's JPEG, MP4, MP3, PDF, Markdown, source file, and Windows-1252 text file. An HTML or SVG file from the source never runs script in the application's origin.
- **R1.14** On the reference server, a scan of the owner's archive takes at most 1.5 times as long as a bare metadata walk of the same tree (§7).
- **R1.15** Every change builds for Linux, macOS, and Windows on amd64 and arm64; only Linux runs the acceptance suite in R1.
- **R1.16** On Linux, a source whose volume is unmounted stays browsable and searchable as offline, and is recognized when the volume is mounted again at a different path.
- **R1.17** On the FAT-capability fixture, a rescan with no changes matches every entry by path, size, and time within 2 seconds, and creates no new entries.
- **R1.18** A source can be added only through the picker, inside an allowed root. A request naming a location outside the allowed roots, or a raw path, is refused.

### R2: Duplicates, opportunities, and gems

Background hashing over the index (archives included); duplicate groups and folder relations; Opportunities; review lists; Compare; Gems without version families. Owner category overrides (§6.6), owner-marked groups (§6.5), and scheduled rescans (§7) follow in a short step after R2, before R3.

- **R2.1** Hashing finds every duplicate group in the corpus and shows hashing coverage while it runs.
- **R2.2** `Fotos - Copia` against `Fotos` shows the files unique to each side, including the edited photo found only in the copy.
- **R2.3** A zip and its unpacked folder are shown as the same.
- **R2.4** Deciding a copy from a duplicate group, Compare, a review list, or Gems uses the same decision controls as the detail panel and changes nothing on the other copies: no decision, suggestion, or tag.
- **R2.5** Each opportunity card's bytes equal the sum of its review list.
- **R2.6** Gems lists the corpus's unique personal photos and documents, and the spreadsheet inside `Microsoft Office`.
- **R2.7** "No other copy" claims state the hashed share of the content.
- **R2.8** The viewer opens a photo inside the corpus's zip without writing anything to disk.

### R3: Organizing

Write permission per source; the shared executor (journal first, no-overwrite rename, crash recovery); immediate moves and renames with an undo history; bulk moves with a preview of the list and conflicts; rescuing kept entries out of folders; moving the files unique to one copy of a folder into another copy (§10.5).

- **R3.1** A move or rename never overwrites; a destination that appears during execution stops that item.
- **R3.2** A crash between journal and rename recovers without moving anything twice; an ambiguous item is marked for manual recovery.
- **R3.3** Undo returns a moved or renamed entry to its previous path, or asks for a destination when that path is taken.
- **R3.4** A bulk move shows every entry and conflict before it runs, and moves only the entries shown.
- **R3.5** Decisions and tags follow an entry through a move or rename, and a rescan does not treat it as a new entry.
- **R3.6** Moving the files unique to `Fotos - Copia` into `Fotos` leaves `Fotos - Copia` with no file that lacks a copy in `Fotos`.
- **R3.7** No write happens on a source without write permission. Turning it on needs a confirmation, and is unavailable when the configuration forbids writes.

### R4: Cleanup

Cleanup plans (draft, preflight, approval, execution), quarantine, restore, the pre-delete check (§10.6) with its rule layer, purge, and CSV export, on the R3 executor.

- **R4.1** A discarded folder whose subtree contains a kept entry is not quarantined, in whole or in part. The plan lists it as blocked, with the kept entries that block it, and the rest of the plan proceeds.
- **R4.2** An entry that changed since the plan was drafted is skipped and reported.
- **R4.3** Restore returns an item to its original path, or asks for a destination when the path is taken.
- **R4.4** A plan cannot quarantine the last copy of a duplicate group on duplicate grounds.
- **R4.5** Purge reports the space freed, and notes that ZFS snapshots may retain it.
- **R4.6** The pre-delete check hashes every file in the purge set, archive members included. It reports as safe only files with a re-verified copy outside the purge set and quarantine, and states the exact count and bytes of unique files.
- **R4.7** Unique files that the rules flag as personal or valuable block the purge until each is restored, moved out, or confirmed one by one. Unique likely-junk files need one confirmation for their group.
- **R4.8** Any change to the purge set or to a verified copy after the check invalidates it before the purge runs.
- **R4.9** A plan exports as CSV with path, size, operation, and reason for every item.

### R5: Media dates

Media metadata reading, date detection, effective dates with bulk corrections, optional write-back of modification times, and organizing by date template (§10.7).

- **R5.1** Every photo and video in the corpus gets an effective date with its source, and the photos whose modification time disagrees with EXIF are flagged.
- **R5.2** The camera with a constant clock offset is detected, and one bulk correction fixes all its photos.
- **R5.3** WhatsApp-named images without EXIF take their date from the file name.
- **R5.4** Writing back a modification time changes no file content, and undo restores the previous time.
- **R5.5** Organizing by `Fotos/{year}/{month}/` previews every move, offers same-content collisions for discard, suffixes same-name different-content files, and overwrites nothing.

### R6: Classifier assistant (Jev)

The `typesafe` adapter on the new state (§9.2); text excerpts under their own grant; questions for category, triage, tags, destination, and deeper-analysis priority; probabilities in review lists, with sorting and bulk acceptance above a threshold; ranking in the pre-delete report; spend caps; evaluation on an owner-labeled corpus.

- **R6.1** Jev answers are stored with their probabilities and model version, and never override an owner decision or a rule.
- **R6.2** Bulk acceptance applies only to the listed items at or above the chosen threshold, shows them before confirming, and skips anything kept.
- **R6.3** An unchanged entry is never sent twice for the same question and model version.
- **R6.4** Nothing is sent for a source without a grant.
- **R6.5** Swapping Jev for a `structured_chat` profile needs configuration only.
- **R6.6** The pre-delete report ranks unique files by Jev's probabilities, and a model answer alone never moves a file into the purgeable groups when the rules flag it as personal.
- **R6.7** Without the text-excerpt grant, no request carries file content. With it, a request carries at most about 4 KB of text from one file. The preview shows the exact state for both cases.

### R7: Versions, extended viewer, and pt-BR

Version families; previews through optional tools (`ffmpeg`, `libvips` or ImageMagick, LibreOffice); thumbnails; the Brazilian Portuguese translation.

- **R7.1** The corpus's `curriculo*` and `TCC_versao_final*` files form families, showing size, date, and the differences in text between members.
- **R7.2** With `ffmpeg` installed, the corpus's AVI video plays in the viewer. Without it, the viewer names the missing tool.
- **R7.3** Thumbnails and converted previews are cached in the state directory under a size cap, never in a source.
- **R7.4** The interface is fully translated to pt-BR, with locale formatting of numbers, sizes, and dates.

### R8: Platforms and removable media

macOS and Windows completed and tested; removable and optical media; the platform-specific parts of the platform layer. This milestone comes last.

- **Testing.** The owner has no Mac. Automated tests run on hosted macOS and Windows CI runners. The manual smoke test in a real browser, with real external disks and the system's permission prompts, is done on macOS by an outside tester and on Windows by the owner or an outside tester.

- **R8.1** The acceptance scenarios of R1 to R7 pass on macOS (APFS) and Windows (NTFS), except R1.14, which is measured on the reference server.
- **R8.2** Moves use each platform's no-replace primitive. On a case-insensitive filesystem, a destination that differs only in case is a collision.
- **R8.3** A FAT32 or exFAT USB stick plugged in again under a different mount point or drive letter is recognized as the same source, and its rescan matches entries without stable file identity.
- **R8.4** An optical disc is a read-only source. It is never a destination or a cleanup target, and its files count as copies, verifiable only while it is mounted.
- **R8.5** Windows paths longer than 260 characters and reserved names (`CON`, `NUL`) are indexed and displayed losslessly.
- **R8.6** On macOS, a folder that the system's privacy permissions block is shown as unreadable, with a hint to grant access.
- **R8.7** Plugging in a removable volume offers to add it as a source, or brings an existing offline source back online.

## 15. Regression corpus

A generator in the repository builds a synthetic "messy disk" with a ground-truth file. It includes at least:

- a copied `C:` with `Arquivos de programas` (several applications, and an office spreadsheet inside `Microsoft Office`), `WINDOWS/system32`, and `Documents and Settings` (with a browser profile, a messenger history, and temporary files);
- a second, partial copy of that backup with one edited document;
- `Fotos` and `Fotos - Copia`, which differ in a few files, including one edited photo only in the copy;
- a zip of one year of photos, and its unpacked folder;
- a `Downloads` folder with 2000s installers, a zip and its unpacked application, two identical `Setup.exe` files, a partial download, and MP3s also found in `Musicas`;
- documents with version families (`curriculo*`, `TCC_versao_final*`), tax returns by year, and a credentials file;
- source projects with `.git`, a copy of one project with one changed file, Java `bin` output, and `node_modules`;
- a game with saves, disk images and a copy of one, `temp` with recovery `*.CHK` files, `RECYCLER`, `System Volume Information`, and `Thumbs.db`;
- a phone backup with unique photos and contacts;
- photos whose modification time disagrees with their EXIF date, a camera with a constant clock offset, WhatsApp-named images without EXIF, MP4 and AVI videos, a PDF, a Markdown file, source files, and a Windows-1252 text file;
- names that are not valid UTF-8, and an unreadable folder;
- names that differ only in case, and a fixture filesystem that reports FAT capabilities (no stable identity, 2-second local-time stamps).

CI uses the corpus and never private files or paid model calls.

## 16. Kept, changed, and dropped from v0.2

| v0.2 | v0.3 |
|---|---|
| Directory first; atomic nodes have no indexed descendants (§3.1-2) | Dropped. Full metadata index; groups are a review aid (ADR 0008) |
| Read-only observation, plans-only mutation, no overwrite, journal and recovery | Kept (I1-I3); organizing moves use the same executor (§10.5) |
| Owner intent survives; late results cannot win | Kept (I4, I9), with simpler per-entry change checks instead of epochs and revision families |
| Dispositions (unreviewed, preserve, cleanup_candidate, review) and path protection pins | Replaced by undecided, keep, discard, later, inherited down the tree; keep is the protection (§6.7) |
| Provider-neutral classifier, Jev and structured_chat, cost and evaluation | Kept and repurposed: Jev classifies and routes deeper analysis over the full index (§9) |
| Managed inbox (M3a) | Dropped for now (§13) |
| Notifications and per-scope reconciliation (M2a) | Replaced by full rescans; change feeds later |
| Copy search with its own snapshot (M4, M4b) | Engine kept; results computed over the index; no separate snapshot |
| Server-rendered pages, no SPA | Replaced by an embedded React SPA |
| HTTPS and proxy requirements for remote use | Moved to deployment; plain HTTP allowed on a local network |
| Quarantine, restore, and purge (M5, M5b) | Kept, in R4, on the executor built for organizing in R3 |
