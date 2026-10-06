# Design

## Context

This is a greenfield repository: it contains only the v0.2 specification and the OpenSpec scaffolding. Motivation is in proposal.md, and the behavior contracts are in `specs/*/spec.md`. This document covers how M1 is built and the choices the specification leaves open.

Constraints that shape the approach:

- Toolchain: the host has Go 1.22.2, and `GOTOOLCHAIN=auto`. The specification pins Go 1.27, and the current release is go1.27.1 (go.dev/dl, checked 2026-10-01). `os.Root`, required by §12.2, exists only from Go 1.24.
- M1 has no category policy. That arrives in M2. Discovery therefore implements the §5.3 frontier and decision hook with a single outcome, the fallback branch.
- Every later milestone adds tables and columns. M1's schema must extend cleanly through forward migrations, without placeholder columns.

## Goals / Non-Goals

**Goals:**
- Establish the package seams that §8.2 names. Keep mutation-capable code out of the tree entirely, since no `internal/actions` exists in M1.
- Prove the atomic-boundary contract by counting calls on an instrumented filesystem (§14 coding-agent instructions), not by timing.
- Run under a hardened deployment from day one: loopback default, private state directory, read-only source mounts.

**Non-Goals:**
- No `observations` history table, `evidence_dependencies`, dirty scopes, or the location/intent revision counters. M2a and M3 add each one through their own migrations, together with the code that reads it.
- No `openat2`-based resolver and no `O_NOATIME`. See D3.
- No HTML-over-the-wire framework, no frontend build step, and no third-party router or CLI framework.

## Decisions

### D1. Toolchain pin through `go.mod` (§8.1)
`go.mod` declares `go 1.27` and `toolchain go1.27.1`. With `GOTOOLCHAIN=auto`, the host's Go 1.22.2 downloads and runs 1.27.1 automatically. The container builder image is pinned to `golang:1.27.1`.
- Rejected: targeting the host's Go 1.22. It lacks `os.Root` and contradicts the specification.
- Rejected: vendoring a toolchain. The pin is enough, and offline builds are covered in the operator docs.

### D2. TOML configuration with strict decoding (§12.3)
The configuration format is TOML, decoded with `github.com/BurntSushi/toml`. `MetaData.Undecoded()` turns unknown keys into startup errors. The specification requires *commented* example configurations, and TOML supports comments without YAML's implicit typing and indentation hazards.
- Rejected: JSON, which has no comments.
- Rejected: YAML, because of implicit type coercion and a larger parser surface.
- Rejected: environment variables only, which make a list of sources awkward to express.

Secrets are not needed in M1. The configuration grammar nevertheless reserves `*_file` and `*_env` reference fields, so secrets are never written inline in the file.

### D3. Filesystem boundary: `os.Root` plus per-component identity verification (§5.4, §12.2)
`internal/fsaccess` exposes a narrow interface: open the source root; list a directory in batches; lstat a named child; read link text; and report statfs and mount facts. The interface has no write methods.

The real implementation wraps `os.Root`:
- **Per-component traversal.** Code never passes a multi-component path to `os.Root`. To descend, it calls `Lstat(name)` on the current handle. If the result is a directory, it calls `OpenRoot(name + "/.")` and then `Stat(".")` on the new handle, which stats the opened directory itself, and re-`Lstat`s the name on the parent. A mismatch in device, inode, or type yields `changed_during_observation`. This closes the gap where `os.Root` follows symlinks that point inside the root.
  - The `/.` suffix makes the final component open with `O_DIRECTORY|O_NOFOLLOW`. A bare `OpenRoot(name)` opens without `O_DIRECTORY`, which blocks forever on a FIFO and would open a regular file swapped in under the name.
  - The re-`Lstat` catches a name swapped for a symlink to the very directory observed, which `Stat(".")` alone would accept.
- **Batched listing.** Directories are listed with raw `getdents64` on the directory descriptor, not `(*os.File).ReadDir(n)`. For files opened through `os.Root`, Go 1.27's `ReadDir` lstats every entry, which doubles the syscalls, hides `DT_UNKNOWN`, and silently drops entries that vanish between the two calls.
- **Mount boundaries.** A child is a boundary when its `st_dev` differs from its parent's, or when its path appears among the mount points in a `/proc/self/mountinfo` snapshot taken at the start of the scan. The snapshot check catches same-device bind mounts.
- **Special files.** These are identified from the directory-entry type, confirmed by `lstat` when the type is unknown, and are never opened.
- **Errors.** `ENOENT` maps to `absent`. `EACCES` and `EPERM` map to `unreadable`. `EIO`, `ESTALE`, `ENOTCONN`, `ENODEV`, and a missing root map to `unavailable`.

Test support:
- An instrumented wrapper counts every call by operation and path depth.
- A synthetic in-memory implementation generates trees lazily, so the 100,000-entry A1 fixture costs nothing until it is touched.

Rejected alternatives:
- `openat2` with `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_XDEV` as the boundary. §12.2 mandates Go's rooted APIs. This stays a candidate for later defense-in-depth.
- `O_NOATIME`. It fails with `EPERM` when the process does not own the file, and read-only mounts already prevent atime writes. The operator docs recommend `ro` mounts.

### D4. Lossless names and a reversible display escape (§4.1, A17)
Names are stored as raw `BLOB` bytes, and identity comparisons are byte comparisons.

The display form keeps printable UTF-8 as is. It escapes `\` as `\\`, renders invalid bytes as `\xNN`, and renders C0/C1 controls, DEL, and bidirectional override or isolate code points as `\u{XXXX}`. The mapping is injective, which the A17 scenarios require.

The API returns both `name` (display) and `name_b64` (raw). Ordering uses raw-byte order, which SQLite `BLOB` comparison provides.

### D5. Node identity and cursors
Nodes use `INTEGER PRIMARY KEY AUTOINCREMENT`, so IDs are never reused. The API exposes them as opaque decimal strings. A children cursor is base64url of `(raw name bytes, node id)`; this keeps keyset pagination stable even when rows are inserted concurrently.
- Rejected: UUIDs or ULIDs. They make indexes larger and add nothing for a single-node application.

### D6. M1 schema subset of §8.3
The first migration creates:

| Table | Contents |
|---|---|
| `schema_migrations` | Applied migration versions |
| `sources` | Config ID, label, root, mode, availability and reason, identity fields, `source_epoch`, read-only flag, `configured` |
| `nodes` | `source_id`, `parent_id`, `name BLOB`, kind, `inventory_mode`, `decision_reason`, `active`, `mount_boundary`, `link_text BLOB`, file metadata, `observation_revision`, `first_observed_at`, `last_observed_at`, `coverage_state`, `probe_scan_id` |
| `node_issues` | Special files and access errors, with raw names |
| `descriptors` | Append-only probe snapshots |
| `reconciliation_runs` | One listing run: epoch, generation, entries seen, `complete`, error |
| `jobs` | Scheduled work |
| `job_events` | Published job changes |
| `command_requests` | Idempotency records |
| `users` | The administrator credential |
| `sessions` | Session state |
| `audit_events` | Security audit trail |

Constraints:
- A partial unique index on `nodes(parent_id, name) WHERE active = 1`.
- One root node per source.
- Foreign keys everywhere.
- Indexes on `(source_id, active)`, `(parent_id, name)`, and `jobs(state, available_at)`.

Later milestones add their own columns, such as category, disposition, `intent_revision`, and `dirty_version`.
- Rejected: creating the full §8.3 schema now. Untested columns would drift from the milestones that define their semantics.

### D7. Descriptor storage and digest (§6)
A descriptor is a bounded JSON document stored in `descriptors` with typed columns: `node_id`, `node_revision`, `schema_version`, `observed_at`, `ruleset_version`, and `digest`.

Inside the JSON, entry names are `name_b64` plus `name`. Each observed entry has a stable `evidence_id` (`e1`, `e2`, …), assigned in examination order.

The digest is SHA-256 over a canonical semantic form. That form excludes the node ID, revision, and timestamps; sorts examined entries by raw bytes; and includes coverage. Identical evidence observed at a different time therefore produces the same digest. M3's classification cache depends on this.

### D8. Versioned marker and signal policy (§5.2, §6)
`policies/markers/v1.toml` is embedded in the binary and lists at most 16 marker names for targeted lookups. Examples: `.git`, `.hg`, `.svn`, `go.mod`, `package.json`, `Cargo.toml`, `pom.xml`, `CACHEDIR.TAG`, `DCIM`, `desktop.ini`, `unins000.exe`, `uninstall.exe`. It also lists name-pattern signal rules, such as `executable_present`, `uninstaller_name_present`, `vcs_marker_present`, `build_manifest_present`, `cachedir_tag_present`, and `camera_folder_present`.

A marker already seen among the examined entries is not looked up again. Signal rules match ASCII case-insensitively, because historical Windows names vary in case. This affects only matching; stored identity is unchanged.

Signals cite evidence IDs and never set a category. The policy version is recorded in each descriptor.

### D9. Discovery job flow (§5.1–§5.3)
A `scan` job runs in four steps:
1. **Identity check.** Compare the source's identity with the recorded one and capture the starting epoch. Then snapshot mountinfo.
2. **Root listing run.** Open a `reconciliation_runs` row. Read the root with `ReadDir(batch)`. For each batch, run one write transaction that upserts nodes and marks child directories `probe_state = pending` for this scan. Between batches, check cancellation, the node budget, and the epoch.
3. **Frontier probes.** Process pending directories in node-ID order, which is breadth-first. Each probe commits its descriptor and sets `inventory_mode = atomic` with `decision_reason = no_policy_retain_atomic`. The commit is a compare-and-swap on `source_epoch`: if the epoch changed, the job fails with `source_epoch_changed` and writes nothing more.
4. **Complete.** Close the run, marking it `complete` only if every batch succeeded.

The node budget counts nodes *created* by the scan. When the budget is reached, the job pauses with `node_budget_reached`.

On resume, the root is re-enumerated from the start rather than from a stored offset (§5.5.4 forbids durable OS listing offsets). Already-known entries are upserted, which is idempotent, and the resumed job receives a fresh node allowance.

No step ever marks an unseen node inactive.

### D10. SQLite access pattern (§8.1)
The application uses two `*sql.DB` handles on `modernc.org/sqlite`:
- A writer with `SetMaxOpenConns(1)`. Every write goes through it, which serializes writes.
- A read-only pool of four connections.

Pragmas are `journal_mode=WAL`, `foreign_keys=ON`, `busy_timeout=5000`, and `synchronous=FULL`. `FULL` is chosen now because M5's operation journal needs it, and batched writes keep its cost small. Write transactions stay bounded at one batch (≤256 rows).

Checkpointing combines WAL auto-checkpoint with a periodic passive checkpoint. `curator backup` uses `VACUUM INTO` a temporary file in the destination directory, runs `PRAGMA integrity_check`, and then renames the file into place with no-replace semantics (`link` + `unlink`, which fails if the destination exists).

At startup, statfs rejects NFS, SMB, CIFS, and SMB2 filesystem magic values for the state directory.

### D11. Jobs and events (§8.4)
Scheduling:
- A single in-process scheduler polls `jobs` and also wakes on a channel when a job is enqueued.
- Leases last 30 s and are renewed every 10 s. At startup, every job left `running` by a previous process is requeued with `attempts + 1`. The default maximum is 3 attempts.
- Each source device, keyed by the root's `st_dev`, has its own worker semaphore with default capacity 1.

Idempotency: `command_requests(key PRIMARY KEY, command, payload_digest, response_json, created_at)` stores each command's response. A repeated key returns that stored response if the payload digest matches, and HTTP 409 if it does not.

Events:
- `job_events` uses autoincrement IDs and keeps 10,000 rows or 24 h of history.
- `GET /api/events` is a server-sent event stream that honors `Last-Event-ID`. When a requested ID is older than the retained window, the stream emits a `reset` event.

Cancellation sets `cancel_requested`, which the worker checks between filesystem calls. A watchdog marks the source `unresponsive` when a single call exceeds 30 s.

### D12. Authentication details (§12.1)
- **Password hashing.** Argon2id via `golang.org/x/crypto/argon2`, using parameter set v1 `m=19456 KiB, t=2, p=1` (the OWASP baseline). Hashes are stored as PHC strings, `$argon2id$v=19$m=…,t=…,p=…$salt$hash`. A semaphore caps concurrent verifications at 2 to bound memory use.
- **CLI.** `admin set-password` reads the password through `golang.org/x/term` without echo and requires 15–1024 bytes.
- **Sessions.** Each token is 32 random bytes, and `sessions` stores only its SHA-256. Defaults are 1 h idle and 12 h absolute, both configurable.
- **Pre-login protection.** A pre-login row carries the login CSRF token and is replaced at login.
- **CSRF delivery.** The CSRF token is rendered in a `<meta>` tag and a hidden form field. JavaScript sends it as `X-CSRF-Token`. The `Origin` check, with `Referer` as fallback only when `Origin` is absent, compares against the configured external origin; `Origin: null` is rejected.
- **Referrer policy.** Responses send `Referrer-Policy: same-origin`. Under `no-referrer`, the Fetch standard serializes `Origin` as `null` for non-CORS POSTs, so every HTML form post (login, logout) would fail the strict origin check. `same-origin` still sends no referrer to any other origin.
  - Rejected: `no-referrer` plus accepting `Origin: null` when `Sec-Fetch-Site: same-origin`. That weakens the origin rule and fails closed on browsers without Fetch Metadata.
  - Rejected: JavaScript-only login and logout. Login would then require JavaScript.
- **Throttling.** Exponential backoff runs in memory, keyed by client address and by a global account key. A restart resets it.
  - Rejected: persisting throttle state. The added complexity is not justified for a single owner, and audit events already persist.

### D13. Web layer (§9)
The web layer uses the standard library `net/http` with Go 1.22+ method and path patterns, `html/template` for rendering, and `embed` for `web/static` and the templates.

Pages:
- `/login`
- `/`: the overview
- `/nodes/{id}`: the explorer listing combined with the inspector panel
- `/jobs/{id}`

The JavaScript is a single ES module with no build step. It handles pagination, sends commands with idempotency keys from `crypto.randomUUID()`, and subscribes to SSE.

The CSP is `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`.
- Rejected: an SPA framework, which §8.1 forbids.

### D14. Source identity (§4.1)
The identity key is (filesystem type, `f_fsid` or `st_dev` when `f_fsid` is zero, root inode). Mount ID, mount source, and major:minor are recorded as evidence but excluded from equality, because they change on every mount.

A mismatch moves the source to `needs_reconfirmation`. Erring in this direction is safe: a false prompt costs one CLI command, while a false match would merge two different disks' histories.

### D15. CLI surface
The CLI uses the standard library `flag` package with hand-dispatched subcommands: `serve`, `check-config`, `admin set-password`, `source confirm`, `backup`, and `version`.
- Rejected: cobra or urfave, an extra dependency that buys nothing at this command count.

### D16. Deployment artifacts (§12.3)
- **Container.** `deploy/Containerfile` builds in two stages: a pinned `golang:1.27.1` builder with `CGO_ENABLED=0`, then a distroless static `nonroot` runtime.
- **Compose.** `deploy/compose.yaml` publishes only `127.0.0.1:8080`, mounts sources `:ro`, keeps state on a named volume, and sets the non-loopback listen opt-in inside the container.
- **systemd.** `deploy/curator.service` sets a dedicated `User=`, `StateDirectory=curator` with `StateDirectoryMode=0700`, `ProtectSystem=strict`, `ReadOnlyPaths=` for the sources, `NoNewPrivileges=yes`, `PrivateTmp=yes`, and `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`.
- **Example config.** `deploy/examples/read-only-no-model.toml` is the only commented example in M1. The other §12.3 examples belong to M2a, M3, and M3a.

### D17. Test layout (§13.1)
- **Unit and integration tests** run under `go test -race ./...`.
- **A1** runs on the synthetic filesystem through the instrumented wrapper, with real-disk 1,000- and 100,000-entry variants behind the `e2e` build tag.
- **A11 symlink, FIFO, and swap cases** use real temporary directories. The swap case is driven by a hook in the instrumented wrapper between `Lstat` and `OpenRoot`.
- **A11 nested and bind mounts** run inside `unshare --user --mount --map-root-user` when the kernel permits it. Otherwise the test fails with a named skip reason under `-tags e2e`, never a silent pass. Mountinfo parsing is also unit-tested from fixtures.
- **A15** uses `httptest`.
- **A20** runs a complete scenario over a read-only bind or the synthetic filesystem, with zero recorded mutating calls.

## Risks / Trade-offs

- **The toolchain auto-download needs network access during the first build.** → The container builder is pinned, and the operator docs cover offline builds with a locally installed go1.27.1 and `GOTOOLCHAIN=local`.
- **`os.Root` follows symlinks that point inside the root.** → Per-component traversal with post-open identity checks (D3). This remains a race with external writers; the outcome is recorded as `changed_during_observation`, never followed silently.
- **Identity heuristics may raise false reconfirmation prompts** (for example on tmpfs or overlay, where `f_fsid` is zero). → This errs in the safe direction (D14). The prompt and the CLI fix are documented.
- **Hung network or USB mounts block a worker.** → Per-device worker isolation keeps other devices running. The watchdog flags the source `unresponsive`, and cancellation is honest about blocked calls.
- **Mount-namespace tests need kernel support.** → Mount logic is unit-tested with fakes and fixtures, and the real-mount tests report an explicit skip reason.
- **Argon2 verification is a memory-exhaustion vector.** → Throttling runs before verification, and a concurrency semaphore caps parallel verifications.
- **Directory order can differ between scans, so a first-N sample can change and with it the descriptor digest.** → Accepted for M1. The digest still reflects the actual evidence, and the M3 cache treats such a change as new evidence.

## Migration Plan

This is a greenfield deployment with no data migration. Rollback means stopping the service and restoring the database from a `curator backup` copy taken before the upgrade. An older binary refuses to start against a newer schema, so a rollback without restoring the backup fails loudly instead of corrupting data.

## Open Questions

- **Default session lifetimes** (1 h idle, 12 h absolute) are configurable. They can be tuned after use without changing any spec.
- **The exact marker list in `policies/markers/v1.toml`** can change freely, because the policy file is versioned and its version is recorded in each descriptor.
