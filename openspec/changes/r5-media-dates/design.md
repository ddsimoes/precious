# Design

## Context

See proposal.md for why. What R5 builds on:
- **Hashing (R2).** A per-source `ClassBulk` job with a plan pass (`file_content` rows inserted in ID windows), identity-checked opens (`content.OpenAt` and its unexported folder `chain`), bounded `ReadAt`, `Yield` between units, and batched commits that re-check the `entries` identity (I9). Rescans drop `file_content` and `archives` in the writer's transaction (`stDropContent`); `index.MoveEntry` keeps them (`keepContent`).
- **The executor (R3, R4).** Journaled steps (intent → step → sync → confirm → outcome), `executor.Index` for the index side, `Writer` methods only it may call (guard test), plan commands in `organize`, undo by reverse items, History, and CSV export.
- **The index.** `entries.mtime_ns` is both a file's identity and the input of folder aggregates (`newest_ns`, `oldest_ns`, `dir_stats.by_year`), Search's year filter, and the Map's age. `index.intentCond` decides which missing rows carry owner intent (`MissingIntentAt`, `IntentBelow`, `freePath`).
- **Zones.** `cmd/precious` already embeds `time/tzdata` (serve.go's blank import), so zone names resolve on hosts without zone files.

What R5 has to add:
- **Parsing.** No EXIF or ISO-BMFF parser exists, and `go.mod` holds no media library.
- **Writing a time.** `fsaccess.Writer` cannot set a time; `actions.kind` and `action_items.op` are CHECK lists (0007), so a new kind or op rebuilds both tables. The shipped deployments run as a service account without `CAP_FOWNER`, which `utimensat` needs on files it does not own.
- **The corpus.** Its JPEGs carry no EXIF, its only MP4 is an ffmpeg test clip with no creation time, and no fixture disagrees with its own date.

## Goals / Non-Goals

**Goals:** close R5.1–R5.5 on Linux without weakening I1–I9. Precious never invents a date finer than its evidence, never loses a correction, and changes nothing on a disk but what the owner ran.

**Non-Goals:**
- `exiftool` (no milestone yet), the camera-release check (ADR 0012), writing EXIF or XMP (§13).
- Metadata of formats without a parser here (MKV, WebM, WMV, MPEG, MTS, AVI, PNG, GIF, WebP, …): they get name, folder, and modification-time dates.
- The effective date in Search, folder aggregates, or the Map (D20).
- Removing folders a date organize empties (§13: no change the owner did not request).

## Decisions

### D1. Native, bounded header parsers in `internal/media` (§10.7.1, §12; ADR 0012)

`internal/media` parses, with the standard library only:
- **EXIF** fields: `DateTimeOriginal`, else `DateTimeDigitized`, else IFD0 `DateTime`; `OffsetTimeOriginal`; `SubSecTimeOriginal`; `GPSDateStamp` + `GPSTimeStamp` (UTC); `Make`, `Model`; `BodySerialNumber`, else `CameraSerialNumber`. No maker notes, no orientation. They are read from:
  - **JPEG**: APP1 `Exif\0\0`;
  - **TIFF and TIFF-based RAW** (`tif`, `tiff`, `cr2`, `nef`, `arw`, `dng`, `pef`, `srw`, and `orf` and `rw2` with their own header magics `IIRO`/`MMOR` and `IIU\0`): IFD0 and its Exif and GPS IFDs, with the same TIFF parser;
  - **ISO-BMFF images** (`heic`, `heif`, `avif`): `meta` → `iinf`/`iloc` → the `Exif` item, parsed as TIFF;
  - **CR3**: `moov` → Canon's `uuid` box (`85c0b687-820f-11e0-8111-f4ce462b6a48`) → `CMT1` (IFD0), `CMT2` (Exif IFD), and `CMT4` (GPS IFD), each parsed as TIFF.
- **ISO-BMFF video** (`mp4`, `m4v`, `mov`, `3gp`, `3g2`; and CR3): `moov/mvhd` `creation_time` (1904 epoch, read as UTC; 0 means absent).

**Bounds.** The first `ReadAt` takes up to 256 KiB. ISO-BMFF boxes are walked by their 8- or 16-byte headers with `ReadAt` wherever they sit, skipping `mdat` by size; inside `moov`, only child headers, `mvhd`, and CR3's `CMT` boxes are read. At most 1 MiB is read per file, at most 1,024 boxes or IFD entries, depth at most 8, and every offset is checked against the file's size. A malformed structure yields no value. `Read` turns a panic into "no metadata" and logs it; fuzz targets call the inner parsers without that net, so a panic fails them.

This is the reading of §10.7.1's "from the start of each file only": header structures, never the media payload, even when a MOV's `moov` sits at the end (ADR 0012).

Rejected alternatives:
- **A third-party EXIF or MP4 module.** It adds a dependency surface for untrusted input that §12 does not need.
- **Only the first window.** It misses every MOV and MP4 whose `moov` follows `mdat`.
- **`exiftool` now.** It is optional (§12), and no milestone has taken it on; native reading covers R5.1–R5.5.
- **AVI `IDIT`.** §10.7.1 and §12 do not list AVI, and the corpus AVI is random bytes.

### D2. Every photo and video is media; one predicate (§10.7.3)

A media file is a present regular file, outside the quarantine, whose `file_kind` is `image` or `video` (the policy's tables, §6.2). Archive members are not media. One SQL fragment, `dates.MediaCond(alias)`, says so, and every reader, the plan pass, `Rederive`, `ExpandTargets`, and the entry-dates read use it. `media.IsMediaKind` is its Go twin, and a test checks they agree.

`media.FormatOf(ext)` says which media files are read (D1). Every other media file (MKV, WebM, WMV, MPEG, MTS, M2TS, VOB, AVI, PNG, GIF, WebP, BMP, PSD, SVG, ICO, generic `raw`, …) is `none` in `media_meta` and dated by name, folder, or modification time. `pef` and `srw` are read once the policy's tables call them images; R5 does not change the rules.

Rejected alternatives:
- **A list of 15 extensions (the earlier draft).** RAW and camcorder files would get no date, and a date organize would split `IMG_0001.JPG` from its `IMG_0001.CR2`.
- **Excluding icons and drawings by name.** One more list to keep; an icon's date is its folder's or its modification time, which is harmless.

### D3. Metadata is cached by identity, like digests (I9)

`media_meta` has one row per media file, keyed by `entry_id`, with the identity of the `entries` row the read started from, as `file_content` has:
- **Rescans** drop it in the writer's transaction when a file's facts change (a new `stDropMedia` beside `stDropContent`).
- **Moves by Precious** keep it (`keepContent` adds `media_meta` to its tables).
- **A written time** carries it to the new times (D15).
- **The read pass** loads each pending row with its `entries` identity (path, size, `mtime_ns`, `ctime_ns`, `ino`, `dev`), opens the file only when the disk matches that row under `content.Matches`' tolerance, and restats the open file the same way after the read.
- **The commit** applies a read only while the entry is `present` and its `entries` row still equals, exactly, the identity loaded at the read's start, and the `media_meta` row is still `pending`. It writes that loaded identity into the row. So a FAT source, where a scan keeps an index time within tolerance of the disk's, is not dropped forever.

States: `pending`; `read` (whatever it found); `none` (a format D1 does not read); `unreadable` (the open was refused). A file found changed stays `pending` and counts as `changed` in progress; the next scan resets it.

Rejected: rows that carry their own identity and are re-validated on each read. Every reader would need the same check, and a moved file would read as changed.

### D4. One `media` job per source, and no lost request (§10.7.1)

Kind `media`, `ClassBulk`, bound to its source. It is requested through `dates.EnqueueMedia`:
- after each scan of that source (`scanner.OnScanDone`);
- at startup for every source;
- from `ActionDone`, next to hashing, so moves and written times re-derive;
- by correction commands (D11).

Offline and unavailable sources are requested too. Their job skips passes 1 and 2, since passes 3 and 4 need only the index (§6.1).

**No lost request: the relations pattern (r4 Addendum G1), plus mutual exclusion.**
- **A request** (`EnqueueMedia`, in the requester's transaction) sets `media_sources.dirty = 1` for the source. It then calls `EnqueueOnce` with scope `media:<source>` and payload `{"if_dirty":true}`. When the job returned is `running`, it also calls `EnqueueOnce` with scope `media-next:<source>` and the same payload, as `relations.enqueueRelate` does.
- **The start**, one write transaction, in this order:
  1. while `media_sources.passes_job` names another media job whose state is `running`, return `jobs.Defer{Until: now + 3 s, Reason: "media_running"}` (a deferral uses no attempt);
  2. on a first attempt (`job.Attempt ≤ 1`) with `if_dirty`, return having run nothing when `dirty` is 0: a loop that began after the last request already covered it; a retry (`Attempt > 1`, after a lost worker or lease) ignores `if_dirty`;
  3. set `passes_job` to its own ID (a stale ID, of a job no longer running, is overwritten).

  `passes_job` exists because a runner marks a deferred or yielding job `running` too: the job state alone cannot tell which of two media jobs runs the passes. The single writer serializes step 1 and step 3, so two handlers never both pass, and nothing is read by two jobs.
- **The loop.** Each iteration clears `dirty` in a write transaction, runs passes 1–4, and reads `dirty` again in a write transaction: set means a request came during the passes, so it loops; clear means it releases `passes_job` in that same transaction and returns.
- **Failure.** On any error, a cancel included, the handler sets `dirty = 1` and releases `passes_job` in one write transaction under `context.WithoutCancel`, then returns the error. The request it was serving is then served by the follow-up, the next request, or the next start.
- **Pausing.** The handler never returns `jobs.Pause`; a job paused by hand is resumed by the next request's `EnqueueOnce` (`jobs.Tx.once`).

The start also defers 3 s (reason `organizing`), before step 3, while `executor.OrganizeActive` holds for its source. That function is injected by `DeferWhile`, as for the scanner, since `executor` imports `content`. The passes:
1. **Plan.** Insert `media_meta` rows (`pending`, or `none` for formats D1 does not read) for media files (`MediaCond`) without one, in ID windows of 50,000, one write per window.
2. **Read.** Open each `pending` row of a media file (`MediaCond`, so never in the quarantine) through `content.Opener`, the exported folder chain, read (D1), restat, and commit up to 64 results per write with D3's re-check. It yields between files.
3. **Dates.** `Rederive` (D9) over every media entry of the source in ID windows of 256, each window derived inside its write transaction. The last window rewrites the source's summary (D10).
4. **Cameras.** Detection (D8), written in one transaction at the end of the pass, always. A request during the pass makes the loop run again, and that run's write replaces this one.

Progress: `phase` (1–4), `files`/`of_files` read, `bytes`, `changed`, `unreadable`, and `media`/`of_media` derived.

Rejected alternatives:
- **Folding it into hashing.** Hashing reads only files that share a size, and its plan is global.
- **`ClassInteractive`.** It reads every photo on a first run, which is background work.
- **A generation counter with a claim (the earlier revision).** A claimer that failed, was cancelled, or ran out of attempts never ran its end check, and every request it had absorbed was lost (review).
- **Two scopes without mutual exclusion (relations' exact pattern).** Relations runs in a pool of capacity 1; media jobs are device jobs, so a queued follow-up takes the device at the first `Yield` and both read the same rows.
- **Writing the cameras only when no request came (the earlier revision).** Steady moves and scans would keep the cameras from ever being written.

### D5. The effective date: precedence and plausibility (§10.7.1, §10.7.3)

Candidates, most trusted first:

| Source | Date | Precision | Confidence |
|---|---|---|---|
| `owner` | the correction (D11) | as set | `high` (set), `medium` (shift) |
| `exif` | capture with offset: an instant; without: a wall time in the zone (D7) | second | `high` with offset, else `medium` |
| `gps` | GPS date and time, UTC | second | `high` |
| `container` | MP4/MOV creation time (UTC) | second | `medium` |
| `file_name` | D6 patterns | as the pattern | `low` (`medium` when refined) |
| `folder_name` | nearest dated ancestor folder, D6 | year, month, or day | `low` (`medium` when refined) |
| `mtime` | the modification time, when known (`domain.KnownModTime`) | second | `lowest` |

- **The owner's date always wins.** It is not tested for plausibility; `set-date-correction` validates it instead (D11).
- **Otherwise the first plausible candidate wins.** A candidate is implausible when it falls before 1990-01-01, after the read time plus one day, or exactly at `1970-01-01`, `1980-01-01`, `2000-01-01`, or `2001-01-01 00:00:00` (camera defaults). An implausible candidate is skipped, and the next one wins.
- **No candidate.** The source is `none`, with no date and confidence `none`. This happens only for an unknown modification time with nothing else.
- **What each date stores.**
  - `instant`, for the start of the period when coarser than a second.
  - `local`, the wall time in the capture's own offset when known, else in the zone: `YYYY`, `YYYY-MM`, `YYYY-MM-DD`, or `YYYY-MM-DDTHH:MM:SS`.
  - `offset_min`, when known.

Templates and renames use `local`; write-back uses `instant`. GPS ranks below an EXIF capture because it may come from a stale fix. An EXIF date with an offset is as good as GPS, and is the camera's own.

### D6. Name and folder dates: a finite list, with refinement (§10.7.1)

**File-name patterns** (at the start of the name, case-insensitive prefix; the date must be valid):

| Pattern | Precision | Kind |
|---|---|---|
| `IMG_YYYYMMDD_HHMMSS`, `VID_YYYYMMDD_HHMMSS` | second | wall time |
| `PXL_YYYYMMDD_HHMMSSmmm` | second | UTC instant |
| `YYYYMMDD_HHMMSS` | second | wall time |
| `YYYY-MM-DD HH.MM.SS` | second | wall time |
| `Screenshot_YYYY-MM-DD-HH-MM-SS`, `Screenshot_YYYYMMDD-HHMMSS` | second | wall time |
| `Screenshot_YYYY-MM-DD` | day | wall time |
| `IMG-YYYYMMDD-WA`, `VID-YYYYMMDD-WA` | day | wall time |

**Folder names** count when the name is, or starts with, `YYYY`, `YYYY-MM`, or `YYYY-MM-DD`, followed by the end, a space, `-`, `_`, or `.`, with a year from 1990 to the read year. The nearest such ancestor below the source's top wins. `fotos_2005_do_pendrive` and `celular_backup_2009` carry no folder date.

**Refinement.** When a `file_name` or `folder_name` candidate coarser than a second contains the modification time (in the zone, within the filesystem's local-time tolerance), the effective date takes the modification time. It keeps the name or folder as its `source`, with `refined: true`, precision `second`, and confidence `medium`. Otherwise the coarse date wins, with its own precision. Nothing finer is ever invented: a template or rename that needs more is refused `date_too_coarse`.

Rejected alternatives:
- **Source `mtime` for a refined date (the coordinator's draft).** WhatsApp images modified on their name's day would read "date from the modification time", and R5.3 asks for the name. Refinement is recorded instead.
- **A year found anywhere in a name.** `fotos_2005_do_pendrive` and `Backup_PC_2004` would date photos by when they were copied.

### D7. One configured time zone (§10.7.1)

- `[dates] time_zone` is an IANA name, resolved through the zone database `cmd/precious` already embeds. Absent or empty, it is the server's local zone (`time.Local`), and `check-config` and the server's start log warn that it is unset. The shipped container's local zone is UTC, so the example configurations set it.
- It turns wall times into instants (EXIF without an offset, name and folder dates), and instants into the `local` of GPS, container, and `mtime` dates.
- Nonexistent and ambiguous wall times follow Go's `time.Date`.
- `media.ZoneKey(loc)` identifies the resolved zone: its name, plus its UTC offsets on January 1 and July 1 of every year from 1990 to 2040. A changed host zone under the default "Local" therefore changes the key, and every date is re-derived at the next job (D9).
- **FAT sources.** Their modification times are what the mount's `tz=`/`time_offset` makes of the stored wall times. When the mount and `time_zone` disagree, those times shift; flags tolerate ±1 h only, and the operator guide says to mount FAT sources in the configured zone.

Rejected alternatives:
- **A zone per source, or per camera.** No R5 scenario needs it, and a correction covers a trip abroad.
- **The browser's zone.** Stored dates would differ by viewer.
- **Keying on the zone's name.** The default's name is always "Local".

### D8. Flags and camera offsets (§10.7.2, R5.2)

**Flags** (`media_dates.flags` bitmask):
- `1` `mtime_disagrees`: the effective source is `exif`, `gps`, `container`, or `owner`, and the date is more than 24 h from a known modification time (plus 1 h on a `LocalTime` filesystem).
- `2` `implausible`: an `exif`, `gps`, or `container` candidate was implausible (D5). Name, folder, and modification-time candidates never set it.
- `4` `camera_offset`: set by detection.
- `8` `no_date_metadata`: the metadata state is `read` or `none`, and no capture, GPS, or container date was found, plausible or not. A `pending` or `unreadable` file never has it; lists show its metadata state instead (I7).

"A date before the camera model existed" is not flagged (ADR 0012).

**Camera key.** `make|model|serial`: each part trimmed of spaces and NULs, with `|` in a part replaced by `/`. An empty serial leaves a trailing `|`, as in `SONY|DSC-W55|`.

**Detection** (pure `media.Detect`, run by pass 4):
- **Input.** Each photo with a plausible EXIF capture and a camera key gives its capture instant, its folder, and its GPS time if any. A `shift` correction is applied; any other correction leaves the photo out.
- **Events.** An event is a folder whose direct children include photos of at least 2 cameras.
- **Candidates.** Camera C is a candidate in event F when C's photos in F span less than a day, and every one of them lies outside the other cameras' range in F, `[min − 6 h, max + 6 h]`. Its `delta = median(C in F) − median(other cameras in F)`.
- **References.** In each such event, a reference sides with the other cameras:
  - `gps`: the GPS times of their photos in F agree with their own captures within 10 min;
  - `folder_name`: F's folder date (D6) contains every capture of the other cameras in F and none of C's.
- **Own GPS.** C's own GPS gives a candidate only when at least 3 of its photos have GPS, their `capture − GPS` all agree within 10 min, and that difference is beyond 1 h with a known offset, or beyond 14 h without one (a whole time zone cannot explain it). Its reference is `own_gps`. Scattered differences, as a stale fix gives, are ignored.
- **Offset, decided first.** C is `offset` when its candidates all lie within 10 min of each other, and a reference sides with the other cameras in at least one of its events: with candidates in one event, that reference must be `gps` or `own_gps`; with candidates in two or more events, `folder_name` also counts. Every reference-backed `offset` is decided before anything else. The suggestion is `−delta` rounded to the minute. Its events are the folders of those candidates; `camera_offset` is set on C's photos directly in those folders, and the suggestion targets exactly them (D11's `{folder_ids, camera_key}`).
- **Counterparts, then.** The other cameras' candidates are computed again without the `offset` cameras; a camera left alone in an event has no candidate there.
- **No reference.** Cameras with candidates left that are not `offset` are `disagrees`, with no suggestion. That includes both cameras of a 2-camera tie, and both cameras of two or more events with no reference: a pairing is symmetric, so repeated events alone cannot say which clock is wrong.
- **The rest** are `ok`.
- **Blind spot.** An offset of 6 h or less between cameras (daylight saving, a home zone kept abroad) is not detected; the owner shifts such photos by hand. The operator guide says so.

In the corpus, the Sony has candidates in 2 events, and in each the Canon's GPS agrees with the Canon's captures (`gps`), so the Sony is `offset`. The Canon has candidates against the Sony in both events too, but no reference sides with the Sony: it has no GPS, the Canon's own GPS agrees with its captures, and each event's month holds the Canon's captures, not the Sony's. So the Canon is not `offset`; with the Sony set aside it is alone in both events, has no candidate left, and is `ok`.

Rejected alternatives:
- **Suggesting without a reference, from one event or several (the earlier revisions).** A pairing is symmetric: the camera that is right is a candidate as well, so both would be flagged.
- **A median gap alone, over a whole subtree (the earlier draft).** A camera used on one day of a long trip, or photos on the 1st of the next month, looked offset, and the shift covered photos never shown to be offset.
- **A majority of 3 or more cameras as a reference.** No scenario needs it, and it adds false positives.
- **Calendar shifts.** A clock error is a duration. A shift is stored in seconds, and the interface shows years of 365 days and days of 24 h, so "+1 year 3 hours" is 31,546,800 s.

### D9. Materialized effective dates, derived in the writing transaction

`media_dates` holds one row per media file, with:
- the effective date, its source, precision, confidence, refinement, correction kind, and flags;
- the metadata state and `camera_key`;
- `inputs_key`: an FNV-64a of the path, `mtime_ns`, the `media_meta` row and state, the correction, `media.ZoneKey`, and `media.DeriveVersion`.

`(*dates.Service).Rederive(ctx, tx, ids)` reads those inputs inside the caller's transaction, calls the pure `media.Derive`, and writes only rows whose key changed. It keeps the `camera_offset` bit, except for a `set` or `shift` correction, which clears it. It deletes the rows of entries that `MediaCond` no longer holds (missing, quarantined, no longer media). It adjusts the source's summary by the rows it changed (D10).

Pass 3 calls it over the whole source, so moves (the path changes name and folder dates) and written times are picked up after `ActionDone`. Correction commands call it for their targets, and both plan commands for theirs (D14, D16). `GET /api/entries/{id}/dates` derives on read for the candidates list, which is not stored.

Rejected alternatives:
- **Deriving on read.** Filtering, sorting, and paging 2 million entries by date or flag needs stored, indexed values.
- **SQL triggers.** The derivation is Go, and triggers would hide writes from the executor's outcome transaction.

### D10. The reads: their place, indexes, and summary (§11, §12)

Reads live in `internal/dates` and are routed by `(*dates.Service).Routes`, as organize and cleanup do. The entry's dates are a sub-resource, `GET /api/entries/{id}/dates`, so `web/api` and its `Register` signature stay as they are. List rows carry their own entry fields, not `web/api`'s `EntryRow`.

Every read joins `entries` and applies `MediaCond`, so quarantined, missing, and non-media rows are left out at once, before `Rederive` deletes them. A quarantined entry's dates answer `null`.

**The list** needs `source` and answers 200 rows to a page:
- by date (`effective_ns`, then entry ID), through `media_dates_by_time`;
- with a `flag`, through that flag's partial index (`flags & k <> 0`);
- with `date_source`, through `media_dates_by_source`;
- with `camera`, through `media_dates_by_camera`;
- with `within` (a folder), in path order instead, through `entries`' `UNIQUE (source_id, path)` range, with the other filters residual.

With several filters, `within` decides, else `camera`, else `flag`, else `date_source`; the rest are residual. The plan guard (task 2.6) runs `EXPLAIN QUERY PLAN` on analyzed data for each filter and asserts the expected index and no `USE TEMP B-TREE`.

**The summary** is materialized in `media_sources.summary`. Pass 3's last window rewrites it, and every `Rederive` adjusts it in its own transaction. Between a scan's deletions and the next pass it may lag, as the list does. Without `source`, it sums the sources' rows.

Rejected alternatives:
- **A `dates` field on `GET /api/entries/{id}` (the coordinator's draft).** `web/api` would need the zone and the derivation, a shared signature change, and the panel's other sections would wait for it.
- **Aggregating on read.** At 2 million entries it misses the §12 targets.
- **A list across all sources.** It cannot be served by one index.

### D11. Corrections are owner decisions (§10.7.3, I4)

`date_corrections` holds one row per entry, cascading with the entry, so a correction survives rescans, moves, and a return from missing. No job writes it. `index.intentCond` counts a correction as owner intent, so a missing row with one is never deleted to free its path (`freePath`), and `MissingIntentAt` and `IntentBelow` see it.

**Targets.** IDs are strings, parsed with `domain.ParseRef`. Exactly one of:
- `entry_id` (single);
- `entry_ids` (1–1,000);
- `folder_ids` (1–100 folders of one source), optionally with `camera_key`.

Folders without `camera_key` expand to the media files at or below them. With `camera_key`, they expand to that camera's photos directly in them, as D8's events count them. More than 50,000 media is `400 invalid_request`. A target that is itself quarantined is `409 in_quarantine` (r4 D13). A member ref is `400` single, and `not_media` in bulk.

**Kinds.**
- `set`, with `local` as `YYYY`, `YYYY-MM`, `YYYY-MM-DD`, or `YYYY-MM-DDTHH:MM:SS`, stored at that precision, and an optional `offset_min` with a time. A date after now plus one day is `400 invalid_request`; any earlier date is the owner's to set.
- `shift`, with `shift_s` within ±50 years, applied to the EXIF capture, or to the effective date when there is none. A target it would move past now plus one day is skipped `in_future`.
- `use_name` and `use_folder`, which force that candidate. They keep its source and confidence and add `corrected`.

**Refusals.** Single: `409 invalid_entry_state` for a target that is not media, has no name date, has no folder date, or would land in the future. Bulk: such targets are skipped with reasons `not_media`, `no_name_date`, `no_folder_date`, and `in_future`.

**One transaction.** Writing rows, `Rederive` of the targets, `EnqueueMedia`, and the audit event commit together.

Keep is not consulted: corrections change no file (I5 protects removal and decisions).

Rejected alternatives:
- **Storing a correction in `media_meta`.** A rescan drops that row (D3), and I4 forbids losing it.
- **Selections (`select-dates`, "Select all results").** Per-page `entry_ids` and camera targets cover R5.2 and R5.4; selections can come later without a contract change.

### D12. `Writer.SetModTime` (I2, I3, §10.7.4)

- **Linux.** `utimensat(dirfd, name, {UTIME_OMIT, t}, AT_SYMLINK_NOFOLLOW)` through the folder's descriptor, inside `rc.Control` with `retryEINTR`. No file is opened, the access time stays, and the change time becomes the system's.
- **Ownership.** An explicit time needs the file's owner, or `CAP_FOWNER`; write permission is not enough. The shipped systemd unit and Compose file run as a service account with no capabilities, so the operator guide's "Allowing changes in the deployment" says the files to date must be owned by that account, or the deployment opts in to `CAP_FOWNER`, with its risk: the service can then change the times and modes of any file it can reach. A `chown` advances every file's change time, which D13's step compares, so the guide says to rescan the source after changing ownership and before setting file dates (r4 G19's lesson).
- **Errors.** `ENOENT` is absent, `EPERM`/`EACCES` `ErrPermission`, `EROFS` `ErrReadOnly`, and anything else unavailable.
- **Other backends.** synthfs sets the time truncated to its resolution (stored as local time on a `LocalTime` device), sets the change time to now, and can refuse with `EPERM` for a file marked foreign (tests). The portable backend refuses with `ErrNoReplaceUnsupported`. instrument logs `OpSetModTime`.
- **The guard test.** Only the executor calls it; the fixture count rises with the method.

Rejected alternatives:
- **`futimens` on an opened handle.** It opens the file, which the read-only `File` interface does not offer.
- **`os.Chtimes` by path.** It escapes the rooted, no-follow access.
- **`CAP_FOWNER` by default.** It widens what a compromised service can change on every disk it reaches.

### D13. The `set_mtime` step (§10.7.4, R5.4)

An item has op `set_mtime`, one name, `new_mtime_ns`, and, once journaled, `prev_mtime_ns`.

**Intent** re-checks, in R3's transaction:
- writes;
- the entry is present, a regular file, and not quarantined (`refused in_quarantine`);
- `nlink ≤ 1` in the index (`refused hard_link`);
- the new time is distinguishable from the index's (`refused no_change`), meaning not `sameTime` under the source's capabilities: within the resolution, or the ±1 h of a `LocalTime` filesystem;
- for an undo item, the index's time is still the original item's `new_mtime_ns` (`changed identity_changed`).

It records `from_*` from the entry's current path, and identity (with `ctime_ns`) from the index row.

**Step.**
1. Open the folder (rooted descent) and `lstat` the name. It must be a regular file matching the identity (kind, device and inode where stable, size, modification time), whose change time is `sameTime` to the index's when both are known; else `changed`/`identity_changed`. A link count above 1 is `refused hard_link`.
2. Journal `prev_mtime_ns` from that `lstat` in a write transaction.
3. `before` hook, `SetModTime`, `after` hook, `lstat` again. No folder `Sync`: a folder's fsync does not persist a file's times, and Risks covers a power loss.
4. Same size and inode, and a time `sameTime` to the new one: done. Anything else: `manual_recovery`.

**Errors.** `ErrPermission` ends the item `failed`, reason `not_owner`, and the action goes on, with writes still on. `ErrReadOnly` ends it as R3's `failed()` does (`failed`, stopping the action). An absent name is `changed`. `ErrNoReplaceUnsupported` and other errors end it `failed`. It never ends `no_safe_rename`, and never turns writes off.

**Reconcile** from `intent` looks at `from_path`:

| Found | Outcome |
|---|---|
| identity, old time | `planned` (it runs once) |
| identity, new time | `done`, with the outcome recorded |
| absent, or anything else | `manual_recovery`, findings `{"from":"absent"|"other","to":"absent"}` |

**Outcome.** `Index.ApplyModTime` (D15) and `stale.MarkStale(src, from_path)`, in one transaction. An undo item sets the original's `reversed_by`.

The executor never reads the media tables. A correction made after planning does not change a planned action, and the preview is what runs.

Rejected alternatives:
- **Re-reading the effective date at intent.** The executor would depend on `dates`, and the owner approved the preview's times.
- **Stopping the action at the first `not_owner`.** Ownership can differ by file; the others are still done, and History lists the refused ones.
- **Taking the previous time from the index.** On FAT and exFAT a scan keeps an index time within tolerance of the disk's, so an undo would not restore what the disk held.

### D14. "Set file dates": action kind `set_mtime` (§10.7.4)

`plan-set-mtime` (organize) expands its targets (D11's forms, without `entry_id`; at most 10,000 items), calls `Rederive` on them in its transaction, then reads `media_dates`:
- **Planned.** A `set_mtime` item to `effective_ns`, truncated down to a multiple of the source's `TimeResolution` (a 2-second boundary on FAT, where offsets are whole minutes, so UTC and local truncation agree).
- **Unchanged.** Files whose time is already `sameTime` are counted in `summary.unchanged`, not listed.
- **Refused.** `not_dated_yet` (the metadata of a format D1 reads is still `pending`), `date_too_coarse` (precision not `second`, or source `none`), `hard_link`, and `not_media` (an explicit file target). A quarantined target fails the plan with `409 in_quarantine` (`ExpandTargets`, r4 D13).

It is always bulk, so it is always previewed, and it needs writes (`CheckWrites`). Keep does not block it: no decision or place changes.

`plan-undo` reverses its done items with a `set_mtime` item each, to the original item's `prev_mtime_ns`. An item whose index time is no longer the original's `new_mtime_ns` is refused `identity_changed` at planning, and again at intent. `reversible()` and `json.go`'s undo counts add `op = 'set_mtime'`.

### D15. The index follows a written time

`index.ApplyModTime(ctx, tx, index.ModTime{Source, Entry, Facts})`:
- writes the entry's `mtime_ns`, `ctime_ns`, `dev`, and `ino`, and its own `newest_ns`/`oldest_ns`, which are NULL for an unknown time;
- updates `file_content`, `archives`, and `media_meta` rows that describe the file as the index stored it before (size, `mtime_ns`, `ctime_ns`, `ino` equal), giving them the new times, so nothing is hashed, listed, or read again.

That carry is safe only because the step matched the disk's change time to the index's (D13): a file edited in place since the scan, even with its modification time put back, is `changed` and carries nothing. The organize adapter then calls `Refold([entry])`, so the parents' newest, oldest, and by-year figures follow (§6.3). A folder's own times do not change when a child's time is set.

### D16. "Organize by date": action kind `date_organize` (§10.7.5, R5.5)

`plan-date-organize {targets, destination_id, template?, rename?}` (organize) expands its targets, calls `Rederive` on them in its transaction, then plans:

**The template.** 1–4 `/`-separated components of literal text and the tokens `{year}` (4 digits), `{month}` and `{day}` (2 digits), and `{event}`. The default is `{year}/{month}`. No `/`, NUL, braces outside tokens, `.`, or `..` component is allowed, and each component must have a token or text. The needed precision is the finest token's.

**`{event}`.** The file's current parent folder name, with a leading folder date (D6) and the separators after it stripped. When it is empty, the token and the literal text just before it in its component go; an empty component goes too. This settles §10.7.5's event names in the folder.

**Rename.** It is fixed as `YYYYMMDD_HHMMSS_<name>` from `local`. A name already starting with that prefix is kept.

**Items.**
- **Order.** Files are processed in path order.
- **Folders.** They resolve below the destination with `merge.go`'s resolver: an existing folder is reused under the source's case and normalization rules; a missing one is a planned `mkdir`, once; a non-folder or missing-with-intent entry in the way makes the items `conflict` (`name_taken`, `name_taken_by_missing`). `dropUnusedMkdirs` runs at the end.
- **Moves.** One `rename` per file, with `p.move` and its bulk rules (`would_lose_keep`).
- **Refused.** `not_dated_yet` (as in D14), `already_there` (same folder and name), `date_too_coarse`, and `invalid_name` (a name the filesystem cannot hold, or over 255 bytes). A quarantined target fails the plan with `409 in_quarantine`, as in D14.
- **Siblings.** `summary.split_siblings` counts planned files with a same-stem sibling in their folder (same name up to the last `.`, compared under the source's rules) that is not planned into the same destination folder, such as a `.CR2` with no date or a `.THM`. The preview warns, naming them through the items' `detail`.
- **Cap.** 10,000 items, folders included; over it, `400 invalid_request` asks for a narrower scope.

**Undo** is R3's: renames back, then each created folder removed if empty. Folders a run empties stay.

Rejected alternatives:
- **Configurable rename patterns.** §10.7.5 names one.
- **Removing emptied folders.** That is a change the owner did not ask for (§13).
- **Moving siblings along by stem.** A sibling's own date may differ; the owner decides, warned.

### D17. Collisions never overwrite (§10.7.5, I3)

A name in a destination folder is taken by:
- a present child, compared under the source's rules;
- an earlier item of the plan;
- a missing entry with owner intent (`MissingIntentAt`, which counts corrections, D11).

When a present child or earlier item takes it, and both files are `hashed` with the same `content_id`, the item is `refused identical_copy`, with `copy_of` naming that entry. Otherwise the file takes the first free `stem (k)ext`, for k from 1 to 999. The extension starts at the last `.` that is not the first character, and the name is checked against the same three sets. Past 999 it is `conflict name_taken`.

The plan's `summary.files_with_copies` counts planned files with a hashed copy among the targets or at or below the destination. The preview then recommends deduplicating first, linking to the duplicates list. "Discard these copies" sends `set-decision` with `discard` for the refused items' entries, 1,000 per request, after a confirmation. RENAME_NOREPLACE stays the guarantee: a name taken after the preview ends the item `conflict` (R3).

Rejected alternatives:
- **Suffixing identical copies too.** Copies would land side by side, which §10.7.5 asks to avoid.
- **Suffixing the first file in path order.** The first file keeps its name, which is deterministic and stable across previews.

### D18. Migration 0008 (§10.7)

Interfaces lists it in full. `actions` and `action_items` are rebuilt as 0007 did. Five tables are added. Hashes and duplicates are unaffected.

### D19. Corpus fixtures and an independent truth (§15)

New fixtures live in new top-level folders, so `Fotos`, `Fotos - Copia`, `Midia`, and the pendrive copies keep their bytes, order, and relations. The corpus gains an EXIF writer (APP1 spliced after SOI: IFD0 `Make`/`Model`/`DateTime`, the Exif IFD with `DateTimeOriginal`, `OffsetTimeOriginal`, `SubSecTimeOriginal`, `BodySerialNumber`, and a GPS IFD) and an MP4 writer (`ftyp` + `moov/mvhd`). Every true time is a wall time read in UTC, since the tests' zone is `UTC`.

| Path | Content | Exercises |
|---|---|---|
| `Viagens/2010-07 Bahia/IMG_0101…0104.JPG` | Canon (`Canon`, `Canon PowerShot SX230 HS`), 2010-07-17 10:00, 11:00, 12:00, 13:00; 0101 and 0102 with GPS equal to the capture; mtime = capture | the right clock; GPS reference |
| `Viagens/2010-07 Bahia/DSC00301…00306.JPG` | Sony (`SONY`, `DSC-W55`), true times 10:00, 10:30, 11:00, 12:00, 12:30, 13:00 minus 365 days 3 hours (no offset, no GPS); mtime = its capture | R5.2 (equal medians, so the suggestion is exactly +31,546,800 s) |
| `Viagens/2010-07 Bahia/do celular da Ana/IMG_0102.JPG` | no EXIF, own content, mtime 2010-07-17 18:00 | folder refinement; R5.5 suffix |
| `Viagens/2010-12 Natal/IMG_0201…0204.JPG`, `DSC00401…00406.JPG` | as Bahia, true 2010-12-24 19:00–22:00; 0201 with GPS | R5.2 second event |
| `Viagens/2008-03 Ouro Preto/DSCN0001…0003.JPG` | Nikon (`NIKON`, `COOLPIX P5000`, serial `3012345`), 2008-03-22 14:00 + 10 min, offset `-03:00`, subseconds; mtime 2011-01-15 10:00 | R5.1 `mtime_disagrees`; R5.4 |
| `Viagens/2008-03 Ouro Preto/DSCN0004.JPG` | same camera, `2000:01:01 00:00:00`; mtime 2011-01-15 10:00 | `implausible`; month precision |
| `celular_2011/DCIM/Camera/VID_20110423_101500.mp4` | `mvhd` 2011-04-23 13:15:00 UTC; mtime 13:15:30 | `container` |
| `celular_2011/Pictures/Screenshots/Screenshot_2011-05-02-21-14-07.png` | PNG; mtime 2012-02-01 09:00 | name, second precision |
| `celular_2011/WhatsApp/Media/WhatsApp Images/IMG-20110416-WA0003.jpg`, `IMG-20110417-WA0004.jpg` | no EXIF; mtime 2012-02-01 09:00 | R5.3 |
| `celular_2011/WhatsApp/Media/WhatsApp Images/Sent/IMG-20110416-WA0003.jpg` | the same bytes as WA0003; mtime 09:01 | R5.5 `identical_copy` |

All mtimes stay within 2003–2012, and the corpus within 10–40 MiB.

**The truth.**
- `corpus.Entry` gains `Date *DateTruth` for every media file (D2): `effective` (`*time.Time`, UTC; nil for `none`), `local`, `precision`, `source`, `refined`, and `flags`. The values are derived without corrections, for zone `UTC`.
- `GroundTruth` gains `Cameras []CameraTruth`: `key`, `shift_s`, `photos`, and `folders`. The Sony's is `SONY|DSC-W55|`, 31,546,800, 12, and `Viagens/2010-07 Bahia` and `Viagens/2010-12 Natal`.
- Truth for the new fixtures, and for the existing `celular_backup_2009/WhatsApp/Media/WhatsApp Images/IMG-20090612-WA0001.jpg` and `WA0002.jpg` (source `file_name`, refined to their mtimes), is declared by hand. Truth for the other older media (every image and video kind, GIFs and the AVI included) comes from the corpus's own small rule (leading-date folder, containment of the mtime, else mtime), which never imports `internal/media`, so the tests compare two independent implementations. `Midia/video.mp4`'s `mvhd` creation time is 0 (absent), so its truth is from its folder or mtime by that rule.
- `web/ui/e2e/env.ts` gains the same fields.

### D20. Search, aggregates, and the Map stay on the modification time (§6.3, §11.3)

They keep "Year of last change". Changing them would re-fold every folder after every derive pass, and contradict §6.3. Rejected: an effective-date year filter in Search, since the Dates screen's list filters by date already.

### D21. Dates is its own screen (§11)

It sits in the main navigation, between Opportunities and Cleanup. Rejected: a ninth opportunity card. Cards are a closed list of review lists over `review_rows`, while dates need their own filters, cameras, and actions.

## Interfaces

### Import direction

```
media ──▶ standard library only (a leaf)
dates ──▶ media, content (Opener, Row), index (NotQuarantined), jobs, store, sources, commands, domain, clock
organize ──▶ media (templates, names, DateFromRow), dates (EnqueueMedia, Targets, ExpandTargets, Rederive, MediaCond)
executor ──▶ index (ModTime), fsaccess (SetModTime); never media or dates
cmd/precious ──▶ dates
web/api, search, review, relations ──▶ unchanged
```

### Go signatures (foundation adds; slices only add)

```go
// internal/media (foundation, pure)
type Format uint8 // FormatNone, FormatJPEG, FormatTIFF, FormatISOBMFF
func FormatOf(ext string) Format       // D1's list; ext lower-case, no dot; FormatNone otherwise
func IsMediaKind(fileKind string) bool // "image" or "video" (D2)
const FirstWindow = 256 << 10; MaxBytes = 1 << 20
type Meta struct {
	CaptureLocal     string     // "2006-01-02T15:04:05" or with ".000"; "" when none
	CaptureOffsetMin *int
	GPS, Container   *time.Time // UTC instants
	Make, Model, Serial string
}
func Read(r io.ReaderAt, size int64, f Format) (Meta, error) // error only from r; malformed input → partial or empty Meta
type Precision string   // "second" "day" "month" "year"
type Source string      // "owner" "exif" "gps" "container" "file_name" "folder_name" "mtime" "none"
type Confidence string  // "high" "medium" "low" "lowest" "none"
type MetaState string   // "pending" "read" "none" "unreadable"
type Flags uint32       // FlagMtimeDisagrees=1, FlagImplausible=2, FlagCameraOffset=4, FlagNoDateMetadata=8
type Date struct { Instant time.Time; Local string; OffsetMin *int; Precision Precision }
type Correction struct { Kind string; SetLocal string; SetOffsetMin *int; ShiftS int64 } // SetLocal in any D11 form
type Inputs struct {
	Path       []byte      // relative to the source's top
	Mtime      *time.Time  // nil when unknown
	Caps       struct{ LocalTime bool; Resolution time.Duration }
	MetaState  MetaState   // "pending" when there is no row
	Meta       *Meta       // non-nil only for MetaState "read"
	Correction *Correction
	Zone       *time.Location
	Now        time.Time   // plausibility bound; not part of the key
}
type Candidate struct { Source Source; Date Date; Plausible bool }
type Effective struct { Date *Date; Source Source; Confidence Confidence; Refined bool; Corrected string; Flags Flags; Candidates []Candidate }
const DeriveVersion = 1
func Derive(in Inputs) Effective           // never sets FlagCameraOffset; the owner's date bypasses plausibility
func InputsKey(in Inputs) uint64
func ZoneKey(loc *time.Location) uint64    // D7
func ParseLocal(s string, zone *time.Location, offsetMin *int) (Date, error) // D11's set forms
func NameDate(name []byte, zone *time.Location) (Date, bool)
func FolderDate(name []byte, zone *time.Location, now time.Time) (Date, bool)
func CameraKey(make, model, serial string) string
func DateFromRow(effectiveNs int64, local string, offsetMin *int, precision string) (Date, error)
type Photo struct { Entry, Folder int64; Camera string; Capture time.Time; OffsetKnown bool; GPS *time.Time; FolderDate *Date }
type Event struct { Folder int64; DeltaS int64; Photos int; SpanS int64; Reference string /* gps folder_name own_gps "" */ }
type CameraResult struct { Key string; Photos int; State string /* ok offset disagrees */; ShiftS *int64; Events []Event; Others []string }
func Detect(photos []Photo) []CameraResult // offset: Events are exactly the folders whose photos it flags
type Template struct{ /* parsed */ }
func ParseTemplate(s string) (Template, error) // "" → "{year}/{month}"; error is domain invalid_request
func (t Template) String() string
func (t Template) Folders(d Date, event []byte) ([][]byte, error) // ErrTooCoarse
func EventName(folder []byte) []byte
func RenamedName(name []byte, d Date) ([]byte, error)         // ErrTooCoarse; unchanged when already prefixed
var ErrTooCoarse error

// internal/content (foundation): the hashing chain, exported
type Opener struct{ /* folder chain */ }
func NewOpener(root fsaccess.Dir, calls FSCaller) *Opener
func (o *Opener) Open(r Row, caps fsaccess.Capabilities) (fsaccess.File, fsaccess.EntryInfo, error) // OpenAt's errors
func (o *Opener) Close()
// OpenAt and the hashing job use it; behaviour unchanged.

// internal/fsaccess (foundation)
// Writer gains:
//	SetModTime(name []byte, t time.Time) error // D12
// instrument: OpSetModTime; synthfs and portable implement it; synthfs can mark a file foreign (EPERM).

// internal/index (foundation)
type ModTime struct { Source domain.SourceID; Entry domain.EntryID; Facts PostFacts }
func ApplyModTime(ctx context.Context, tx *sql.Tx, m ModTime) error // D15; refuses a folder, another source, a missing entry
// writer: stDropMedia beside stDropContent; keepContent also updates media_meta;
// intentCond also counts a date_corrections row (D11).

// internal/executor (foundation adds the contract; task 2.8 implements the op)
// Index gains:
//	ApplyModTime(ctx context.Context, tx *sql.Tx, m index.ModTime) error // organize's adapter: index.ApplyModTime + Refold([entry])
// New op "set_mtime"; item gains newMtime and prevMtime (new_mtime_ns, prev_mtime_ns) in itemColumns.

// internal/config (foundation)
type Dates struct { TimeZone string `toml:"time_zone"` }
func (d Dates) Location() (*time.Location, error) // "" → time.Local; validated at load

// internal/organize (foundation, additive)
// Options gains Dates *dates.Service, passed in cmd/precious/serve.go; plan-set-mtime and
// plan-date-organize call Options.Dates.Rederive and dates.ExpandTargets (D14, D16).

// internal/dates (foundation: dates.go, targets.go, derive.go, enqueue.go)
const KindMedia jobs.Kind = "media"
type Options struct { Store *store.Store; Runner *jobs.Runner; Sources *sources.Service; Zone *time.Location; Clock clock.Clock; Logger *slog.Logger }
func New(o Options) *Service
func MediaCond(alias string) string // D2; includes index.NotQuarantined(alias); alias must be a plain identifier
func EnqueueMedia(ctx context.Context, tx *jobs.Tx, src domain.SourceID) error
// D4: sets media_sources.dirty; EnqueueOnce scope "media:<src>", payload {"if_dirty":true};
// when that job is running, EnqueueOnce scope "media-next:<src>" with the same payload. Any source state.
type Targets struct {
	EntryID   string   `json:"entry_id,omitempty"`   // single; only when the command allows it
	EntryIDs  []string `json:"entry_ids,omitempty"`  // 1–1,000
	FolderIDs []string `json:"folder_ids,omitempty"` // 1–100, one source
	CameraKey string   `json:"camera_key,omitempty"` // only with folder_ids
}
func (t Targets) Validate(single bool) error // exactly one form; IDs through domain.ParseRef; a member ref is 400 when single
type Skip struct { Entry domain.Ref; Reason string } // reason "not_media" for a member ref or a non-media file
type Expanded struct { Source domain.SourceID; Media []domain.EntryID /* path order */; Skipped []Skip }
func ExpandTargets(ctx context.Context, tx *sql.Tx, t Targets, max int) (Expanded, error)
// not_found, in_quarantine (a target itself quarantined), invalid_request (two sources, over max)
func (s *Service) Rederive(ctx context.Context, tx *sql.Tx, ids []domain.EntryID) error // D9

// internal/dates (task 2.1, slice A)
func (s *Service) Register(r *jobs.Runner)
func (s *Service) DeferWhile(active func(ctx context.Context, q store.Queryer, src domain.SourceID) (bool, error))
func (s *Service) AfterScan(ctx context.Context, src domain.SourceID)
func (s *Service) Startup(ctx context.Context) error

// internal/dates (task 2.6, slice B)
func (s *Service) RegisterCommands(h *commands.Handler)
func (s *Service) Routes(mux *http.ServeMux)

// tests (foundation): internal/dates/helpers_test.go (env, the shared built corpus; slices only add),
// internal/dates/datestest.Seed (fills media_meta with media.Read of each media file, then Rederive).
```

### Tables (migration `0008_media.sql`; foundation)

`actions` and `action_items` are rebuilt as `actions_v8`/`action_items_v8`. Rows are copied with their IDs, items are dropped before actions, both are renamed, and every 0007 index is recreated. Every other CHECK and column stays, with new columns last:
- `actions.kind` adds `'set_mtime','date_organize'`;
- `actions.template TEXT` (a `date_organize`'s template) and `actions.rename INTEGER NOT NULL DEFAULT 0 CHECK (rename IN (0,1))`;
- `action_items.op` adds `'set_mtime'`;
- `action_items.new_mtime_ns INTEGER`, with `CHECK ((op = 'set_mtime') = (new_mtime_ns IS NOT NULL))`, and `action_items.prev_mtime_ns INTEGER CHECK (prev_mtime_ns IS NULL OR op = 'set_mtime')`;
- `action_items.copy_of INTEGER REFERENCES entries(id) ON DELETE SET NULL`, indexed by `action_items_copy_of ... WHERE copy_of IS NOT NULL`.

```sql
CREATE TABLE media_meta (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  source_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('pending','read','none','unreadable')),
  size INTEGER NOT NULL, mtime_ns INTEGER, ctime_ns INTEGER, ino INTEGER,   -- the entries identity the read started from
  capture_local TEXT, capture_offset_min INTEGER CHECK (capture_offset_min BETWEEN -840 AND 840),
  gps_ns INTEGER, container_ns INTEGER,
  make TEXT, model TEXT, serial TEXT,
  read_at INTEGER,
  CHECK (state = 'read' OR (capture_local IS NULL AND gps_ns IS NULL AND container_ns IS NULL
    AND make IS NULL AND model IS NULL AND serial IS NULL)));
CREATE INDEX media_meta_by_source ON media_meta(source_id, state, entry_id);

CREATE TABLE media_dates (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  source_id TEXT NOT NULL,
  effective_ns INTEGER, local TEXT, offset_min INTEGER,
  precision TEXT CHECK (precision IN ('second','day','month','year')),
  source TEXT NOT NULL CHECK (source IN ('owner','exif','gps','container','file_name','folder_name','mtime','none')),
  confidence TEXT NOT NULL CHECK (confidence IN ('high','medium','low','lowest','none')),
  refined INTEGER NOT NULL DEFAULT 0 CHECK (refined IN (0,1)),
  corrected TEXT CHECK (corrected IN ('set','shift','use_name','use_folder')),
  flags INTEGER NOT NULL DEFAULT 0 CHECK (flags BETWEEN 0 AND 15),
  meta_state TEXT NOT NULL CHECK (meta_state IN ('pending','read','none','unreadable')),
  camera_key TEXT,
  inputs_key INTEGER NOT NULL, computed_at INTEGER NOT NULL,
  CHECK ((source = 'none') = (effective_ns IS NULL)),
  CHECK ((effective_ns IS NULL) = (precision IS NULL) AND (effective_ns IS NULL) = (local IS NULL)),
  CHECK (flags & 8 = 0 OR meta_state IN ('read','none')));
CREATE INDEX media_dates_by_time   ON media_dates(source_id, effective_ns, entry_id);
CREATE INDEX media_dates_by_source ON media_dates(source_id, source, effective_ns, entry_id);
CREATE INDEX media_dates_by_camera ON media_dates(source_id, camera_key, effective_ns, entry_id) WHERE camera_key IS NOT NULL;
CREATE INDEX media_dates_mtime_disagrees  ON media_dates(source_id, effective_ns, entry_id) WHERE flags & 1 <> 0;
CREATE INDEX media_dates_implausible      ON media_dates(source_id, effective_ns, entry_id) WHERE flags & 2 <> 0;
CREATE INDEX media_dates_camera_offset    ON media_dates(source_id, effective_ns, entry_id) WHERE flags & 4 <> 0;
CREATE INDEX media_dates_no_date_metadata ON media_dates(source_id, effective_ns, entry_id) WHERE flags & 8 <> 0;

CREATE TABLE date_corrections (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('set','shift','use_name','use_folder')),
  set_local TEXT CHECK (set_local IS NULL OR length(set_local) IN (4,7,10,19)),
  set_offset_min INTEGER CHECK (set_offset_min BETWEEN -840 AND 840),
  shift_s INTEGER CHECK (shift_s BETWEEN -1577880000 AND 1577880000),
  batch_id TEXT NOT NULL, created_at INTEGER NOT NULL,
  CHECK ((kind = 'set') = (set_local IS NOT NULL)),
  CHECK ((kind = 'shift') = (shift_s IS NOT NULL)),
  CHECK (set_offset_min IS NULL OR (kind = 'set' AND length(set_local) = 19)));

CREATE TABLE media_cameras (
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  camera_key TEXT NOT NULL,
  make TEXT, model TEXT, serial TEXT,
  photos INTEGER NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('ok','offset','disagrees')),
  suggested_shift_s INTEGER,
  basis TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(basis)),
  computed_at INTEGER NOT NULL,
  PRIMARY KEY (source_id, camera_key),
  CHECK ((state = 'offset') = (suggested_shift_s IS NOT NULL))) WITHOUT ROWID;

CREATE TABLE media_sources (                 -- D4, D10
  source_id TEXT PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
  dirty INTEGER NOT NULL DEFAULT 0 CHECK (dirty IN (0,1)), -- set by every request, cleared at each loop's start
  passes_job INTEGER,                        -- the media job running its passes, if any (D4's mutual exclusion)
  summary TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(summary)),
  summary_at INTEGER, detected_at INTEGER) WITHOUT ROWID;
```

Text formats other slices read:
- `capture_local`, `set_local`, and `media_dates.local` use D5's and D11's forms.
- `camera_key` follows D8.
- `media_cameras.basis` is `{"events":[{"folder_id":"812","path":"Viagens/2010-07 Bahia","path_b64":"…","delta_s":-31546800,"photos":6,"span_s":10800,"reference":"gps"|"folder_name"|"own_gps"|null}],"others":["Canon|Canon PowerShot SX230 HS|"]}`. For `offset`, the events' folders are the suggestion's `folder_ids`.
- `media_sources.summary` is the summary read's body (below) for that source, without `time_zone` and `detected_at`.

### Commands (strict JSON; IDs are strings)

| Command | Body | Success | Errors |
|---|---|---|---|
| `set-date-correction` | `Targets` (D11) + `correction: {kind, local?, offset_min?, shift_s?}` | 200 `{applied, skipped_count, skipped: [{entry_id, path, path_b64, reason}] (≤100, path order), batch_id}` | 400 `invalid_request` (shape, a bad date, a date after now plus one day, over 50,000 media, a member ref when single); 404 `not_found`; 409 `in_quarantine`; single only: 409 `invalid_entry_state` |
| `clear-date-correction` | `Targets` | 200 `{cleared, batch_id}` | as above, without `invalid_entry_state` |
| `plan-set-mtime` | `Targets` without `entry_id` | 201 `{action, items, next_cursor, summary: {unchanged}}` | 400 (shape, two sources, over 10,000 items); 404; 409 `in_quarantine`, `source_offline`, `writes_disabled`, `writes_unavailable`, `recovery_needed` |
| `plan-date-organize` | `Targets` without `entry_id` and `camera_key` + `destination_id`, `template?`, `rename?` | 201 `{action, items, next_cursor, summary: {files_with_copies, split_siblings}}` | as `plan-set-mtime`, plus 400 for a bad template, or a destination that is not a present folder of the targets' source, is inside an archive, or is in the quarantine |
| `plan-undo` | R3's | | reverses `set_mtime` items (D14) |
| `run-action`, `cancel-action` | R3's | | unchanged |

Single and bulk: only the correction commands take `entry_id`; a single request fails where a bulk one skips. Audit events: `date_correction_set` and `date_correction_cleared`, with detail `{targets, correction, applied, skipped, batch_id}`. Plans and runs audit as R3's do.

### Read API (`dates.Routes`; every read applies `MediaCond`)

- **`GET /api/dates/summary?source=`** answers:

  ```
  {media, metadata: {pending, read, none, unreadable},
   by_source: {owner, exif, gps, container, file_name, folder_name, mtime, none},
   by_confidence: {high, medium, low, lowest, none},
   flags: {mtime_disagrees, implausible, camera_offset, no_date_metadata},
   cameras: {offset, disagrees}, time_zone, time_zone_set, summary_at|null, detected_at|null}
  ```

  From `media_sources.summary` (D10); without `source`, the sum over sources.
- **`GET /api/dates?source=&flag=&date_source=&camera=&within=&cursor=&limit=`** answers `{items: MediaDate[], next_cursor}`. `source` is required (400 without it). Order is by `effective_ns` then entry ID, with `none` last, or by path with `within` (D10). `limit` is 200 by default and at most 1,000. `count=only` answers `{count}`. Unknown parameters are `400 invalid_request`.
  - `MediaDate {entry: {id, source_id, name, path, path_b64, size, mtime|null}, date: DateJSON, metadata: "pending"|"read"|"none"|"unreadable", flags: [name…], camera: {key, make, model, serial}|null, correction: CorrectionJSON|null}`.
  - `DateJSON {instant|null, local|null, offset_min|null, precision|null, source, confidence, refined, corrected|null}`.
  - `CorrectionJSON {kind, local?, offset_min?, shift_s?, created_at}`.
- **`GET /api/dates/cameras?source=`** answers `{items: [{key, make, model, serial, source_id, photos, state, suggested_shift_s|null, events: [{folder: {id, path, path_b64}, delta_s, photos, reference|null}], computed_at}]}`: `offset`, then `disagrees`, then by photos. An event whose folder is gone is left out.
- **`GET /api/entries/{id}/dates`** answers `{dates: EntryDates|null}`, or 404. It is `null` for anything `MediaCond` leaves out: not media, archive members, missing, and quarantined entries.
  - `EntryDates = MediaDate` without `entry`, plus `candidates: [{source, local, offset_min|null, instant, precision, plausible}]`, derived on read (D9).
- **The Action JSON** gains `template` and `rename`.
- **The Item JSON** gains `mtime: {from, to}` (RFC 3339, nanoseconds, UTC; `from` is `prev_mtime_ns` once journaled, else the index time) for `set_mtime`, and `copy_of: {entry, path, path_b64}` for `identical_copy`.
- **History.** `GET /api/history/{id}/items?op=set_mtime` is valid. The export's operation for `set_mtime` is `set_mtime`, and a `date_organize` rename is `move`.
- **Item reasons** gain `date_too_coarse`, `not_dated_yet`, `hard_link`, `no_change`, `not_owner`, `identical_copy`, `invalid_name`, and `not_media`.
- **Jobs.** Kind `media`, with D4's progress keys, appears in `GET /api/jobs/{id}` and the event stream.

## Concurrency

- **Scheduling (D4).** Requests, the handler's start, each loop's clear and end read, and its failure path are all write transactions, so they are ordered:
  - *A request while no job exists, or while one is queued:* it creates or joins the queued `media:` job, with `dirty` set, so that job runs.
  - *A request during passes 1–4:* the `media:` job is `running`, so a `media-next:` follow-up is enqueued; `dirty` is set, so the loop's end read sees it and loops again. The follow-up, once it starts, finds `passes_job` held and defers (no attempt used, so it stays a first attempt), or, after the loop released it, finds `dirty` clear and ends at once.
  - *A request after the loop's end read, while the job is still `running`:* the follow-up is enqueued, finds `passes_job` released and `dirty` set, and runs the passes.
  - *A request while the follow-up runs:* the `media:` job has ended, so a new `media:` job is created; it defers while the follow-up holds `passes_job`, then runs if `dirty` is still set. A further request joins whichever of the two is queued.
  - *The job fails or is cancelled:* its failure transaction sets `dirty` and releases `passes_job`. A queued follow-up, or the next request, runs the passes; with neither, the next server start requests every source. A cancel is honoured: nothing runs until then.
  - *A lost lease or worker:* the runner retries the attempt (same job, `Attempt > 1`), which ignores `if_dirty`, retakes its own `passes_job`, and runs. A crash on the last attempt skips the failure transaction; requests absorbed before that attempt's last clear wait for the next request or the next start, which requests every source. Every loop covers the whole source, so a late request is delayed, never answered wrongly.
  - *A paused job:* the handler never pauses; one paused by hand is resumed by the next request.
- **The read pass.** Each commit of up to 64 results re-checks, in its transaction, that the entry is `present` and its `entries` row equals the identity loaded when the read started, and that the `media_meta` row is `pending`. A rescan that changed the file dropped the row; a `set_mtime` outcome changed the `entries` times and carried the row. Either way, a commit just after them drops the result, and a carried row is read once more. A commit just before them is seen by them: the scan drops the fresh row if the file changed, and `ApplyModTime` carries it. A move just before makes the path differ, so the result is dropped and read again at the new path, as hashing does (R3).
- **The dates pass, the commands, and the plans.** `Rederive` reads its inputs inside the writing transaction, so a result never outlives its inputs. A move or a written time committed just after a pass is picked up by the `media` job that `ActionDone` requests, and by any plan, which re-derives its targets first. Until then, list and summary reads may show the previous path's name and folder dates; that is eventually consistent.
- **The cameras pass.** It reads a snapshot, computes, and writes `media_cameras` and the `camera_offset` bits in one transaction at the end of every pass. A correction committed during the pass may be overwritten there (its targets' bits set again, the suggestion still listed); its request set `dirty`, so the loop runs again and that run's write replaces it. A correction just after the write clears its targets' bits itself, and its request makes the job run again.
- **Correction commands.** They expand targets, check quarantine, write, re-derive, request the job, and audit, in one transaction. A plan or a run committed before sees no correction. A plan just after uses the new dates. A planned action does not change (D13).
- **`plan-set-mtime` and `plan-date-organize`.** These are index-only commands in the write transaction. They re-derive, then re-check writes, recovery, quarantine, keeps, and collisions against the index and the plan. A rescan or edit just after is caught at intent and at the step (identity and change time), and a name taken on disk just after by RENAME_NOREPLACE (`conflict`).
- **The `set_mtime` step.** Its journal of `prev_mtime_ns` commits before the call; a crash between them reconciles to `planned` (old time), and the next attempt journals again. The outcome's `ApplyModTime` and `MarkStale` share the transaction recording `done`. A hashing or `media` read of that file committed just after fails its identity re-check and is dropped. Scans, `purge_check`, and the `media` job defer while `OrganizeActive` holds.
- **Undo.** Intent requires the index's time to be the written one. A rescan in between that saw a hand edit changes it, so the item ends `changed`.

## Risks / Trade-offs

- [Parsers face untrusted bytes] → Bounds on reads, boxes, entries, and depth; offsets checked against the size; fuzz targets per format in CI, with seeds; long fuzzing behind the `slow` tag; a recover net that logs.
- [The service account does not own the files] → `not_owner` per item, the action goes on, and writes stay on. The operator guide's deployment section says how to grant ownership or `CAP_FOWNER`, and the smoke and deploy tasks check it first.
- [Floating times read in the wrong zone] → One explicit setting, warned about when unset and shown on the Dates screen; confidence `medium` for a time without an offset; corrections fix the rest.
- [FAT mounts in another zone] → Documented (D7); flags tolerate ±1 h only.
- [MP4 `mvhd` written in local time by some cameras] → `medium` confidence, the candidates shown in the panel, and the owner's correction.
- [Offsets of 6 h or less go undetected] → Documented (D8); the owner shifts by hand.
- [Medians of different shooting moments] → A candidate needs every capture outside the other cameras' widened range, a suggestion needs two events or GPS, and the preview lists the photos and their new dates before anything changes.
- [A power loss right after `utimensat`, before the inode reaches the disk] → The index reads `done` while the disk may keep the old time. The next scan sees a change and re-reads the file. Nothing is lost, and an undo ends `changed` instead of writing.
- [The first run opens every photo] → Device-bound bulk class, yields, and progress. Later runs read only what changed.
- [Hard links] → `set_mtime` refuses them at planning, intent, and the step (D13), since one write would change every name.
- [Corpus ripple] → New folders only. The corpus task lists and updates every test with hard-coded corpus numbers (ScoutCorpus's list).

## Migration Plan

- **Upgrade.** Migration 0008 rebuilds `actions` and `action_items`, keeping IDs and history, and adds five empty tables. At start, the `media` job reads every online source's media once, and derives offline sources' dates from the index.
- **Configuration.** `[dates] time_zone` is optional, warned about when unset. The example configurations and the operator guide set it.
- **Deployment.** Write-back needs the files owned by the service account, or `CAP_FOWNER` (D12); after a `chown`, a rescan before any write-back (it re-hashes and re-reads the media).
- **Rollback.** Restore the backup taken before deploying: an r4 binary refuses a newer schema.

## Addendum: decisions made during implementation

- **Z1. `"Local"` is refused.** `time.LoadLocation("Local")` returns `time.Local`, so `time_zone = "Local"` would choose the server's zone while silencing the unset warning. `Dates.Location` refuses it, like an unknown name, with a problem naming `dates.time_zone`. Any name `time.LoadLocation` resolves, `"UTC"` included, is accepted; the config tests link `time/tzdata`, as the binary does, so they need no host zone files.
- **Z2. Where check-config shows the zone.** The effective zone is a comment line above the TOML on standard output: `# Media dates are read in America/Sao_Paulo (-03, UTC-03:00).`, or `# dates.time_zone is unset: media dates are read in the server's local zone (UTC, UTC+00:00).` The abbreviation and offset are taken at the current time, since `time.Local`'s name is always "Local". The warning is one line on standard error and the exit status stays 0; the TOML prints `time_zone = ""`, so the output still loads back to the same configuration.
- **Z3. The start warning is the first thing `serve` does.** A `WARN` record with the same text as check-config's warning and a `zone` attribute describing the local zone, logged before anything else starts. An unknown name stops `serve` in the shared `loadConfig`, before the state directory is touched, as it stops `check-config`.
- **Z4. Example zone.** Both example configurations set `time_zone = "America/Sao_Paulo"`, the name the spec uses; the Compose steps (in `compose.yaml` and the operator guide) say to edit it with `external_origin`.
- **W1. synthfs `SetModTime` checks in `utimensat`'s order.** The name is looked up first (absent), then the device's read-only state (`ErrReadOnly`), then the foreign mark (`EPERM`, `ErrPermission`), so an absent name on a read-only device is absent, as on a read-only tmpfs remount. The other synthfs writes keep checking read-only first, as their system calls do.
- **W2. The foreign mark is `(*synthfs.Node).Foreign()`.** It marks the file a hard link's names share, does not advance the change time (it models ownership as built, not a `chown`), and affects only `SetModTime`: renames and unlinks depend on the folder, not the owner.
- **W3. synthfs stores a set time as its device would.** With capabilities: truncated to `TimeResolution`, and with `LocalTime` as the wall time of `t` in the zone the device is mounted with when it is set (so moving that zone later shifts it, as for built times). A device without capabilities stores `t` exactly (monotonic reading dropped). The change time advances on the FS clock; the folder's times do not change. A folder or a symlink can be set, as `utimensat` allows; refusing anything but a regular file is the executor's step (D13).
- **W4. `instrument.Call` gains `ModTime`,** the time a `SetModTime` asked for (additive), so executor tests can assert the written time from the log.
- **W5. Linux error mapping goes through `WriteError`.** `ENOENT` is absent, `EPERM`/`EACCES` `ErrPermission`, `EROFS` `ErrReadOnly`, anything else unavailable; a time a 32-bit `timespec` cannot hold (`ERANGE` from `unix.TimeToTimespec`) is unavailable before any system call.
- **W6. The "another user" e2e test runs only as real root.** `inUserNamespace` maps a single user, so no second owner exists there; the test skips unless root, and `scripts/e2e-docker.sh` runs it. It drops to another user on one locked OS thread with raw `setgroups`/`setresgid`/`setresuid` (Go's wrappers change every thread), never unlocking it, so the thread exits with the goroutine. The foreign file is mode `0666`, so the refusal shows write permission is not enough, and a file that thread owns is set as a control.
- **I1. 0008 is exactly Interfaces, and the guard covers what it adds.** Tables, columns, CHECKs, and indexes are as listed; the rebuilt tables keep 0007's column order with the new columns last, and every 0007 index is recreated. The store's foreign-key guard checks every reference into `entries`, `actions`, `action_items`, and `purge_checks`, and the references into `sources` of `actions`, `media_cameras`, and `media_sources`. `jobs.source_id` and `review_rows.source_id` predate R5 and are searched without a usable index; 0008 leaves them as they are. The 0007 column and index test now pins the v7 schema (`migrationsUpTo(7)`), as the 0006 one pins v6.
- **I2. One list of content tables.** `index.contentTables` (`file_content`, `archives`, `media_meta`) is what `keepContent` and `ApplyModTime` carry, so a further cache keyed by entry identity is added in one place. `stDropMedia` runs beside `stDropContent` and `stDropArchive` in the same `opUpdate`, so the delete commits or fails with the `entries` update.
- **I3. `ApplyModTime`'s guards and carry.** It refuses any entry that is not a present file of the source (folders, the root, links, special files, missing entries, other sources, unknown IDs). It carries the content rows only when the post-step inode equals the stored one, as `keepContent` does; the carried rows take the new `mtime_ns` and `ctime_ns` (NULL for an unknown change time), and their size and inode stay. The entry's own `newest_ns`/`oldest_ns` follow `ownRange`, as a scan writes them.
- **I4. A correction is intent everywhere `intentCond` is.** `MissingIntentAt`, `IntentBelow`, `freePath` (moves and new folders), and `RemoveFolder`'s check of missing children all count a `date_corrections` row; `ErrMissingIntent`'s message names it. The item reasons table's `name_taken_by_missing` text ("your decision, tags, or category"), owned by slice D, should add "or a date correction".
- **P1. A bad template is `media.ErrInvalidTemplate`.** `media` stays standard-library only, and `internal/domain` imports `golang.org/x/text`, so `ParseTemplate` cannot return a `domain.Error`. Every refusal wraps the exported sentinel `media.ErrInvalidTemplate` (`errors.Is`), and slice D's `plan-date-organize` maps it to `400 invalid_request` with `domain.Wrap`. Agreed with the coordinator.
- **P2. A malformed structure gives nothing from that file.** A fault in a file's structure (a header, an IFD or box outside the file or its parent, a loop between IFD0, Exif, and GPS IFDs, more than 1,024 IFD entries or boxes or `iloc` items, depth over 8, the budget spent) makes `Read` return an empty `Meta` for the whole file. A single tag whose value lies outside the file only leaves that value out. Only an error of the `ReaderAt` is returned; a short read at the file's end is a fault, not an error. Text values over 256 bytes, and Exif items and CMT boxes past 256 KiB, are cut there.
- **P3. The offset and subseconds belong to `DateTimeOriginal`.** `OffsetTimeOriginal` and `SubSecTimeOriginal` apply only when the capture comes from `DateTimeOriginal`; the `DateTimeDigitized` and IFD0 `DateTime` fallbacks, taken when the one above is absent or does not parse, carry neither. A blank or impossible EXIF date (`0000:00:00`, February 30th) is no date. Dates use `:` or `-` in the date and a space or `T` before the time.
- **P4. Years 1700–2200.** Every date read or set (EXIF, GPS, names, folders, the owner's `set`, the `mvhd` time up to 2201-01-01) has a year from 1700 to 2200, so every instant fits `effective_ns`; `DateFromRow` reads back years 1600–2300, which a UTC instant at the bounds can show in another zone. Fuzzing found both cases (checked-in seeds `pxl-year-0001`, `pxl-year-1700`).
- **P5. A known modification time is the last resort.** D5 says "none" happens only for an unknown modification time, so a known one wins when no other candidate is plausible, even an implausible one (a FAT default 1980-01-01); its candidate reads `plausible: false`, and it never sets `implausible`. Name and folder candidates are tested for plausibility like the others, without the flag.
- **P6. Instants keep their fractions; `local` is to the second.** The EXIF subseconds (as milliseconds), PXL's milliseconds, GPS's fractional seconds, and the modification time's nanoseconds stay in the instant, so a date taken from the modification time equals it and plans no change. `Meta.CaptureLocal` carries ".000" when there are subseconds; `Date.Local` never does.
- **P7. `offset_min` only when the source records one.** EXIF with `OffsetTimeOriginal` and an owner's `set` with an offset carry it; GPS, container, name, folder, and modification-time dates have their `local` in the zone and no offset.
- **P8. Distances.** `mtime_disagrees` measures from the instant at second precision, and from the period otherwise (an owner's `2012-02` containing the modification time does not disagree); its limit is 24 h, plus 1 h on a `LocalTime` filesystem. Refinement widens the period by 1 h on a `LocalTime` filesystem and by nothing otherwise.
- **P9. Corrections in `Derive`.** A `shift` moves the EXIF capture even when implausible, else the uncorrected effective date (refined if it was), keeping its precision and offset; a date coarser than a second starts its new period. With nothing to shift, or a `use_name`/`use_folder` without that candidate, or a `set` whose text does not parse, the correction does not apply (`Corrected` is empty). `use_name` and `use_folder` force their candidate even when implausible, with refinement as usual. The owner's date is listed first among the candidates.
- **P10. `InputsKey` also covers the capabilities.** `LocalTime` and `Resolution` change refinement and flags, so they are in the key with D9's inputs; every field is length-prefixed. `Now` is not.
- **P11. Detection details.** Own-GPS candidates arise only in events, and replace that camera's plain candidate there. The `gps` reference needs at least one of the other cameras' photos in the event with GPS, and all of those within 10 minutes. An event's folder date is its photos' `FolderDate`, which the caller sets to the event folder's D6 date; without a zone, its period ends by the offset its instant implies. A median of an even count is the mean of the middle two; deltas round to the second, the suggestion (minus the median of the candidates' deltas) to the minute, half away from zero. Results are by key; `ok` cameras list no events; `disagrees` cameras list the events of their candidates without the `offset` cameras; `Photos` counts all the camera's photos given.
- **P12. Small choices.** `CameraKey` is "" when make, model, and serial are all empty, and such photos have no camera. `FormatOf` also reads `jpe` and `jfif` as JPEG. A name pattern followed by a digit is no match (`IMG_20150312_1430001`). A folder named `2010-13 x` dates to the year 2010 (the longest valid form). `EventName` strips D6's syntax without the read-year bound (it has no clock). A template component left as `.` or `..` after an empty `{event}` goes, like an empty one.
- **P13. The corpus's date truth.** `DateTruth.flags` are flag names in bit order, as the list API names them, and include `camera_offset` on the Sony's 12 photos, since the truth is what the `media` job leaves, cameras pass included. `GroundTruth.Cameras` lists every camera the cameras pass lists: the Sony with its shift and two folders, and the Canon (8 photos) and the Nikon (3: `DSCN0004.JPG`'s capture is implausible, so detection is not given it) with `shift_s` 0 and no folders. The FAT fixture has no cameras (`null`).
- **P14. The corpus's own rule.** Which files are media is the policy's image and video extensions, copied into `internal/corpus` so the truth depends on no rules code. A media file without hand-declared truth is dated by the nearest ancestor whose name starts with `YYYY`, `YYYY-MM`, or `YYYY-MM-DD` (then the end, a space, `-`, `_`, or `.`; from 1990): the modification time, refined, when it lies in that period, else the period; with no such folder, the modification time; always with `no_date_metadata`. `TestDateTruthMatchesMedia` (test only) reads every media file through `media.Read`, derives with `media.Derive` and `media.Detect` for the zone UTC, and finds the hand-declared truth and the rule equal to them.
- **P15. Fixture details D19 leaves open.** EXIF is written big-endian, with IFD0 `DateTime` equal to the capture. The Nikon's subseconds are "12", "34", "56" (.120, .340, .560 s), and `DSCN0004.JPG` carries the same `-03:00` offset as its siblings. The MP4 is `ftyp`, `moov/mvhd`, and 24,000 random bytes of `mdat` (not playable). The corpus grows from 518 to 562 entries (23.7 to 23.9 MB); its root time is unchanged. `TestOlderFoldersUnchanged` pins a hash of the r4 corpus's `Fotos`, `Fotos - Copia`, `Midia`, and pendrive copies (132 entries: paths, kinds, times, digests, order). No Go test's hard-coded corpus numbers changed.
