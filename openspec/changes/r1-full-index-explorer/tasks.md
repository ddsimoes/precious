# Tasks

Slices:
- **P:** platform (`internal/fsaccess`);
- **R:** rules (`internal/rules`, `policies`);
- **S:** sources (`internal/sources`);
- **X:** index (`internal/index`, `tools/walkbench`);
- **Q:** search, read API, and viewer (`internal/search`, `internal/web/api`, `internal/viewer`);
- **D:** decisions and tags (`internal/decisions`);
- **U:** interface (`web/ui`).

Merge order:
1. Foundation (group 1).
2. P, R, and the search package (group 6) in parallel.
3. S, after P.
4. X, after P, R, and S.
5. D, after group 6.
6. Q's API and viewer, after X and D.
7. U can start after group 1. Its tests mock the API; its browser checks are in group 10.

Each group adds its section of `docs/operator.md` at its `<!-- owner: X -->` marker.

## 1. Foundation (coordinator)

- [x] 1.1 Tag the last v0.2 commit `curator-m4b` before any deletion (D1). Verify that `git tag --points-at HEAD` lists it on `main` before 1.2 starts.
- [x] 1.2 Delete the packages and files of D1 and rename the binary:
  - `cmd/curator` becomes `cmd/precious`, with `serve`, `check-config`, `admin set-password`, `backup`, and `version`;
  - `source confirm` and `eval` are removed;
  - `serve` wires auth, store, jobs, and the SPA shell only, until group 10;
  - cookie names, the database file name, temp-dir prefixes, messages, `deploy/*` files, and examples are renamed to `precious` (D13, D17).

  Paths: `cmd/`, `internal/` (deletions), `web/`, `deploy/`, `migrations/`, `policies/`. Verify:
  - `go build ./...`, `go vet ./...`, and `go test -race ./...` pass;
  - `grep -rniI curator --exclude-dir=.git --exclude-dir=openspec --exclude=directory-first-curator-spec-v0.2.md --exclude=*.md .` lists nothing outside comments that name the `curator-m4b` tag.
- [x] 1.3 Add the domain vocabulary of the Interfaces section:
  - `EntryID`, `FileKind`, the 16 `Category` values, `Family` and `FamilyOf`, the 5 `Trait` values, `Triage`, and `Decision`;
  - the new error codes, with their statuses in `web/apierr`.

  Remove the v0.2 types: `NodeID`, `Disposition`, the protection, mode, and suggestion types, and the reconcile and inbox constants. Paths: `internal/domain`, `internal/web/apierr`. Verify with table tests that:
  - every value round-trips;
  - `FamilyOf` covers all 16 categories;
  - every new code maps to the status in the Interfaces section.
- [x] 1.4 Write `migrations/0001_baseline.sql` exactly as in the Interfaces schema, and rename the database file to `precious.db`. Opening a state directory that holds `curator.db` logs one line naming it and never opens it (D6). Paths: `migrations/`, `internal/store`. Verify with store tests that:
  - `(source_id, path)` is unique;
  - every `CHECK` rejects a bad value;
  - deleting a source cascades to entries, `dir_stats`, and `entry_tags`;
  - an FTS insert and a contentless delete work;
  - `curator.db` is left byte-identical.
- [x] 1.5 Rework `internal/config` (D16):
  - remove `[[sources]]`, `[inbox]`, `[watch]`, `[reconciliation]`, `[discovery]`, `[inspection]`, `[copies]`, `[classifier]`, the classifier tables, and the secrets code;
  - add `[sources] allowed_roots` and `[scan] batch_size` / `list_batch`;
  - accept an `http://` origin on any host when `allow_insecure_http = true`;
  - put the POSIX mode check of `EnsureStateDir` behind a non-windows build tag.

  Replace `deploy/examples/*.toml` with one commented `deploy/examples/precious.toml`. Paths: `internal/config`, `deploy/examples`, `cmd/precious/checkconfig.go`. Verify with config tests for:
  - **Removed settings fail loudly**, with every key named;
  - the allowed-roots validation;
  - the scan ranges;
  - the listener rules;
  - `check-config` printing the new effective settings;
  - the example configuration loading.
- [x] 1.6 Pin the platform-layer signatures of the Interfaces section:
  - `Volume`, `Mount`, `Capabilities`, `FS.Mounts`, and `FS.Capabilities`;
  - move today's Linux code behind `//go:build linux`;
  - add `NewPortable()` (`os.Root`, `ReadDir`, `Lstat`), the default backend on darwin and windows, returning one `path`-kind volume per path and the unknown capability set;
  - give `synthfs` and `instrument` the new methods with one volume per device;
  - put `store.checkLocalFilesystem` behind a `linux` build tag.

  Paths: `internal/fsaccess/**`, `internal/store`. Verify:
  - every existing `fsaccess` test passes on Linux;
  - a test runs `NewPortable` on Linux over a temp tree: listing, no-follow of a symlink to outside, and `Known: false`;
  - `GOOS=darwin` and `GOOS=windows go build ./...` succeed.
- [x] 1.7 Give `jobs` the `Registry` interface. The claim key comes from `Registry.DeviceKey`, or `source:<id>` while it is unknown. Remove the `sources` import, `KindIntake`, `PendingWork`, and `PauseNodeBudgetReached`, and update every test fake. Paths: `internal/jobs`. Verify that `go test -race ./internal/jobs/` passes with a fake registry whose device keys group two sources on one device.
- [x] 1.8 Trim the command dispatcher to `cancel-job` plus a `Register(name, decode)` hook that slices use from their own packages. Paths: `internal/commands`. Verify with the dispatcher tests (idempotency replay, `idempotency_key_reused`, body limit, and unknown command 404) and the `cancel-job` tests.
- [x] 1.9 Session endpoints and SPA serving (D13):
  - `GET /api/session`, `POST /api/session/login`, and `POST /api/session/logout`;
  - the public-path allow list;
  - `GET /assets/*` with immutable caching, and `index.html` for every other non-`/api` GET;
  - the committed `web/dist` placeholder and its "build the UI" page;
  - the CSP gaining `media-src 'self'` and `frame-src 'self'`.

  Paths: `internal/web/{authhttp,middleware}`, `internal/web/spa`, `web/embed.go`, `web/dist/`. Verify with Go tests that:
  - an unauthenticated `/api/home` returns 401 JSON;
  - `/` and `/map/12` return the shell;
  - login without the CSRF header or with a wrong `Origin` returns 403;
  - login and logout round-trip and set the `precious_session` cookie;
  - the headers match D12/D13.
- [x] 1.10 Scaffold `web/ui`:
  - Vite, React, TypeScript, Tailwind, and the vendored Radix-based components;
  - React Router with the routes of D14;
  - TanStack Query, and react-i18next with the `en` catalog;
  - a working login screen on the session endpoints;
  - `make ui` (`npm ci && npm run build` into `web/dist`).

  Paths: `web/ui/**`, `Makefile`. Verify:
  - `npm run build` and `npm test` pass;
  - Vitest covers the login form (success, `login_failed`) and the **vocabulary test** of D14;
  - after `make ui`, a Go test finds the built `index.html` embedded;
  - the built `index.html` has no inline script or style.
- [x] 1.11 Write `internal/corpus` and `tools/gencorpus` (D18): the §15 tree into `synthfs` or a real directory, plus `ground_truth.json`, the FAT-capability fixture, names that are not valid UTF-8, and an unreadable folder. Paths: `internal/corpus`, `tools/gencorpus`. Verify with tests that:
  - both builders produce the same paths and sizes;
  - every ground-truth path exists;
  - the `curriculo*` and `Fotos - Copia` files are present;
  - `go run ./tools/gencorpus -out DIR` writes a tree that `find` counts as the ground truth says.
- [x] 1.12 Add `indextest.Seed`, which writes `entries`, `dir_stats`, and FTS rows exactly as the scanner will: paths, totals, `eff_decision = 'undecided'`. Paths: `internal/index/indextest`. Verify with a test that a seeded three-level tree has consistent folder totals and searchable names.
- [x] 1.13 Add `.github/workflows/ci.yml` (D19): Linux `go vet` and `go test -race`, six cross-builds, and the npm build and tests. Paths: `.github/workflows/`, `Makefile` (`make cross`). Verify that `make cross` builds linux, darwin, and windows on amd64 and arm64 locally, and that `actionlint`, or a YAML parse, accepts the workflow.
- [x] 1.14 Rewrite `docs/operator.md` as an R1 skeleton: installation, configuration reference, and a section per slice with its owner marker. Update the README build instructions (`make ui`, `make build`). Paths: `docs/operator.md`, `README.md`, and the docs tests in `cmd/precious`. Verify that the docs tests (configuration reference against `config.Defaults`, links) pass.

## 2. Platform (slice P)

- [x] 2.1 Implement Linux volume identity (D4): `uuid` through `/dev/disk/by-uuid` matched by major:minor; `zfs` by dataset name; `fsid` for btrfs; the `path` fallback; labels from `by-label`; `DeviceKey`. The `/proc` and `/dev` roots are injectable for tests. Paths: `internal/fsaccess/volume_linux.go` and tests. Verify with fixture-tree tests covering an ext4 partition, a ZFS dataset, a btrfs subvolume, a bind mount (relative root), and tmpfs (weak `path`).
- [x] 2.2 Implement the Linux capability table (D3) behind `FS.Capabilities`. Paths: `internal/fsaccess/capabilities_linux.go` and tests. Verify with a table test over every row, the unknown fallback, and read-only from the mount flags.
- [x] 2.3 Add the `synthfs` knobs:
  - `SetVolume`, `SetCapabilities`, and `Remount` (new inodes when identity is not stable);
  - case-insensitive lookup;
  - mtimes truncated to the resolution, and local-time storage;
  - mount-table changes for `Mounts()`.

  Paths: `internal/fsaccess/synthfs`. Verify with tests for each knob, and that every existing `synthfs` test passes.
- [x] 2.4 Test **R1.15 Every change builds for six targets**: `make cross` succeeds. On Linux, a server test using `NewPortable` starts and reports a source with `known: false` and a `path` volume. Paths: `Makefile`, `internal/fsaccess`. Verify that both pass.
- [x] 2.5 Document platform support and capabilities at the P marker. Verify with the docs tests.

## 3. Rules (slice R)

- [x] 3.1 Move `observe` into `internal/rules` and write `policies/markers/v3.toml`: the §6.2 file kinds, the system-junk, partial-download, installer, and disk-image signals, and the `indicator` flags of D9. Paths: `internal/rules`, `policies/`. Verify with loader tests (unknown keys rejected, `FileKind` per extension, `AppendNameSignals` allocation-free with a benchmark) and pattern tests.
- [x] 3.2 Implement the v2 grammar, `ClassifyFile`, `ClassifyFolder`, families, triage, groups, the veto flag, and `Explain` (D9). Paths: `internal/rules`. Verify with unit tests for each condition kind, priority ties (`conflicting_rules`), traits union, the `*.CHK` review exception, and a veto with indicators against one without.
- [x] 3.3 Write `policies/rules/v2.toml` covering §8. Verify with fact-based tests:
  - `RECYCLER`, `System Volume Information`, `Thumbs.db`, `setup.exe`, `x.iso`, `y.part`, `node_modules`, `Arquivos de programas/Winamp`, and `WINDOWS` each get the §8 category;
  - a folder of year folders full of JPEGs is `personal_media`;
  - program icons are not indicators.
- [x] 3.4 Document categories, families, triage, groups, and the veto at the R marker. Verify with the docs tests.

## 4. Sources (slice S)

- [x] 4.1 Implement `sources.Service`: `List`, `Get`, `Open` (mount resolution and remount), `Refresh`, the `jobs.Registry` methods, and the one-minute refresh loop. Paths: `internal/sources`. Verify with `synthfs` tests for online, offline (volume gone), unavailable (root unreadable), and the capabilities JSON written.
- [x] 4.2 Test **R1.16 An unmounted volume stays browsable and is found again at another path**:
  1. Seed entries for a source.
  2. Vanish the volume: the source is offline and its entries are still served.
  3. Reattach at a new mount point: the same source ID is online with the new mount point and the same entries.

  Paths: `internal/sources` tests. Verify that it passes.
- [x] 4.3 Implement the picker: allowed-roots defaults per OS, HMAC handles, and children listing with folders only, at most 1,000, and symlinks never entered. Paths: `internal/sources/picker.go`. Verify with tests for a forged handle, a symlink pointing outside, a restart invalidating handles, and truncation.
- [x] 4.4 Implement `add-source`, `rename-source`, and `remove-source` (D5): prepare outside the transaction; re-check overlap in it; refuse the state directory; `job_active`; the audit events. Paths: `internal/sources/commands.go`. Verify with command tests for each error in the Interfaces table and for cascade deletion.
- [x] 4.5 Test **R1.18 A source can be added only through the picker**:
  - a valid handle adds it;
  - a raw path in `handle` returns 400;
  - a forged handle returns 400;
  - a handle for a folder outside every allowed root returns 403 `outside_allowed_roots`;
  - nothing else in the API accepts a path.

  Paths: `internal/sources` tests. Verify that it passes.
- [x] 4.6 Serve `GET /api/sources` and `GET /api/picker`. Paths: `internal/sources/http.go`. Verify with handler tests of the JSON shapes in the Interfaces section, including `strong` and `active_job`.
- [x] 4.7 Document sources, allowed roots, offline sources, and weak volumes at the S marker. Verify with the docs tests.

## 5. Index (slice X)

- [x] 5.1 Implement the scanner (D7):
  - the walk with batch, yield, and cancel, and the writer goroutine with path-resolved parents;
  - the first-scan inserts, FTS rows, post-order aggregates with `by_family`, and indicator lists;
  - `rules` classification;
  - `eff_decision` inherited from the parent, read in the batch transaction;
  - progress keys;
  - the `scan` kind and `index.StartScan`;
  - the `start-scan` command.

  Paths: `internal/index`. Verify with `synthfs` tests for kinds, symlinks, special files, mount boundaries, the pipeline backpressure, and `go test -race ./internal/index/` under 60 s.
- [x] 5.2 Test **R1.1 Every entry is indexed and folder totals add up** on the `synthfs` corpus: every ground-truth path is indexed, and every folder's `total_bytes` equals the sum of its files. Verify that it passes.
- [x] 5.3 Test **R1.8 Non-UTF-8 names round-trip losslessly**: `f\xE9.txt` is stored byte-exact, has an FTS display name, and its path matches the raw bytes. Verify that it passes. (The API side is in 8.2.)
- [x] 5.4 Test **R1.9 A scan never writes to the source**: an `instrument` recording of a full scan and rescan holds no write-class call. Verify that it passes. (The read-only mount run is 10.3.)
- [x] 5.5 Implement the rescan diff, missing entries, returning entries, unreadable and partial folders, and the cancel and restart behavior (D7), with time comparison by capabilities (D8). Paths: `internal/index`. Verify with tests for a changed, an added, a deleted, and a returning file, a failed listing that keeps its children, and a cancel mid-tree followed by a full rescan.
- [x] 5.6 Test **R1.6 A rescan updates sizes and keeps decisions and tags**:
  1. Seed decisions and tags.
  2. Change the corpus.
  3. Rescan: sizes are updated, deleted files are missing, every decision and tag is kept, and a returning file has its old ID.

  Verify that it passes.
- [x] 5.7 Test **R1.17 FAT rescan with no changes creates no entries**: on the FAT-capability fixture, with mtimes shifted by up to 2 s and by one hour (DST), an unchanged rescan writes no new rows and updates none. Verify that it passes.
- [x] 5.8 Test **R1.4 Rules classify the corpus as its ground truth says**: scan the `synthfs` corpus and compare every ground-truth category, triage, and group. Verify that it passes.
- [x] 5.9 Test **R1.5 The spreadsheet in Microsoft Office vetoes discard**: `Arquivos de programas/Microsoft Office` has triage `review`, `veto = 1`, and its indicators include `OFFICE11/Meu orcamento casamento.xls`. Verify that it passes.
- [x] 5.10 Write `tools/walkbench` (D19): a primed warm bare walk, then a bare walk and a scan into a scratch state directory, printing both times and the ratio. Paths: `tools/walkbench`. Verify that it runs on the generated corpus and prints a ratio.
- [x] 5.11 Document scanning, rescans, missing and partial entries, progress, and the walkbench method at the X marker. Verify with the docs tests.

## 6. Search package (slice Q, part 1)

- [x] 6.1 Implement `internal/search`:
  - `Parse`, `Page`, and `Resolve` over `entries` and FTS;
  - name substring, extension, file kind, size, year, category, effective decision, `Within` (path range), and tags as prefix ranges (D10, D11);
  - keyset cursors;
  - the 10,000 count cap.

  Paths: `internal/search`. Verify with tests on `indextest.Seed` trees for each filter, combined filters, cursor stability, bad values (400), and an inherited tag found below its folder.

## 7. Decisions and tags (slice D)

- [x] 7.1 Implement `SetDecision` (D10): individual and bulk modes, inherit, the subtree `eff_decision`/`eff_from` update stopping at own decisions, `InheritFrom`, `Totals`, `Effective`, and the audit event. Paths: `internal/decisions`. Verify with tests for nested own decisions, inherit clearing, a 404 for any unknown ID with nothing changed, and the audit row.
- [x] 7.2 Test **R1.7 Deciding a folder decides its subtree**:
  1. Discard `Backup_PC_2004` with one descendant kept.
  2. Every other descendant's effective decision is discard, and the kept one is keep.
  3. `Totals` moves those bytes from undecided to discard.

  Verify that it passes.
- [x] 7.3 Test **R1.11 Bulk discard skips kept entries**: a selection with an explicitly kept file and a file inside a kept folder skips both (`skipped_count` 2, with paths) and applies the rest. An individual request on the kept file changes it. Verify that it passes.
- [x] 7.4 Implement tags: `SetTags` (own tags only for removal), tag create, rename, and delete with unique case-insensitive names, `Effective` tag origins, and the audit events. Paths: `internal/decisions/tags.go`. Verify with tests for `tag_exists`, a delete removing a tag everywhere, and the removal of an inherited tag on a descendant changing nothing.
- [x] 7.5 Test **R1.12 A folder tag applies to everything inside**: `familia` on `Fotos/2006` appears on every entry below it as inherited from `Fotos/2006`, and `search` by tag returns them. Verify that it passes.
- [x] 7.6 Implement `create-selection` (explicit IDs, count, bytes, kept, one-hour expiry, pruning) and register every D command in `internal/commands`. Paths: `internal/decisions/selections.go` and commands. Verify with tests for `selection_expired`, entries added after creation being excluded, and the request and response shapes of the Interfaces table.
- [x] 7.7 Document decisions, inheritance, bulk safety, tags, and selections at the D marker. Verify with the docs tests.

## 8. Read API and viewer (slice Q, part 2)

- [x] 8.1 Serve `/api/home`, `/api/entries/{id}`, `/children`, `/treemap`, and `/api/tags` with the shapes of the Interfaces section. Paths: `internal/web/api`. Verify with handler tests on seeded trees: sort keys and order, cursor paging, the treemap `other` bucket, ancestors, and the intent origins.
- [x] 8.2 Serve `/api/search` with the count. Extend the R1.8 coverage: `name_b64` of `f\xE9.txt` equals the raw bytes and the display is `f\xE9.txt`. Verify with handler tests.
- [x] 8.3 Test **R1.2 Every folder shows its size** through the API: every folder row of the scanned corpus, including the groups under `Arquivos de programas`, has `total_bytes` and `total_files` matching the ground truth. Verify that it passes.
- [x] 8.4 Test **R1.3 Search finds files anywhere** through the API: by name, extension, size, year, and tag, including `Microsoft Office/OFFICE11/Meu orcamento casamento.xls` inside a group. Verify that it passes.
- [x] 8.5 Implement the viewer (D12): `/content` with the type table, range requests, identity-checked chunked reads, and the per-type headers; `/text` with BOM, UTF-8, and Windows-1252 decoding, the 1 MiB cap, and the language and Markdown flags; `invalid_entry_state` and `source_offline`. Paths: `internal/viewer`. Verify with handler tests for each row of the type table, a range request, each decoding path, and both errors.
- [x] 8.6 Test **R1.13 The viewer shows the corpus files safely** at the HTTP level:
  - JPEG, MP4, MP3, PDF, Markdown, a source file, and the Windows-1252 file get the right type and decoding;
  - the HTML file is `application/octet-stream` with `attachment`;
  - the SVG carries `sandbox`;
  - every response carries `nosniff`.

  Verify that it passes. (The in-browser check is in 10.2.)
- [x] 8.7 Test **R1.10 Map pages stay fast at 2 million entries**, under the `slow` tag: scan a 2,000,000-entry generated `synthfs` tree, then time 200 pages of children sorted by bytes (p95 < 300 ms) and 50 treemap levels (p95 < 500 ms). Verify with `go test -tags slow -run R1_10 ./internal/web/api/`.
- [x] 8.8 Document the read API, search syntax, and the viewer's safety rules at the Q marker. Verify with the docs tests.

## 9. Interface (slice U)

- [x] 9.1 App shell:
  - layout and navigation;
  - session handling (401 sends the user to `/login`);
  - the error envelope shown as messages;
  - size, number, and date formatting by locale;
  - the job event stream into the query cache, reconnecting with `last_event_id`.

  Paths: `web/ui/src/app/**`. Verify with Vitest for formatting, 401 redirect, and the event reconnect.
- [x] 9.2 Sources screen: the list with state, volume strength, capabilities, totals, and active scan; the folders-only picker; add, rename, and remove with confirmation; Scan now; live progress. Paths: `web/ui/src/sources/**`. Verify with Vitest on mocked API responses, including `outside_allowed_roots` and `job_active` messages.
- [x] 9.3 Home screen: totals, bytes by family, kind, and year, decision totals, the per-source filter, and active scans. Paths: `web/ui/src/home/**`. Verify with Vitest on mocked responses.
- [x] 9.4 Map screen:
  - the treemap and the virtualized table, synchronized;
  - drill-down and breadcrumbs;
  - sorting and cursor paging;
  - color modes: family, kind, age, decision, and tag;
  - selecting a row opens `?entry=`.

  Paths: `web/ui/src/map/**`. Verify with Vitest for sync, sort, and paging, and for the "other" bucket.
- [x] 9.5 Search screen:
  - the filters of D11;
  - virtualized results;
  - select some, or select all through `create-selection`, with a confirmation showing count, bytes, and kept;
  - bulk decision and tags, and a skipped report.

  Paths: `web/ui/src/search/**`. Verify with Vitest for the confirmation content and the skipped list.
- [x] 9.6 Detail panel:
  - path and ancestors, sizes, dates, and breakdowns;
  - classification with rule sentences and veto indicators;
  - the own and effective decision with its origin, and individual decision controls, including inherit;
  - own and inherited tags with origins, and adding or removing own tags;
  - technical details collapsed.

  Paths: `web/ui/src/detail/**`. Verify with Vitest on mocked responses.
- [x] 9.7 Viewer component: image, video, audio, a PDF frame, highlighted text and code, and sanitized Markdown with no remote content. Paths: `web/ui/src/viewer/**`. Verify with Vitest that a Markdown script, an `onerror` attribute, and remote images are stripped.
- [x] 9.8 Document the screens at the U marker. Verify with the docs tests.

## 10. Integration (coordinator)

- [x] 10.1 Wire `cmd/precious serve`:
  - sources service and refresh loop;
  - the jobs runner with `sources` as the registry;
  - the `index` handler, the commands of S, X, and D, and `web/api`, `viewer`, and the SPA;
  - shutdown.

  Paths: `cmd/precious`. Verify with a serve test over a temp corpus: log in, add the source through a picker handle, scan, then `/api/home` totals match the ground truth.
- [x] 10.2 Add the Playwright suite in `web/ui/e2e` against the built binary and a generated corpus, and add it to CI. It covers:
  - **R1.18** adding the source through the picker;
  - scanning with live progress;
  - **R1.2** Map sizes for `Arquivos de programas`;
  - **R1.3** Search for the spreadsheet;
  - **R1.7** discarding `Backup_PC_2004` and seeing Home change;
  - **R1.11** bulk discard showing its skipped list;
  - **R1.12** tagging `Fotos/2006` and searching by tag;
  - **R1.13** opening each viewer type, where an HTML and an SVG with scripts never run script in the app (checked with a `window` flag);
  - no CSP violations reported.

  Verify with `npx playwright test` headless.
- [x] 10.3 Add an e2e test for **R1.9** on a real read-only tmpfs: a full scan and rescan succeed, and nothing changes on the mount (a listing with mtimes before and after). Paths: `internal/index/*_e2e_test.go`. Verify that it passes in privileged Docker `golang:1.27.1`.
- [x] 10.4 Finish `docs/operator.md`:
  - remove the owner markers;
  - write the intro for R1;
  - add "Moving from curator to precious" (fresh install, `curator.db` untouched, rollback);
  - link ADR 0008.

  Verify that `grep -n 'owner:' docs/operator.md` returns nothing, and that the docs tests pass.
- [x] 10.5 Run the final verification:
  - `gofmt -l .` is empty;
  - `go vet ./...` and `go vet -tags e2e,slow ./...` are clean;
  - `go test -race ./...` passes, with every package under 60 s;
  - `go test -race -tags slow ./...` passes, including R1.10;
  - `make cross` passes (R1.15);
  - `npm run lint`, `npm test`, and `npm run build` pass;
  - the Playwright suite passes;
  - the e2e tests pass in privileged Docker.
- [x] 10.6 Run **R1.14 Scan within 1.5× of a bare walk** on the reference server: `tools/walkbench` warm, and cold after a pool export and import if the warm ratio exceeds 1.5. Record both numbers in the design addendum. Verify that the recorded ratio is ≤ 1.5.
- [x] 10.7 Smoke-check the built binary with a throwaway script, with no network access:
  1. `check-config`, then `serve`.
  2. Log in through the session API.
  3. Add a generated corpus through a picker handle and scan it.
  4. Compare `/api/home` with the ground truth.
  5. Search, decide, tag, and fetch `/content` and `/text`.
  6. Run `precious backup` and check its integrity.

  Verify that it reports success, then delete the script.
- [x] 10.8 The owner smoke-tests the milestone in a real browser on the regression corpus (§14): add a source, scan, Home, Map, Search, detail, viewer, decisions, and tags. Verify by the owner's sign-off, with any findings recorded in the design addendum.

## 11. After the owner's smoke test (D21–D23)

Findings of the first 10.8 run on the owner's archive. 10.8 is repeated after this group.

- [x] 11.1 Compute composition and the inside list in the scanner (D21):
  - `domain.FileFamily`;
  - `dir_stats.by_family` bottom-up, with only groups outside Containers counted as one item;
  - `dir_stats.inside`, up to 10 notable entries, merged in post-order;
  - the `inside` column in `0001_baseline.sql`;
  - `indextest.Seed` following the same rules.

  Paths: `internal/domain`, `internal/index`, `migrations`. Verify with tests for:
  - the classification scenarios "A photo folder keeps its other contents visible", "A loose photo counts as personal", "Clues from the top", and "A nested group appears once";
  - Home's `by_family` summing to the total;
  - the scanner still matching `indextest.Seed` column by column, and R1.1 and R1.4 still passing.
- [x] 11.2 Serve `composition` in EntryRow and `stats.inside` in `GET /api/entries/{id}` (D21). Paths: `internal/web/api`, `internal/search`. Verify with:
  - handler tests of both shapes;
  - plan tests showing the children, treemap, and search queries still use their indexes;
  - `go test -tags slow -run R1_10 ./internal/web/api/` still within its targets.
- [x] 11.3 Show the composition in the interface (D21):
  - a composition bar on folder rows in the Map table and Search results;
  - the dominant family's share next to the category when the folder is mixed;
  - treemap family colors from the dominant composition family;
  - in the detail panel, the composition and the "Inside this folder" list with links;
  - for an unclassified file, its family by file type.

  Paths: `web/ui/src/**`. Verify with Vitest for the bar, the share label, the treemap color of an unclassified photo, and the inside list.
- [x] 11.4 Preview files in the detail panel (D22): image, video, audio, PDF, and the first 40 lines of text, source, and Markdown, with Open kept for the full viewer. Paths: `web/ui/src/detail/**`, `web/ui/src/viewer/**`. Verify with Vitest for each preview type and the hostile Markdown cases, and with Playwright that a corpus JPEG shows in the panel without Open.
- [x] 11.5 Make the screens fit the window (D23):
  - column priorities and contained horizontal scrolling for the Map table and Search results;
  - the detail panel over the Map below 1,600 px;
  - the Search filter grid.

  Paths: `web/ui/src/**`. Verify with Playwright at 1366×768 and 1920×1080 that no element of the Map (with and without the panel), Search, or the panel extends past its card or the viewport and no two controls overlap, and by reviewing screenshots.
- [x] 11.6 Update `docs/operator.md` for composition, the inside list, and previews. Redeploy the smoke instance on the owner's server and rescan its sources, then repeat 10.8. Verify with the docs tests and the owner's sign-off.
