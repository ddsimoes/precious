# ADR 0012: Media metadata is read from header structures; the camera-release check waits

- Status: accepted
- Date: 2026-10-08
- Context: `precious-spec-v0.3.md` §10.7.1, §10.7.2, §12; OpenSpec change `r5-media-dates`, design D1, D8.

## Context

§10.7.1 says the media job reads metadata "from the start of each file only". In MP4 and MOV files, the `moov` box that holds the creation time often follows the media data, at the end of the file, so the first bytes alone cannot date most phone videos.

§10.7.2 gives "a date before the camera model existed" as an example of an implausible capture date. Checking it needs the release date of every camera model, which neither the files nor the index hold.

## Decision

1. **Header structures only.** The job reads a first window of at most 256 KiB, then follows the ISO-BMFF box headers with positioned reads to reach `moov` or `meta` wherever they sit, reading only box headers and the boxes that hold metadata (`mvhd`, the Exif item, and CR3's `CMT` boxes). TIFF-based files, RAW included, are read through their IFD offsets the same way. It never reads media payload, at most 1 MiB per file. "From the start of each file only" is read as "header structures only, never the content".
2. **No camera-release check in R5.** Implausible capture, GPS, and container dates are those before 1990, after the read time plus one day, and the camera defaults `1970-01-01`, `1980-01-01`, `2000-01-01`, and `2001-01-01` at midnight; an owner's date is never tested. A check against camera release dates is left until a milestone takes on a model table; the owner decides which.

## Alternatives rejected

- **The first window only.** It would leave most MP4 and MOV videos without a container date, against §10.7.1's source list.
- **Reading the file's tail.** It reads bytes that are not metadata, and still misses a `moov` in the middle.
- **A bundled list of camera release dates.** It would be data to maintain, outside §12's native reading, for one example of a flag.

## Consequences

- A video with its `moov` at the end costs a few positioned header reads, not a read of the file.
- A capture date that is plausible by these rules, but earlier than its camera's release, is not flagged. The owner can still correct it.
