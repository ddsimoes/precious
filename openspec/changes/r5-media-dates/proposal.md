# Proposal

## Why

The owner can see, deduplicate, organize, and clean up the disk (R1–R4), but photos and videos can only be placed by their modification time, which copies and camera clocks have often made wrong. R5 (§14) gives every photo and video a trustworthy date and organizes them by it (§10.7).

## What Changes

- **Reading** (§10.7.1, §12). A background `media` job per source reads, read-only, the header of each photo and video: EXIF (JPEG, TIFF and TIFF-based RAW, HEIC/HEIF/AVIF, CR3) and the MP4/MOV creation time, with camera make, model, and serial. Parsers are in-repo, standard library only, bounded, and fuzzed. Results are cached by file identity and survive moves (I9).
- **Effective dates** (§10.7.3). Each photo and video gets an effective date with its source, precision, and confidence: owner correction > EXIF > GPS > container > file name > folder name > modification time. One configured zone, `[dates] time_zone`, turns wall times into instants.
- **Detection** (§10.7.2). Flags: modification time disagrees, implausible capture date, camera clock offset, no date metadata. A camera whose clock is constantly off against the other cameras at an event gets a suggested shift for the photos it was seen at, only when GPS (at one event) or GPS or the folder's date (at two or more) sides with the other cameras; otherwise both are listed as disagreeing.
- **Corrections** (§10.7.3). Bulk set (to a year, month, day, or second), shift, use the name date, use the folder date, and clear, by entries, folders, or one camera in given folders. They are owner decisions (I4).
- **Write-back** (§10.7.4). A new organizing action, "Set file dates", sets a file's modification time to its effective date through the shared executor (a new `set_mtime` step), journaled with the previous time, and undoable. It changes no content and no digest. It needs the files owned by the service account, or `CAP_FOWNER` (operator guide).
- **Organize by date** (§10.7.5). A new organizing action moves media into a template such as `Fotos/{year}/{month}/`, optionally renaming to `{date}_{time}_{name}`. Same name and same content is offered for discard; same name and other content gets a suffix; nothing is overwritten. The preview recommends deduplicating first.
- **Screens** (§11). A Dates screen, a Dates section in the detail panel, the two actions' dialogs, and History entries for them.
- **Corpus** (§15). Photos with EXIF and GPS, a camera with a constant offset, copies whose modification time disagrees, WhatsApp names without EXIF, a phone video, and a screenshot, all with ground truth.

Acceptance scenarios this change must make pass: **R5.1, R5.2, R5.3, R5.4, R5.5**.

Not changed: Search's year filter, folder aggregates, and the Map's age stay on the modification time (§6.3, §11.3).

## Capabilities

### New Capabilities

- `media-dates`: reading media metadata, effective dates, detection, camera offsets, corrections, and the dates reads.

### Modified Capabilities

- `organizing`: "Set file dates" and "Organize by date" actions, their undo, and their History entries.
- `source-writes`: the executor may also set a file's modification time, and reconciles that step after a crash.
- `file-index`: the media metadata cache follows the file's identity through rescans, moves, and written times.
- `owner-intent`: date corrections are owner decisions.
- `inventory-explorer`: the Dates screen and the detail panel's Dates section.
- `server-config`: `[dates] time_zone`.

## Deferred

- Metadata through the optional `exiftool` (§12): no milestone owns it yet; R7 owns only the preview tools. R5 reads natively.
- Metadata of photo and video formats without a parser here (MKV, WebM, WMV, MPEG, MTS, AVI, PNG, GIF, WebP, …): no milestone owns it yet. They get name, folder, and modification-time dates in R5.
- Flagging "a date before the camera model existed" (§10.7.2): no milestone owns it yet; it needs camera release data (ADR 0012).
- Writing dates into EXIF or XMP sidecars: out of scope (§13).
- "Select all results" on the Dates screen, and detecting camera offsets of 6 hours or less: no milestone owns them yet. Per-page and folder targets cover R5.2 and R5.4; small offsets are shifted by hand.
- The effective date in Search, folder aggregates, and the Map: not planned (§6.3 defines them by modification time).
- Write-back on macOS and Windows: R8, with their no-replace writers (the portable backend refuses every write).
