# Tasks

## 1. Foundation (merged first; 1.1–1.3 and 1.5 independent, 1.4 after 1.1, 1.6 after 1.1–1.5, 1.7 after 1.5)

- [x] 1.1 Migration `0008_media.sql`, exactly as in design Interfaces: it rebuilds `actions` and `action_items` (items dropped first, IDs kept, every 0007 index recreated) with the new kinds, op, and columns, and adds `media_meta`, `media_dates` with its seven indexes, `date_corrections`, `media_cameras`, and `media_sources`. Owns `migrations/`, `internal/store/*schema_test.go`, and the `docs/operator.md` section "Upgrading from R4". Verify with a store test:
  - a version-7 database with actions, items, and a purge check migrates with every row and ID kept;
  - each new CHECK rejects a bad value: `set_mtime` without `new_mtime_ns`, `prev_mtime_ns` on a rename, a `shift` without `shift_s`, a `set_local` of length 8, an offset on a date-only `set`, `offset` without a suggestion, flags 16, flag 8 with `meta_state` `pending`, and `dirty` 2;
  - deleting an entry cascades its media rows and nulls `copy_of`, and every foreign key into `entries`, `sources`, and `actions` is indexed.

  Run `go test -race ./internal/store`.
- [x] 1.2 `fsaccess.Writer.SetModTime` (design D12): Linux (`utimensat` on the folder's descriptor, `UTIME_OMIT` access time, `AT_SYMLINK_NOFOLLOW`), portable (refuses), synthfs (resolution, local time, change time now, and `EPERM` for a file marked foreign), instrument (`OpSetModTime`), the package doc, and the guard test's fixture. Owns `internal/fsaccess/...`. Verify with synthfs unit tests and an `e2e && linux` test on a tmpfs:
  - the time is set, the access time and bytes are unchanged, and the change time advances;
  - a symlink's own time is set, never its target's;
  - an absent name is absent, a read-only remount gives `ErrReadOnly`, and a file owned by another user, set by an unprivileged process, gives `ErrPermission`;
  - on a synthfs FAT device, the time is truncated to 2 s and read back in its zone.

  Run `go test -race ./internal/fsaccess/...` and `scripts/e2e-docker.sh`.
- [x] 1.3 `[dates] time_zone` (design D7): `config.Dates`, `Location()`, validation naming `dates.time_zone`, and `check-config` printing the effective zone and warning when it is unset. No zone-database work: `cmd/precious` already embeds it. Owns:
  - `internal/config` and `cmd/precious/checkconfig.go`;
  - `time_zone` set in `deploy/examples/precious.toml` and `deploy/compose.precious.toml`;
  - the `docs/operator.md` Configuration reference entry, with the FAT mount-zone note.

  Verify with tests of "An unknown zone" and "An unset zone"; the existing `cmd/precious/tzdata_test.go` covers "A zone on a host without zone files". Run `go test -race ./internal/config ./cmd/precious`.
- [x] 1.4 The index side (design D3, D11, D15):
  - `stDropMedia` beside `stDropContent`;
  - `keepContent` also carries `media_meta`;
  - `intentCond` counts a `date_corrections` row;
  - `index.ModTime` and `ApplyModTime`.

  Owns `internal/index` and the `docs/operator.md` paragraphs "Rescans", "Moves made by Precious", and "Missing entries" (a correction keeps a missing row). Verify with tests:
  - a rescan of a changed file deletes its `media_meta` row in the transaction that updates `entries`, and keeps its `date_corrections` row;
  - `MoveEntry` keeps a matching `media_meta` row with the new change time;
  - a missing entry with only a correction is reported by `MissingIntentAt` and `IntentBelow`, and `MoveEntry` onto its path fails with `ErrMissingIntent`, keeping the correction;
  - `ApplyModTime` writes the times and own `newest_ns`/`oldest_ns`, carries matching `file_content`, `archives`, and `media_meta` rows, leaves non-matching ones, and refuses a folder;
  - a rescan right after finds nothing changed.

  Run `go test -race ./internal/index`.
- [ ] 1.5 `internal/media`, pure, standard library only (design D1, D2, D5–D8, D16, D17's names), exactly the Interfaces signatures. Owns `internal/media`. Verify with tests:
  - table tests per format from hand-built bytes: JPEG APP1; TIFF big- and little-endian; ORF and RW2 headers; HEIC and AVIF `iloc`; CR3 `CMT` boxes; MP4 with `moov` first and last; MOV;
  - bounds: at most 1 MiB read, counted with a recording `ReaderAt`; loops, offsets outside the file, and huge counts give no value;
  - native fuzz targets per parser with checked-in seeds, run as seeds by `go test`; a long randomized property run behind the `slow` tag;
  - `Derive`: every row of D5's table; implausible defaults; refinement and its tolerance on `LocalTime`; `use_name` and `use_folder`; `shift`, including one that crosses 1990; `set` to 1978 and to each precision, never skipped; `none`;
  - flags: `implausible` from an EXIF default and never from a 1980 modification time; `no_date_metadata` for `read` and `none` only, never for `pending` or `unreadable`;
  - `NameDate` and `FolderDate` for each D6 pattern, plus near misses (`fotos_2005_do_pendrive`, `IMG_20151399_…`); `ParseLocal` for each set form;
  - `Detect`: the corpus's shape (two events, the Canon's GPS: the Sony `offset`, the Canon `ok`), one event backed by the other camera's GPS, one event backed by own GPS, one event with only a folder date (`disagrees`), two events with only folder dates (`offset`), two events with no reference (both `disagrees`, no suggestion), a two-camera tie, inconstant candidates, a camera used on one day of a week-long trip, photos on the 1st of the next month in a `2010-07 X` folder, own GPS constant and scattered, and an offset of 5 h (not detected); `offset` events are exactly the folders flagged;
  - templates (tokens, `{event}` stripping, invalid templates), `RenamedName` idempotence, and `ErrTooCoarse`;
  - `InputsKey` changes with each input except `Now`; `ZoneKey` differs for two zones that are both named "Local".

  Run `go test -race ./internal/media` (under 60 s).
- [ ] 1.6 Shared contracts for the slices (design Interfaces), owning only these additions:
  - `content.Opener`, with `OpenAt` and the hashing job moved onto it unchanged;
  - `executor.Index.ApplyModTime`, implemented in organize's adapter (`index.ApplyModTime` + `Refold`);
  - the `set_mtime` op constant and `new_mtime_ns`/`prev_mtime_ns` in the executor's item, ending `failed` (`unknown step`) until 2.8, as r4 F6;
  - `internal/dates`: `KindMedia`, `Options`, `New`, `MediaCond`, `EnqueueMedia` with D4's protocol, `Targets`, `Validate`, `ExpandTargets`, and `Rederive` with its summary adjustment;
  - `dates.New` constructed in `cmd/precious/serve.go`, and `organize.Options.Dates` (additive) set from it there;
  - `internal/dates/helpers_test.go` (an env with store, synthfs, sources, and runner; the corpus built once per package) and `internal/dates/datestest.Seed`. Slices A and B only add to them.

  Verify with tests:
  - `MediaCond` and `media.IsMediaKind` agree on every file kind and on quarantined, missing, and member rows;
  - `Validate` and `ExpandTargets` for each form: string IDs, a member ref (400 single, `not_media` bulk); folders expand to media below, in path order, outside the quarantine; `camera_key` takes that camera's photos directly in the folders; a quarantined target is `in_quarantine`; two sources and over `max` are 400;
  - `Rederive` writes, skips an unchanged key, deletes the rows of missing, quarantined, and non-media entries, keeps the camera bit, clears it for `set` and `shift`, and keeps the summary equal to a full recount;
  - `EnqueueMedia`: sets `dirty`; a request while no job is active creates `media:<src>`; while it is queued, joins it; while it is `running`, also creates `media-next:<src>`, once; both carry `{"if_dirty":true}`; offline sources are requested too;
  - the adapter refolds the by-year figures after `ApplyModTime`;
  - the hashing tests still pass.

  Run `go test -race ./internal/content ./internal/executor ./internal/organize ./internal/dates ./cmd/precious`.
- [ ] 1.7 The corpus (design D19): the EXIF and MP4 writers, a PNG, the fixtures of D19's table, `DateTruth` for every image and video, `Cameras` with the Sony's two event folders, the 2009 WhatsApp images declared by hand, the independent rule for the other older media, `web/ui/e2e/env.ts`, and every test whose hard-coded corpus numbers change. Owns `internal/corpus`, `tools/gencorpus`, and `web/ui/e2e/env.ts`. Verify:
  - the writers round-trip through `media.Read` (test only; the truth never imports `internal/media`);
  - `TestSizeAndDates` (10–40 MiB, 2003–2012), `TestDeterministic`, `TestBuildersAgree`, and `TestCorpusMatchesGroundTruth` (JSON round-trip of the new fields);
  - `Fotos`, `Fotos - Copia`, `Midia`, and the pendrive copies are byte-identical to before.

  Run `go test -race ./...`.

## 2. Parallel slices (after group 1)

Each slice owns its implementation task and the test tasks labelled with its letter. A test task depends only on its own slice and group 1. Corpus tests in `internal/dates` use the shared helper's corpus, so the package stays under about 60 s.

- [ ] 2.1 Slice A, the `media` job (design D3, D4, D8, D9, Concurrency). Owns:
  - `internal/dates/job*.go`, `plan.go`, `read.go`, and `cameras.go`;
  - the `cmd/precious/serve.go` lines for `Register`, `DeferWhile(executor.OrganizeActive)`, `AfterScan` in `OnScanDone`, and `Startup`;
  - the `ActionDone` line in `internal/organize/index.go` that calls `EnqueueMedia`;
  - the `docs/operator.md` section "Media dates": when the job runs, what it reads, the sources of a date, precision and refinement, the flags, the cameras and the detection's blind spot, and progress.

  The job covers D4's lifecycle (the start transaction with `passes_job`, `if_dirty` on first attempts, the clear-and-reread loop, the failure transaction), the passes (3–4 only when the source cannot be opened), the I9 commit against the identity loaded at the read's start, the summary rewrite, the cameras write at the end of every pass, deferral, quarantine exclusion, and progress. Verify: `go test -race ./internal/dates ./internal/organize ./cmd/precious`.
- [ ] 2.2 Slice A: job tests, owning `internal/dates/job_test.go`:
  - "A malformed file is read safely" (a random-byte `.jpg`, and a truncated JPEG), and "Reading leaves the disk untouched" (instrument: no Writer call);
  - "A rescan reads nothing again" (rescan and a `MoveEntry`: zero reads), and "A file changed during its read" (a hook edits the file between the read and the commit: dropped, read after the next scan);
  - a `set_mtime` outcome (`ApplyModTime`) between the read and the commit drops the result, and the carried row is read exactly once more;
  - on a synthfs FAT device whose index times differ from the disk's within tolerance, every read is applied;
  - D4's races, with hooks between the passes and a recording instrument:
    - "A request while the job is ending": a hook requests the job between its end read and its return; the follow-up runs the passes after it, and no file is read twice;
    - a request during each of passes 1–4: the same job loops once more, and the follow-up then ends without running;
    - the follow-up starting while the first job holds `passes_job`: it defers, without using an attempt;
    - a request while the follow-up runs: a new `media:` job defers until it ends, then runs only if `dirty` is set;
    - "A job that fails with a request pending": a hook fails the job, and another cancels it, after a request; `dirty` is set afterwards, `passes_job` is released, the follow-up runs the passes, and with no follow-up the next request does;
    - a retry (`Attempt` 2) with `dirty` clear runs the passes; a first attempt with `dirty` clear runs nothing;
  - deferral while an organize job is queued;
  - quarantined photos are never enrolled or opened;
  - a correction committed during the cameras pass: the pass still writes; the loop runs again and its write leaves the corrected photos unflagged;
  - on an offline source, a correction's request recomputes the cameras without opening anything.
- [ ] 2.3 Slice A: test of R5.1, owning `internal/dates/r5_1_test.go`. The corpus on synthfs with zone `UTC` is scanned and its `media` job run:
  - every image and video entry's date, precision, source, refinement, and flags equal its `DateTruth`;
  - `mtime_disagrees` is set on exactly the truth's photos;
  - "An implausible capture date is skipped", and "A folder year that disagrees with the modification time";
  - "An unreadable photo" (a synthfs unreadable file): state `unreadable`, no `no_date_metadata`; and no `pending` photo carries `no_date_metadata` while the job runs.
- [ ] 2.4 Slice A: test of R5.3, owning `internal/dates/r5_3_test.go`. Each WhatsApp-named image without EXIF has source `file_name` and its name's date: precision `day` for the 2012 copies, and refined to the modification time for `IMG-20090612-WA0001.jpg`.
- [ ] 2.5 Slice A: test of R5.2's detection, owning `internal/dates/r5_2_detect_test.go`. On the corpus, the cameras pass lists `SONY|DSC-W55|` as `offset`:
  - its suggestion is +31,546,800 s for 12 photos, with events `Viagens/2010-07 Bahia` and `Viagens/2010-12 Natal`, each with reference `gps`;
  - exactly its 12 photos carry `camera_offset`;
  - the Canon and the Nikon are `ok`.

  Also "Two cameras and no reference", "Two events and no reference", and "A camera used on another day of a trip", on synthetic disks.
- [ ] 2.6 Slice B, corrections and reads (design D10, D11, Interfaces). Owns:
  - `internal/dates/commands*.go` and `reads*.go`, with `RegisterCommands` and `Routes`;
  - their `cmd/precious/serve.go` lines;
  - the `docs/operator.md` sections "Correcting dates" and "Dates API" (commands, reads, errors), and the audit events list.

  It covers `set-date-correction`, `clear-date-correction`, the four reads with `MediaCond`, and the audit events. Verify with tests:
  - each read's JSON on seeded rows, `metadata` included; `source` required by the list; unknown parameters are 400;
  - "Listing the flagged photos of one folder", with a quarantined photo left out of the list, the cameras, the summary, and the entry's dates (`null`);
  - a query-plan guard on analyzed data: the list by each flag, by date source, by camera, and `within`, and `count=only`, each use the index D10 names and no `USE TEMP B-TREE`.

  Run `go test -race ./internal/dates ./cmd/precious`.
- [ ] 2.7 Slice B: test of R5.2's correction, owning `internal/dates/r5_2_correct_test.go`. The corpus is seeded with `datestest.Seed`, and the Sony's camera bits are set as detection would. Then one `set-date-correction` with `{folder_ids: [Bahia, Natal], camera_key: "SONY|DSC-W55|"}` and `shift` 31,546,800:
  - it applies 12;
  - each Sony photo's effective date equals its truth plus the shift, which is the Canon's timeline;
  - `camera_offset` is clear;
  - `media_sources.dirty` is set and a `media` job is enqueued.

  Also, owning `internal/dates/corrections_test.go`:
  - "A scanned print from 1978"; a `set` after tomorrow is 400; a shift that would land after tomorrow skips `in_future`;
  - "Using the name date where there is none";
  - "A quarantined target";
  - a single target that is not media (409 `invalid_entry_state`), and a member ref (400);
  - "Corrections survive a rescan", through the scanner, with one photo edited;
  - "A correction is audited";
  - `clear-date-correction` restoring the derived date.

  Run `go test -race ./internal/dates`.
- [ ] 2.8 Slice C, the executor's `set_mtime` (design D12, D13). Owns `internal/executor` and these `docs/operator.md` sections, for file dates:
  - "How Precious changes a disk", and "Recovery after an interruption";
  - "Allowing changes in the deployment": the files to date must be owned by the service account, or the deployment opts in to `CAP_FOWNER` (systemd `AmbientCapabilities=`/`CapabilityBoundingSet=CAP_FOWNER`, Compose `cap_add: [FOWNER]`), with its risk stated. After changing ownership, rescan the source before setting file dates: `chown` advances every file's change time, so until a rescan every item ends `changed` (and the rescan hashes and reads the media again).

  It covers intent re-checks (`in_quarantine`, `hard_link`, `no_change`, undo's written-time check), the step (identity with change time, link count, journaling `prev_mtime_ns`, no folder sync), its error mapping (`not_owner`), settle, reconcile, and an outcome with `ApplyModTime` and `MarkStale`. Verify: `go test -race ./internal/executor`.
- [ ] 2.9 Slice C: test of R5.4 at run time, owning `internal/executor/r5_4_test.go`. Actions are driven by inserting `queued` rows on a synthfs corpus with hashed photos:
  - "Setting a time changes nothing else": instrument shows one `SetModTime` and no other Writer call; the bytes re-hashed with `content.HashEntry` are equal; `file_content` keeps its `content_id`; a hashing job after reads nothing;
  - the undo item restores each journaled previous time, exactly on a FAT device whose index time differs within tolerance;
  - "A file changed since is skipped": an undo after a hand edit ends `changed` with nothing written, and the rest run;
  - "A file changed before its time is set", including an in-place edit with the same size and the modification time put back: `changed`, and `file_content` and `media_meta` keep their old identity;
  - a hard link made after the scan: `refused hard_link` at the step, nothing written;
  - "A file the service does not own" (a synthfs foreign file): `failed`/`not_owner`, the others done, writes on;
  - "Crash after setting the time, before recording it" (hooks), a crash before it, and a crash between journaling and the call;
  - "A FAT card rounds the time";
  - "A rescan after writing back".

  Run `go test -race ./internal/executor`.
- [ ] 2.10 Slice D, organize plans (design D14, D16, D17, Interfaces). Owns `internal/organize` (not the `ActionDone` line) and the `docs/operator.md` sections "Organizing" (Set file dates, Organize by date), "Exporting an action" operations, and the item reasons table. It covers:
  - `plan-set-mtime` with `Rederive` first, and `plan-undo`'s `set_mtime` reversal to `prev_mtime_ns`;
  - `plan-date-organize` with `Rederive` first: the template, folder resolution, renames, collisions, suffixes, `identical_copy` with `copy_of`, `not_dated_yet`, siblings, and `summary`;
  - the Action and Item JSON additions, the `op=set_mtime` filter, and `reversible()` and the undo counts.

  Verify: `go test -race ./internal/organize`.
- [ ] 2.11 Slice D: test of R5.5, owning `internal/organize/r5_5_test.go`. The corpus is on synthfs and hashed, seeded with `datestest.Seed`, and the Sony's shift is seeded as a `date_corrections` row. `{year}/{month}` into `Fotos`, for `folder_ids` `Viagens` and `celular_2011`:
  - the preview lists each missing year and month folder once, then one move per file into its truth month;
  - `Sent/IMG-20110416-WA0003.jpg` is refused `identical_copy` with `copy_of` the other copy, and `summary.files_with_copies` counts them;
  - Ana's `IMG_0102.JPG` is planned as `IMG_0102 (1).JPG`.

  It then runs through the R3 executor with a file created at one planned name after planning: that item ends `conflict`, the file is untouched, and the rest are done. Also:
  - "The event token keeps the event name";
  - "A RAW file beside its JPEG" (`split_siblings` 1);
  - "A missing corrected photo keeps its name" (`name_taken_by_missing`, the correction kept);
  - `{day}` on a month-precision photo refused `date_too_coarse`;
  - `rename` applied once.
- [ ] 2.12 Slice D: test of R5.4 at planning, owning `internal/organize/r5_4_test.go`:
  - `plan-set-mtime` over `Viagens/2008-03 Ouro Preto` plans the three capture instants and refuses `DSCN0004.JPG` `date_too_coarse`;
  - a file already at its date counts in `unchanged`;
  - FAT truncation, and `hard_link`;
  - "A date known only to the year", "A photo not read yet", and a quarantined target answered `409 in_quarantine` by both plans;
  - a photo renamed by Precious since the last `media` job is planned from its new name's date;
  - `plan-undo` of an action whose items were set `done` builds `set_mtime` items to each `prev_mtime_ns`, and refuses one whose index time changed (`identity_changed`);
  - the export names `set_mtime`.

  Run `go test -race ./internal/organize`.
- [ ] 2.13 Slice E, the interface (design D10, D21, Interfaces), built against the contract with stubbed responses. Owns `web/ui/src` and the `docs/operator.md` interface paragraphs (Dates, the detail panel, History, the top bar). It covers:
  - the Dates page and navigation item, for the chosen source: summary with metadata states and the time zone (with a notice when unset), live `media` progress, cameras with "Shift … — N photos" (preview of the events' photos, then confirm, sending their `folder_ids` and `camera_key`), and the list with filters, its metadata state, and multi-select on the page;
  - bulk corrections (set to a year, month, day, or time; shift in years, days, hours, and minutes; use name; use folder; clear);
  - "Set file dates…" and "Organize by date…" (template, destination, rename), both through `PreviewDialog`, with `mtime` from→to lines, the dedupe notice linking to the duplicates list, the siblings warning, and "Discard these copies" after a confirmation;
  - the detail panel's Dates section, from `GET /api/entries/{id}/dates`;
  - History titles, ops, reasons (`not_owner` explained), and Undo for both kinds;
  - the `media` job kind in `api/jobs.ts` and the events;
  - the catalog.

  Verify: `cd web/ui && npm run -s lint && npx vitest run && npm run -s build`.
- [ ] 2.14 Slice E: interface tests (Vitest), owning `web/ui/src/**/*.test.tsx` for these:
  - "R5.2 A camera's suggested shift in one confirmation" (the body sent is `{folder_ids: [<Bahia>, <Natal>], camera_key: "SONY|DSC-W55|", correction: {kind: "shift", shift_s: 31546800}}`);
  - "R5.4 Setting file dates from the screen" (old and new times, refused reasons, nothing run before confirming);
  - R5.5's display: the dedupe notice, the `identical_copy` items, the siblings warning, and "Discard these copies" sending `set-decision` only after confirmation;
  - "A photo with a disagreeing modification time" in the panel, and an unreadable photo showing its state;
  - no banned term.

  Verify: `npx vitest run`.

## 3. Integration

- [ ] 3.1 Playwright tests after the last R4 test ("Home shows what is in quarantine beside the decisions"), with `[dates] time_zone = "UTC"` in `web/ui/e2e/global-setup.ts`:
  - R5.1 and R5.3: the Dates screen's totals, and the `Viagens` and `celular_2011` rows equal their truth, flags included;
  - R5.2: the Sony's suggestion, the shift, its 12 photos at their truth's corrected dates, and no suggestion after the next job;
  - R5.4: "Set file dates…" on `Ouro Preto`, the files' mtimes on disk, History, then Undo restoring them;
  - R5.5: "Organize by date…" of `Viagens` and `celular_2011` into `Fotos`, the preview, "Discard these copies", the suffix, the run, and the disk.

  Verify: `npx playwright test` passes headless.
- [ ] 3.2 Full verification:
  - gofmt, `go vet ./...`, and `go vet -tags e2e,slow ./...`;
  - `go test -race ./...`, with no package over about 60 s;
  - `make test-slow`;
  - `make cross`;
  - UI lint, Vitest, and build;
  - Playwright;
  - `scripts/e2e-docker.sh`;
  - `openspec validate --all --strict`.
- [ ] 3.3 A smoke check of the built binary, run as an unprivileged user, with a throwaway script on a copy of an r4 database:
  - first, check that the user owns the smoke's photo files (`stat -c %U`), and that a file it does not own ends `not_owner`;
  - the migration applies, and History keeps its actions and items;
  - the `media` job reads a photo folder;
  - a correction;
  - a write-back and its undo, with the digests unchanged;
  - an organize by date and its undo;
  - a rescan finds nothing new or changed.

  Then delete the script.
- [ ] 3.4 Deploy on the reference server, after a database backup:
  - set `[dates] time_zone` to the owner's zone;
  - make the corpus source's files owned by the service user (corpus only), as R3 did for its folders, and check it;
  - rescan the corpus source after the `chown` and before any plan, and let hashing and the `media` job finish: the change times moved, so every file is hashed and read again.

  The owner tries, on the corpus source only:
  - the Dates screen and the Sony's shift;
  - a write-back and an undo;
  - an organize by date.

  He signs off, recorded in the design addendum.
