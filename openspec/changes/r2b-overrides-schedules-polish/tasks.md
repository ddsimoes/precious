# Tasks

## 1. Foundation

- [x] 1.1 Prove with a store test that the bundled SQLite's trigram tokenizer accepts `remove_diacritics 1`, and that `confraternizacao` matches `Confraternização 2018`. If it does not, record the Go-folding fallback (design D7) as a decision before 1.2.
- [x] 1.2 Write migration `0003_owner.sql` (design Interfaces):
  - `entry_overrides`;
  - the `sources` schedule and `rescan_requested` columns;
  - the `entry_names` rebuild through a `precious_display_name` SQL function that `store` registers.

  The search indexes moved to `0004_search.sql` with task 4.2 (design D8).

  Verify with an upgrade test from an R2 database:
  - schema version 3, rows kept;
  - every name indexed as the scanner writes it, a non-UTF-8 name included;
  - the schema-version tests updated.
- [x] 1.3 Add the domain types `Override` and `Schedule` (parse, validate, next occurrence after a time in its zone). Verify with unit tests: daily, weekly, a daylight-saving gap, a repeated hour, a zone without daylight saving, and malformed times, days, and zones.
- [x] 1.4 Write the R2 → R2b upgrade and rollback notes in `docs/operator.md`. Verify with the docs tests.

## 2. Owner overrides and group marks

- [ ] 2.1 Implement `rules.ApplyOwner` and `rules.CategoryOf` (design D2). Verify with tests:
  - an owner category sets family and triage;
  - the veto still applies to an owner discard category;
  - all three group states;
  - `CategoryOf` gives the rules' own result for a conflict and for no match.
- [ ] 2.2 Apply overrides in the scan walk (D3) and in the `indextest` mirror. Verify with scan tests:
  - an owner category and a group mark change composition and notable lists exactly as a rule result would;
  - a second unchanged scan writes nothing;
  - the seed and scan comparison holds.
- [ ] 2.3 Add `set-category` and `set-group` (D5):
  - target resolution;
  - 400 for members, and for `set-group` on a file;
  - the audit events;
  - the write-through of the entry's own columns;
  - `StartScan`, or `rescan_requested` with the follow-up scan in `OnScanDone`.

  Verify with tests of the owner-intent and classification scenarios: survival across a rescan and a rules bump, back to the rules, an override during an active scan converging, and an offline source.
- [ ] 2.4 Add `classification.owner`, `rules_category`, and `rules_group` to the detail read. Verify with an API test of an owner category explained as the owner's beside the rule that matched.
- [ ] 2.5 Add the detail panel's classification controls:
  - a category choice with "Back to the rules";
  - a "Review as one item" group switch;
  - "set by you" labels;
  - a note that folder figures update after the scan.

  Verify with Vitest.
- [ ] 2.6 Document overrides and group marks in `docs/operator.md` (Classification and the detail panel). Verify with the docs tests.

## 3. Scheduled rescans

- [ ] 3.1 Implement `RunDue` and its one-minute loop in `serve` (D6). Verify with fake-time tests:
  - a due online source starts a scan;
  - an offline one records a skip;
  - a due scan coalesces into an active scan;
  - downtime gives one catch-up;
  - off never runs;
  - `next_scan_at` advances past now.
- [ ] 3.2 Add `set-source-schedule`, the `sourceJSON` fields `schedule`, `next_scan_at`, and `schedule_skipped`, and the audit event. Verify with the source-registry scenarios as tests (200 with next scan, off, 400 for a bad time, 404 for an unknown source), and update the `sourceKeys` test.
- [ ] 3.3 Import `time/tzdata` in `cmd/precious`. Verify with `make cross`, and a test that resolves `America/Sao_Paulo` with `ZONEINFO` empty.
- [ ] 3.4 Show the schedule and next scan on each source card, with a form (off, daily, weekly, time) that sends the browser's zone, and the last skip when there is one. Verify with Vitest.
- [ ] 3.5 Document scheduled rescans in `docs/operator.md` (Sources, Scanning). Verify with the docs tests.

## 4. Search

- [ ] 4.1 Search names with folded accents through the rebuilt index (D7). Verify with a search test (`confraternizacao` finds `Confraternização 2018`; short searches unchanged), and document the short-search limit in the search package doc.
- [ ] 4.2 Build the page in two stages, with the driving duplicate filters, and write `0004_search.sql` with only the indexes that the plan tests prove necessary (D8). Verify:
  - `TestDupFilter` and `TestDupFilterHardLinks` pass unchanged;
  - plan tests show that no driver does a full scan;
  - `walkbench` shows no scan regression from the new indexes.
- [ ] 4.3 Add `count=only`, and make the UI show "Counting…" until the count arrives. Verify with an API test and Vitest.
- [ ] 4.4 Add the `state=unreadable` filter (server and UI), and link Home's partial notice to it with the source. Verify with the inventory-explorer scenario as an API test and Vitest.
- [ ] 4.5 Search UI:
  - the location line under each name;
  - the copies text of a checked file;
  - the source label on the root row;
  - the source filter control;
  - the remembered source (D9), used by Home, Opportunities, Gems, Search, and the Map's start.

  Verify with Vitest for each, including the remembered source carrying from Home to the Map.
- [ ] 4.6 Add the slow test at 2 million entries: first page within 1 s and count within 2 s at p95, with no filter, each duplicate state, and with and without a source, run without the race detector in the second pass of `make test-slow`. Verify with `make test-slow`, and record the figures in the design addendum.
- [ ] 4.7 Update the Search section of `docs/operator.md` (accents, location, copies, source, unreadable, counting). Verify with the docs tests.

## 5. Map, detail panel, and archives

- [ ] 5.1 Add the Map keyboard: arrows, Enter, Escape, and ignored inside fields and dialogs. Verify with Vitest, including the inventory-explorer scenario.
- [ ] 5.2 Map cells:
  - Decision hides before Duplicated;
  - the quieter Decision cell;
  - unreadable rows read "Could not be read" with "—" figures.

  Verify with Vitest (the narrow-card tests updated).
- [ ] 5.3 Add `only_child` on ancestors and `only_folder` on the entry detail, the merged path steps, and the Map's start descending through a chain. Verify with API tests and Vitest (the `old-disk/home` scenario).
- [ ] 5.4 Add `archive_note` (D10), the panel's wording for each reason, and "Open as a folder" as the main action of a complete archive. Verify with API tests (unsupported, nested, not listed) and Vitest.
- [ ] 5.5 Make `Dialog` give focus back, and limit the event stream's first-open refresh to older queries (D9). Verify with Vitest: the viewer Escape scenario, and fresh queries left alone on a first open.
- [ ] 5.6 Reword the treemap's remainder block and make it focus the table sorted by size. Verify with Vitest.
- [ ] 5.7 Update the Map and detail panel sections of `docs/operator.md`. Verify with the docs tests.

## 6. Compare and Opportunities

- [ ] 6.1 Compare (D11):
  - add `left_path`, `right_path`, and `twin`;
  - make the server choose the first bucket when none is given;
  - show both paths and "extra copy, same as …" in the UI, without the summary-only request.

  Verify with relations and API tests (the duplicates scenarios), Vitest, and the updated Playwright Compare tests.
- [ ] 6.2 Add `GET /api/relations?kind=overlap` and the read-only similar folders page, linked from Opportunities (D12). Verify with an API test (the corpus scenario) and Vitest.
- [ ] 6.3 Count the decided rows in the cards (D13), and show the decided figures and "Nothing left to review" in cards and list headers. Verify by extending the R2.5 test to the decided list, and with Vitest.
- [ ] 6.4 Update the Compare and Opportunities sections of `docs/operator.md`. Verify with the docs tests.

## 7. Integration

- [ ] 7.1 Extend the Playwright suite:
  - an override and a group mark on the corpus, and their figures after the scan;
  - setting a schedule and seeing its next scan;
  - a Search without accents, and the unreadable link from Home;
  - the Map keyboard;
  - Compare's paths and extra copy;
  - similar folders.

  Verify with `npx playwright test` passing headless, with no console error.
- [ ] 7.2 Run the R2 task 8.5 verification:
  - gofmt and vet;
  - `go test -race ./...`;
  - `make test-slow`;
  - `make cross`;
  - UI lint, test, and build;
  - Playwright;
  - Docker e2e;
  - `openspec validate --strict`;
  - `walkbench` with no regression.

  Verify that every step passes.
- [ ] 7.3 Smoke-check the built binary on a copy of an R2 database with a throwaway script:
  - the `0003` upgrade;
  - an override and a group mark followed by the scan;
  - a schedule due within the minute starting a scan;
  - an accent-free search;
  - a backup with integrity checked.

  Verify that it reports success, then delete the script.
- [ ] 7.4 When the reference server is on again, deploy over its database, and have the owner smoke-test overrides, groups, a schedule, Search, the Map keyboard, Compare, and similar folders on his dataset. Verify with his sign-off, with findings in the design addendum.
