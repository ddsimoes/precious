# Tasks

## 1. Module scaffold

- [x] 1.1 Create `go.mod` (`module precious`, `go 1.27`, `toolchain go1.27.1`) and the M1 directories from design D6/§8.2 (`cmd/curator`, `internal/{domain,store,fsaccess,discovery,jobs,web}`, `web/static`, `migrations`, `policies`, `deploy`, `docs`). Verify that `go version` inside the module reports go1.27.1.
- [x] 1.2 Add pinned dependencies (`modernc.org/sqlite`, `golang.org/x/crypto`, `golang.org/x/term`, `golang.org/x/sys`, `github.com/BurntSushi/toml`). Verify that `go mod tidy && go mod verify` succeeds.
- [x] 1.3 Add `cmd/curator` subcommand dispatch with the `flag` package (`serve`, `check-config`, `admin set-password`, `source confirm`, `backup`, `version`). Verify that `go run ./cmd/curator version` prints the build version and that an unknown subcommand exits non-zero with usage.

## 2. Configuration and startup safety

- [x] 2.1 Implement TOML loading with unknown-key rejection, absolute-path checks, duplicate-ID checks, budget defaults (256 entries, 16 markers, depth 0, 0 content bytes, 50,000 nodes, page size 100, batch 256), and the write-mode rejection. Verify with table tests covering each rejection message.
- [x] 2.2 Implement the source-overlap check on resolved operator paths and the state-directory-inside-source check. Verify with tests for nested, identical, and symlinked-alias roots, and confirm that no database file is created on rejection.
- [x] 2.3 Implement listener and origin policy (loopback default, non-loopback opt-in, plain-HTTP-only-for-loopback-in-insecure-mode). Verify with table tests for each server-config scenario.
- [x] 2.4 Implement the trusted-proxy client-address and scheme derivation middleware. Verify with an `httptest` test for **A15 forged forwarded headers ignored** and the trusted-proxy-honored scenario.
- [x] 2.5 Implement state-directory creation (`0700`), the mode check that refuses group/other access, and `0600` state files. Verify with tests that inspect the resulting modes.
- [x] 2.6 Implement `check-config` (print the effective config with secrets redacted, non-zero exit on error). Write `deploy/examples/read-only-no-model.toml` with comments. Verify that `check-config` accepts the example.
- [x] 2.7 Write the `docs/operator.md` sections for configuration reference, listener/origin/proxy setup, and state-directory ownership (UID/GID, modes). Verify that every documented key exists in the config struct, using a test that decodes the documented example.

## 3. State store

- [x] 3.1 Implement the store open path: writer handle (max 1 conn), read-only pool (4 conns), and pragmas (WAL, foreign keys, busy timeout, `synchronous=FULL`). Refuse NFS/SMB/CIFS state directories via statfs. Verify with a test asserting the pragmas and with a unit test of the filesystem-magic rejection.
- [x] 3.2 Implement the numbered migration runner (one transaction per migration, recorded version, refusal on a newer schema). Write migration 0001 with the D6 tables, the partial unique index on active `(parent_id, name)`, and the listed indexes. Verify with tests for a fresh database, a newer-schema refusal that leaves the file unmodified, and rollback after an injected failing migration.
- [x] 3.3 Verify integrity constraints with tests: rejection of a duplicate active child with a byte-identical name, coexistence of `Readme` and `README`, and foreign-key enforcement.
- [x] 3.4 Implement `curator backup <dest>` (`VACUUM INTO` a temp file in the destination directory, `PRAGMA integrity_check`, no-replace `link`+`unlink`, mode `0600`). Verify with a test that backs up during concurrent writes and a test that an existing destination is refused untouched. Document backup and restore in `docs/operator.md`.

## 4. Filesystem boundary

- [x] 4.1 Define the read-only `fsaccess` interface (open source root, batched directory listing, child lstat, readlink, statfs/mount facts) with no mutating methods. Define the error-outcome mapping (`absent`, `unreadable`, `unavailable`, `changed_during_observation`). Verify with unit tests of the errno mapping.
- [x] 4.2 Implement the `os.Root` backend with per-component traversal and post-open identity verification (D3). Verify with real-tempdir tests: **A11 symlink escape** (`escape -> /etc`), an internal symlink that is not followed, and **A11 traversal name** rejection.
- [x] 4.3 Implement the instrumented wrapper (per-operation counters, call log, pre-open hook) and the lazy synthetic tree filesystem. Verify with a test that the counters match a scripted call sequence.
- [x] 4.4 Implement special-file detection without opening. Verify with **A11 FIFO in a source**: a real `mkfifo` entry, discovery completes, an issue is recorded, and the call log shows no open of it.
- [x] 4.5 Implement swap detection. Verify with **A11 directory swapped for symlink**: the pre-open hook replaces a directory with `-> /`, the result is `changed_during_observation`, and nothing is read through the link.
- [x] 4.6 Implement mount-boundary detection (`st_dev` change plus a `/proc/self/mountinfo` snapshot parser). Verify with parser fixture tests for bind mounts and octal-escaped mount paths. Add the **A11 nested mount** and bind-mount tests under `-tags e2e` using `unshare --user --mount --map-root-user`, with an explicit skip reason when unsupported.
- [x] 4.7 Implement batched listing (`ReadDir(batch)`, each batch processed before the next is requested). Verify with a test that the instrumented call log shows bounded batch sizes for a 200,000-entry synthetic directory.
- [x] 4.8 Verify **A17 non-UTF-8 name** and **A17 normalization-distinct names** on a real tempdir: the bytes `66 E9 2E 74 78 74` and NFC/NFD `é` round-trip as distinct, exact raw names.
- [x] 4.9 Verify **A20 zero mutating calls**: a full discovery over the instrumented filesystem records no mutating operation and 0 content bytes.

## 5. Source registry

- [x] 5.1 Implement config-to-`sources` sync at startup (insert new, update label and mode, mark missing ones `unconfigured`). Verify with a test that a removed source becomes `unconfigured`, keeps its nodes, and has its scans refused with `source_unconfigured`.
- [x] 5.2 Implement identity capture (filesystem type, `f_fsid`/`st_dev`, root inode; mount ID and source as evidence) and epoch 1 on first observation. Verify with a test against a tempdir source.
- [x] 5.3 Implement identity comparison, the `needs_reconfirmation` state, and `curator source confirm <id>` (epoch +1, audit event). Verify with tests using an injected identity mismatch: scans are refused with `source_identity_changed`, and confirmation increments the epoch exactly once.
- [x] 5.4 Implement availability detection and reasons (missing root, I/O error, permission error, unresponsive). Verify with **A5 source disappears** (rename the tempdir root away: unavailable, nodes kept as history) and the source-returns scenario (same identity, epoch unchanged).
- [x] 5.5 Report read-only mount state from statfs `ST_RDONLY`. Verify with a test using the statfs fake, and with **A20 read-only mount reported** under `-tags e2e` using a read-only bind in a user namespace.
- [x] 5.6 Document sources, identity reconfirmation, and `source confirm` in `docs/operator.md`.

## 6. Job runner and command contract

- [x] 6.1 Implement the `jobs` persistence and state machine (`queued`, `running`, `paused`, `succeeded`, `failed`, `cancelled`, plus `cancel_requested`), with payload version and terminal reason. Verify with state-transition tests that reject illegal transitions.
- [x] 6.2 Implement the scheduler, 30 s leases with 10 s renewal, startup requeue of orphaned `running` jobs, and a maximum of 3 attempts. Verify with a crash-simulation test (abandon the worker, restart the scheduler, job requeued with attempts +1) and an attempts-exhausted test.
- [x] 6.3 Implement per-device worker semaphores (default 1). Verify with a test showing that two same-device scans never overlap and that UI read queries are served during a running scan.
- [x] 6.4 Implement `command_requests` idempotency and the JSON error envelope with the stable codes from the job-runner spec. Verify with tests: a same key and payload return the original job, and a same key with a different payload returns 409 `idempotency_key_reused`.
- [x] 6.5 Implement single-active-scan coalescing (resume a paused scan, or return the active job ID). Verify with a test that a concurrent `start-scan` creates no second job.
- [x] 6.6 Implement cancellation (checked between filesystem calls) and the 30 s watchdog that sets `cancel_requested` and flags the source unresponsive. Verify with a cancel-mid-scan test, and with a blocked-call test that uses a blocking hook in the instrumented filesystem.
- [x] 6.7 Implement `job_events` with retention (10,000 rows or 24 h) and the SSE endpoint with `Last-Event-ID` resume and a `reset` event. Verify with **A16 browser disconnects** (disconnect the client mid-scan, reconnect, receive every later event, scan unaffected) and the expired-history reset test.

## 7. Directory discovery

- [x] 7.1 Write `policies/markers/v1.toml` (≤16 markers, signal rules) and the embedded loader that validates the marker count and rule syntax. Verify with a loader test that rejects 17 markers.
- [x] 7.2 Implement the shallow probe: examine ≤256 entries, perform ≤16 marker lookups (skipping markers already examined), depth 0, 0 content bytes; build the descriptor (D7) with evidence IDs, marker outcomes, signals citing evidence, coverage, and stop reason. Verify with the truncated-listing (1,000 entries → `entry_budget`) and marker-outcome (`present`, `absent`, `unreadable`) tests.
- [x] 7.3 Implement the canonical semantic form and SHA-256 digest. Verify with a test that two probes of identical evidence at different times and node revisions produce the same digest, and that a coverage change alters it.
- [x] 7.4 Implement the scan job (identity check, epoch capture, mountinfo snapshot, root listing run in batched transactions, durable frontier, breadth-first probes, epoch compare-and-swap commit, `no_policy_retain_atomic`). Verify with the scan-resumes-after-crash test (40 of 100 probed, restart, the remaining 60 probed once each) and the startup-does-not-scan test.
- [x] 7.5 Verify **A1 large application unit stays one record**: on the synthetic filesystem, a 100,000-entry application unit yields one active node, zero active descendants, ≤256 entries examined, ≤16 marker lookups, and no access below its immediate entries.
- [x] 7.6 Verify **A1 cost independent of hidden size**: the 1,000- and 100,000-descendant synthetic fixtures with identical immediate entries produce identical filesystem call counts. Add the real-disk variant under `-tags e2e`.
- [x] 7.7 Implement the node budget pause (`node_budget_reached`, frontier kept, resume re-enumerates the root with a fresh allowance). Verify with a 60,000-entry synthetic root: the scan pauses, then a second `start-scan` completes and the total node count equals 60,000 with no skips.
- [x] 7.8 Verify **A5 partial listing**: an injected I/O error after 300 entries keeps those 300, marks the run `partial` with the error, and the job ends with the error named.
- [x] 7.9 Verify **A5 unreadable child**: a `0000` directory records coverage `error`/`unreadable`, and its API representation is not empty.
- [x] 7.10 Verify **A5 source vanishes mid-scan**: removing the root during frontier processing stops the job with `source_unavailable`, the source is marked unavailable, and earlier nodes are unchanged.
- [x] 7.11 Verify **A5 entry missing on rescan**: a second scan after deleting an entry leaves its node active, with its original `last_observed_at`.
- [x] 7.12 Verify the source-epoch scenario: bumping the epoch mid-scan via the hook makes the job fail `source_epoch_changed`, and no later descriptors are written.
- [x] 7.13 Verify **A17 non-UTF-8 and case-distinct names discovered** end to end through the store: `Report.txt`, `report.txt`, and an invalid-UTF-8 name become three nodes with byte-exact names.
- [x] 7.14 Document discovery behavior, budgets, coverage states, and the "atomic until M2 policy" limitation in `docs/operator.md`.

## 8. Authentication

- [x] 8.1 Implement Argon2id hashing (PHC string, parameter set v1 `m=19456,t=2,p=1`, 16-byte salt, verification semaphore of 2) and rehash-on-login for outdated parameters. Verify with tests for round-trip, wrong password, and parameter upgrade.
- [x] 8.2 Implement `curator admin set-password` (terminal-only, entered twice, 15–1024 bytes, revokes all sessions, writes an audit event). Verify with tests using a fake terminal: non-terminal input is refused, and an existing session is invalidated.
- [x] 8.3 Implement sessions (32-byte token, SHA-256 stored, rotation on login, 1 h idle and 12 h absolute defaults, logout revoke) and the pre-login CSRF session. Verify with tests for rotation, idle expiry under a fake clock, and logout replay.
- [x] 8.4 Implement cookie attributes (`__Host-` prefix and `Secure` for HTTPS origins, `HttpOnly`, `SameSite=Strict`, `Path=/`). Verify with an `httptest` assertion on `Set-Cookie`.
- [x] 8.5 Implement auth middleware (API 401, page redirect, login-page allowlist). Verify with **A15 unauthenticated API request rejected** and the redirect scenario.
- [x] 8.6 Implement CSRF token plus `Origin`/`Referer` validation on all state-changing requests, including login and logout. Verify with **A15 cross-site command rejected** and **A15 missing CSRF token rejected**, each also asserting that no job row exists.
- [x] 8.7 Implement login throttling (per address plus global, exponential backoff, generic error, checked before Argon2 runs) and audit events. Verify with a ten-failure test that measures that verification is skipped while throttled, and with an audit row that contains no password.
- [x] 8.8 Implement body-size limits (413) and security headers (the CSP from D13, nosniff, `Referrer-Policy: same-origin`, no CORS). Verify with tests on a page response, an API response, and a 413 response.
- [x] 8.9 Document administrator setup, password reset, session settings, and the reverse-proxy requirements in `docs/operator.md`.

## 9. Explorer UI and read API

- [x] 9.1 Implement the display-name escape (D4) and `name_b64`. Verify with property tests that the escape is injective, plus **A17 distinct names display distinctly** (`0xE9` vs literal `\xE9`) and bidi-override escaping.
- [x] 9.2 Implement the read API (`/api/sources`, `/api/nodes/{id}`, `/children` with the keyset cursor, `/evidence`, `/api/jobs/{id}`) with 404 `not_found`. Verify with HTTP contract tests including **A17 lossless name in API** and 250-child cursor pagination (100/100/50, no duplicates or omissions).
- [x] 9.3 Implement the overview template (per-source availability, identity, read-only state, last scan, active jobs, coverage counts, known bytes labeled partial beside a count of unknown-size units). Verify with a rendered-HTML test of the unknown-sizes scenario.
- [x] 9.4 Implement the explorer and inspector templates (row fields, atomic row with no expand control, symlink, mount, and issue markers, escaped source-relative path, truncation wording "first N entries in directory order, not a sample", the "descendants not cataloged" note). Verify with rendered-HTML tests for the atomic-row and truncated-listing scenarios.
- [x] 9.5 Implement the embedded ES module (pagination, commands with `crypto.randomUUID()` idempotency keys and `X-CSRF-Token`, SSE progress with reset handling) with no inline script. Verify with a test asserting that every served HTML response has no inline `<script>` body and no external-origin URL.
- [x] 9.6 Verify **A15 malicious filename**: a directory named `<img src=x onerror=alert(1)>` renders as escaped text in the explorer, the inspector, and JSON.

## 10. Deployment artifacts

- [x] 10.1 Write `deploy/Containerfile` (pinned `golang:1.27.1` builder, `CGO_ENABLED=0`, distroless static `nonroot` runtime). Verify that `podman build` (or `docker build`) succeeds and the image runs `curator version`.
- [x] 10.2 Write `deploy/compose.yaml` (publishing only `127.0.0.1:8080`, sources mounted `:ro`, a named state volume, the explicit non-loopback opt-in). Verify that `docker compose config` validates and that the published port is bound to loopback.
- [x] 10.3 Write `deploy/curator.service` (dedicated user, `StateDirectory` with mode `0700`, `ProtectSystem=strict`, `ReadOnlyPaths` for sources, `NoNewPrivileges`, `PrivateTmp`, restricted address families). Verify with `systemd-analyze verify deploy/curator.service`.
- [x] 10.4 Document the container, Compose, and systemd installation; upgrades (back up first, older binaries refuse newer schemas); offline builds (`GOTOOLCHAIN=local`); and recommended `ro` source mounts in `docs/operator.md`.

## 11. Integration

- [x] 11.1 Run `go vet ./...` and `go test -race ./...`. Verify that both pass with zero failures.
- [x] 11.2 Run `go test -race -tags e2e ./...` on a Linux host. Verify that every A1/A11/A20 e2e test either passes or reports its named kernel-capability skip reason.
- [x] 11.3 Verify **A20 read-only and cloud disabled** end to end: build the binary, configure one read-only source, set the admin password, then log in, start a scan, follow SSE progress, page through the explorer, and open an inspector with an HTTP client script against the running server. Assert that no request leaves loopback and that the source saw zero write attempts.
- [x] 11.4 Smoke-check the built binary: `curator check-config` on the shipped example, `curator serve` on loopback, the login page returns 200 with the CSP header, and `curator backup` produces a database that passes the integrity check.
