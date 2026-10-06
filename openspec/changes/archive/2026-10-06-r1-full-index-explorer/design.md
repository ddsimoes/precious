# Design

## Context

See `proposal.md` for why. The M4b code base, which this change replaces, has these properties:

- **Platform.** `internal/fsaccess` is Linux-only: getdents, `O_PATH`, `/proc/self/mountinfo`, and `Stat_t.Ctim`. `GOOS=darwin` and `GOOS=windows` builds fail there and in `internal/store` (`unix.Statfs`).
- **Walkers.** The only full-tree walkers are `inspection/walk.go`, which keeps counters only, and `compare/list.go`, which writes batched snapshot rows. Their loop shapes are reusable; their code is not.
- **Jobs.** The runner (`internal/jobs`) is sound. Its claim key reads `sources.identity_dev`, and it imports `sources` only for the watchdog's `SetUnresponsive`.
- **Commands.** The dispatcher (`internal/commands/commands.go`) is generic: an `operation` with `canonical`, `prepare`, and `apply`, an `Idempotency-Key`, and the `{error:{code,message}}` envelope. Every command it registers belongs to a removed concept, except `cancel-job`.
- **Auth.** `internal/auth` and `internal/web/{middleware,session,clientip,apierr}` are reusable. Login is a server-rendered form, and the CSRF token reaches scripts only through a `<meta>` tag. The CSP is `default-src 'none'; script-src 'self'; style-src 'self'; …`.
- **Rules.** `internal/observe` (name patterns, signals, file kinds) and `internal/classify/{policy,evaluate}.go` are pure and TOML-driven, but take a depth-0 descriptor as input.
- **Database.** modernc SQLite 3.53 ships FTS5 with the `trigram` tokenizer, which was checked on this tree.

## Goals / Non-Goals

**Goals:**
- One scan pass writes every entry and every folder aggregate, and keeps its own overhead within 1.5 times a bare walk (R1.14).
- Every list the interface shows (children by size, treemap level, search) is served from an index, so a page costs the same at 2 million entries as at 2,000 (R1.10).
- Linux-specific code sits behind the platform layer, so darwin and windows build and run today, and R8 replaces only backends.

**Non-Goals:**
- Reading file content anywhere except the viewer: no hashing (R2) and no media metadata (R5).
- Native mount and volume discovery on macOS and Windows (R8).
- Writing to a source (R3).

## Decisions

### D1. Code disposition and the `curator-m4b` tag (§12)

- **Tag first.** The commit before any R1 deletion is tagged `curator-m4b`. Later milestones restore code from it: `compare` in R2, `classify` and `eval` in R6.
- **Deleted in R1:**
  - `discovery`, `reconcile`, `watch`, `inbox`, `inspection`, `compare`, `eval`, `classify`, `inventory`, `intent`, and `scenario`;
  - `web/explorer`, `web/templates`, `web/static`, and `authhttp/login.html`;
  - migrations `0001`–`0007`, `policies/rules/v1.toml`, `policies/markers/v1.toml`, `policies/markers/v2.toml`, and `policies/tasks`;
  - the `cmd/curator` tests of removed features, and the `source confirm` and `eval` subcommands.
- **Kept and changed:**
  - `fsaccess`, `store`, `jobs`, and `commands` (the dispatcher only);
  - `auth`, `web/{middleware,session,clientip,apierr}`, `config`, and `domain`;
  - `observe`, merged into `rules` (D9).
- **Deviation recorded in the spec.** §14 R1 said "the classifier contract kept compiling with the fake adapter only". Nothing in R1 calls a classifier, and the contract's item types are v0.2 descriptors that R6 replaces (§9.2). Keeping them would mean config keys and tests for unreachable code. `precious-spec-v0.3.md` §12 and §14 were updated to restore the classifier from the tag in R6.
- **Rejected:**
  - Keeping `compare/unpack` and the classifier adapters as unused libraries. Dead code with its own test upkeep.
  - Keeping every package compiling against a shim schema. A second model inside the binary.

### D2. Packages and import direction (§12)

```text
domain, clock, policies                                   (leaves)
fsaccess (+synthfs, instrument)      <- domain
config                               <- domain, web/clientip
store                                <- migrations
rules                                <- domain, policies          (pure: markers, rules, evaluation)
sources                              <- fsaccess, store, config, domain, clock
jobs                                 <- store, config, domain, clock, web/apierr   (no longer imports sources)
index                                <- jobs, sources, fsaccess, rules, store, domain, clock
decisions                            <- store, domain, clock, auth (audit), search
search                               <- store, domain
viewer                               <- sources, fsaccess, store, domain
web/api                              <- store, domain, sources, rules, decisions, search, jobs
commands                             <- jobs, sources, index, decisions, search, auth
web/spa                              <- precious/web (embedded dist)
corpus                               <- fsaccess/synthfs, domain           (fixtures + ground truth)
cmd/precious, tools/gencorpus, tools/walkbench  <- everything they wire
```

- **`jobs` stops importing `sources`.** It declares the `jobs.Registry` interface (Interfaces section), which `sources.Service` implements.
- **File ownership** follows the task slices: P (`fsaccess`), S (`sources`), R (`rules`, `policies`), X (`index`), D (`decisions`), Q (`search`, `viewer`, `web/api`), and U (`web/ui`). The coordinator owns the foundation files listed in tasks group 1.
- **Rejected:** one `inventory` package for everything, as in v0.2. Its 7,000 lines were the hardest code to change.

### D3. The platform layer (§12, §6.1)

- **The `fsaccess.FS` interface** keeps `OpenRoot`, `Dir`, and `File` with their no-follow, special-file, and mount-boundary rules. It gains:
  - `Mounts()`: the current mount table in a portable shape;
  - `Capabilities(path)`.
- **Backends, by build tag:**
  - `os_linux.go` is today's code, plus volume identity and the capability table (D4).
  - `os_portable.go` (`!linux`) uses `os.OpenRoot`, `(*os.File).ReadDir`, and `Root.Lstat`. Identity comes from `FileInfo.Sys()` where the type exposes it. `Mounts()` returns a single `path`-kind volume for each path asked for, and capabilities are the conservative unknown set:
    - case-insensitive;
    - no stable identity;
    - 2-second resolution;
    - local time unknown;
    - `Known: false`.
- **Each OS-specific helper gets a build-tagged twin:** `store.checkLocalFilesystem` (statfs, Linux only), `config.EnsureStateDir` (POSIX mode check skipped on windows), and test files using `syscall.Stat_t` or `unix.Mount` (tagged `linux`).
- **Capability table on Linux, by `fs_type`:**

  | fs_type | Case-sensitive | Stable identity | Resolution | Local time | Read-only |
  |---|---|---|---|---|---|
  | ext2/3/4, xfs, btrfs, zfs, f2fs, tmpfs | yes | yes | 1 ns | no | from the mount |
  | vfat | no | no | 2 s | yes | from the mount |
  | exfat | no | no | 10 ms | no | from the mount |
  | ntfs, ntfs3, fuseblk(ntfs) | treated as no | yes | 100 ns | no | from the mount |
  | iso9660, udf | yes | yes | 1 s | no | always |
  | anything else | the unknown set | | | | |

  NTFS is treated as case-insensitive because Windows will treat the names that way.
- **Rejected:**
  - A darwin/windows backend that returns `ErrUnsupported`. That is a stub; the portable backend is real, only less precise.
  - Native darwin (`getfsstat`) and windows (volume GUID) backends now. They belong to R8, with their testers.

### D4. Volume identity on Linux (§6.1)

| fs_type | Volume kind | Volume ID | DeviceKey (claim key) | Strong |
|---|---|---|---|---|
| zfs | `zfs` | the dataset name from `mountinfo` source | `pool:<pool>` | yes |
| a block device listed in `/dev/disk/by-uuid` (matched by major:minor) | `uuid` | the filesystem UUID | `dev:<major:minor of the device>` | yes |
| btrfs without a by-uuid match | `fsid` | the statfs `f_fsid` | `fsid:<f_fsid>` | yes (UUID-derived) |
| anything else | `path` | the mount point | `mount:<mount point>` | no |

- Labels come from `/dev/disk/by-label` when present.
- A source stores the volume kind and ID and its root as a path relative to the volume's mount root. Bind mounts use the `mountinfo` root field.
- **Resolving a source:**
  1. Find a current mount with the same kind and ID.
  2. If none, the source is `offline`.
  3. If one, the source's absolute root is that mount's point joined with the relative root. A different mount point than last time is a remount (R1.16).
- A `path`-kind source is recognized only at the same mount point. The Sources screen says so (Strong = false).
- **Rejected:**
  - `st_dev` or the v0.2 `fsid`/`root_ino` triple. Neither is stable across reboots or remounts.
  - Running `blkid` or `zfs get guid`. They are external commands; the dataset name and udev symlinks are enough.

### D5. Sources, allowed roots, and the picker (§6.1, §11.10)

- **Sources live in the database.** IDs are slugs from the label, made unique with a suffix (`fotos`, `fotos-2`) and never reused. `[[sources]]` leaves the configuration.
- **Allowed roots.** `sources.allowed_roots` in the configuration replaces the defaults when set. The defaults are:
  - Linux: the owner's `$HOME`, `/media`, `/mnt`, `/run/media`, and `/srv`, those that exist;
  - macOS: `$HOME` and `/Volumes`;
  - Windows: `%USERPROFILE%` and every drive letter.

  Each root is cleaned and resolved with `filepath.EvalSymlinks` once at startup.
- **Picker handles.** A handle is `base64url(path) + "." + HMAC-SHA256(key, path)`. The key is 32 random bytes made at server start, so handles expire on restart. Expanding a handle checks:
  1. the HMAC;
  2. that the path, after `EvalSymlinks`, is inside an allowed root;
  3. that it is a directory reached without following a symlink below the root.

  The browser never sends a typed path (R1.18).
- **Adding a source:**
  1. `prepare`, outside the transaction, resolves the handle, opens the root, reads the volume and capabilities, and makes sure the folder is neither inside nor around an existing source of the same volume.
  2. `apply` re-checks that overlap under the writer lock and inserts the source with its root entry.
  3. The command does not scan: the screen offers "Scan now", which is `start-scan`.
- **Removing a source** deletes its rows (cascade) after its active scan, if any, is cancelled. Otherwise the command returns `409 job_active`.
- **Availability refresh** runs every minute and when the Sources list is read. It updates `state`, `mount_point`, and `capabilities`, and never touches entries.
- **Rejected:**
  - Typed paths filtered by prefix. They are open to symlink tricks and break the spec's "never by typed paths" rule.
  - Handles stored in the database. They would need a pruning job for no added safety.

### D6. Schema baseline and entry identity (§6.2, §7)

- One migration, `0001_baseline.sql` (Interfaces section), replaces `0001`–`0007`. The database file is `precious.db`. An existing `curator.db` in the state directory is left untouched, and a log line says so.
- **Every entry has one row,** unique on `(source_id, path)`. `path` is the raw `/`-joined path below the source root, empty for the root. `parent_id` links the tree.
- **A rescan matches by path.** A row keeps its ID, decision, and tags across rescans, missing periods, and returns.
- **Changes.**
  - A change of kind at the same path is a different entry: the old row goes missing, and a new row is inserted.
  - A change of identity (inode) on a filesystem with stable identity only updates the row and bumps `scan_gen`, which R2 uses to invalidate hashes.
- **Rejected:**
  - Nested-set (`pre`/`post`) numbering. Every rescan would rewrite every row, which defeats "write only changed rows".
  - Matching by inode first. Moves made outside Precious are R3, and FAT has no stable inodes.

### D7. The scanner (§7; slice X)

- **Shape.**
  - A depth-first walk with an explicit stack, reusing `compare/list.go`'s batch, yield, and cancel loop. It `Lstat`s every entry, reads every directory with `ReadBatch(256)`, and calls `rt.FSCall` around each filesystem operation and `rt.Yield` after each directory.
  - It never follows a symlink, never opens a special file, and never enters a mount boundary unless the source opts in (it never does in R1). A boundary is recorded as a directory entry with `mount_boundary = 1` and no children.
- **Pipeline.** The walker sends row operations to one writer goroutine over a bounded channel (capacity 4 batches). The writer commits batches of up to 1,000 operations in one `store.Write` transaction, with prepared statements, so walking and writing overlap.
  - A directory's row is inserted when the directory is listed, so that its children can reference it. It is updated with its totals once the directory finishes.
  - Operations name their parent by path, not by ID. The writer keeps a path → ID map for the directories open on the walk's stack, plus the IDs that the rescan diff already read, and it assigns IDs on insert.
  - Two scans of different sources can therefore write concurrently through the single writer, with no shared ID counter.
- **Aggregates in the same pass.** When a directory's last child is done, its subtree totals are final, because the walk is post-order. The walker then:
  1. computes its `dir_stats` and the entry's `total_*`, `newest_ns`, `oldest_ns`, and `main_kind`;
  2. runs `rules.ClassifyFolder`;
  3. computes its composition (`dir_stats.by_family`) from its content, bottom-up (D21): the sum of its children's contributions. A child group whose category is outside the Containers family counts whole under its own family; every other child folder contributes its own composition; a file contributes its size under its file family (`domain.FileFamily`): its file rule's family, or, when its category is `unknown`, by file kind:
     - image, video, audio, document, and source: Personal and valuable;
     - installer and executable: Programs and system;
     - system: Disposable;
     - archive and other: Containers;
  4. computes its `dir_stats.inside` list (D21);
  5. queues the row update;
  6. adds the totals to its parent's accumulator.

  Files are classified with `rules.ClassifyFile` when they are listed.
- **Rescan diff.** For each directory, the walker reads its stored children by `(parent_id)` from the read pool before listing, then compares:
  - an unchanged child (same kind, size, and mtime within the resolution, D8) is not written;
  - a changed child is updated;
  - a new child is inserted;
  - a stored child not seen in a complete listing becomes `missing`.

  A directory whose listing failed keeps its stored children as they were. It is marked `unreadable` and its ancestors `partial`. A folder that goes missing takes its whole stored subtree with it, by path range, in the same batch.
- **First scan** skips the read, because nothing is stored.
- **Restart.**
  - A scan interrupted by a crash or a cancel is not resumed mid-tree. The next attempt walks again from the root, and the rows already written make that walk mostly reads.
  - A cancelled scan leaves rows written so far. Aggregates of directories it did not finish are not trusted: their ancestors keep their previous totals and get `partial = 1` until a complete scan.
- **Progress keys:** `phase` (1 walking, 2 finishing), `dirs`, `files`, `bytes`, `written`, `unreadable`, and `missing`.
- **Finishing.** In one transaction, the scan sets `sources.scan_gen`, `last_scan_at`, `last_scan_job`, and `rules_version`, and clears `partial` where the subtree was completed.
- **Rejected:**
  - A durable frontier as in v0.2. Resumability mid-tree costs a table and a state machine; a rescan of unchanged rows is cheap.
  - Computing aggregates afterwards in SQL. That is a second full pass over 2 million rows.

### D8. Time comparison and FAT matching (§7; R1.17)

- **An unchanged mtime** means |Δ| ≤ the filesystem's resolution.
- **On a local-time filesystem (FAT)**, |Δ − k·3600 s| ≤ resolution also counts as unchanged, for k ∈ {−1, +1} (a daylight-saving shift).
- **Sizes** must be equal.
- **Rejected:** any ±hours tolerance for time-zone moves. It would hide real edits made within the same hour of the day; a source moved to another time zone shows as changed, which is the honest outcome.

### D9. Rules v2 (§6.6, §8; slice R)

- **One pure package, `rules`,** holds the markers file (`policies/markers/v3.toml`: signals and file kinds) and the rules file (`policies/rules/v2.toml`). `observe`'s loader, pattern matcher, and `AppendNameSignals` move here unchanged in behavior.
- **File kinds** (§6.2): image, video, audio, document, source, archive, installer, executable, system, other.
  - `system` covers `Thumbs.db`, `desktop.ini`, `.DS_Store`, and `*.CHK`.
  - Disk images (`iso`, `img`, `nrg`, `mdf`, `cue`) are `archive`. `bin` counts as a disk image only next to a same-stem `.cue`.
- **Rule grammar v2:**
  - `target = "file" | "folder"`;
  - `name` patterns on the entry's own name;
  - `child_signals` (any, all, or none, among the immediate children);
  - `subtree_signals` (`{signal, min_count}`, anywhere below);
  - `kind_share` (`{kind, min_files, min_bytes_share}`, over the subtree);
  - `priority`, then `category` and/or `traits`, then `explain`.
- **Precedence.**
  - The highest priority wins. Two categories at the top priority give `unknown` (`conflicting_rules`); no match gives `unknown` (`no_rule_matched`).
  - Traits are the union over all matching rules.
- **Families** are fixed by category (§6.6 table).
- **Groups:** a folder whose category is `application_installation`, `os_installation`, `source_project`, `application_user_data`, `cache`, `generated_artifacts`, or `backup`.
- **Build output without sibling checks.** §8 says "`bin` and `obj` next to sources". The grammar has no sibling condition. Instead, a folder named `bin`, `obj`, `target`, or `build` is `generated_artifacts` when its subtree holds compiled-output signals (`*.class`, `*.o`, `*.obj`, `*.pyc`) at or above a minimum count. A program's `bin` folder of `.dll` files therefore stays part of its application.
  - Rejected: a sibling condition in the grammar. It needs the parent's listing while classifying a child, before the parent is finished.
- **Triage:**

  | Triage | Categories |
  |---|---|
  | keep | personal_media, documents, source_project, application_user_data |
  | discard | system_junk, cache, temporary_data, generated_artifacts, installer_download, application_installation |
  | review | the other categories |

  `*.CHK` and `found.000` are `system_junk` but get `review`.
- **The veto** (R1.5). A folder whose triage would be `discard` and whose subtree holds user-material indicators gets `review`, `veto = 1`, and up to 20 examples. The scanner keeps a bounded list of indicator entries per folder and merges it upward.
  - Indicator signals are those flagged `indicator = true` in markers v3: office documents, camera-named photos (`DSC*`, `IMG_*`, `P*.JPG`, and photos under `DCIM`), saves, profiles, mail stores, credentials, and databases.
  - Icons and other images that programs ship are not indicators. Otherwise every application folder would be vetoed.
- **Explanation:** each rule's `explain` sentence plus the cited names. No coverage disclaimers, because the index is complete (§4.6).
- **Rejected:**
  - Keeping the depth-0 descriptor input. That was the cause of 11 of 16 `unknown` folders.
  - Classifying in SQL after the scan. It is a second pass.

### D10. Decisions and tags (§6.7, §6.9; slice D)

- **Own decision:** `entries.decision` ∈ {NULL (inherit), `undecided`, `keep`, `discard`, `later`}.
- **Effective decision:** stored in `entries.eff_decision`, with `eff_from`, the entry whose own decision applies, or NULL for the default `undecided`.
- **Changing a decision.** The command updates the target, then the subtree by path range in the same transaction, stopping at descendants that have their own decision. It uses `WITH RECURSIVE` over children to find those cut points, then path-range updates that exclude the cut subtrees.
- **The scanner** sets a new row's `eff_decision` and `eff_from` from its parent's row, read inside the writing transaction.
- **Bulk and individual.**
  - An individual request (`entry_id`) can set any value on that one entry, including changing a keep.
  - A bulk request (`entry_ids` or `selection_id`) skips every entry whose `eff_decision = 'keep'` unless the requested value is `keep`. It reports the count and up to 100 of the skipped entries (R1.11).
- **Home totals** are `SUM(size) … GROUP BY eff_decision` over present files of the source.
- **Tags.** Own tags are rows in `entry_tags`. Effective tags are an entry's own tags plus those of its ancestors, found by walking `parent_id` (depth is bounded by the tree). A tag search joins the tagged entries' paths as prefix ranges.
  - Removing a tag in bulk removes own tags only. An inherited tag can only be removed at the folder it comes from.
- **Selections** (§11.3 "select all results"). `create-selection` resolves a search query to explicit entry IDs inside its transaction and stores them with a one-hour expiry. Bulk decisions and tags then name the selection. The confirmation the owner sees before applying shows its count, its bytes, and its kept count.
- **No revision compare-and-swap on decisions.** A single owner, cheap and reversible decisions, and plans that re-check everything later (R4). Each command writes one audit event.
- **Rejected:**
  - Computing effective decisions on read. The decision filter in search and the Home totals would need ancestor resolution for every candidate row.
  - Materializing effective tags. Entries × tags rows for every descendant of a tagged folder.
  - v0.2's per-entry `intent_revision`. It gives 409s in a one-owner tool.

### D11. Read API and paging (§11; slice Q)

- **Children** are paged by keyset cursor over `(parent_id, <sort key>, id)`, with one index per sort key: `total_bytes`, `total_files`, `newest_ns`, and `name`.
- **Treemap:** one level per call. It returns the top 300 children by `total_bytes`, plus an `other` bucket with their count and bytes.
- **Search:**
  - name through the FTS5 trigram index (`entry_names`, contentless, `contentless_delete=1`) over the display name;
  - the other filters through columns, path prefix as a range on `(source_id, path)`, and tags as prefix ranges (D10).
  - Search pages carry an exact count up to 10,000 and `"10000+"` beyond.
- **The 300 ms and 500 ms targets** (R1.10) are tested on a 2-million-entry tree made by scanning a `synthfs` generated tree (build tag `slow`).
- **Rejected:**
  - `LIKE '%x%'` without FTS. A full scan of 2 million names on every keystroke.
  - Offset paging. It gets slow on deep pages.

### D12. The viewer (§11.12; slice Q)

- **`GET /api/entries/{id}/content`** streams the file through `fsaccess` (identity checked against the row, `O_NOATIME`), with `http.ServeContent` for range requests. A file whose identity on disk no longer matches its row is `409 invalid_entry_state`; a rescan updates it. The response type comes from Precious's own table, never from sniffing:

  | Kind | Content-Type | Extra headers |
  |---|---|---|
  | jpeg, png, gif, webp, avif, bmp | `image/*` | `CSP: sandbox; default-src 'none'` |
  | svg | `image/svg+xml` | `CSP: sandbox; default-src 'none'; style-src 'unsafe-inline'` |
  | mp4, m4v, webm, mov | `video/*` | sandbox CSP |
  | mp3, m4a, aac, ogg, opus, wav, flac | `audio/*` | sandbox CSP |
  | pdf | `application/pdf` | `CSP: default-src 'none'; frame-ancestors 'self'` (no sandbox: Chrome refuses PDFs in sandboxed frames) |
  | anything else | `application/octet-stream` | `Content-Disposition: attachment` |

  Every response also carries `X-Content-Type-Options: nosniff` and `Cross-Origin-Resource-Policy: same-origin`. HTML, XML, and SVG never get a type a browser would run as a page in the app's origin (R1.13).
- **`GET /api/entries/{id}/text`** returns the first 1 MiB decoded as JSON, with this precedence:
  1. a BOM, giving UTF-8 or UTF-16;
  2. valid UTF-8;
  3. otherwise Windows-1252, through `golang.org/x/text/encoding/charmap`.

  The client renders source code with highlight.js and Markdown with `marked` plus DOMPurify, with no remote images and links that open nothing in the app's origin.
- **The application CSP** gains `media-src 'self'` (video and audio) and `frame-src 'self'` (the PDF frame). Everything else stays as today, including `img-src 'self'`.
- **Rejected:**
  - Sniffing types (`http.DetectContentType`). It turns an `.svg` or `.html` file into a page.
  - Serving content from a separate origin. That needs a second listener or host, which is a deployment burden for a LAN tool.

### D13. Session endpoints and serving the SPA (§12; coordinator)

- **The session endpoints:**
  - `GET /api/session` creates a pre-login session when there is none (`auth.Service.StartPreLogin`) and returns `{authenticated, csrf_token, admin_exists}`.
  - `POST /api/session/login` takes `{password}`.
  - `POST /api/session/logout`.

  The form `/login` and `/logout` handlers are removed. CSRF stays as today: the `X-CSRF-Token` header plus an `Origin` equal to `server.external_origin`.
- **Serving.** `GET /assets/*` serves hashed files with `Cache-Control: immutable`. Any other non-`/api` GET serves `index.html` (`no-store`), so client routes deep-link. Both are public. Every other `/api` path returns `401` JSON without a session.
- **Without a built UI.** `web/dist` holds a committed placeholder. When no built `index.html` is embedded, the server serves a short page saying how to build the UI (`make ui`), so `go build` and `go test` never need Node.
- **Cookie names:** `__Host-precious_session` and `precious_session`.
- **Plain HTTP on a LAN** (§3): `allow_insecure_http = true` now permits an `http://` external origin on any host. A non-loopback `listen` still needs `allow_non_loopback_listen = true`.
- **Rejected:** a server-rendered login page kept beside the SPA. Two UIs, and two CSRF paths.

### D14. Front-end stack and layout (§11, §12; slice U)

- **Stack:**
  - `web/ui` is a Vite + React + TypeScript project, with npm and a committed lockfile.
  - Routing: React Router.
  - Data: TanStack Query.
  - Tables: TanStack Table with TanStack Virtual.
  - Treemap: ECharts, with only the treemap chart and canvas renderer imported.
  - Styling: Tailwind CSS with Radix-based components (shadcn/ui, vendored).
  - Translation: react-i18next, with `en` only in R1.
  - Viewer: highlight.js (core plus listed languages), `marked`, and DOMPurify.
  - Tests: Vitest, and Playwright for end-to-end.
- **Build.** `vite build` writes `web/dist`, which `web/embed.go` embeds (`//go:embed all:dist`).
- **No inline `<script>` or `<style>`,** so the strict CSP holds. React's style properties go through CSSOM, which `style-src 'self'` allows.
- **Routes:**
  - `/login`;
  - `/` (Home);
  - `/map/:entryId`;
  - `/search`;
  - `/sources`.

  The detail panel is `?entry=<id>` on Map and Search. Job progress comes from `/api/events` (EventSource) into the Query cache.
- **The vocabulary rule** (§11.11) is enforced by a Vitest test. It fails when any `en` string contains a banned term: atomic, expanded, descriptor, epoch, intent revision, frontier, or coverage scope.
- **Rejected:**
  - Material UI and Emotion. They inject `<style>` at runtime, which the CSP blocks.
  - Mantine. It injects a CSS-variables `<style>` tag.
  - Committing `web/dist` builds. Noisy diffs and stale assets.

### D15. Jobs changes (§12)

- **Registration.** The `scan` kind is registered by `index`, with class `ClassReconciliation`, which keeps its name for compatibility of the arbiter's weights. `KindIntake`, `PauseNodeBudgetReached`, and `PendingWork` are removed.
- **Single flight.** One active scan per source is enforced by the existing partial unique index on `kind = 'scan'`.
- **The claim key** becomes `sources.device_key` (D4), or `source:<id>` while it is unknown.
- **Rejected:** a new runner. The current one is tested and fits.

### D16. Configuration (§12)

- **Top level:** `state_dir`, `[server]`, `[auth]`, `[jobs]`, `[sources]` (`allowed_roots`), and `[scan]` (`batch_size` 1,000; `list_batch` 256).
- **Removed keys are unknown keys,** so an old configuration fails with every offending key named. That is the existing strict loader, and it is intended (**BREAKING**).
- **Rejected:** accepting and ignoring old keys. It hides that features are gone.

### D17. CLI and naming (§12)

- **`cmd/precious` subcommands:** `serve`, `check-config`, `admin set-password`, `backup`, and `version`. The default paths are `/etc/precious/precious.toml` and `/var/lib/precious`.
- **Development tools** are not product commands:
  - `tools/gencorpus` writes the regression corpus to a directory;
  - `tools/walkbench` runs a bare metadata walk and a scan into a scratch database and prints both times and their ratio (R1.14).
- **Deploy files** are renamed to `precious` throughout.
- **Rejected:** a `precious dev` subcommand. Test tooling does not belong in the product's help.

### D18. Regression corpus (§15; coordinator)

- **`internal/corpus`** builds the §15 tree into either a `synthfs.FS` or a real directory, through one builder interface. It also writes `ground_truth.json`, listing for each path its kind and, where the corpus asserts it, the expected category, triage, veto, and file kind.
- **Fixtures** include the FAT-capability fixture (synthfs), non-UTF-8 names, and an unreadable folder. On a real directory that folder gets mode 000, and its test is skipped when running as root.
- **Rejected:** checking in a corpus. It is binary-heavy, and names that are not valid UTF-8 do not survive every checkout.

### D19. CI and the R1.14 measurement (§7, §14)

- **`.github/workflows/ci.yml`:**
  - `go vet` and `go test -race ./...` on Linux;
  - `go build` for linux, darwin, and windows on amd64 and arm64 (R1.15);
  - `npm ci`, `npm run build`, `npm test`, and the Playwright suite against the built binary on the generated corpus.
- **The measurement is warm, not cold** (deviation from §7's wording). The reference server's ZFS ARC cannot be dropped without exporting the pool. So `walkbench` primes the cache with one bare walk, then times a bare walk and a scan, each warm. A warm bare walk is the fastest baseline, so the ratio is a stricter bound than the cold one the spec names. If the warm bound fails, the owner can export and import the pool, run cold, and record both numbers. `precious-spec-v0.3.md` §7 stays as written: cold is the target, and warm is how R1 checks it first.
- **Rejected:** dropping caches automatically. It needs root and unmounts the pool.

### D20. Deferrals that touch spec wording

These spec items do not ship in R1. Each is named in the proposal with its milestone:
- owner category overrides (§6.6 precedence lists them first): R2;
- owner-marked groups (§6.5): R2;
- scheduled rescans (§7): R2;
- recognizing moves made outside Precious by file identity (§7 "file identity and path"): R3.

R1 matches by path (D6).

### D21. Folder composition and what is inside (owner smoke test; slices X, Q, U)

- **Problem found in the smoke test (10.8)** on the owner's archive:
  - a folder's single category hid what else it holds: `archive/local` read "Personal media", though it holds `downloads` and the installed program `homeplanner`;
  - because a folder outside Containers counted its whole subtree under its own family, Home put all 779 GiB under Personal;
  - a loose JPEG with no matching rule read "Not classified, Containers", so photo folders turned gray in the treemap.
- **Composition is computed from content, bottom-up** (D7 step 3). Only a group outside Containers counts as one unit, because it is reviewed as one item. A `backup` group and every non-group folder contribute their content. Home sums the roots' compositions, so it shows the real split.
- **File family.** `domain.FileFamily(category, fileKind)` gives a file's family: its category's family, or, for `unknown`, the file-kind mapping of D7. The scanner and the read API share it. An entry's `category` and `family` fields still follow its category.
- **Composition on every row.** EntryRow gains `composition`: `dir_stats.by_family` for folders, one element from the file family for files. The interface uses it in three places:
  - the Map table and Search results draw it as a bar;
  - the category label adds the dominant family's share when another family holds at least 1% of the bytes, for example "Personal media · mostly personal (98%)";
  - the treemap's family colors use the dominant composition family, so loose photos are Personal.

  The dominant family is the one with the most bytes; ties go in the order personal, programs, disposable, containers.
- **Inside.** `dir_stats.inside` lists up to 10 notable entries below a folder, largest first:
  - groups, of any category;
  - folders with less than half of their bytes in the folder's dominant family;
  - files whose file family differs from the folder's dominant family.

  A notable entry is not searched further. A non-notable child folder contributes its own list, and the post-order walk merges these lists. Each child keeps its 10 largest, so the merged 10 are exact. Each item carries `entry_id`, `path`, `path_b64`, `category`, `family`, `group`, `bytes`, and `files`. The detail panel shows the list as "Inside this folder", each item a link.
- **Rejected:**
  - A single "mixed" badge: it shows no proportions and leaves Home wrong.
  - Computing composition or the inside list at read time: it takes a subtree pass per request, too slow at 2 million entries.

### D22. Previews in the detail panel (owner smoke test; slice U)

- **Preview without "Open".** The detail panel shows a file's preview directly:
  - an image: the file itself through `/content`, scaled by the browser and loaded lazily;
  - video and audio: players with `preload="metadata"`;
  - text, source code, and Markdown: the first 40 lines from `/text`, rendered as in the viewer;
  - a PDF: a frame of the panel's width.

  Other types say there is no preview and offer the download. "Open" keeps the full viewer.
- **No new endpoint.** The same safety rules as the viewer apply (D12). Server-made thumbnails stay in R7 (§11.12), so a large photo is transferred whole, which is acceptable on a local network.

### D23. Screens fit the window (owner smoke test; slice U)

- **Map table and Search results never spill out of their cards.** Columns have priorities: Changed and Suggestion hide first when the card is narrow. A table that still does not fit scrolls horizontally inside its card, with header and rows aligned.
- **The detail panel does not squeeze the Map.** Below 1,600 px of window width it opens over the Map instead of beside it.
- **Search filters** are laid out on a grid with no overlapping controls.
- **Checked by Playwright** at 1366×768 and 1920×1080, on the Map with and without the panel, on Search, and on the detail panel:
  - no element extends past its card or the viewport;
  - no two controls overlap;
  - screenshots are reviewed.

## Interfaces

### Schema (`migrations/0001_baseline.sql`)

```sql
-- jobs, job_events, command_requests, users, sessions, audit_events: copied from the
-- v0.2 0001/0002 definitions, except jobs.source_id REFERENCES sources(id) ON DELETE CASCADE
-- and job_events.job_id REFERENCES jobs(id) ON DELETE CASCADE (addendum A1).
CREATE TABLE sources (
  id            TEXT PRIMARY KEY,
  label         TEXT NOT NULL,
  volume_kind   TEXT NOT NULL CHECK (volume_kind IN ('uuid','zfs','fsid','path')),
  volume_id     TEXT NOT NULL,
  volume_label  TEXT,
  fs_type       TEXT NOT NULL,
  strong        INTEGER NOT NULL CHECK (strong IN (0,1)),
  rel_root      BLOB NOT NULL,              -- '/'-joined, '' = volume root
  device_key    TEXT,
  capabilities  TEXT NOT NULL,              -- JSON Capabilities (below)
  state         TEXT NOT NULL CHECK (state IN ('online','offline','unavailable')),
  state_reason  TEXT,
  mount_point   BLOB,                       -- absolute, NULL when offline
  scan_gen      INTEGER NOT NULL DEFAULT 0,
  rules_version TEXT,
  last_scan_at  INTEGER, last_scan_job INTEGER,
  unresponsive_since INTEGER,
  created_at    INTEGER NOT NULL,
  UNIQUE (volume_kind, volume_id, rel_root)
);
CREATE TABLE entries (
  id           INTEGER PRIMARY KEY,
  source_id    TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  parent_id    INTEGER REFERENCES entries(id) ON DELETE CASCADE,
  name         BLOB NOT NULL,
  path         BLOB NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('directory','file','symlink','special')),
  special_kind TEXT,
  size         INTEGER NOT NULL DEFAULT 0,
  alloc        INTEGER,
  total_bytes  INTEGER NOT NULL DEFAULT 0,  -- file: size; directory: subtree sum
  total_files  INTEGER NOT NULL DEFAULT 0,  -- file: 1; directory: subtree count
  mtime_ns     INTEGER, ctime_ns INTEGER,
  newest_ns    INTEGER, oldest_ns INTEGER,  -- directory: subtree range; file: own mtime
  dev INTEGER, ino INTEGER, nlink INTEGER, mode INTEGER,
  link_text    BLOB,
  ext          TEXT, file_kind TEXT, main_kind TEXT,
  category     TEXT, family TEXT, traits TEXT,      -- traits: JSON array of trait ids
  triage       TEXT, is_group INTEGER NOT NULL DEFAULT 0, veto INTEGER NOT NULL DEFAULT 0,
  rule_ids     TEXT,                                -- JSON array of rule ids
  state        TEXT NOT NULL CHECK (state IN ('present','missing','unreadable')),
  partial      INTEGER NOT NULL DEFAULT 0,
  mount_boundary INTEGER NOT NULL DEFAULT 0,
  first_seen   INTEGER NOT NULL, last_seen INTEGER NOT NULL, missing_since INTEGER,
  scan_gen     INTEGER NOT NULL,
  decision     TEXT CHECK (decision IN ('undecided','keep','discard','later')),
  decision_at  INTEGER,
  eff_decision TEXT NOT NULL DEFAULT 'undecided' CHECK (eff_decision IN ('undecided','keep','discard','later')),
  eff_from     INTEGER REFERENCES entries(id) ON DELETE SET NULL,
  UNIQUE (source_id, path)
);
CREATE INDEX entries_by_name   ON entries(parent_id, name, id);
CREATE INDEX entries_by_bytes  ON entries(parent_id, total_bytes, id);
CREATE INDEX entries_by_files  ON entries(parent_id, total_files, id);
CREATE INDEX entries_by_newest ON entries(parent_id, newest_ns, id);
CREATE INDEX entries_decided   ON entries(source_id, decision) WHERE decision IS NOT NULL;
CREATE INDEX entries_eff       ON entries(source_id, eff_decision, kind);
CREATE INDEX entries_eff_from  ON entries(eff_from) WHERE eff_from IS NOT NULL;  -- addendum A2
CREATE TABLE dir_stats (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  dirs INTEGER NOT NULL, files INTEGER NOT NULL, symlinks INTEGER NOT NULL, specials INTEGER NOT NULL,
  unreadable INTEGER NOT NULL, mount_boundaries INTEGER NOT NULL,
  by_kind TEXT NOT NULL,     -- {"image":{"files":83,"bytes":68516866}, ...}
  by_year TEXT NOT NULL,     -- {"2004":{"files":22,"bytes":18264064}, ...}  (mtime year, UTC)
  by_family TEXT NOT NULL,   -- {"personal":{"files":..,"bytes":..},"programs":..,"disposable":..,"containers":..}  composition, bottom-up (D7, D21)
  signals TEXT NOT NULL,     -- {"installer_name_present":3, ...}  subtree counts
  indicators TEXT NOT NULL,  -- [{"entry_id":"812","path_b64":"...","path":"...","signal":"office_document"}] ≤ 20
  inside TEXT NOT NULL       -- [{"entry_id","path_b64","path","category","family","group","bytes","files"}] ≤ 10, largest first (D21)
);
CREATE VIRTUAL TABLE entry_names USING fts5(name, content='', contentless_delete=1,
  tokenize='trigram case_sensitive 0');                 -- rowid = entries.id, name = display name
CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE, created_at INTEGER NOT NULL);
CREATE TABLE entry_tags (
  entry_id INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  added_at INTEGER NOT NULL, PRIMARY KEY (entry_id, tag_id)) WITHOUT ROWID;
CREATE INDEX entry_tags_by_tag ON entry_tags(tag_id, entry_id);
CREATE TABLE selections (id TEXT PRIMARY KEY, query TEXT NOT NULL, count INTEGER NOT NULL,
  bytes INTEGER NOT NULL, kept INTEGER NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
CREATE TABLE selection_entries (selection_id TEXT NOT NULL REFERENCES selections(id) ON DELETE CASCADE,
  entry_id INTEGER NOT NULL, PRIMARY KEY (selection_id, entry_id)) WITHOUT ROWID;
```

`sources.capabilities` JSON, which slices S, X, and Q read:
`{"known":true,"read_only":false,"case_sensitive":true,"normalization_sensitive":true,"stable_identity":true,"local_time":false,"hard_links":true,"time_resolution_ns":1}`.

### Go signatures (foundation unless marked)

```go
// internal/domain
type EntryID int64                         // replaces NodeID; String/ParseEntryID like NodeID
type FileKind string                       // image video audio document source archive installer executable system other
type Category string                       // the 16 of §6.6
type Family string                         // personal, programs, disposable, containers
type Trait string                          // contains_user_material contains_credentials contains_database contains_vcs possible_generated_content
type Triage string                         // keep discard review
type Decision string                       // undecided keep discard later ("" = inherit, own value only)
func FamilyOf(Category) Family
func FileFamily(Category, FileKind) Family // FamilyOf, or the file-kind mapping of D7 for unknown (D21)
// error codes added: outside_allowed_roots(403) source_exists(409) source_offline(409)
// job_active(409) tag_exists(409) selection_expired(409) invalid_entry_state(409)

// internal/fsaccess (slice P implements; foundation pins)
type VolumeKind string                     // "uuid" "zfs" "fsid" "path"
type Volume struct { Kind VolumeKind; ID, Label, FSType, DeviceKey string; Strong bool }
type Mount struct { Point string; Root []byte; Volume Volume; ReadOnly bool }
type Capabilities struct {
    Known, ReadOnly, CaseSensitive, NormalizationSensitive, StableIdentity, LocalTime, HardLinks bool
    TimeResolution time.Duration
}
type FS interface {
    OpenRoot(path string) (Dir, error)
    Mounts() ([]Mount, error)
    Capabilities(path string) (Capabilities, error)
}
// Dir, File, EntryInfo, DirEntry, Error: unchanged. EntryInfo.Dev/Ino are meaningful only when StableIdentity.
// synthfs gains SetVolume(dev uint64, Volume), SetCapabilities(dev uint64, Capabilities), Remount(dev) (new inodes when !StableIdentity).

// internal/jobs
type Registry interface {
    DeviceKey(ctx context.Context, q Queryer, source domain.SourceID) (string, error)
    SetUnresponsive(ctx context.Context, source domain.SourceID, since time.Time) error
}

// internal/sources (slice S)
type State string                          // online offline unavailable
type Source struct {
    ID domain.SourceID; Label string; Volume fsaccess.Volume; RelRoot []byte
    State State; StateReason string; MountPoint string; Caps fsaccess.Capabilities
    RootEntry domain.EntryID; ScanGen int64; LastScanAt *time.Time
}
type Opened struct { Source Source; Root fsaccess.Dir; AbsRoot string }
func New(st *store.Store, fs fsaccess.FS, cfg config.Sources, clk clock.Clock) (*Service, error)
func (s *Service) List(ctx context.Context) ([]Source, error)
func (s *Service) Get(ctx context.Context, id domain.SourceID) (Source, error)
func (s *Service) Open(ctx context.Context, id domain.SourceID) (Opened, error)   // resolves the mount; source_offline when absent
func (s *Service) Refresh(ctx context.Context) error
func (s *Service) PickerRoots(ctx context.Context) ([]PickerItem, error)
func (s *Service) PickerChildren(ctx context.Context, handle string) (PickerListing, error)
type PickerItem struct { Handle, Name, Path, VolumeLabel, FSType string; IsSource bool }
type PickerListing struct { Entry PickerItem; Children []PickerItem; Truncated bool }
// commands use: PrepareAdd(ctx, handle, label) (Candidate, error); Add(ctx, tx, Candidate) (Source, error);
// Rename(ctx, tx, id, label) error; Remove(ctx, tx, id) error. Service implements jobs.Registry.

// internal/rules (slice R)
type SignalID string
type Policy struct{ /* … */ }
func Default() *Policy
func Load(markers, rules []byte) (*Policy, error)
func (p *Policy) Version() string                                      // "rules-v2+markers-v3"
func (p *Policy) FileKind(name []byte) domain.FileKind
func (p *Policy) AppendNameSignals(dst []SignalID, name []byte, kind domain.EntryKind) []SignalID
func (p *Policy) IsIndicator(SignalID) bool
type KindTotals struct{ Files, Bytes int64 }
type FileFacts struct { Name []byte; Kind domain.FileKind; Size int64; SiblingStems map[string]bool }
type FolderFacts struct {
    Name []byte
    ChildSignals map[SignalID]int; SubtreeSignals map[SignalID]int
    Files, Bytes int64; ByKind map[domain.FileKind]KindTotals
    Indicators int
}
type Result struct {
    Category domain.Category; Family domain.Family; Traits []domain.Trait; Triage domain.Triage
    Group, Veto bool; Rules []string; Reason string   // matched | conflicting_rules | no_rule_matched
}
func (p *Policy) ClassifyFile(FileFacts) Result
func (p *Policy) ClassifyFolder(FolderFacts) Result
func (p *Policy) Explain(ruleIDs []string) []string

// internal/index (slice X)
const KindScan jobs.Kind = "scan"
func NewHandler(st *store.Store, src *sources.Service, pol *rules.Policy, clk clock.Clock, cfg config.Scan) *Handler  // jobs.Handler
func StartScan(ctx context.Context, tx *jobs.Tx, src domain.SourceID) (jobs.Accepted, error)              // single flight
// test helper (foundation): indextest.Seed(t, st, tree) writes rows exactly as the scanner would (paths, totals, eff_*).

// internal/decisions (slice D)
type Mode int // Individual, Bulk
type SetDecision struct { Decision domain.Decision; Inherit bool; EntryID domain.EntryID; EntryIDs []domain.EntryID; SelectionID string }
type Skipped struct { EntryID domain.EntryID; Path string; PathB64 []byte }
type SetDecisionResult struct { Applied int; SkippedCount int; Skipped []Skipped }   // Skipped ≤ 100
func (s *Service) SetDecision(ctx context.Context, tx *sql.Tx, req SetDecision) (SetDecisionResult, error)
type SetTags struct { EntryIDs []domain.EntryID; SelectionID string; Add, Remove []int64 }
func (s *Service) SetTags(ctx context.Context, tx *sql.Tx, req SetTags) (applied int, err error)
func (s *Service) CreateTag(ctx, tx, name string) (Tag, error); RenameTag(ctx, tx, id int64, name string) error; DeleteTag(ctx, tx, id int64) error
func (s *Service) CreateSelection(ctx context.Context, tx *sql.Tx, q search.Query) (Selection, error)
func InheritFrom(ctx context.Context, tx *sql.Tx, parent domain.EntryID) (eff domain.Decision, from *domain.EntryID, err error) // scanner calls in its batch tx
func Effective(ctx context.Context, q store.Queryer, id domain.EntryID) (Intent, error)
type Intent struct { Own domain.Decision; Effective domain.Decision; From *Ref; Tags []TagRef }
type TagRef struct { ID int64; Name string; Own bool; From *Ref }
type Ref struct { ID domain.EntryID; Path string; PathB64 []byte }
func Totals(ctx context.Context, q store.Queryer, src domain.SourceID) (map[domain.Decision]Total, error)

// internal/search (slice Q)
type Query struct {
    Source domain.SourceID; Name string; Ext []string; FileKinds []domain.FileKind
    MinSize, MaxSize *int64; YearFrom, YearTo *int; Categories []domain.Category; Triages []domain.Triage
    Tags []int64; Decisions []domain.Decision; Within *domain.EntryID; Sort, Order string
}
func Parse(v url.Values) (Query, error)
func Page(ctx context.Context, q store.Queryer, query Query, cursor string, limit int) (Result, error)
func Resolve(ctx context.Context, q store.Queryer, query Query, max int) ([]domain.EntryID, error)   // max 1,000,000
```

### Commands (`POST /api/commands/{name}`, `Idempotency-Key` required, envelope unchanged)

| Command | Request | Success | Errors |
|---|---|---|---|
| `add-source` (S) | `{"handle":"…","label":"Old disk"}` (label optional: folder name) | 201 `{"source":SourceJSON}` | 400 invalid_request (bad handle); 403 outside_allowed_roots; 409 source_exists (same volume and root, or nested in/around one) |
| `rename-source` (S) | `{"source_id","label"}` | 200 `{"source":SourceJSON}` | 404 unknown_source |
| `remove-source` (S) | `{"source_id"}` | 200 `{}` | 404; 409 job_active |
| `start-scan` (X) | `{"source_id"}` | 202 `{"job_id","state","coalesced"}` | 404; 409 source_offline |
| `cancel-job` | unchanged | | |
| `set-decision` (D) | individual: `{"entry_id":"12","decision":"keep"}`; bulk: `{"entry_ids":["12",…≤1000],"decision":"discard"}` or `{"selection_id":"…","decision":"later"}`. `"decision":"inherit"` clears the own decision | 200 `{"applied":n,"skipped_count":k,"skipped":[{"entry_id","path","path_b64"}]}` (skipped is always empty for individual) | 400 (both or neither of entry_id and entry_ids/selection_id; over 1000); 404 not_found (any ID, and the whole request fails); 409 selection_expired |
| `set-tags` (D) | `{"entry_ids":[…]}` or `{"selection_id"}`, `"add":[tag_id]`, `"remove":[tag_id]` | 200 `{"applied":n}` | 400; 404 (entry or tag); 409 selection_expired |
| `create-tag` / `rename-tag` / `delete-tag` (D) | `{"name"}` / `{"tag_id","name"}` / `{"tag_id"}` | 201 / 200 / 200 `{"tag":{"id","name"}}` | 400 (empty name, over 64 characters); 409 tag_exists; 404 |
| `create-selection` (D) | `{"query":{…search query fields…}}` | 201 `{"selection_id","count","bytes","kept":{"count","bytes"},"expires_at"}` | 400 |

Each command writes its audit event in its transaction: `source_added`, `source_renamed`, `source_removed`, `decision_set` (one per request, with counts), `tags_set`, and `tag_created`, `tag_renamed`, `tag_deleted`.

### Read endpoints (Q; S for sources and picker; coordinator for session)

- **`GET /api/session`** → `{"authenticated":bool,"csrf_token":"…","admin_exists":bool}`.
- **`POST /api/session/login`** with `{"password"}` → 200 `{"authenticated":true,"csrf_token"}` or 401 `login_failed`.
- **`POST /api/session/logout`** → 204.
- **`GET /api/sources`** → `{"sources":[SourceJSON]}`, where SourceJSON is:
  `{"id","label","state","state_reason","mount_point","path","rel_root","volume":{"kind","id","label","fs_type","strong"},"capabilities":{…},"root_entry_id","totals":{"bytes","files","dirs"},"last_scan_at","active_job":{"job_id","state","progress"}|null}`.
  `path` and `rel_root` are display strings: `path` is the source's absolute root folder where its volume is mounted now (through a mount of the volume that holds it, so a bind mount is accounted for), null while the source has no mount point; `rel_root` is the root folder inside the volume, `""` for the volume's root.
- **`GET /api/picker`** → `{"roots":[PickerItem]}`.
- **`GET /api/picker?handle=H`** → `{"entry":PickerItem,"children":[PickerItem],"truncated":bool}`, with at most 1,000 children (folders only). It returns 400 for a bad handle and 403 `outside_allowed_roots`.
- **`GET /api/home?source=ID`**, where an omitted source means all of them, returns:
  `{"totals":{"bytes","files","dirs"},"by_family":[{"family","bytes","files"}],"by_kind":[…],"by_year":[{"year","bytes","files"}],"decisions":{"undecided":{"bytes","files"},"keep":…,"discard":…,"later":…},"partial":bool,"scans":[{"source_id","job_id","state","progress"}]}`.
  The family, kind, and year breakdowns are the root entries' `dir_stats`, summed over sources.
- **EntryRow** is the row shape used by children, treemap, and search:
  `{"id","source_id","name","name_b64","path","path_b64","kind","file_kind","main_kind","category","family","triage","group","veto","size","total_bytes","total_files","mtime","newest","oldest","state","partial","mount_boundary","decision":own|null,"eff_decision","tag_ids":[own],"composition":[{"family","bytes","files"}]}`.
  `composition` is the folder's `by_family`, or for a file one element under its file family (D21).
  Times are RFC 3339, or null.
- **`GET /api/entries/{id}`** → `{"entry":EntryRow,"ancestors":[{"id","name","name_b64"}],"classification":{"category","family","traits","triage","group","veto","rules":[{"id","explain"}],"indicators":[…]},"intent":{"decision","eff_decision","from":Ref|null,"tags":[{"id","name","own","from":Ref|null}]},"stats":{"dirs","files","unreadable","mount_boundaries","by_kind","by_year","inside":[{"entry_id","path","path_b64","category","family","group","bytes","files"}]}|null}`.
- **`GET /api/entries/{id}/children?sort=bytes|files|newest|name&order=desc|asc&cursor=&limit=`**, where limit defaults to 200 and is capped at 1,000 → `{"items":[EntryRow],"next_cursor":string|null}`.
- **`GET /api/entries/{id}/treemap`** → `{"entry":EntryRow,"items":[EntryRow],"other":{"count","bytes"}}`.
- **`GET /api/search?…`** (fields of `search.Query`, plus `cursor` and `limit`) → `{"items":[EntryRow],"next_cursor","count":n|"10000+"}`.
- **`GET /api/tags`** → `{"tags":[{"id","name","own_count"}]}`.
- **`GET /api/entries/{id}/content`**: see D12. **`GET /api/entries/{id}/text`** → `{"encoding","text","truncated":bool,"language":string|null,"markdown":bool}`.
- **Read errors:**
  - 404 `not_found` (an unknown or malformed ID);
  - 400 `invalid_request` (a bad query value or cursor);
  - 409 `source_offline` (content or text of an offline source);
  - 409 `invalid_entry_state` (content of a non-file, or of a missing entry).
- **Jobs and events:** `GET /api/jobs/{id}` and `GET /api/events` are unchanged. A scan's progress keys are listed in D7.

## Transaction boundaries

| Path (transaction) | Re-checked inside the writing transaction | A command commits just before | A command commits just after |
|---|---|---|---|
| Scan batch (X) | That the job still owns its lease, through the runner; the source row exists. Each new row's parent row exists and its `eff_decision`/`eff_from` are read here | `set-decision` on an ancestor: the batch reads the updated value and inherits it. `remove-source`: the source row is gone, the batch fails `unknown_source`, and the job ends | `set-decision` updates the subtree by path range and covers the rows just inserted. `remove-source` deletes them by cascade |
| Scan finish (X) | The source row exists; this job is the source's active scan | `remove-source`: the finish fails and the job ends | A new `start-scan` coalesces until the job is terminal, then queues |
| `add-source` (S) | No source of the same volume has an equal, enclosing, or enclosed root | Another `add-source` of the same folder: `409 source_exists` | — |
| `remove-source` (S) | No non-terminal scan job for the source | `start-scan`: `409 job_active` | A late scan batch fails (above) |
| `set-decision` (D) | Every named entry exists. For bulk, `eff_decision` is read here, so a keep set just before is skipped. The selection has not expired | Another decision on an ancestor: this request's subtree update starts from the committed state | The scanner inserts children under a decided folder and inherits (above) |
| `set-tags`, tag commands (D) | Entries and tags exist; tag names are unique | `delete-tag`: `404` for that tag | — |
| `create-selection` (D) | The query is resolved against the committed index inside the transaction | Rows a scan just inserted are included | A scan's later inserts are not in the selection, which is the point of explicit IDs |
| Availability refresh (S) | It writes only `state`, `state_reason`, `mount_point`, and `capabilities` of existing sources | `remove-source`: zero rows updated | — |

## Risks / Trade-offs

- **[The warm R1.14 bound may fail on a fast ZFS ARC]** → The pipeline overlaps walking and writing (D7). If it still fails, the cold measurement (D19) decides, and the result is recorded in the design addendum.
- **[Database size]** About 2 million `entries` rows, four child indexes, and FTS trigrams make an estimated 1–1.5 GB. → Documented in the operator docs; the state directory needs that space.
- **[A crash mid-scan re-walks the tree]** → Unchanged rows are not written (D7), so the second walk is mostly reads.
- **[Decision updates on huge subtrees]** Discarding a 500,000-entry folder updates 500,000 rows in one transaction, a few seconds. → It holds the writer lock, so scan batches wait. The interface shows the command as pending.
- **[The portable backend is imprecise on macOS and Windows]** → Its capabilities are marked `Known: false`. The interface shows that the volume cannot be recognized if moved, and R8 replaces the backend.
- **[PDF without a sandbox]** → `default-src 'none'` and `frame-ancestors 'self'`. A PDF runs in the browser's viewer, not as the application's page.
- **[Node in the build]** → It is needed only for `make ui`. `go build` and `go test` work without it (D13).

## Migration Plan

1. Install the `precious` binary beside `curator`, with a new configuration (`/etc/precious/precious.toml`) and a new state directory. `precious admin set-password`.
2. Start `precious serve`, sign in, and add sources through the picker. Nothing is imported from `curator.db`, which no R1 code reads.
3. Rollback: stop `precious` and start `curator` with its untouched configuration and state.

## Addendum: decisions made during implementation

- **A1.** `job_events.job_id` cascades on job deletion. Without it, removing a source whose jobs had events failed on the foreign key.
- **A2.** `entries_eff_from` indexes the `eff_from` foreign key. Without it, every deleted entry made SQLite scan `entries` (`ON DELETE SET NULL`), and removing a 100,000-entry source took minutes. A store test now requires every foreign key into `entries` to be indexed.
- **A3.** `store.Queryer` is the one read interface (`jobs.Registry.DeviceKey` takes it); `jobs.Queryer` is gone. Worker-pool claim keys are `workers:<pool>`, so they cannot collide with the `pool:<pool>` ZFS device keys of D4.
- **A4.** `commands.Register` is a method on the handler, `(*Handler).Register(name, Decoder)`; slices register their commands from `serve`.
- **A5.** Volume identity (D4) resolves `/dev/disk/by-uuid` links through `/sys/class/block/<name>/dev`, so no device node is needed. The systemd unit adds `BindReadOnlyPaths=-/dev/disk` (kept with `PrivateDevices=yes`) and Compose binds `/dev/disk` read-only; without them every ext4/vfat/NTFS volume falls back to the weak `path` identity. The container bind is verified; the systemd bind inside a private `/dev` still needs a check as root.
- **A6.** Rules grammar: `kind_share` takes a list `kinds` (photo folders are mostly video by bytes), rules may set `triage = "review"` (the `*.CHK` and `found.000` exception lives in policy), markers gain `except` patterns and `[[file_kind_pairs]]` (`.bin` with a same-stem `.cue` is a disk image; `rules.FileKindNear`, `PairStem`, `Paired`). Indicators also include recovered fragments and Office autosaves.
- **A7.** Known rule limits: file rules cannot see their folder, so every loose `*.exe` is `installer_download`, including executables inside installed programs and `WINDOWS` (the folder's own category is what makes the group); package folders with a `package.json` inside `node_modules` are `source_project` groups nested in the `generated_artifacts` group.
- **A8.** Search gained the `triage` filter of precious-spec §11.3 (`search.Query.Triages`). Extension, file kind, category, size, year, and triage have no index: alone they read every entry (about 1 s at 2 million entries [estimate]).
- **A9.** A selection's bytes count each byte once: an entry inside a selected folder adds nothing. Kept bytes count a kept entry unless it sits inside a selected folder that is itself kept.
- **A10.** A path whose entry kind changes (a file replaced by a folder) deletes the old row and its subtree, with their decisions and tags, because `UNIQUE (source_id, path)` cannot hold the missing row beside the new one.
- **A11.** Entry IDs are assigned by the scan writer as `MAX(id)+1…` inside each batch transaction, for multi-row inserts; the scanner checks its lease through `jobs.state = 'running'` and the attempt number.
- **A12.** The viewer compares a file with its row by kind, state, size, mtime (resolution, ±1 h on local-time filesystems), and inode only on filesystems with stable identity; `fsaccess.OpenFile` then re-checks the opened handle. Downloads also carry `sandbox; default-src 'none'`.
- **A13.** First warm `walkbench` numbers on the development machine (i9-13980HX, ext4 on NVMe): `/usr` 391,615 entries, walk 1.86 s, scan 23.9 s, ratio 12.8; `/usr/share` 178,818 entries, ratio 12.6. Writing is CPU-bound in SQLite (about 35–55 µs per entry with six `entries` indexes and the trigram FTS); the 2-million-entry synthfs scan runs at about 19,000 entries/s. The warm bound of R1.14 fails here; task 10.6 measures on the reference server (D19).
- **A14.** R1.10 on 2,000,000 scanned entries: children pages by bytes p95 5.7 ms, treemap levels p95 4.6 ms.
- **A15.** R1.14 on the reference server (task 10.6): an Ubuntu 24.04 LXC container on Proxmox VE 9.2, Celeron J4125 (4 cores), 4 GB for the container, 15 GB on the host; source a ZFS dataset mounted into the container, on a RAIDZ1 of four WD40EFZZ hard disks, no L2ARC; state on a separate LVM volume. 1,487,647 entries; peak memory 53 MB; database 1.1 GB (about 780 bytes per entry).
  - **Warm:** walk 29.95 s, scan 4 m 9 s, ratio 8.31.
  - **Cold: walk 170.2 s, scan 208.8 s, ratio 1.23 — within 1.5.**
  - **Method (deviation from "pool export and import"):** `sync; echo 3 > /proc/sys/vm/drop_caches` on the Proxmox host before each run, which empties the dentry and inode caches and shrinks the ARC to its floor without stopping the guests. Before the walk the ARC held 397 MB (below `c_min`, 518 MB) and 11,000 dentries; the walk grew it to 3.7 GB and 1.5 million dentries. Before the scan the ARC was back at 341 MB with 8 MB of dnodes. What stays in the ARC can only speed up the walk, so the ratio is, if anything, overstated.
  - The cold scan ran faster than the warm one (209 s against 249 s); the cause was not investigated.
- **A16.** Owner smoke test (task 10.8), on the reference server, over the regression corpus and the owner's ZFS dataset.
  - **First run, 2026-10-05.** Findings:
    - layout overflow at 1366×768;
    - no preview without Open;
    - mixed folders hidden behind one category, and Home counting 100% as Personal;
    - unclassified photos gray.

    Fixed by D21–D23 (tasks 11.1–11.6).
  - **Second run, 2026-10-06.** One finding: a laptop backup's `Downloads/Downloads` read as a program profile, because an exported bookmarks file or mail archive at its top fired the profile rule (priority 70) over the Downloads name rule (52). The Downloads rule now has priority 75; covered by `TestRulesDownloadsWinOverProfileFiles` and verified by a rescan on the server.
  - **Sign-off:** the owner accepted R1 ("Muito bom! Tá ficando bom"). The one gap he named is duplicate handling, which is R2: duplicate folders, a zip against its unpacked folder, subset or equal comparisons, and the unique files to keep.
