# Design

## Context

See proposal.md for why. Facts from R1 and R2 that shape this design:
- **Classification is computed only in the scan walk** (`internal/index/scan.go`).
  - Files are classified in `file()`, folders in `finish()`.
  - A folder's composition (`dir_stats.by_family`) and notable list (`dir_stats.inside`) roll up from its children's effective category and `group` flag. A group outside `containers` counts whole.
  - The writer skips rows equal to the stored ones, so an unchanged rescan writes nothing.
  - A rules change takes effect at the next scan and keeps digests (R2 B1).
- **Reads use `entries` columns live.** Search, the treemap, review lists, and Gems read `entries.category`, `family`, `triage`, `is_group`, and `veto`. Review lists are rebuilt only by the `relate` job (`relations.RequestRefresh`), which also runs after every scan.
- **The scan path.** `index.StartScan` is single-flight per source and refuses offline sources. `OnScanDone` runs only after a successful scan. `sources.last_scan_at` changes only on success.
- **Infrastructure gaps.**
  - The job runner has no timers.
  - `clock.Clock` has only `Now`.
  - There is no Settings page; Sources is the settings place (§11.10).
- **The name index.** It is a contentless FTS5 table, `entry_names`, with `trigram case_sensitive 0`, filled with `domain.DisplayName`. Migrations are SQL only, but the driver (modernc v1.60.1) accepts Go scalar functions.
- **Search** computes the copies subqueries for every row it sorts. The duplicate filters are correlated `EXISTS` tests with no driving index. That is why an empty search takes about 1 s and the duplicate filters 1.5–3 s on 158,000 entries.

## Goals / Non-Goals

**Goals:**
- Owner overrides and group marks that give exactly what a scan with that classification would give, with the scanner as the one source of truth for folder figures.
- A scheduler that cannot double-run, skip silently, or miss a catch-up, tested with a fake clock.
- The polish items of the proposal, each testable.

**Non-Goals:**
- These wait for the owner's decisions, asked one at a time, and come in a later change:
  - Gems' sections and what counts as valuable (WhatsApp statuses, stickers and voice notes, thumbnails, downloaded movies, "only in a copy" over lopsided overlaps);
  - the installers card holding whole download folders;
  - the Map's "Duplicated" wording against the cards' redundant bytes;
  - the relation counts in the panel against Compare's buckets.
- A Compare cache across requests. It would lag behind hashing between `relate` passes, and the opening cost is halved without it (D16).
- Accent folding for searches shorter than three characters.
- Decoding tar names. Change feeds. A separate Settings page. Write permission per source (R3).

## Decisions

### D1. Overrides in their own table

```sql
CREATE TABLE entry_overrides (
  entry_id   INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  category   TEXT CHECK (category IN (<the sixteen>)),          -- NULL: the rules decide
  group_mark INTEGER CHECK (group_mark IN (0, 1)),               -- NULL: the rules decide
  updated_at INTEGER NOT NULL,
  CHECK (category IS NOT NULL OR group_mark IS NOT NULL)
);
```

- **What the table holds.** A row exists only while the owner overrides something. Returning both values to the rules deletes the row.
- **`entries` keeps the effective values,** so search, the treemap, review lists, and their indexes are unchanged.
- **Deletion.** The row goes away only with its entry: when the source is removed, or a kind change replaces the entry. A missing entry keeps its row (I4).
- **Rejected:** override columns on `entries`. The scanner writes rows positionally and compares whole rows, so mixed owner and derived values would make every comparison and write path aware of ownership. It would also widen 2 million rows for a few hundred overrides.

### D2. One precedence function in `rules`

`rules.ApplyOwner(res Result, o Override) Result` is the only place where precedence lives:
- **Category.** It replaces the category, then derives family (`FamilyOf`) and triage (`triageOf`), with the veto when triage is `discard` and indicators exist.
- **Group.** The group flag is the owner's mark when set, otherwise `groupCategories[effective category]`.
- **Traits and rule IDs** stay as the rules observed them.

"What the rules would set" is derived on read from the stored `rule_ids`, by the same priority resolution the rules use (`rules.CategoryOf`), so it is never stored twice.

### D3. The scan applies overrides; an override starts a scan

- **Applying in the walk.**
  - At its start, a scan loads its source's overrides into a map keyed by entry ID; there are usually few.
  - `file()` and `finish()` call `ApplyOwner` right after the rules, before `row.classify`, `FileFamily`, and the composition contribution.
  - So composition, notable lists, and every row come out exactly as for a rule result. A rescan with unchanged overrides writes nothing.
- **What a command does, in one transaction:**
  - it upserts or deletes the override row;
  - it writes the target's own effective columns, so the entry reads its new values at once;
  - it starts a scan of the source through `index.StartScan`.
- **When a scan is already active,** the command sets `sources.rescan_requested`. `OnScanDone` clears the flag and starts one more scan. The active scan may have loaded the old overrides, and the follow-up scan converges. A burst of overrides costs one follow-up scan.
- **An offline source** keeps the override, and its figures follow at its next scan. The response says that no scan started.
- **Rejected: patching the ancestors' composition in the command.** It would duplicate the scanner's roll-up rules (group contribution, file families) in a second place, and it still could not patch the notable lists exactly, because only the dominant family's list is stored. A rescan costs 6–12 s on the owner's 158,000 entries, and it is metadata only (I1).

### D4. Group marks are tri-state, folders only

- `group_mark` 1 marks a folder, 0 unmarks a rule group, and NULL follows the rules.
- Everything that reads `is_group` follows: composition, notable lists, Gems' "outside any group" and rescue, and the Map's group label. Rule cards select by category and need no change.
- `set-group` on a file is 400 (owner-intent spec).

### D5. Commands and reads

- **Commands.** `set-category {entry_id | entry_ids | selection_id, category: <one of the sixteen> | "rules"}` and `set-group {…, group: true | false | "rules"}`.
  - They live in `internal/decisions`, beside decisions and tags, and reuse its target resolution, its 1,000-ID limit, its members-are-400 rule, idempotency, and the `category_set` and `group_set` audit events.
  - The response is `{"applied": n, "scan": {"job_id","coalesced"} | null}`.
- **Reads.** The detail's `classification` gains `"owner": {"category": string|null, "group": bool|null}`, `"rules_category"`, and `"rules_group"`. The EntryRow is unchanged.

### D6. Schedules are per source, run by a small loop

- **Storage.** Migration `0003` adds to `sources`:
  - `scan_schedule TEXT`, JSON `{"every":"day"|"week","at":"HH:MM","weekday":0-6,"zone":"Area/City"}`, NULL when off;
  - `next_scan_at INTEGER`;
  - `schedule_skipped_at INTEGER` and `schedule_skip_reason TEXT`.
- **The loop.** `serve` starts it beside `srcs.Run`: every minute it calls `schedule.RunDue(ctx, now)`. For each source with `next_scan_at <= now`:
  - online: `index.StartScan`, which coalesces into an active scan;
  - otherwise: record the skip;
  - in both cases: set `next_scan_at` to the first occurrence after `now`.

  That rule gives one catch-up after downtime and never a double run, and it survives restarts because it is persisted. Tests call `RunDue` with a fake time.
- **Time zones.** They resolve with `time.LoadLocation`. `cmd/precious` imports `time/tzdata`, about 450 KB, so zones resolve on every platform. A local time that does not exist because of a daylight-saving change runs at Go's normalized time, and one that occurs twice runs once.
- **The command.** `set-source-schedule {source_id, schedule | null}` follows `rename-source`, with the `source_schedule_set` audit event. `sourceJSON` gains `schedule`, `next_scan_at`, and `schedule_skipped: {at, reason} | null`.
- **Rejected:**
  - a global cron-style config key. Disks differ, the spec puts the schedule in Settings, and a config edit needs a restart;
  - periodic jobs in the runner. It has no timers, and a loop around the existing single-flight `StartScan` is smaller.

### D7. Accent-insensitive names: rebuild the name index in `0003`

- **The migration.** `0003` drops `entry_names`, recreates it with `tokenize='trigram case_sensitive 0 remove_diacritics 1'`, and repopulates it with `INSERT … SELECT id, precious_display_name(name) FROM entries`. `precious_display_name` is `domain.DisplayName`, registered by `store` as a deterministic SQL function before migrating, so every name, including non-UTF-8 ones, is indexed exactly as the scanner writes it.
- **The tokenizer.** It folds the indexed text and the query alike.
- **The first task** proves with a store test that the bundled SQLite supports `remove_diacritics` for trigram.
- **The fallback.** If it does not, both the scanner and the query fold names in Go (NFD, combining marks removed) into a separate FTS column. The specs are unchanged either way.
- **Short searches** (under three characters) keep the existing substring scan, without folding.

### D8. Search: a two-stage page, driving filters, a separate count

- **The page.** It first selects only entry IDs, with the filter and the sort, and limits them. A second statement reads the full columns, including copies, for those IDs alone.
- **New indexes** go in their own migration, `0004_search.sql`, which the Search slice writes after measuring the plans: candidates are `entries(total_bytes, id)`, `entries(source_id, total_bytes, id)`, and `entries(source_id, path) WHERE state = 'unreadable'`. Each one also costs every scan's writes, so only those that a plan test proves necessary are added, and `walkbench` confirms that the scan does not regress (R1.14). (Decided while applying task 1.2: the foundation adds no speculative index.)
- **Driving the filters:**
  - `dup=copies` drives from the contents with two or more copies (entries and members), and keeps the exact `otherCopy` test on those candidates only;
  - `dup=unique` and `dup=unchecked` drive from `file_content_by_source(source_id, state, size)`.
- **The count** moves to its own request: `GET /api/search?…&count=only` returns `{"count"}`. The UI shows "Counting…" until it arrives.
- **Target.** A slow test seeds 2 million entries, as R1.10 does, and checks the spec's first-page (1 s) and count (2 s) targets at p95 without the race detector, like `TestRelateAtScale`.
- **Rejected:** a stored duplicate-state column. It would have to be maintained by every hashing commit across sources.

### D9. Search and Map presentation (UI)

- **Search rows** show the parent folder under the name in muted text, cut from the left, and prefixed by the source label when all sources are searched. The root row shows the source label.
- **The Duplicated cell of a checked file** says "3 copies" or "No other copy". Folders keep their percent.
- **Unreadable rows** (`state` unreadable) say "Could not be read" and show "—" for size, files, and duplicated.
- **Search gets a `state=unreadable` filter** (server: parse, validate, filter, driven by the partial index of D8). Home's partial notice links to it, with the source.
- **Map columns.** Decision hides before Duplicated. The Decision cell is blank for an entry with no own decision whose effective decision is undecided, and short otherwise ("Discard ↑", "With archive").
- **Map keys.** The Map table gets the review list's key model: up and down arrows move the selection, Enter drills into a folder or opens the viewer for a file, and Escape closes the panel. Keys are ignored in fields and dialogs.
- **Path collapse.**
  - The detail's ancestors gain `"only_child": bool`, true when the ancestor holds nothing but the next ancestor (one indexed count per ancestor).
  - The path merges runs of them into one step.
  - `GET /api/entries/{id}` gains `"only_folder": id | null`, which the Map follows from a source's start, with a replace navigation.
- **The remembered source.** It lives in `localStorage` (key `precious.source`), written by the source choice and read by Home, Opportunities, Gems, Search, and the Map's start. A `source` in the URL wins.
- **Dialog focus.** `Dialog` stores the focused element when it opens, and gives focus back to it on close if it is still in the page.
- **The event stream.** On its first open, it refreshes only queries older than the stream, with `cancelRefetch: false`, which ends the duplicate GETs on every load. Reconnects still refresh everything.
- **The treemap's remainder block** reads "N smaller items: see the table" and, when clicked, focuses the table sorted by size.

### D10. Archives that were not opened

The detail's `archive` stays null for those files. A new `"archive_note": "unsupported" | "nested" | "not_listed" | null` gives the reason:
- `nested` for a member whose name classifies as an archive;
- `unsupported` for a known archive extension Precious does not open (7z, rar, and others kept in `domain`);
- `not_listed` for a supported archive without a listing.

The panel words each reason, and says that what is inside is not checked for copies. A complete archive's main button is "Open as a folder", and the viewer's Open stays secondary.

### D11. Compare: both paths, extra copies, one computation on open

- **Paths.** `CompareItem` gains `LeftPath` and `RightPath`, each side's path inside that side; the UI shows both when they differ.
- **Extra copies.** An identical item with one side gets `Twin`, the ref of the file on the other side holding the same content. The UI shows "extra copy, same as …".
- **One computation.** With no `bucket`, the server picks the first bucket holding files, in R2 B22's order, and returns its items and its name. The page uses it and stops making the summary-only request.
- **Counts and bytes are unchanged.**

### D12. Similar folders: a read of the visible generation

- **The endpoint.** `GET /api/relations?kind=overlap&source=&cursor=&limit=` reads `relations` of the visible generation, ordered by matched bytes, then ID, descending. It uses the RelationJSON of the panel with `self` `a`, and filters by source on either side. 280 rows on the owner's data need no index.
- **Where it shows.** The Opportunities screen links to `/opportunities/similar`, a read-only list with Compare links.
- **Rejected:** a review list with a card. Overlaps are not copies, and the spec gives them no bytes to free.

### D13. Cards count their decided rows

- `review.Cards` sums open and decided rows in one pass over `review_rows` with `openSQL`, so it runs no second query.
- `CardJSON` gains `decided_rows` and `decided_bytes`. Cards and list headers show "· N decided (X)" and, when nothing is open, "Nothing left to review".
- The R2.5 test extends to decided equals the decided list.

## Interfaces

- **Migration `0003_owner.sql`:**
  - `entry_overrides` (D1);
  - `sources` columns `scan_schedule`, `next_scan_at`, `schedule_skipped_at`, `schedule_skip_reason`, and `rescan_requested INTEGER NOT NULL DEFAULT 0`;
  - the `entry_names` rebuild (D7);

  Schema version 3. An R2 database upgrades in place with no rescan.
- **Migration `0004_search.sql`:** the search indexes of D8. Schema version 4.
- **Commands:**
  - `set-category` and `set-group` (D5), audits `category_set` and `group_set`;
  - `set-source-schedule` (D6), audit `source_schedule_set`.
- **Read API:**
  - `classification.owner`, `rules_category`, `rules_group`; `archive_note`; ancestors `only_child`; `only_folder`;
  - `sourceJSON.schedule`, `next_scan_at`, `schedule_skipped`;
  - `GET /api/search?…&count=only` and `state=unreadable`;
  - `compareItemJSON.left_path`, `right_path`, `twin`, and a top-level `bucket` when the request has none;
  - `GET /api/relations?kind=overlap`;
  - `CardJSON.decided_rows` and `decided_bytes`.

## Risks / Trade-offs

- **[Rebuilding the name index takes long on a large index at upgrade]** → The slow test measures the `0003` migration over 2 million names, and the operator docs state the time. On the owner's 158,000 entries it takes seconds.
- **[The bundled SQLite lacks trigram `remove_diacritics`]** → The first task proves it with a store test. Otherwise use the Go folding fallback of D7.
- **[Every override costs a full rescan]** → Rescans read metadata only and coalesce. At 2 million entries one takes minutes, which is acceptable for an action the owner takes rarely and in bursts. The entry itself updates at once.
- **[An override set during a scan is briefly reverted by that scan]** → `rescan_requested` runs one more scan, which converges.
- **[A scheduled scan runs while the owner works]** → Scans are reconciliation-class jobs that yield, and pages keep R1.10's targets during a scan.
- **[Time zones and daylight saving]** → Tests cover a daylight-saving gap, a repeated hour, and a zone with no daylight saving.
- **[The count arrives after the page]** → The UI shows "Counting…" and never shows a wrong number.

## Migration Plan

1. Take a backup with `precious backup`, then install. `0003` applies at start: it creates tables and columns, and rebuilds the name index (seconds per 100,000 names).
2. Overrides and schedules start empty. No rescan is needed.
3. **Rollback.** The R2 binary refuses schema version 3, so restore the backup.

## Addendum: decisions made during implementation

- **C1.** `twin` is `{path, entry}`: the twin's path inside its side and its EntryRow (task 6.1). A bare ref could not name the file, and the extra copy has no path on the twin's side otherwise. The twin is the other side's first file of the content in path order, the one the first pair holds.
- **C2.** `GET /api/compare` always returns `bucket`, also when the request names one (task 6.1). Two empty sides open on `only_left`, as the UI's former `firstBucket` did.
- **C3.** The Compare page keeps B22's replace navigation without a second request (task 6.1): it remembers the group the server opened on, keeps reading the page fetched without a group while the address names that group, and pages it with the group and cursor of the page itself, so a later page never depends on the server choosing the same group again.
- **C4.** `review.Cards` reads `openSQL` once per row through a `MATERIALIZED` CTE (task 6.3); without it SQLite's flattener copies the expression into each of the four sums. A row whose `openSQL` is NULL (its entry gone) counts as neither open nor decided, as in `Rows`.
- **C5.** Every card with no open row reads "Nothing left to review", decided rows or not (task 6.3), and the decided figure leaves out its bytes when they are 0 ("3 decided"), as B25 does for open rows.
- **C6.** The similar folders endpoint pages by `(matched_bytes, id)` with an opaque cursor, and leaves out an item whose side was deleted since the relate pass (task 6.2). A member folder side counts on its archive's source.
- **S1.** The scheduler is `internal/schedule`: `schedule.New(store, runner, sources, clock, log)` with `RunDue(ctx, now)` and `Run(ctx)`. `Run` calls `RunDue` at once and then every minute; `serve` stops it before the job runner, since it starts scans on the runner.
- **S2.** `RunDue` refreshes availability (bounded at 10 s) before deciding, but only when a source is due, so a disk unplugged since the last one-minute refresh is skipped rather than refused by `StartScan`. It then reads the due sources again under the writer lock, and starts or joins each scan and moves its `next_scan_at` in that one transaction, which is what makes a due time run once.
- **S3.** A skip records the due time itself (`schedule_skipped_at` is the 03:00 that was skipped, not the minute it was noticed) and the state as the reason. A due time that runs, including one that joins an active scan, clears the skip; `set-source-schedule` leaves it alone.
- **S4.** A stored schedule that no longer validates (a zone the binary does not know) is skipped with the reason `invalid_schedule` and its `next_scan_at` cleared, so it is not retried every minute; setting the schedule again restores it. It is logged as an error.
- **S5.** `set-source-schedule` requires the `schedule` key (`null` turns it off), so a client that omits it does not turn a schedule off by accident. The schedule is decoded strictly and validated before the transaction; `next_scan_at` is computed from the registry's clock.
- **S6.** The card shows the schedule's time as stored ("Daily at 03:00"), naming its zone only when it is not the browser's, and the next scan and the last skip like the other dates (the interface language, the browser's zone).
- **M1.** `only_child`, `only_folder`, and the path collapse count children in every state, as the children listing shows them: a missing item still breaks a chain. A member folder's ancestors get `only_child` too (the archive by its top members, member folders by their children); `only_folder` is computed for entry folders only, since the Map's start begins at a source's top.
- **M2.** The source's top is always its own step in the Map's path, labelled with the source: only the folders below it merge, so `old-disk/home` reads as one step after the source. A merged step that includes the open folder is the current step.
- **M3.** The Map's start follows `only_folder` with one fetch per folder into the entry cache (at most 64), then navigates with replace. A remembered source that no longer exists falls back to the first source.
- **M4.** The Map keys live in `EntryTable` behind a `keyboard` prop, so Search keeps its behavior. Arrow moves replace the address (one history entry per walk, not per row); the row the keys select takes the focus once the virtualizer draws it. Past the last loaded row the down arrow asks for the next page and stays. Enter on a file opens the viewer from the Map itself; Enter on a symlink or special entry does nothing, as the panel has no Open for them.
- **M5.** The domain's unsupported archive extensions are 7z, rar, xz, txz, lz, lzma, zst, z, cab, arj, lzh, lha, ace, cpio, jar, and war, matched on the last extension, ASCII case-insensitively; a test proves the archive package opens none of them. Disk images (iso, img) stay out: they are not archives the owner expects to browse. `nested` applies to a member whose name is an opened or an unsupported archive. `not_listed` applies only to a present file: a missing or unreadable one already says why.
- **M6.** An archive with a listing that stopped (partial, encrypted, damaged, …) also says that what is inside is not checked for copies. For a complete archive, Show in Map now opens the folder holding the archive, since Open as a folder opens the archive itself.
- **M7.** `Dialog` reads its opener once per mount, so React's development double effect does not record the focus inside the dialog. The event stream's "first open" is the first connection after `start()`; a retry after a failed first connection refreshes everything.
- **Q1. `0004_search.sql` holds only `entries_unreadable`.** None of D8's size indexes is added. Measured on a 391,615-entry walkbench of `/usr`, `entries(total_bytes, id)` with `entries(source_id, total_bytes, id)` slowed the scan from about 24 s to 28.5–36.7 s, which fails R1.14. Neither is needed:
  - a page by bytes that `dup` drives merges ranges of the existing `file_content_by_source(source_id, state, size)` (Q2);
  - the first page with no filter reads `entries` once: 0.27 s at 2 million entries, 0.29 s with a source (p95), against the 1 s target.

  `entries_unreadable` is named with `INDEXED BY`, because without statistics SQLite took `entries_by_source_size` or another `(source_id, …)` index and read the whole source (2.7 s at 2 million). With only this partial index the walkbench of `/usr` shows no regression: scan 12.5 / 15.2 / 21.4 s with it against 16.4 / 14.4 / 20.4 s without, alternating runs on a shared, noisy machine (walk/scan ratios 8.6–17.7 either way; reference about 11.6).
- **Q2. A `dup` page by bytes merges size-ordered ranges.** Each (source, content state) range of `file_content_by_source` is in size order. `file_content.size` is the entry's size: hashing copies it at insert, and the scan drops the row when the file changes. A file's `total_bytes` is its size, so the arms of a `UNION ALL … ORDER BY size, entry_id` compound are each in the page's order. SQLite merges them (`MERGE (UNION ALL)` in the plan) and stops after the page, with the exact copy test on the rows read only. Other orders, and `dup` combined with a stronger driver (name, Within, tags, unreadable), select the matches and sort them.
- **Q3. Counts.** `copies` and `elsewhere` count from the contents with two or more file rows or an archive member, grouped as they stream over `file_content_by_content` (D8). `unique` and `unchecked` read their (state, source) ranges in turn, with the states that need no copy test first and `hashed` last, so a capped count stops in the cheap ranges. A first version that drove `unique` through `state IN (…)` read `hashed` first, the order SQLite gives an `IN` list, and counted in 1.08 s.
- **Q4. `state` takes only `unreadable`,** single-valued, and it drives before a name: there are few unreadable entries. In a selection's JSON it is `"state":"unreadable"`. Entries that could not be read are folders. The scanner marks no file entry unreadable: a file it cannot read is a content state, which `dup=unchecked` finds.
- **Q5. `count=only`** takes the same parameters as the page and ignores `cursor` and `limit`. Its only value is `only`, given once (`400` otherwise). A page carries no `count` any more. `TestDupFilter` changed one line, because it read the removed `Result.Count`; it now reads the number of rows.
- **Q6. The 2-million-entry slow test** (`TestSearchStaysFastAt2MillionEntries`, `internal/web/api`) generates its index in SQL, as R2's review test does, instead of scanning: two sources of 1,000 folders of 999 files. By file: 50% unique by size, 5% sampled, 25% hashed with a copy on the other source, 5% hashed unique, 15% unchecked. Ten unreadable folders per source. It times through the handler, 20 runs each. Figures without the race detector, p95:

  | Search | First page | Count |
  |---|---|---|
  | no filter | 274 ms | 0.4 ms |
  | `dup=copies` | 11 ms | 52 ms |
  | `dup=unique` | 12 ms | 40 ms |
  | `dup=unchecked` | 8 ms | 38 ms |
  | `state=unreadable` | 1 ms | 0.2 ms |

  With `source=a`, the same searches take 286 ms, 12 ms, 11 ms, 8 ms, and 1 ms for the first page, and 1 ms, 66 ms, 42 ms, 38 ms, and 0.2 ms for the count. `within=<root of a>&dup=elsewhere` takes 11 ms and 75 ms. Over all 220 pages, p95 is 220 ms with a maximum of 301 ms; over all 220 counts, p95 is 61 ms with a maximum of 75 ms.

  The test skips under the race detector (`raceEnabled`, new in package `api`) and runs in `make test-slow`'s second pass.
- **Q7. The UI's remembered source** is read through `useSourceParam` (`web/ui/src/lib/sourceParams.ts`):
  - `?source=` wins, and an empty value means all sources;
  - otherwise it uses the remembered source, unless the sources list has loaded without that source, so a removed source cannot leave a screen stuck on it;
  - `SourceFilter` writes the choice;
  - a link that names a source, such as Home's partial notice, does not change the remembered choice.
- **Q8. Schema version 4.** The R2 binary now refuses `version 4`. The upgrade notes say so.
- **O1.** `rules.Result` gains `Folder` and `Indicated` (user material below), set by `ClassifyFolder`, so that `ApplyOwner(res, o)` keeps its D2 signature and applies the group default and the veto to folders only. `Policy.Recall(ruleIDs, folder, indicated)` rebuilds the rules' whole result from stored rule IDs with the same tally `classify` uses; `CategoryOf` is its category. A rule ID the policy does not know is ignored.
- **O2.** The follow-up scan of D3 is the same job run once more: the scan that ends with `rescan_requested` set clears it in its finishing transaction and returns `jobs.Defer{Until: now, Reason: "rescan_requested"}`, because a second scan cannot be queued while its own job is active (`jobs_one_active_scan`). Only the last pass runs `OnScanDone`. A command sets the flag only when it joins a running scan (a queued one reads the overrides when it starts). Residual window: an override committed between a scan's finishing transaction and the runner recording the job's end leaves the flag set; that disk's next scan applies the override and runs one extra pass.
- **O3.** `decisions.New(clk, pol, scans)` takes the policy (for the write-through) and a `ScanStarter`; serve passes `index.StartScan`. decisions cannot import index, because index's tests import decisions.
- **O4.** `set-category` accepts files and folders only (a symbolic link or special file is `invalid_request`), since the scan classifies nothing else. Targets in several sources start a scan of each online one; the response's `scan` is the first source's. Audit `old` uses `rules` for no override (as `inherit` for decisions); a group's values are `true`, `false`, or `rules`.
- **O5.** The classification scenario "the owner corrects a folder's category" says the folders above count `site_antigo` as personal. Under D2, `documents` is no group category, so without a mark the folder adds its own composition: its `.git` internals count under containers. The test asserts that composition exactly; marking the folder as a group as well counts it whole as personal.
