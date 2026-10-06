# Proposal

## Why

Precious cannot answer the owner's first question, "where is my space going?". Most folders show no size, the important ones are classified `unknown`, there is no search, and the interface buries decisions under evidence (ADR 0008). This change is milestone **R1** of `precious-spec-v0.3.md` (§14). It replaces the directory-first catalog with a full metadata index, a portable platform layer, and an embedded single-page app, so the owner can see every folder's size, find any file, look at it, and record decisions and tags. Nothing on disk changes.

## What Changes

- **Full metadata index (§6.2, §6.3, §7).**
  - A scan walks every folder and records every entry, with folder aggregates computed in the same pass.
  - A rescan updates changed rows, marks vanished entries missing, and keeps decisions and tags.
  - Indexing costs at most 1.5 times a bare metadata walk.
- **Sources (§6.1).**
  - Sources are added in the interface through a picker limited to allowed roots, never by typed paths.
  - Each source is located by volume identity plus a path inside the volume.
  - A source whose volume is not mounted is offline but stays browsable. It is recognized again when the volume is mounted at another path.
- **Portable core (§3, §12).**
  - A platform layer with detected filesystem capabilities.
  - Linux is complete; macOS and Windows build and run on a conservative portable backend.
  - CI builds all six OS/architecture targets.
- **Rules v2 (§6.6, §8).**
  - Rules see whole-subtree aggregates and classify files as well as folders.
  - Sixteen categories in four families; groups.
  - A user-material veto that names the indicators it found.
- **Decisions and tags (§6.7, §6.9).**
  - Decisions are undecided, keep, discard, or later, inherited down the tree.
  - Bulk actions never change a keep.
  - Tags are inherited as a union.
- **Interface (§11.1–§11.3, §11.8, §11.10–§11.12).**
  - A React SPA embedded in the binary, in English through translation keys.
  - Screens: login, Home, Map (treemap and table), Search, detail panel, Sources, and live job progress.
  - A viewer for browser-native media, PDF, text, source code, and Markdown, which never lets disk content run as part of the application.
  - After the owner's smoke test: each folder's composition by family and its notable entries inside, a preview in the detail panel, and screens that fit a 1366×768 window (design D21–D23).
- **BREAKING.**
  - A new database (`precious.db`, fresh schema); v0.2 databases are not migrated.
  - `[[sources]]` and the inbox, watch, reconciliation, discovery, inspection, copies, and classifier settings leave the configuration.
  - The binary, commands, cookies, state directory, and deploy files are renamed from `curator` to `precious`.
  - All server-rendered pages and their read API are removed.
- **Removed (git tag `curator-m4b` keeps them):**
  - the directory-first discovery and reconciliation;
  - the filesystem watcher, managed inbox, peek, and aggregate walks;
  - the review queues and the copy search;
  - the whole classifier stack (contract, providers, routing, evaluation).

Acceptance scenarios: R1.1–R1.18 (§14). Each has a spec scenario whose title starts with its ID, and its own test task.

Deferred, with owning milestone:
- **R2:**
  - hashing, duplicates, folder relations, archive members, Opportunities, review lists, Compare, and Gems;
  - owner category overrides and owner-marked groups;
  - scheduled rescans.
- **R3:** write permission and every write to a source; recognizing entries moved outside Precious by file identity.
- **R6:** the classifier contract, providers, routing, and evaluation, rebuilt on the §9.2 state from the `curator-m4b` tag.
- **R7:** optional preview tools (`ffmpeg`, `libvips`, LibreOffice), thumbnails, and pt-BR.
- **R8:** native macOS and Windows backends (mount discovery, volume identity, capabilities), removable-media detection, optical media, and their acceptance runs.

## Capabilities

### New Capabilities

- `file-index`: scanning and rescanning sources into a full metadata index with folder aggregates, missing entries, and the indexing-cost bound.
- `classification`: rules over names and subtree aggregates; categories, families, traits, triage, groups, and the user-material veto. R6 extends it with model suggestions.
- `file-viewer`: opening indexed files in the interface safely. R7 extends it with optional tools.
- `platform-support`: operating systems, architectures, and the portable backend. R8 extends it.

### Modified Capabilities

- `source-registry`:
  - sources are added through the picker within allowed roots;
  - a source is located by volume identity, with offline sources and remounts recognized;
  - configured sources, epochs, and reconfirmation are removed.
- `filesystem-boundary`: the platform layer with detected filesystem capabilities, and lossless names on every platform; v0.2 probe and peek bounds are removed.
- `owner-intent`: inherited decisions and tags with keep-safe bulk actions replace overrides, dispositions, pins, and intent revisions.
- `inventory-explorer`: the SPA screens and JSON read API replace the server-rendered pages.
- `admin-auth`: JSON session endpoints for the SPA, and the rename to `precious`.
- `server-config`: allowed roots; the removed sections; the rename.
- `job-runner`: the scan job replaces the scan, refine, aggregate-walk, and intake jobs.
- `state-store`: the fresh baseline schema in `precious.db`.

Retired entirely (all requirements removed; `.openspec.yaml` sets `retire_capabilities: true`):
- discovery, inspection, and reconciliation: `directory-discovery`, `directory-inspection`, `incremental-reconciliation`;
- the v0.2 inventory model: `inventory-boundaries`, `inventory-accounting`, `directory-classification`;
- the inbox: `inbox-intake`, `inbox-routing`;
- `review-queue` and `content-comparison`;
- the classifier stack: `classifier-routing`, `classifier-providers`, `classifier-evaluation`.

## Impact

- **Removed packages:** `discovery`, `reconcile`, `watch`, `inbox`, `inspection`, `web/explorer`, `compare`, `eval`, `classify` (all of it), `inventory`, `intent`, and `scenario`. Also `web/templates`, `web/static`, and the `cmd/curator` serve tests tied to them.
- **Kept and changed:**
  - `fsaccess`: split into a Linux backend and a portable backend, with volume and capability reporting;
  - `store`: portable;
  - `jobs`: the scan kind, a volume claim key, and no intake class;
  - `commands`: the dispatcher kept, the commands new;
  - `auth` and `web/{middleware,session,authhttp,clientip,apierr}`: JSON session endpoints;
  - `config`, `domain`, and `observe` (name signals and file kinds only).
- **New packages:**
  - platform layer and volume identity;
  - sources;
  - index (scanner and aggregates);
  - rules;
  - decisions and tags;
  - read API;
  - viewer.
- **Front end and tooling:**
  - `web/ui`, a Vite React TypeScript project whose build is embedded;
  - Playwright browser tests;
  - a regression-corpus generator;
  - a GitHub Actions workflow.
- **Database:** one new baseline migration replaces `0001`–`0007`.
- **Dependencies:**
  - Go: `golang.org/x/text`, for Windows-1252 and UTF-16 decoding;
  - Node and npm at build time only.
- **Docs:** `docs/operator.md` rewritten for R1, `README` build instructions, and deploy files renamed.
