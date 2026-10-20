# Spec Delta

## Purpose

Gives every photo and video a date the owner can trust: reads media metadata read-only, derives an effective date with its source and confidence, flags doubtful dates and camera clock offsets, and takes the owner's corrections (§10.7.1–§10.7.3).

## ADDED Requirements

### Requirement: Media metadata is read from file headers only
A background `media` job per source SHALL read the capture date and time-zone offset, the GPS time, the MP4/MOV creation time, and camera make, model, and serial of present photos and videos outside the quarantine: EXIF in JPEG, TIFF, TIFF-based RAW, HEIC/HEIF/AVIF, and CR3. It SHALL read only header bytes, at most 1 MiB per file, through identity-checked read-only opens, and never write to a source (I1). Malformed input SHALL yield no value, never a failure of the job.

#### Scenario: A malformed file is read safely
- **WHEN** a `.jpg` holds random bytes, or an EXIF directory points outside the file or loops
- **THEN** the job records no metadata for it, reads at most 1 MiB of it, and goes on with the next file

#### Scenario: Reading leaves the disk untouched
- **WHEN** the job reads every photo and video of the corpus
- **THEN** the instrumented filesystem records no write call, and no file's times change

### Requirement: Metadata is cached by file identity
Metadata SHALL be kept with the file identity it was read from, and read again only when a rescan finds the file changed. A move by Precious and a modification time Precious wrote SHALL keep it. A read whose file changed, moved, or left the index before its result is recorded SHALL be dropped (I9). A request for the job SHALL be kept until a job serves it, also when a job fails or is cancelled, and two jobs of one source SHALL never read at once.

#### Scenario: A rescan reads nothing again
- **WHEN** every photo was read and the source is rescanned with nothing changed, then a photo is moved by Precious
- **THEN** the next job reads no file

#### Scenario: A file changed during its read
- **WHEN** a photo is modified on disk after the job opened it and before its result is recorded
- **THEN** the result is dropped, and the photo is read again after the next scan

#### Scenario: A request while the job is ending
- **WHEN** a correction requests the job after the running job's last check and before it ends
- **THEN** another job runs after it, and no file is read by both

#### Scenario: A job that fails with a request pending
- **WHEN** a move requests the job while it runs, and the job then fails or is cancelled
- **THEN** the request is still pending, and the follow-up job runs the passes, or, when there is none, the next request or server start does

### Requirement: Every photo and video has an effective date
Each present file of kind image or video SHALL have one effective date with its source (`owner`, `exif`, `gps`, `container`, `file_name`, `folder_name`, `mtime`), precision, and confidence, taking the owner's correction if any, else the first source in that order with a plausible date. A date with no time-zone offset SHALL be read in `[dates] time_zone`. Effective dates SHALL follow moves and corrections without a rescan.

#### Scenario: R5.1 Every photo and video gets an effective date with its source
- **WHEN** the corpus is scanned and the `media` job ends
- **THEN** every photo and video, GIFs and AVIs included, has the effective date, precision, and source its ground truth records

#### Scenario: An implausible capture date is skipped
- **WHEN** a photo's EXIF capture date is a camera default such as `2000-01-01 00:00:00`
- **THEN** its effective date comes from the next source, and it is flagged `implausible`

### Requirement: Dates from names and folders keep their precision
A date in a file name or folder name SHALL count only from the documented patterns, with the precision the pattern gives. When it contains the file's modification time, the effective date SHALL take that time, keeping the name or folder as its source. A coarser date SHALL never be made finer by invention.

#### Scenario: R5.3 WhatsApp-named images without EXIF take their date from the file name
- **WHEN** `IMG-20110416-WA0003.jpg` has no EXIF and was copied in 2012
- **THEN** its effective date is 2011-04-16 with source `file_name` and precision `day`; and `IMG-20090612-WA0001.jpg`, modified on 2009-06-12, takes that modification time, still with source `file_name`

#### Scenario: A folder year that disagrees with the modification time
- **WHEN** a photo without metadata sits in `…/2006/Praia` and was modified in 2008
- **THEN** its effective date is the year 2006, with source `folder_name` and precision `year`

### Requirement: Doubtful dates are flagged
Each effective date SHALL carry the flags that apply: `mtime_disagrees` (a capture, GPS, container, or owner date more than 24 hours from the modification time, beyond the filesystem's local-time tolerance), `implausible` (a capture, GPS, or container date only), `camera_offset`, and `no_date_metadata` (the file was read, or is a format with no parser, and has no capture, GPS, or container date). A file not read yet, or unreadable, SHALL show that state, never `no_date_metadata` (I7).

#### Scenario: R5.1 Photos whose modification time disagrees with EXIF are flagged
- **WHEN** the corpus's photos copied years after they were taken are read
- **THEN** exactly those photos are flagged `mtime_disagrees`

#### Scenario: An unreadable photo
- **WHEN** a photo cannot be opened by the job
- **THEN** its metadata state is `unreadable`, it is not flagged `no_date_metadata`, and its date comes from its name, folder, or modification time

### Requirement: Camera clock offsets are detected
For each camera (make, model, serial), Precious SHALL compare its captures with the other cameras' at the same event folder. A camera whose captures there all lie over 6 hours outside the others', by an offset constant within 10 minutes, SHALL get a suggested shift and its photos in those events the flag `camera_offset`, only when a reference sides with the others: GPS at one event, or GPS or the folder's date at two or more. Otherwise both cameras SHALL be listed as disagreeing.

#### Scenario: R5.2 The camera with a constant clock offset is detected
- **WHEN** the corpus's two events each hold photos of a Sony DSC-W55 whose clock is 1 year 3 hours behind and of a Canon with GPS
- **THEN** the cameras list shows the Sony with the suggested shift +1 year 3 hours for its 12 photos in those two event folders, each flagged `camera_offset`, and the Canon is not flagged

#### Scenario: Two cameras and no reference
- **WHEN** one event holds photos of two cameras a day apart, with no GPS, no folder date, and no other event
- **THEN** both cameras are listed as disagreeing, and neither has a suggested shift

#### Scenario: Two events and no reference
- **WHEN** two events each hold photos of the same two cameras, a constant year apart, with no GPS and no folder date
- **THEN** both cameras are listed as disagreeing, neither has a suggested shift, and no photo is flagged `camera_offset`

#### Scenario: A camera used on another day of a trip
- **WHEN** a folder holds a week of one camera's photos and one day of another camera's, within that week
- **THEN** neither camera is flagged, and no shift is suggested

### Requirement: The owner corrects dates in bulk
`set-date-correction` SHALL record a correction (`set` to a year, month, day, or second; `shift`; `use_name`; `use_folder`) for one entry, up to 1,000 entries, up to 100 folders, or one camera's photos in given folders, at most 50,000 media; `clear-date-correction` SHALL remove it. Effective dates SHALL change in the same transaction. The owner's date SHALL win whatever its age, but not after tomorrow. Bulk requests SHALL skip and report what they cannot apply; single ones SHALL fail with `409 invalid_entry_state`.

#### Scenario: R5.2 One bulk correction fixes all its photos
- **WHEN** the owner applies the Sony's suggested shift to its photos in its two event folders
- **THEN** each of its 12 photos has the effective date of its ground truth, the `camera_offset` flags are gone, and after the next `media` job the Sony is no longer listed with a suggestion

#### Scenario: A scanned print from 1978
- **WHEN** the owner sets `1978` on a scanned photo
- **THEN** its effective date is the year 1978 with source `owner` and precision `year`, and a date organize by `{year}/{month}` refuses it `date_too_coarse`

#### Scenario: Using the name date where there is none
- **WHEN** the owner applies `use_name` to five photos, two of which have no date in their names
- **THEN** three are corrected and two are reported skipped with reason `no_name_date`

#### Scenario: A quarantined target
- **WHEN** a correction names a quarantined photo
- **THEN** it fails with `409 in_quarantine`, and nothing changes

### Requirement: Dates are readable through the API
`GET /api/dates/summary`, `GET /api/dates`, `GET /api/dates/cameras`, and `GET /api/entries/{id}/dates` SHALL answer the totals by source, confidence, metadata state, and flag; a server-paged list of one source's media with filters by flag, source of date, camera, and folder; the cameras with their suggestions and events; and one entry's date with every candidate. Quarantined and missing entries SHALL be left out of every read. Each filter SHALL be served by an index at 2 million entries.

#### Scenario: Listing the flagged photos of one folder
- **WHEN** the owner lists `flag=mtime_disagrees` within `Viagens`
- **THEN** the answer holds exactly the flagged photos below it, with a cursor when more remain, and no quarantined photo
