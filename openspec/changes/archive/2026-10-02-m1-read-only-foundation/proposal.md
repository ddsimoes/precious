# Proposal

## Why

The repo holds only the v0.2 design (`directory-first-curator-spec-v0.2.md`). Every later milestone needs the same base: a single authenticated Go binary, durable SQLite state, operator-configured sources, a filesystem boundary that cannot escape or mutate, and a discovery engine that does no work proportional to hidden descendants (§3, §14). This change is milestone **M1, read-only foundation**. It delivers a useful inventory without AI and makes acceptance scenarios **A1, A5, A11, A15, A17, and A20** pass.

## What Changes

- New `curator` executable. Subcommands: `serve`, `check-config`, `admin set-password` (interactive, no echo), `source confirm <id>` (accept a changed filesystem identity), and `backup <dest>` (consistent online SQLite copy).
- TOML configuration covering sources, state directory, bind address, external origin, trusted proxies, and discovery budgets. Unsafe combinations are rejected at startup (§12.3): overlapping roots, state inside a source, write mode, non-loopback plain HTTP without explicit dev mode, and unknown keys.
- Single-administrator authentication (§12.1): Argon2id password hashes with versioned parameters, opaque server-side sessions rotated at login, CSRF tokens plus Origin validation on every state change, login throttling, request-size limits, a restrictive CSP, and forwarded headers trusted only from allowlisted proxies.
- Source registry (§4.1): roots come from config only. Each source records a stable ID, its observed filesystem identity, a source epoch, and availability. A different filesystem at an old path blocks scans until reconfirmed.
- Rooted, read-only filesystem access (§5.4, §12.2). Symlinks are never followed, special files never opened, nested and bind mounts never crossed, directories read in bounded batches, and filename bytes kept losslessly.
- Directory-first discovery (§5.1–§5.3). A durable, breadth-first frontier expands each source root by one level and shallow-probes every child directory (≤256 entries, ≤16 marker lookups, depth 0, 0 content bytes). Each directory is persisted as a versioned descriptor and kept **atomic**. M1 implements only the §5.3 fallback branch ("retain an atomic review item"); M2 adds the category policy that decides further expansion.
- Durable SQLite job runner (§8.4) with leases, crash recovery, idempotent command keys, cancellation, batched progress, and an SSE event stream with last-event-ID resume.
- Web UI and JSON read API (§9.1, §9.3, M1 subset): overview, paginated explorer, and node inspector showing coverage, errors, and truncation. Unknown sizes stay unknown. Every filesystem-derived string is escaped.
- Deployment assets (§12.3): multi-stage container image, Compose example, hardened systemd unit, commented read-only/no-model config example, and operator documentation.

Deferred, with owning milestone:
- Categories, deterministic category rules, overrides and protection pins, one-level refinement, aggregate walks, and the review queue → **M2**.
- Missing-entry tombstones, dirty scopes, scheduled reconciliation, and fsnotify → **M2a**. An M1 rescan never infers absence.
- Classifiers, provider profiles, privacy policy, and spend → **M3**.
- Inbox roles and intake → **M3a**.
- Hashing and duplicates → **M4**.
- Plans, quarantine, moves, and write mode → **M5**.
- Purge and recent-auth re-check → **M5b**.
- Archives and relationships → **M6**.

## Capabilities

### New Capabilities
- `server-config`: configuration loading, startup safety validation, bind/origin/proxy policy, state-directory permissions, and process-level deployment behavior.
- `admin-auth`: single-owner credential lifecycle, login, session lifecycle, CSRF/origin protection, throttling, and security headers.
- `state-store`: SQLite persistence guarantees covering location, migrations, integrity constraints, and consistent backup.
- `source-registry`: operator-configured sources, filesystem identity, source epochs, availability, and reconfirmation.
- `filesystem-boundary`: rooted, no-follow, no-mount-crossing, never-mutating access to source contents, with bounded reads and lossless names.
- `directory-discovery`: breadth-first frontier, shallow probes, descriptors, budgets, coverage/error honesty, and the atomic-boundary contract.
- `job-runner`: durable jobs, leases, recovery, idempotent commands, cancellation, progress, and resumable event delivery.
- `inventory-explorer`: overview, explorer, and inspector screens and their JSON read API, including safe rendering of filesystem-derived values.

### Modified Capabilities
None. No specs exist yet.

## Impact

- New Go module (`go 1.27`, `toolchain go1.27.1`) using the §8.2 layout. Only the M1 packages are created: `cmd/curator`, `internal/{domain,store,fsaccess,discovery,jobs,web}`, `web/static`, `migrations`, and `policies`.
- New dependencies: `modernc.org/sqlite`, `golang.org/x/crypto` (Argon2id), `golang.org/x/term`, `golang.org/x/sys/unix`, and `github.com/BurntSushi/toml`.
- New files: `deploy/` (Containerfile, compose.yaml, curator.service, example config), `docs/operator.md`, and `docs/adr/` for any spec deviations.
- No external services. No network egress at runtime.
