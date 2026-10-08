# Precious operator guide

Precious helps you make sense of a disk that has been collecting files for years. It indexes every file and folder on the disks you add, shows where the space goes, lets you find any file, and lets you record what to keep and what to discard, all from a web browser. It only reads your disks, except where you allow changes on one: there it moves, renames, and creates folders when you ask, and it never deletes, overwrites, or writes into a file (see [Changing disks](#changing-disks)). This guide covers building, installing, configuring, and running it. The product specification is [`precious-spec-v0.3.md`](../precious-spec-v0.3.md).

This release, R2, adds duplicates to the full index and explorer of R1. Besides sources added from the browser, complete scans and rescans with every folder's size, classification rules, Home, Map, Search, the detail panel, the file viewer, and your decisions and tags, Precious now reads file content in the background to find copies: duplicate files, folders and archives that hold the same files, a side-by-side Compare, and opportunity cards with review lists, among them your own files found inside programs. It browses and views inside zip and tar archives without unpacking them. Duplicates are information only: you decide each copy yourself, and nothing in R2 changes a file on a disk. Organizing (moving, renaming, and creating folders on the sources where you allow it) comes with R3 (see [Changing disks](#changing-disks)); cleanup (quarantine and deletion) comes in a later release. Why the product was reset from the earlier `curator` design is recorded in [ADR 0008](adr/0008-product-reset.md).

## Installation

Precious is one static binary with no runtime dependencies; the web interface is built into it. Linux on amd64 and arm64 is the delivered and tested platform; the binary also builds for macOS and Windows (see [Platform and supported systems](#platform-and-supported-systems)). Run it under systemd, as a container image, or with Compose. Every variant needs a configuration file (start from the commented [`deploy/examples/precious.toml`](../deploy/examples/precious.toml); see [Configuration reference](#configuration-reference)) and a private state directory on local storage (see [State directory](#state-directory)).

### Building

Building needs Go and, for the web interface only, Node.js (the current LTS release) with npm. From the repository root:

```sh
make ui       # npm ci and npm run build in web/ui; writes the interface to web/dist
make build    # make ui, then go build -o bin/precious ./cmd/precious
make test     # go test -race ./... and the web/ui tests
make e2e      # the browser suite in web/ui/e2e (Playwright, Chromium)
make cross    # bin/precious-<os>-<arch> for linux, darwin, and windows on amd64 and arm64
```

`go build` embeds whatever `web/dist` holds. Without `make ui`, `go build` and `go test` still work and need no Node.js, but the server then shows a short page saying the interface was not built, instead of the interface.

`make e2e` builds the interface and the binary, writes the regression corpus with `tools/gencorpus` into a temporary folder, sets the administrator password by running `precious admin set-password` under a pseudo-terminal (util-linux `script`), starts `precious serve` on a free loopback port, and runs the Playwright tests in `web/ui/e2e` against it in one browser session: sign-in, adding sources through the picker, a scan with live progress, Home totals against the corpus's ground truth, Map sizes, search, decisions, bulk discard, tags, and the viewer, failing on any Content Security Policy violation, console error, or script run from a corpus file. It needs Linux (for `script`) and Playwright's Chromium, installed once with `cd web/ui && npx playwright install chromium`. The server's log is kept as `web/ui/test-results/serve.log`. Playwright's Chromium has no H.264 decoder, so there the suite checks that the corpus's MP4 is served as `video/mp4` and that the player fails only on the codec.

`go.mod` pins the toolchain to go1.27.1. With Go's default `GOTOOLCHAIN=auto`, any Go 1.21 or newer downloads go1.27.1 on first use, which needs network access. To stamp a version and strip the binary, build after `make ui` with:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=v0.1.0" -o bin/precious ./cmd/precious
bin/precious version    # precious v0.1.0 (<commit>) go1.27.1
```

#### Offline builds

1. Install go1.27.1 from the official archive (<https://go.dev/dl/>, check its SHA-256) and put its `bin` directory first on `PATH`.
2. On a machine with network access, run `go mod download` in the checkout, then copy the module cache (`go env GOMODCACHE`) to the same path on the offline machine.
3. The interface's npm packages need network access too. Run `make ui` on the machine with network access and copy the resulting `web/dist` directory into the offline checkout; `go build` embeds it as plain files.
4. Build with every download disabled:

```sh
GOTOOLCHAIN=local GOPROXY=off CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w -X main.version=v0.1.0" -o bin/precious ./cmd/precious
```

`GOTOOLCHAIN=local` uses the installed Go and never fetches another; an older one stops with `go: go.mod requires go >= 1.27 (running go 1.22.2; GOTOOLCHAIN=local)`. `GOPROXY=off` turns a missing module into an error instead of a network request.

### systemd

[`deploy/precious.service`](../deploy/precious.service) runs the binary as a dedicated unprivileged account inside a read-only view of the system:

| Setting | Effect |
|---|---|
| `User=precious`, `Group=precious` | dedicated system account, no login shell |
| `StateDirectory=precious`, `StateDirectoryMode=0700`, `UMask=0077` | `/var/lib/precious` owned by `precious` with mode `0700`; every file the service creates is owner-only |
| `ProtectSystem=strict`, `ProtectHome=read-only` | the whole filesystem, `/home` included, is read-only to the service; only the state directory is writable |
| `ReadOnlyPaths=` | the disks to index, listed explicitly in a drop-in (below); a disk you allow Precious to change goes in `ReadWritePaths=` instead (see [Allowing changes in the deployment](#allowing-changes-in-the-deployment)) |
| `NoNewPrivileges=yes`, `CapabilityBoundingSet=` | no capabilities and no way to gain privileges |
| `PrivateTmp=yes`, `PrivateDevices=yes` | private `/tmp`; no device nodes |
| `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6` | local and IP sockets only |

The remaining `Protect*`, `Restrict*`, `LockPersonality`, and `MemoryDenyWriteExecute` settings close kernel interfaces a Go server never uses.

```sh
sudo useradd --system --user-group --home-dir /var/lib/precious --no-create-home \
  --shell /usr/sbin/nologin precious
sudo install -m 0755 bin/precious /usr/local/bin/precious
sudo install -d -m 0755 /etc/precious
sudo install -m 0640 -o root -g precious deploy/examples/precious.toml /etc/precious/precious.toml
sudoedit /etc/precious/precious.toml    # external_origin, sources.allowed_roots
sudo install -d -m 0700 -o precious -g precious /var/lib/precious
sudo install -m 0644 deploy/precious.service /etc/systemd/system/precious.service
sudo systemctl edit precious            # add the drop-in shown below
sudo -u precious /usr/local/bin/precious check-config --config /etc/precious/precious.toml
sudo -u precious /usr/local/bin/precious admin set-password --config /etc/precious/precious.toml
sudo systemctl enable --now precious
```

`state_dir` must be `/var/lib/precious`, the directory `StateDirectory=precious` manages. Sources are not configured in the file: the owner adds them in the interface, from folders below `sources.allowed_roots`. Make those disks visible to the service in a drop-in, so that upgrading the unit file does not lose them:

```ini
[Unit]
# Start only once the disks are mounted.
RequiresMountsFor=/srv/old-disk /media/backup-2003

[Service]
ReadOnlyPaths=/srv/old-disk /media/backup-2003
# Read access through a group instead of changing the disks' permissions.
SupplementaryGroups=archive-readers
```

The `precious` account needs read permission on files and read and execute permission on directories on every disk it indexes; grant it through group membership (`SupplementaryGroups=`) rather than by changing the disks. A listed `ReadOnlyPaths=` path that does not exist stops the service from starting; for a removable disk that may be absent, prefix the path with `-` and leave it out of `RequiresMountsFor=`.

### Container image

[`deploy/Containerfile`](../deploy/Containerfile) builds in two stages: the pinned `golang:1.27.1` builder compiles with `CGO_ENABLED=0`, `-trimpath`, and a stripped, version-stamped binary; the runtime is `gcr.io/distroless/static-debian12:nonroot`, which has no shell or package manager and runs as UID/GID 65532. The image holds only `/precious` and an empty `/var/lib/precious` (owner 65532, mode `0700`). Configuration, state, and disks are supplied at run time, so nothing secret is in the image.

The builder compiles whatever `web/dist` holds in the build context, so run `make ui` first:

```sh
make ui
docker build -f deploy/Containerfile --build-arg VERSION=v0.1.0 -t precious:v0.1.0 .
docker run --rm precious:v0.1.0 version
```

With Podman, add `--ignorefile deploy/Containerfile.dockerignore` (Docker picks that file up automatically). The entrypoint is `/precious` and the default command is `serve --config /etc/precious/precious.toml`; pass another subcommand to run it instead. The builder runs on the build host's platform and cross-compiles, so `docker buildx build --platform linux/arm64 ...` needs no emulation.

### Compose

[`deploy/compose.yaml`](../deploy/compose.yaml) runs the image with:

- the port published on host loopback only (`127.0.0.1:8080:8080`);
- [`deploy/compose.precious.toml`](../deploy/compose.precious.toml) mounted read-only as `/etc/precious/precious.toml`;
- the state on the named volume `precious_state` at `/var/lib/precious`;
- each disk bind-mounted read-only below `/sources` (`read_only: true`, the long form of `:ro`), with `create_host_path: false` so a missing disk fails the start instead of appearing as an empty directory; `compose.precious.toml` sets `sources.allowed_roots = ["/sources"]`;
- a read-only root filesystem, a `tmpfs` `/tmp`, all capabilities dropped, and `no-new-privileges`.

Inside the container Precious must listen on `0.0.0.0:8080`, because a published port cannot reach the container's own `127.0.0.1`. That is a non-loopback listener, so `compose.precious.toml` sets `allow_non_loopback_listen = true`; it is safe only because Compose publishes the port on host loopback. Do not change the publish to `8080:8080`.

```sh
make ui
$EDITOR deploy/compose.precious.toml    # external_origin
$EDITOR deploy/compose.yaml             # one read-only bind per disk, below /sources
docker compose -f deploy/compose.yaml up -d --build
docker compose -f deploy/compose.yaml exec precious \
  /precious admin set-password --config /etc/precious/precious.toml
```

Then open <http://127.0.0.1:8080>. `PRECIOUS_VERSION=v0.1.0` in the environment stamps and tags the image. The configuration file must be readable by UID 65532 (mode `0644` is fine; it holds no secrets). For remote access, put an HTTPS reverse proxy on the host in front of `127.0.0.1:8080`, set `external_origin` to the proxy's origin, delete `allow_insecure_http`, and add the address the proxy's connections arrive from inside the container (the gateway of the `precious_default` network, from `docker network inspect precious_default`) to `trusted_proxies`.

### Read-only disk mounts (recommended)

Precious writes to a disk only when you allow changes on that source and organize it (see [Changing disks](#changing-disks)); scanning, hashing, and viewing never write. Present every disk read-only at the operating-system level as well, unless you allow changes on it (see [Allowing changes in the deployment](#allowing-changes-in-the-deployment)):

- Mount the filesystems read-only on the host, for example `mount -o ro,nosuid,nodev,noexec /dev/sdb1 /srv/old-disk` or `ro,nosuid,nodev,noexec` in `/etc/fstab`. A read-only mount also prevents the access-time updates that reading directories and files would otherwise write.
- For an original disk that must not change at all, set the block device read-only first (`blockdev --setro /dev/sdb`): an `ro` mount of ext3/ext4 with a dirty journal still replays the journal unless you add `noload`.
- Under systemd, list each disk in `ReadOnlyPaths=`; under Compose, bind each disk read-only. Only a disk whose source you allow Precious to change needs `ReadWritePaths=` or a writable bind.
- systemd's read-only settings do not extend to mounts created after the service started, and a container bind mount does not see filesystems mounted beneath it later. Restart Precious after attaching a disk it should see.

## First run

```sh
precious check-config --config /etc/precious/precious.toml        # validate; prints the effective settings
precious admin set-password --config /etc/precious/precious.toml  # interactive; required before anyone can sign in
precious serve --config /etc/precious/precious.toml
```

Then open the configured `server.external_origin` in a browser and sign in.

- **`check-config`** validates the file without starting the server. On success it prints the effective configuration (your values over the defaults) as TOML and exits 0; on failure it prints every problem on standard error and exits 1. It does not create the state directory.
- **`admin set-password`** sets the password of the single administrator account. There is no default password and no web page that creates one. Run it on the server, as the user that owns the state directory, from an interactive terminal: the password is read twice without echo and must be 15 to 1024 bytes long, and piped or redirected input is refused. The same command resets a forgotten password, and the reset signs every browser out. It may run while the server is running.
- **`serve`** runs the web server. It listens on `127.0.0.1:8080` by default, so only a browser on the same machine can reach it.

### Plain HTTP on a local network

To reach Precious from other machines on the local network without HTTPS, listen on every interface and name the address browsers use:

```toml
[server]
listen = "0.0.0.0:8080"
allow_non_loopback_listen = true
external_origin = "http://192.168.1.10:8080"
allow_insecure_http = true
```

Both opt-ins are required: `allow_non_loopback_listen` for a listener other than loopback, and `allow_insecure_http` for an `http://` origin. `external_origin` must be exactly what the browser's address bar shows (scheme, host, and any non-default port); state-changing requests whose `Origin` differs are refused, so sign-in fails from any other address. On plain HTTP the password and session cookie cross the network unencrypted; use it only on a network you trust.

### Behind an HTTPS reverse proxy

For access from outside the local network, keep the default loopback listener, terminate TLS in a reverse proxy on the same host, and set:

```toml
[server]
listen = "127.0.0.1:8080"
external_origin = "https://precious.example.net"
trusted_proxies = ["127.0.0.1", "::1"]
```

An nginx server block for it:

```nginx
server {
    listen 443 ssl;
    server_name precious.example.net;
    ssl_certificate     /etc/ssl/precious/fullchain.pem;
    ssl_certificate_key /etc/ssl/precious/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        # Replace, rather than append to, anything the client sent.
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        # Job progress uses server-sent events; do not buffer them.
        proxy_buffering off;
        proxy_read_timeout 1h;
    }
}
```

The proxy must pass the browser's `Origin`, `Referer`, `Cookie`, and `Set-Cookie` headers through unchanged (nginx and Caddy do by default) and must not add permissive CORS headers. Forwarded headers (`Forwarded`, `X-Forwarded-For`, `X-Forwarded-Proto`) are honored only from peers listed in `server.trusted_proxies`; list only the proxies you operate.

### State directory

`state_dir` holds Precious's private state: the SQLite database `precious.db` and its `precious.db-wal` and `precious.db-shm` files. It must be an absolute path on local storage; on Linux, Precious refuses a state directory on NFS, SMB, or CIFS. At startup it creates a missing state directory with mode `0700` (its parent must already exist), and on Linux and macOS it refuses to start when the existing directory is accessible to group or other users; fix that with `chmod 0700 <state_dir>`. The database files are created owner-only.

Precious never changes file ownership. Run it, and every administration command (`admin set-password`, `backup`), as the dedicated service account that owns the state directory, for example `sudo -u precious precious backup ...`; running them as root would create root-owned files the service cannot open. In the container image that account is UID/GID `65532:65532`.

## Administrator account and security

Precious has one account, the administrator. There is no default password and no web page that creates one: `precious admin set-password` sets and resets it (see [First run](#first-run)). The database stores only an Argon2id hash, upgraded at the next sign-in when a later release raises its work parameters.

### Sessions

Signing in creates a new server-side session and discards the one that showed the sign-in page. The browser holds only a random token; the database stores only its SHA-256 digest, so sessions survive a server restart.

| Key | Default | Meaning |
|---|---|---|
| `auth.session_idle` | `1h` | A session unused for this long ends. |
| `auth.session_absolute` | `12h` | A session ends this long after sign-in, however active it is. |
| `auth.login_max_backoff` | `15m` | The longest wait imposed by sign-in throttling. |

Signing out revokes the session at once. Resetting the password revokes every session.

The session cookie is always `HttpOnly`, `SameSite=Strict`, and `Path=/`. With an `https://` external origin it is named `__Host-precious_session` and is `Secure`; with an `http://` origin (`allow_insecure_http = true`) it is named `precious_session`, without `Secure`, because browsers drop `Secure` cookies on plain HTTP.

### Sign-in throttling

After a failed sign-in, the next attempt is refused for 1 s, and each further consecutive failure doubles the wait, up to `auth.login_max_backoff`. Failures count per client address and for the account as a whole, so attempts spread over many addresses are slowed too. A refused attempt shows the same "Login failed." message as a wrong password. A successful sign-in clears the counters, and so does a restart. Because the account-wide limit applies to everyone, a sustained guessing attack can delay your own sign-in by up to `auth.login_max_backoff`; if Precious is reachable from the internet, put a VPN or an authenticating reverse proxy in front of it.

### Browser protection

- Every state-changing request, sign-in and sign-out included, must carry the session's CSRF token in the `X-CSRF-Token` header and an `Origin` header equal to `server.external_origin` (or, without `Origin`, a `Referer` on that origin). Anything else gets HTTP 403.
- Without a session, every `/api` request gets HTTP 401, except reading the session state and signing in. Other pages load the interface, which shows the sign-in page.
- Request bodies larger than `server.max_request_bytes` (default 1 MiB) get HTTP 413.
- Every response carries `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; media-src 'self'; frame-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`, `X-Content-Type-Options: nosniff`, and `Referrer-Policy: same-origin`. File contents shown by the viewer get stricter policies of their own (see [Viewer safety](#viewer-safety)).
- Precious sends no CORS headers; no other site can call its API.

### Audit trail

Security events and every change you make are written to the `audit_events` table with the time and the client address, never a password, token, or file content: `password_set`, `sessions_revoked`, `login_succeeded`, `login_failed`, `login_throttled`, `logout`, `source_added`, `source_renamed`, `source_removed`, `source_schedule_set`, `source_writes_set`, `decision_set`, `tags_set`, `tag_created`, `tag_renamed`, `tag_deleted`, `category_set`, `group_set`, `action_run`, `action_cancelled`, and `recovery_resolved` (see [Organizing](#organizing)). To read the latest ones, run this as the user that owns the state directory:

```sh
sqlite3 <state_dir>/precious.db \
  "SELECT datetime(occurred_at / 1000, 'unixepoch'), kind, client_addr, detail FROM audit_events ORDER BY id DESC LIMIT 20"
```

## Backup and restore

Precious's own state is the database `<state_dir>/precious.db`, accompanied by `precious.db-wal` and `precious.db-shm` while it is open. The indexed disks are never part of this backup, and Precious is not a backup system for them.

### Taking a backup

`precious backup --config <file> <destination>` writes a consistent copy while the server keeps running and writing:

1. It copies the database as of a single point in time with SQLite's `VACUUM INTO`, from a separate read-only connection, into a new owner-only (`0600`) temporary file in the destination's directory.
2. It runs `PRAGMA integrity_check` on the copy and flushes it to disk.
3. It publishes the copy under the destination name with `link` + `unlink`, which never replaces a file: if the destination already exists, the command fails and leaves that file untouched.
4. It prints the destination path and exits 0. Any failure exits non-zero with a message and removes the temporary file.

The command reads `state_dir` from the configuration and fails if `precious.db` is not there; it never creates the state directory or an empty database, and it applies no migrations. The destination directory must already exist and be on a filesystem that supports hard links (ext4, XFS, Btrfs, ZFS); for FAT or exFAT media, back up to a local disk and copy the file afterwards. Keep backups off the indexed disks and, ideally, off the disk that holds the state directory.

Always use `precious backup`. Copying `precious.db` with `cp` or `rsync` while Precious runs, or after a crash, misses transactions still held in `precious.db-wal`, so the copy is neither complete nor guaranteed consistent.

Under systemd, run the command as the service account, which owns the database files:

```sh
sudo install -d -m 0700 -o precious -g precious /var/backups/precious
sudo -u precious /usr/local/bin/precious backup --config /etc/precious/precious.toml \
  /var/backups/precious/precious-$(date +%Y%m%d-%H%M%S).db
```

With Compose, write the copy into the state volume and copy it out:

```sh
name=precious-$(date +%Y%m%d-%H%M%S).db
docker compose -f deploy/compose.yaml exec precious \
  /precious backup --config /etc/precious/precious.toml /var/lib/precious/$name
docker compose -f deploy/compose.yaml cp precious:/var/lib/precious/$name ./
docker run --rm -v precious_state:/v alpine rm /v/$name    # the image has no shell
```

### Restoring

A restore replaces the whole database: everything Precious recorded after the backup is lost.

1. Stop Precious.
2. Move the current `precious.db` aside together with its `precious.db-wal` and `precious.db-shm`, if present. Never leave an old `-wal` or `-shm` file beside a restored database: SQLite would apply the old log to the restored file and corrupt it.
3. Install the backup as `precious.db`, owned by the service account, mode `0600`.
4. Start Precious. It applies any migrations the backup lacks, and refuses a backup whose schema is newer than the binary (see [Upgrades](#upgrades)).

Under systemd:

```sh
sudo systemctl stop precious
sudo sh -c 'cd /var/lib/precious && for f in precious.db precious.db-wal precious.db-shm; do
  if [ -e "$f" ]; then mv "$f" "pre-restore-$f"; fi; done'
sudo install -m 0600 -o precious -g precious /var/backups/precious/precious-20261001-120000.db \
  /var/lib/precious/precious.db
sudo systemctl start precious
```

With Compose:

```sh
docker compose -f deploy/compose.yaml stop precious
docker run --rm -v precious_state:/v -v "$PWD/precious-20261001-120000.db:/backup.db:ro" alpine sh -c '
  cd /v && for f in precious.db precious.db-wal precious.db-shm; do
    if [ -e "$f" ]; then mv "$f" "pre-restore-$f"; fi
  done
  cp /backup.db precious.db && chown 65532:65532 precious.db && chmod 0600 precious.db'
docker compose -f deploy/compose.yaml start precious
```

The renamed `pre-restore-*` files still form a matching set; delete them once the restored instance works.

## Upgrades

Precious R1 started from a fresh database: on first start Precious creates `precious.db` with its baseline schema in the state directory. A database from the earlier release, `curator.db`, is never opened, imported, or changed; when one is in the state directory, Precious logs a line naming it and leaves it as it is.

Upgrade a running installation in this order:

1. **Back up first.** Run `precious backup` (see [Backup and restore](#backup-and-restore)) while the old version is still running.
2. **Check the configuration with the new build** before installing it, for example `./precious check-config --config /etc/precious/precious.toml`. A new version may reject keys or values the old one accepted.
3. **Replace and restart.** systemd: `sudo install -m 0755 bin/precious /usr/local/bin/precious && sudo systemctl restart precious`. Compose: `PRECIOUS_VERSION=v0.2.0 docker compose -f deploy/compose.yaml up -d --build`.

At startup Precious applies pending numbered migrations, each in its own transaction, and records each applied version. If a migration fails, its changes are rolled back, the database stays at the previous version, and Precious exits with the error.

Migrations only move forward. An older binary refuses to start against a database that a newer one has migrated, and leaves the file unmodified; the error reads `store: database schema is newer than this binary supports: database has version N, binary supports up to M`. A database whose recorded migrations have different names from the binary's, such as an older release's database renamed to `precious.db`, is refused the same way. To roll back an upgrade, stop Precious, reinstall the older binary or image, restore the backup taken in step 1, and start it. Never edit `schema_migrations` by hand to get past these checks.

### Upgrading from R1 to R2

R2 adds hashing, archives, duplicates, Compare, and opportunities. Its migration, `0002_content`, only adds tables: every R1 entry, decision, tag, selection, job, and audit event stays as it was, and no rescan is needed for any of that. One figure does wait for a rescan: R2 treats a modification time on the epoch's first day (before 1970-01-02, the time a disk records when it lost the real one) as unknown, and an R1 folder's newest and oldest dates and its by-year figures leave such files out after the folder's next scan.

1. **Back up first** with the R1 binary still running: `precious backup` (see [Taking a backup](#taking-a-backup)).
2. **Check the configuration** with the R2 binary. An R1 configuration stays valid: the new `[hashing]`, `[archives]`, and `[duplicates]` sections have defaults (see [Configuration reference](#configuration-reference)), and `[copies]` is still refused.
3. **Replace and restart.** At startup `0002_content` applies to the R1 database in one transaction, and every online source gets a hashing job, which reads file content in the background (see [Hashing](#hashing)). The first run on a large archive can take hours; scans, pages, and decisions are not blocked while it runs.

To roll back, stop Precious, reinstall the R1 binary or image, [restore](#restoring) the backup taken in step 1, and start it. The R1 binary refuses the migrated database (`database has version 2, binary supports up to 1`) and leaves it unmodified, so the backup is the only way back. Decisions and tags recorded after the upgrade are lost with it.

### Upgrading from R2

The update after R2 adds the owner's category overrides and group marks, scheduled rescans, and the Search, Map, and Compare improvements. Its migration `0003_owner` adds the table of overrides and the schedule columns of each source, both empty, and rebuilds the name index so that a search ignores accents. The rebuild reads every name once, which takes seconds per 100,000 entries. Migration `0004_search` adds a small index of the entries that could not be read. Every entry, decision, tag, digest, and listing stays as it was, and no rescan is needed.

1. **Back up first** with the R2 binary still running: `precious backup`.
2. **Check the configuration** with the new binary; an R2 configuration stays valid.
3. **Replace and restart.** The migrations apply at startup, each in one transaction.

To roll back, stop Precious, reinstall the R2 binary, [restore](#restoring) the backup, and start it. The R2 binary refuses the migrated database (`database has version 4, binary supports up to 2`) and leaves it unmodified. Overrides, group marks, and schedules set after the upgrade are lost with it.

### Upgrading from R2b

The update after R2b removes Gems and keeps its rescue list as the opportunity card "Your files inside programs". Its migration `0005_rescue` rebuilds the table of review rows: the rescue rows stay, under the card's list, and the rows of Gems' "unique" and "only in a copy" lists are dropped. Every other card's rows, every entry, decision, tag, override, and digest stay as they were, and the cards read at once, with no rescan. The relations pass that starts with the server then recomputes the review rows under the new rules (a minute or so on a large archive).

1. **Back up first** with the R2b binary still running: `precious backup`. The migration is one-way.
2. **Check the configuration** with the new binary; an R2b configuration stays valid.
3. **Replace and restart.** The migration applies at startup, in one transaction.

To roll back, stop Precious, reinstall the R2b binary, [restore](#restoring) the backup, and start it. The R2b binary refuses the migrated database (`database has version 5, binary supports up to 4`) and leaves it unmodified. Decisions, tags, and overrides set after the upgrade are lost with it.

### Moving from curator to precious

Precious replaces the earlier `curator` release; the two share no data. Moving over is a fresh installation:

1. Install Precious as described in [Installation](#installation), with its own configuration file (`/etc/precious/precious.toml`), state directory (`/var/lib/precious`), service account, and unit (`deploy/precious.service`). The old configuration does not load: its sections are refused by name (see [Configuration reference](#configuration-reference)).
2. Set the password, start the server, and add your disks again on the Sources screen; there is no `[[sources]]` list any more. The first scan of each source rebuilds its index.
3. Stop and disable the old service when you no longer need it. Its database, `curator.db`, is never opened or changed by Precious, even when it sits in the same state directory, where Precious only logs a line naming it.

To go back, stop Precious and start the old `curator` service again: its binary (tag `curator-m4b`), configuration, and `curator.db` are exactly as they were. Decisions and tags recorded in Precious stay in `precious.db` and are not visible to `curator`.

## Configuration reference

Precious reads one TOML file named with `--config`. Loading is strict: an unknown key (for example a misspelled `sever`), a malformed value, or any unsafe combination stops startup with a non-zero exit status, and the error names every offending key. The sections of earlier releases (`[[sources]]`, `[discovery]`, `[inspection]`, `[reconciliation]`, `[inbox]`, `[watch]`, `[copies]`, `[classifier]`, `[[classifier_profiles]]`, and `[[classifier_policies]]`) are unknown keys too: an older configuration is refused with each of its removed keys named, never silently ignored.

Run `precious check-config --config <file>` to validate a file without starting the server (see [First run](#first-run)). [`deploy/examples/precious.toml`](../deploy/examples/precious.toml) is a commented example that lists every key with its default.

Durations are strings such as `"30s"`, `"15m"`, or `"12h"`.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `state_dir` | string | required | Absolute path of the private state directory (database and WAL files), on local storage. See [State directory](#state-directory). |
| `server.listen` | string | `"127.0.0.1:8080"` | TCP listen address as `host:port`. Any host other than `127.0.0.0/8`, `::1`, or `localhost` requires `server.allow_non_loopback_listen`. |
| `server.external_origin` | string | required | The exact origin browsers use, `scheme://host[:port]` with no path, query, fragment, or user information. It sets the expected `Origin` of state-changing requests and cookie security. |
| `server.allow_non_loopback_listen` | boolean | `false` | Opt-in for a non-loopback `server.listen`, for example `0.0.0.0:8080` inside a container or on a local network. |
| `server.allow_insecure_http` | boolean | `false` | Permits an `http://` external origin, on any host, so Precious can be served over plain HTTP on a local network. Without it only `https://` origins are accepted. See [Plain HTTP on a local network](#plain-http-on-a-local-network). |
| `server.trusted_proxies` | array of strings | `[]` | CIDR prefixes (`"10.0.0.0/8"`) or single addresses (`"127.0.0.1"`, meaning `/32` or `/128`) of reverse proxies whose forwarded headers are honored. See [Behind an HTTPS reverse proxy](#behind-an-https-reverse-proxy). |
| `server.max_request_bytes` | integer | `1048576` | Maximum request body size in bytes. Must be positive. |
| `auth.session_idle` | duration | `"1h"` | A session expires after this much inactivity. Must be positive. |
| `auth.session_absolute` | duration | `"12h"` | A session expires this long after sign-in regardless of activity. Must be at least `auth.session_idle`. |
| `auth.login_max_backoff` | duration | `"15m"` | Upper limit of the exponential backoff after failed sign-ins. Must be positive. |
| `jobs.workers_per_device` | integer | `1` | Concurrent filesystem workers per device. At least 1. |
| `jobs.max_attempts` | integer | `3` | Attempts before a job that keeps losing its worker fails. At least 1. |
| `jobs.lease` | duration | `"30s"` | Length of the lease a worker holds on a running job. Must be positive. |
| `jobs.lease_renew` | duration | `"10s"` | How often a running worker renews its lease. Must be positive and shorter than `jobs.lease`. |
| `jobs.call_watchdog` | duration | `"30s"` | A single filesystem call running longer than this marks its source unresponsive and lets a cancel request show at once. Must be positive. |
| `jobs.event_retention_rows` | integer | `10000` | Job events kept for reconnecting browsers. At least 1. |
| `jobs.event_retention_age` | duration | `"24h"` | Job events older than this are discarded. Must be positive. |
| `sources.allowed_roots` | array of strings | `[]` | Folders the picker offers, and below which sources may be added. Each entry must be the absolute path of an existing directory; entries are cleaned. Empty selects the platform defaults: on Linux the service account's home, `/media`, `/mnt`, `/run/media`, and `/srv`, those that exist. |
| `sources.allow_writes` | boolean | `true` | Whether any source may be changed. `false` forbids writes on every source, whatever its own write permission, which then reads as unavailable with the reason `forbidden_by_config`. Every source starts with writes off either way; the owner turns them on per source. |
| `scan.batch_size` | integer | `1000` | Most row changes a scan commits in one write transaction, 1 to 10000. |
| `scan.list_batch` | integer | `256` | Most directory entries one directory read returns, 1 to 4096. |
| `hashing.read_chunk_bytes` | integer | `1048576` | Bytes one read call takes when Precious reads a file's content to compare it, 65536 (64 KiB) to 16777216 (16 MiB). |
| `hashing.yield_bytes` | integer | `67108864` | Bytes a hashing job reads before it gives way to scans and pages, even inside one file, 1048576 (1 MiB) to 1073741824 (1 GiB). |
| `archives.max_members` | integer | `1000000` | Most members Precious lists in one archive, 1 to 5000000. An archive with more is left partial, with no members. |
| `archives.max_unpacked_bytes` | integer | `1099511627776` | Most unpacked bytes Precious reads from one archive, 1048576 (1 MiB) to 17592186044416 (16 TiB). An archive that unpacks to more is left partial. |
| `archives.max_ratio` | integer | `100` | Highest ratio of unpacked to packed bytes accepted in one archive, 2 to 100000. A higher ratio, typical of an archive bomb, leaves the archive partial. |
| `archives.max_time` | duration | `"4h"` | Longest time Precious spends reading one archive, from 1 minute to 7 days. A slower archive is left partial. |
| `archives.view_max_bytes` | integer | `67108864` | Largest compressed zip member the viewer unpacks into memory to serve with ranges, 1048576 (1 MiB) to 1073741824 (1 GiB). A larger one is streamed without ranges. |
| `duplicates.refresh_interval` | duration | `"10m"` | How often duplicate folders and the review lists are recomputed while hashing runs, from 1 minute to 24 hours. They are also recomputed when hashing ends and after each scan. |

## Platform and supported systems

Precious builds for Linux, macOS, and Windows, each on amd64 and arm64 (`make cross` builds all six). How much it knows about your disks depends on the system:

| System | Support |
|---|---|
| Linux, amd64 and arm64 | Complete and tested. Precious reads the system's mount table, identifies each volume so a disk is recognized wherever it is mounted, and detects each filesystem's capabilities from its type. |
| macOS and Windows, amd64 and arm64 | Precious builds and runs, and sources can be added, scanned, browsed, searched, and viewed. Every source is recognized only at its current location, and every filesystem gets the conservative capabilities described below. Native volume recognition and filesystem detection for these systems are planned for a later release (R8 in the product specification). |

The API and the interface are the same on every system; only the reported volume and capabilities differ. On every system Precious reads a source without changing it, unless you allow changes on it, which needs the no-replace rename only Linux offers in this release (see [Changing disks](#changing-disks)). It never follows a symlink, never opens a FIFO, socket, or device, and never enters another filesystem mounted inside a source.

### Volume identity

A source is recorded as a volume plus a folder inside that volume, not as an absolute path. A USB disk that was at `/media/fotos` and comes back at `/run/media/you/FOTOS` is therefore the same source, in the same folder. On Linux, Precious identifies a volume by the first of these that applies:

| Kind | Identified by | Example | Strong |
|---|---|---|---|
| `zfs` | the ZFS dataset name | `tank/fotos` | yes |
| `uuid` | the filesystem UUID listed in `/dev/disk/by-uuid` | `66cdfab2-e862-4156-a1a3-10f060de7fa3`, `2658-C1FD` | yes |
| `fsid` | the btrfs filesystem ID reported by the kernel, one per subvolume | `5566778811223344` | yes |
| `path` | the mount point alone | `/mnt/share` | no |

- A **strong** identity follows the volume to wherever it is mounted next.
- A **weak** `path` identity is the fallback: for filesystems with none of the above (network shares, tmpfs, most FUSE mounts), and for every source on macOS and Windows. Such a source is recognized only at that same mount point, and the Sources screen says so. Mounted anywhere else, it shows as offline.
- A folder bind-mounted somewhere else belongs to its own volume, so a source added through the bind mount and one added on the original mount are on the same volume.
- The label shown next to a volume comes from `/dev/disk/by-label` when the filesystem has one.

Precious learns all this by reading `/proc/self/mountinfo`, `/dev/disk/by-uuid`, `/dev/disk/by-label`, and `/sys/class/block`. It needs no privileges and runs no external command such as `blkid`. If the service cannot see `/dev/disk`, filesystems that would be identified by UUID get the weak `path` identity instead; ZFS datasets and btrfs keep theirs. Keep `/dev/disk` visible:

- **systemd:** `PrivateDevices=yes` gives the service its own `/dev`, without `/dev/disk`. The shipped [`deploy/precious.service`](../deploy/precious.service) keeps `PrivateDevices=yes` and binds `/dev/disk` back in read-only with `BindReadOnlyPaths=-/dev/disk`. Keep that line if you write your own unit.
- **Containers:** mount the host's `/dev/disk` read-only, with `-v /dev/disk:/dev/disk:ro` or a Compose volume `/dev/disk:/dev/disk:ro`, as the shipped [`deploy/compose.yaml`](../deploy/compose.yaml) does. The container's own read-only `/sys` is enough; no device needs to be passed in.

### Filesystem capabilities

Each source records the capabilities of its filesystem, detected when the source is added and on every availability check. On Linux they follow the filesystem type:

| Filesystem type | Names | Stable file identity | Time resolution | Local time | Read-only | No-replace rename |
|---|---|---|---|---|---|---|
| ext2, ext3, ext4, xfs, btrfs, zfs, f2fs, tmpfs | case-sensitive | yes | 1 ns | no | as mounted | yes |
| vfat (FAT) | case-insensitive | no | 2 s | yes | as mounted | yes |
| exfat | case-insensitive | no | 10 ms | no | as mounted | yes |
| ntfs3 | case-insensitive | yes | 100 ns | no | as mounted | yes |
| ntfs, NTFS through ntfs-3g (`fuseblk`) | case-insensitive | yes | 100 ns | no | as mounted | no |
| iso9660, udf (optical discs) | case-sensitive | yes | 1 s | no | always | no |
| anything else, and every filesystem on macOS and Windows | case-insensitive | no | 2 s | no | as mounted (Linux only) | no |

The last row is the conservative set, reported with `known: false`: Precious assumes the least it can rely on. NTFS is treated as case-insensitive because Windows treats its names that way. An NTFS volume mounted through ntfs-3g shows the type `fuseblk`, which other drivers use too; Precious recognizes it by its NTFS volume serial (16 hexadecimal digits in `/dev/disk/by-uuid`), and gives any other `fuseblk` filesystem the conservative set. No filesystem normalizes Unicode in names, so two names that differ only in Unicode composition stay two names everywhere.

What the capabilities change:

- **Case sensitivity.** On a case-insensitive filesystem, names that differ only in letter case, such as `Fotos` and `FOTOS`, are the same name wherever Precious compares names. Every entry still keeps its name exactly as the filesystem lists it.
- **Time resolution.** A rescan treats a file as unchanged when its size is the same and its modification time differs by no more than the resolution. A FAT memory card, which stores times in 2-second steps, therefore does not show every file as modified, while on ext4 a change of a microsecond is a change.
- **Local time.** FAT stores times in local time, without a time zone, so after a daylight-saving change every time on the card can move by an hour. On a local-time filesystem a difference of one hour (within the resolution) also counts as unchanged. A card read in another time zone shows its files as changed: Precious does not hide edits made within the same hour.
- **Stable file identity.** Where it is missing (FAT, exFAT, and the conservative set), device and inode numbers change between mounts, so Precious does not rely on them and matches a file by its path, size, and modification time.
- **Read-only.** Follows the mount; optical discs always report read-only. Precious never writes to a read-only source.
- **No-replace rename.** Whether the filesystem's driver can rename an entry only when the new name is free, failing instead of replacing what holds it (`RENAME_NOREPLACE` on Linux). Precious changes a source only where it can, so a move or a rename can never overwrite a file; without it, writes on that source are unavailable. Should a filesystem refuse the flag at run time even so (old OpenZFS releases do), Precious stops, moves nothing, and turns that source's write permission off.

## Sources

A source is a folder Precious indexes: a whole disk, or one folder on it. Sources are added, renamed, and removed on the Sources screen and live in the database; the configuration file lists none, only where they may be added from.

### Adding a source

On the Sources screen, add a source and choose its folder in the folder picker:

- The picker starts at the [allowed roots](#allowed-roots) and shows only folders, never files. It never shows or enters a symbolic link, so a link cannot lead it out of an allowed root. A folder already added is marked as a source.
- A folder with more than 1,000 subfolders shows 1,000 of them and says the list is incomplete. To reach a folder that is not shown, configure an allowed root closer to it.
- The label defaults to the folder's name. The source's ID is made from the label when the source is added (`Fotos` becomes `fotos`, a second `Fotos` becomes `fotos-2`) and never changes; renaming a source changes only its label. An ID is never given to a later source, even after its source is removed.
- Adding a source does not scan it. Start the first scan from the Sources screen.

Some folders are refused:

- a folder that is already a source, lies inside one, or holds one on the same volume (`source_exists`): each file is indexed by one source only;
- the state directory, a folder inside it, or a folder holding it (`outside_allowed_roots`);
- a folder outside every allowed root, including one reached through a symbolic link that points outside (`outside_allowed_roots`).

The browser never sends a typed path. Each folder the picker shows carries an opaque handle signed with a key that Precious makes when it starts, and adding a source names that handle. After a restart the old handles are refused, so reopen the picker.

Removing a source deletes its index: its entries, folder totals, decisions, tag assignments, digests, archive listings, history of changes by Precious, and its rows in relations and review lists. The tags themselves stay, and no file on the disk is touched. A source cannot be removed, and nothing changes, while its scan is queued, running, or paused, or while a change by Precious on it (a move, a rename, a new folder) is waiting its turn or running (`job_active`); cancel it first. Nor can it while a step of a change is still being recorded or waits for you to check it in History (`recovery_needed`): removing the source would delete the only record of a step that may be half done. Resolve the step first. A hashing job of the source does not block removal: it is cancelled and goes away with the source, and the duplicates of the other sources are recomputed without it.

### Allowed roots

`sources.allowed_roots` lists the folders the picker starts from; a source can be added only at or below one of them. When it is empty or absent, these defaults apply, keeping only those that exist:

| System | Default allowed roots |
|---|---|
| Linux | the home folder of the account running Precious, `/media`, `/mnt`, `/run/media`, `/srv` |
| macOS | the home folder of the account running Precious, `/Volumes` |
| Windows | the user profile folder, and every drive letter |

A configured list replaces the defaults entirely: with `allowed_roots = ["/tank"]` the picker offers only `/tank`. Each root is resolved through symbolic links once at startup, and a folder is accepted only when its resolved path lies inside a root; restart Precious after changing the list. The shipped systemd unit uses `ProtectHome=read-only`, under which the home folders stay visible but read-only; the Compose example uses `/sources`, where it binds the disks, as its only root.

### Offline and unavailable sources

Precious checks where each source's volume is mounted every minute, whenever the Sources screen loads the list, and whenever it opens a source. Each source is in one of three states, shown with its reason:

| State | Reason | Meaning |
|---|---|---|
| `online` | | The volume is mounted and the source's folder opens. |
| `offline` | `volume_not_mounted` | No mounted filesystem has the source's volume identity: the disk is detached, unmounted, or not visible to the service. |
| `unavailable` | `root_missing` | The volume is mounted, but the source's folder no longer exists on it. |
| `unavailable` | `root_unreadable` | The folder exists, but the service account may not read it. |
| `unavailable` | `root_unavailable` | Opening the folder failed with an input/output error, or a stale or disconnected mount. |
| `unavailable` | `root_covered` | The folder's path now leads to another filesystem, mounted over it or reached through a symbolic link, so Precious does not treat that filesystem as the source. |

A source that is not online stays browsable. Its folders, totals, search results, decisions, and tags are all there, because they come from the index, and a check never changes or marks missing any entry. What needs the disk is refused with `source_offline` until the source is online again: scanning, and viewing a file's content.

A source is found by its volume identity and its folder inside the volume (see [Volume identity](#volume-identity)), not by its absolute path. A disk with a strong identity that was at `/media/fotos` and is attached again at `/run/media/you/FOTOS` is the same source, online at the new mount point with the same entries, decisions, and tags; nothing needs to be done. A different disk mounted where the source's disk used to be leaves the source offline, and none of its files appear under it.

Under systemd's read-only settings or in a container, a disk mounted after the service started may stay invisible to it until Precious restarts (see [Read-only disk mounts](#read-only-disk-mounts-recommended)).

### Rescan schedule

Each source can be rescanned on a schedule: off (the default), daily at a time of day, or weekly on a day of the week at a time of day. Set it with **Change schedule** on the source's card; the time is in the browser's time zone, which is stored with the schedule, so the scan runs at that wall-clock time there whatever the server's own zone. The card shows the schedule, the time of the next scheduled scan, and, when the last due time was skipped, when and why. See [Scheduled scans](#scheduled-scans) for how the scans run.

| Command | Request | Response |
|---|---|---|
| `set-source-schedule` | `{"source_id":"fotos","schedule":{"every":"week","weekday":0,"at":"03:00","zone":"America/Sao_Paulo"}}`; `"every":"day"` has no `weekday`; `"schedule":null` turns it off | 200 `{"source": …}` |

`weekday` is 0 (Sunday) to 6 (Saturday), `at` is `HH:MM` from `00:00` to `23:59`, and `zone` is an IANA time zone name, resolved from a copy of the time zone database built into the binary, so it works on every system. A malformed schedule (a bad time, weekday, or zone, a field missing or unknown, or no `schedule` at all) is refused with `invalid_request`, and an unknown source with `unknown_source`; a refused request changes nothing. Setting a schedule computes its next scan from the current time, and turning it off clears it. Each accepted request writes one `source_schedule_set` audit event with the previous and the new schedule.

Each source in `GET /api/sources` carries `schedule` (the object above, or `null` when off), `next_scan_at` (the next due time, or `null`), and `schedule_skipped` (`{"at","reason"}` for the last due time when it was skipped, or `null` when it ran). The reason is the source's state then, `offline` or `unavailable`, or `invalid_schedule` for a stored schedule that no longer validates, which stops its scans until it is set again.

### Changes by Precious

Each source has its own write permission: whether Precious may move, rename, and create folders on it when you ask. Every source starts with it off, so Precious only reads it. Turn it on with **Allow changes…** on the Sources screen, which asks first, and off with **Turn off**, which takes effect at once; the next step of a change in progress checks it and stops.

| Command | Request | Response |
|---|---|---|
| `set-source-writes` | `{"source_id":"fotos","enabled":true}`; `"enabled":false` turns it off | 200 `{"source": …}` |

Each source in `GET /api/sources` carries `writes`: `{"enabled":false,"unavailable":null}`. `enabled` is the permission. `unavailable` is `null` when it can be turned on, or the reason it cannot, the first that applies:

| Reason | Meaning |
|---|---|
| `forbidden_by_config` | [`sources.allow_writes`](#configuration-reference) is `false`, which forbids changes on every source. |
| `read_only` | The filesystem is mounted read-only, as the shipped systemd unit and Compose file mount the disks (see [Read-only disk mounts](#read-only-disk-mounts-recommended)); allow writes there first. |
| `no_replace_rename` | The filesystem cannot rename without the risk of replacing an existing file (`capabilities.no_replace_rename` is `false`; see [Filesystem capabilities](#filesystem-capabilities)). |

The reasons read the capabilities as last recorded, which Precious refreshes every minute and whenever the list is loaded. Turning writes on while `unavailable` is not `null` is refused with `409 writes_unavailable` and changes nothing; turning them off always succeeds, also where they are unavailable. A missing or malformed `enabled`, or any other field, is `invalid_request`, and an unknown source `unknown_source`. Each change writes one `source_writes_set` audit event with the source, the new value, and the previous one; a request that sets the value the source already has changes nothing and writes no event.

### Volumes that cannot be recognized when moved

A source on a volume with the weak `path` identity, reported with `"strong": false`, is recognized only at the mount point it was added at. Mounted anywhere else, it shows as offline. This applies to filesystems with no UUID, dataset, or btrfs identity (network shares, tmpfs, most FUSE filesystems), to disks whose UUID the service cannot see, and to every source on macOS and Windows in this release. To keep such a source:

- On Linux, let the service see `/dev/disk`, so disks with a filesystem UUID get a strong identity. The shipped systemd unit and Compose file already do; see [Volume identity](#volume-identity). A source added while `/dev/disk` was hidden keeps its weak identity and shows as offline once its disk is identified by UUID; remove it and add the folder again.
- Mount the filesystem at the same mount point every time: an `/etc/fstab` entry or a systemd mount unit for a network share, or the same drive letter or volume path on macOS and Windows.
- If the volume has moved for good, remove the source and add the folder at its new location as a new source, then scan it. Decisions and tags of the removed source are not carried over.

## Scanning and the index

A scan reads a source's folders and records every file, folder, symbolic link, and special file in it, with the folder totals, breakdowns, and classification the screens show. Start one with **Scan now** on the Sources screen, or with the `start-scan` command, or let a [rescan schedule](#scheduled-scans) start it; adding a source does not scan it.

### What a scan reads and records

A scan lists every folder in batches of `scan.list_batch` entries and reads each entry's metadata (`lstat`): kind, size, modification and change times, permissions, link count, and file identity. It reads no file content. It never writes to the source, never follows a symbolic link (the link is recorded with its target text), never opens a FIFO, socket, or device (recorded as a special file), and never enters a folder where another filesystem is mounted: that folder is recorded as a mount boundary, with no contents. Names are kept byte for byte, including names that are not valid UTF-8, which the interface shows with `\xNN` escapes.

Each folder's totals are complete when its last entry is done, in the same pass: its total bytes and files, the newest and oldest file time, its main file kind, its counts of folders, files, links, special files, unreadable folders, and mount boundaries below it, its bytes by file kind, by year (of the files' modification times, UTC), and by family (its [composition](#what-a-folder-is-made-of)), and the [notable entries inside it](#what-a-folder-is-made-of). Links and special files count no bytes. A modification time on the epoch's first day (before 1970-01-02) counts as unknown, since it is what a disk records when it lost the real time: it sets no folder date, and its file is counted under an unknown year, shown after the others. The rules classify every file as it is listed and every folder once it is complete (see [Classification rules](#classification-rules)); a folder whose discard suggestion is vetoed keeps up to 20 examples of the user material below it.

Rows are written in batches of up to `scan.batch_size` changes per database transaction, on a separate writer, so reading the disk and writing the index overlap. When the database falls behind, the scan waits for it rather than holding more of the tree in memory. The index needs roughly 1 to 1.5 GB of space in the state directory for 2 million entries.

One scan per source runs at a time: a second request while a scan is queued, running, or paused returns that scan (`"coalesced": true`). A source that is not online is refused with `source_offline`, and an unknown one with `unknown_source`.

| Command | Request | Response |
|---|---|---|
| `start-scan` | `{"source_id":"fotos"}` | 202 `{"job_id","state","coalesced"}` |

### Rescans

Every scan walks the whole source again. It compares each entry with the one stored at the same path and writes only what changed:

- An entry is **unchanged** when it has the same kind and size and a modification time within the filesystem's tolerance (see [Filesystem capabilities](#filesystem-capabilities)): its time resolution, and on a FAT card also a daylight-saving shift of one hour. Where the system reports a change time for both the stored and the observed entry, it must be within the same tolerance too, so a file whose modification time was set back after a change is still seen as changed. On filesystems with stable file identity it must also be the same file (device and inode). An unchanged entry is not written, so a rescan of an unchanged disk changes no entry; only the source's record of its last scan is updated.
- A **changed** entry is updated in place and keeps its ID, decision, and tags. When its size, times, or identity changed, it also loses what hashing knew about its content and, for an archive, its list of members, in the same write; they are read again by the next hashing job. An entry rewritten only because the rules classify it differently keeps them.
- A **new** entry is added. It has no decision of its own and takes its folder's effective decision, as read when its row is written: a file appearing inside a discarded folder reads discard.
- An entry whose **kind changed** at the same path, such as a file replaced by a folder of the same name, is a different entry: the old one, with whatever was below it, is removed from the index together with its decisions and tags, and the new one is added.

Changing the rules between releases does not need anything special: the next scan reclassifies every entry whose classification differs and writes only those.

A scan that finishes successfully starts a hashing job for every online source (see [Hashing](#hashing)); a failed or cancelled scan does not.

### Moves made by Precious

When Precious itself moves or renames an entry, or creates or removes a folder, on a source, the index follows in the same database transaction that records the step, without a rescan. A moved entry keeps its ID, so its decision, tags, classification overrides, digests, and archive listing go with it, and so does everything below a moved folder; its effective decision then comes from its new folder unless it has its own. Its classification, and the totals, breakdowns, and classification of every folder above its old and new places, are recomputed from the index exactly as a scan computes them, so after moves and renames the next rescan finds nothing to change (after a new folder, it only records the folder's own size on disk). A folder's indicator examples are therefore the first 20 by path; the first scan after upgrading reorders those lists once. A large folder moves in one transaction, which holds other writes to the index for a moment (well under 10 seconds for 100,000 entries). A missing entry that holds the destination's name is removed from the index, with whatever is below it, unless one of them carries your decision, a tag, or an override: the step is then refused. A scan waits while Precious is changing its source.

### The quarantine folder

Each source's quarantine is the folder `.precious-quarantine` at the source's top. Scans walk it like any other folder, so the index always matches what is in it: a file deleted there by hand goes missing, and a file put there by hand, or left there by an interrupted step, is added. They never count it, though: the source's top folder leaves it out of its totals, breakdowns, and classification, so the source's size, Home, and every folder's figures exclude what is in quarantine. The quarantine folder's own row still adds up what it holds, which is how Home shows the bytes in quarantine. Hashing, duplicates, review lists, relations, the Map, and Search leave everything at or below that path out. A file or link with that name at the top is likewise left out of the totals. A folder of that name anywhere below the top is an ordinary folder.

### Scheduled scans

A source with a [rescan schedule](#rescan-schedule) is scanned when its time comes, exactly as **Scan now** would: hashing and relations follow it, and it changes no decision, tag, override, or group mark. Precious looks for due sources when it starts and then every minute, so a scan starts within a minute of its time. For each due source:

- An **online** source gets a scan. When one of its scans is already queued, running, or paused, the due scan joins it rather than starting a second.
- A source that is **offline or unavailable** is skipped until its next time, checked just before. The skip is recorded on the source with its time and the state, and the card shows it; the next scheduled scan that runs clears it.
- Either way, its next scan becomes the schedule's first time after now, in the same transaction, so a due time is used once and never runs twice.

The next scan time is stored in the database, so it survives restarts. When the server was down at one or more due times, it starts one scan when it is back, not one per missed time, and the next scan is the schedule's next time. A time that a daylight-saving change skips runs once that day, shifted by the change (02:30 on the day clocks go forward an hour runs at 03:30), and a time that occurs twice runs once, the first time.

### Missing entries

An entry that a complete listing of its folder no longer shows becomes **missing**, and so does everything stored below a missing folder. A missing entry stays in the index with its ID, decision, tags, and the time it went missing; it no longer counts in its folders' totals. When it appears again at the same path, it is **present** again under the same ID, with its decision and tags.

### Unreadable and partial folders

A folder the scan cannot open or list completely, typically for lack of permission, is **unreadable**. What it held before stays in the index as it was (a folder never goes missing because it could not be read), it counts only what the scan could read, and every folder above it is **partial**: its totals do not include whatever the unreadable folder holds. Give the service account read access and rescan; a complete scan clears both marks.

When the source itself disappears during a scan (the disk is unplugged), the scan fails with `source_offline` and the index keeps what it had written.

### Cancelled and interrupted scans

Cancelling a scan, or a crash or restart while one runs, keeps every row it had written. The folders it had not finished keep the totals they had before it (none, on a first scan) and are marked partial. A scan is never resumed in the middle: the next one starts at the source's root again. Because unchanged entries are only read, not written, that second pass costs little more than walking the tree.

### Progress

A running scan reports these counters in its job's `progress` (`GET /api/jobs/{id}` and the event stream):

| Key | Meaning |
|---|---|
| `phase` | `1` while walking the tree, `2` while recording the finished scan |
| `dirs` | folders reached so far |
| `files` | regular files seen so far |
| `bytes` | bytes of those files |
| `written` | entry rows inserted or updated so far |
| `unreadable` | folders that could not be read |
| `missing` | entries marked missing so far |

### Measuring scan speed

The target is that a scan takes at most 1.5 times as long as a bare metadata walk of the same tree, which only lists folders and reads each entry's metadata, as `find -printf '%s %T@'` does. `tools/walkbench` measures it on a source tree, without the server:

```sh
go run ./tools/walkbench -root /tank/archive [-state DIR] [-cpuprofile FILE] [-only walk|scan]
```

It walks the tree once to warm the caches, then times a bare walk (the same batched listing and `lstat` calls the scan makes, without following links or crossing mount points) and a full first scan of the tree into a new database in DIR (by default a temporary directory, removed afterwards; a given DIR must not hold a database yet), run as a scan job. It prints the number of entries, both times, and their ratio; `-cpuprofile` writes a CPU profile of the scan for `go tool pprof`.

Both measurements are warm. A warm walk is the fastest baseline, so the warm ratio is stricter than the cold one the target names: on a tree of many small files on a fast disk, where the warm walk takes a few microseconds per entry while the scan writes an entry row, its name-index row, and six index entries for each, the warm ratio is well above 1.5. For the cold measurement, empty the filesystem caches (on ZFS by exporting and importing the pool, elsewhere with `echo 3 > /proc/sys/vm/drop_caches` as root; walkbench does not do that, because it needs root) and run `-only walk`, then empty them again and run `-only scan` with a fresh `-state` directory. Each run times just that measurement, without warming the caches first; the cold ratio is the scan's time divided by the walk's.

## Hashing

Precious finds copies by reading file content and computing its SHA-256 digest. Hashing only reads; it never writes to a source, and it opens every file read-only, without updating its access time where the filesystem allows that.

### When it runs

- **After every successful scan**, a hashing job starts for every online source, not only the scanned one, because a new file can share its size with a file on another source.
- **At server start**, every online source gets a hashing job.
- **On request**, `start-hash` with `{"source_id":"…"}` starts the source's job (202, or the running job with `"coalesced":true`; 404 `unknown_source`; 409 `source_offline`).

A source has at most one hashing job at a time. Hashing is background work: on each disk it gives way to scans and to work you are waiting for, after every commit and after every `hashing.yield_bytes` read, even in the middle of a file. A source whose volume is not mounted is not hashed, and its digests stay as they were; its files still count as copies of files elsewhere.

### Which files are read

Each job starts by grouping, without reading anything, every present non-empty file and every file inside a listed archive, on every source, by size. Hard links to one file count once.

- A file whose size no other file shares has **no other copy** and is never read.
- Zero-byte files are never read and are never duplicates.
- A file whose size is shared is read, unless its digest is already known.

A digest stays valid until a rescan finds that the file's size, modification time, change time, or identity changed; the rescan then discards it. A file that did not change is never read again, across jobs and restarts.

### Reading order

1. The source's zip archives that are not listed yet: only their central directories (see [Archives](#archives)).
2. Files of at least 1 MiB, and every tar, tar.gz, tar.bz2, gzip, and bzip2 archive, largest first. A file of at least 16 MiB is first compared by the digest of three 64 KiB samples, at its start, middle, and end; it is read in full only when another file of its size has the same samples. Equal samples alone never make two files duplicates.
3. Smaller files inside pairs of folders that look like copies of each other by their file sizes, such as `Fotos` and `Fotos - Copia`, folder by folder.
4. Every other smaller file, folder by folder.

### Reading safety

Each file is reached from the source root one folder at a time, never through a symbolic link or across a mount, and opened only when its size, times, and identity still match its index row. Content is read in `hashing.read_chunk_bytes` chunks. A digest is kept only when every byte was read, the file ended at its indexed size, and the open file still showed the same size, times, and identity at the end. Otherwise:

- a file that changed, or no longer matches its row, is **not checked**; a rescan updates it and the next job reads it again;
- a file that cannot be opened or read (permissions, I/O errors) is **unreadable**.

Results are written at most 64 files at a time. Each write checks again that the file's index row has the size, times, and identity the read saw; a rescan that updated the row in the meantime wins, and the result is dropped.

### Coverage

Coverage is published per source and for all sources together (Home, and the detail of every claim):

| Figure | Files and bytes |
|---|---|
| could have a copy | every file whose size is shared, or that was read |
| checked | files with a digest, and large files whose samples differ from every other file of their size |
| not checked | files still to read, and files that changed while read |
| unreadable | files that could not be read |

A file inside a listed archive counts under the archive's source. A file with no other copy by size is not in these figures: its claim needs no read. Every "no other copy" claim states the checked share of all sources, because a copy can be anywhere. Archives Precious does not open (7z, rar, partly read or damaged ones) count as plain files, so a file inside them is never seen as a copy.

### Check now

`check-now` with `{"entry_ids":["12","m45"]}` (one or two folders, archive files, or folders inside archives) hashes what is not checked yet inside them before any other hashing on their disks, for example to compare two folders while the first hashing run has not reached their small files. It answers 202 with `{"jobs":[{"job_id","state","coalesced"}]}`, one job per source; a second request for the same source adds its folders to the job already waiting or running. A file is 400 `invalid_request`, an unknown ID 404, and a folder on an unmounted source 409 `source_offline`. The rest of the source continues with its regular hashing job afterwards.

### Progress and cancelling

Hashing jobs have kind `hash` (and `hash_now` for `check-now`). Their progress, through `GET /api/jobs/{id}` and `GET /api/events`:

| Key | Meaning |
|---|---|
| `phase` | 1 listing zip archives, 2 large files and archives, 3 small files |
| `candidate_files`, `candidate_bytes` | the source's files that could have a copy |
| `checked_files`, `checked_bytes` | of those, the files checked so far |
| `read_bytes` | bytes this job has read |
| `archives_listed` | archives this job has listed |
| `unreadable` | the source's files that could not be read |

Cancelling a hashing job (`cancel-job`) abandons the file being read and keeps every result already written; the next job reads only what is not checked yet. While hashing runs, duplicate folders and the review lists are recomputed every `duplicates.refresh_interval`, and once more when the job ends.

### Cost on large archives

A zip costs a read of its central directory, plus a read of each member whose size another file shares. A tar, tar.gz, tar.bz2, gzip, or bzip2 archive is read once from start to end, whatever its size, because its members are only known by reading it; its own digest comes from that same read. On an archive of hundreds of gigabytes of tar.gz, the first hashing run therefore takes hours; it is done once, and an archive that does not change is never read again.

## Archives

Precious opens archives in memory to list their members, so that a photo inside a zip counts as a copy of the same photo elsewhere, and so that you can browse and view the members. Nothing is ever unpacked to disk, not even to a temporary file, and the archive file itself is only read.

### Formats

An archive is recognized by its name and confirmed by its first bytes:

| Name | Format |
|---|---|
| `.zip` | zip, stored or deflate |
| `.tar` | tar |
| `.tar.gz`, `.tgz` | tar.gz |
| `.tar.bz2`, `.tbz2`, `.tbz` | tar.bz2 |
| `.gz` | a single gzip-compressed file, named without `.gz` |
| `.bz2` | a single bzip2-compressed file, named without `.bz2` |

One trailing `.old`, `.bak`, or `.orig` is ignored, so `fotos.zip.bak` is a zip. Letter case does not matter.

**What stays unopened:** 7z, rar, and every other format; Office documents and `.jar` files, although they are zips inside; encrypted zips; and archives inside archives, which are members like any other file. An unopened archive is a plain file: it is hashed when its size is shared, and its contents are not seen.

### How each format is read

- **zip:** only the central directory is read to list the members. A member is read later, when another file shares its size.
- **tar, tar.gz, tar.bz2, gzip, bzip2:** the archive is read once from start to end, and every member is listed and hashed in that pass.

An archive is listed once. Its listing and its members' digests are kept until a rescan finds that the archive file changed; the next hashing job then lists it again.

### Budgets

Reading one archive stops at the first of these budgets, from `[archives]` (see [Configuration reference](#configuration-reference)):

| Setting | Stops when |
|---|---|
| `archives.max_members` | the archive holds more members (folders included) |
| `archives.max_unpacked_bytes` | its members unpack to more bytes |
| `archives.max_ratio` | its members unpack to more than this many times the bytes read from the archive, plus 64 MiB, as a zip bomb does |
| `archives.max_time` | reading it takes longer |

An archive stopped by a budget is **partial**, names the budget it reached, and gets no members.

### Outcomes

| State | Meaning | Members |
|---|---|---|
| complete | every member listed | yes |
| partial | a budget was reached | none |
| rejected | a member path is absolute or leaves the archive (`..`), two members share a path, or a member is both a file and a folder; the member is named | none |
| encrypted | a zip member is encrypted | none |
| unsupported | not really an archive of its format, a compression method other than store or deflate, or a multi-disk zip | none |
| corrupt | a checksum or size mismatch, data cut off, or another format error | none |
| changed | the file changed while it was read | none |
| unreadable | the file could not be read | none |

Every state but complete leaves the archive a plain file. Member names are kept as their raw bytes, symbolic links inside archives keep their text and are never followed, and members have no decision or tags of their own: they follow their archive's. A zip member's name that is not valid UTF-8 is shown decoded from code page 850, as zip tools on Portuguese Windows write it, so `Anota\x87\xE4es.txt` reads `Anotações.txt`. The raw bytes are still what is stored, in `name_b64` and `path_b64`.

### Viewing members

A member opens in the viewer under the same types and safety rules as a file, read from the archive in memory. A member stored without compression in a zip supports byte ranges, so a video can seek; a compressed zip member up to `archives.view_max_bytes` is unpacked into memory and supports ranges too; a larger one, and every member of a tar-family archive, is streamed from the start. When the archive file no longer matches the index, the viewer answers 409 `invalid_entry_state` until a rescan.

## Classification rules

Every file and folder Precious indexes gets a **category**, a few **traits**, and a **triage** suggestion: keep, discard, or review. Fixed rules give them, from names and from what each folder holds. They are suggestions to order your review: they never delete, move, or decide anything, and your own decisions always come first.

The rules look at:

- **the entry's own name**, such as `Thumbs.db`, `node_modules`, `RECYCLER`, or `*.iso`;
- **what a folder holds directly**, such as a `.git` folder or a `package.json` file at its top;
- **what a folder holds anywhere inside**, such as compiled `.class` files somewhere below a `bin` folder;
- **what most of a folder's bytes are**, such as photos, videos, and music making up at least 60% of them.

Letter case does not matter in names. A file's kind (image, video, audio, document, source, archive, installer, executable, system, or other) comes from its name only, never from its content. A `.bin` file counts as a disk image only when a `.cue` file with the same name sits beside it; a lone `.bin` is `other`.

### Categories and families

There are 16 categories. Charts and colors group them into four families; details and filters show the exact category.

| Family | Category | What the rules put there |
|---|---|---|
| Personal and valuable | `personal_media` | Camera folders (`DCIM`), a memory card or phone backup holding one, and folders whose bytes are mostly photos, videos, or music. |
| | `documents` | Folders whose bytes are mostly texts, spreadsheets, presentations, or PDFs. |
| | `source_project` | Software projects: a folder with version control (`.git`, `.svn`, and similar) or a build file (`package.json`, `build.xml`, `Makefile`, and similar) at its top. |
| | `application_user_data` | What programs keep for you: saved games, program profiles with settings, bookmarks, or mail, the per-user `Application Data` folder, mailboxes, and key or password files (`id_rsa`, `*.kdbx`, `*.pfx`). |
| Programs and system | `application_installation` | Installed programs: folders whose bytes are mostly program files (`.exe`, `.dll`), or that hold an uninstaller such as `unins000.exe`. Each program under `Program Files` is its own item. |
| | `application_configuration` | Program settings. No rule assigns it in this version. |
| | `os_installation` | Copies of an operating system: a `WINDOWS` folder with `system32` inside, and `system32`, `I386`, and similar folders. |
| | `installer_download` | Installers and disk images: `setup*.exe`, `*install*.exe`, `*.msi`, `*.iso`, `*.img`, `*.nrg`, `*.mdf`, and a `.bin` with its `.cue`. Any other loose `.exe` file also lands here, because a program file on its own is most often an old download. |
| Disposable | `system_junk` | What operating systems leave behind: recycle bins (`$RECYCLE.BIN`, `RECYCLER`, `.Trash-*`), `System Volume Information`, `Thumbs.db`, `desktop.ini`, `.DS_Store`, and what a disk check recovered (`found.000`, `*.CHK`). |
| | `cache` | `Temporary Internet Files`, `cache` folders, and folders marked with `CACHEDIR.TAG`. |
| | `temporary_data` | `Temp` and `tmp` folders, `*.tmp` files, the `~$` lock files office programs leave beside open documents, and unfinished downloads (`*.part`, `*.partial`, `*.crdownload`). |
| | `generated_artifacts` | What build tools download or make: `node_modules`, `__pycache__`, and a `bin`, `obj`, `target`, or `build` folder that holds compiled files (`.class`, `.o`, `.obj`, `.pyc`). A program's own `bin` folder of `.dll` files stays part of the program. |
| Containers | `download_collection` | A `Downloads` folder: things fetched over time, each worth its own look. The name wins even when a bookmarks export or a mail archive sits at its top. |
| | `backup` | A copy of a whole Windows drive: a programs folder next to `Documents and Settings` or `WINDOWS`. |
| | `mixed` | The `Program Files` folder itself, whose programs are classified one by one, and folders that hold a drive copy among other things. |
| | `unknown` | No rule recognized the entry, or two rules of equal weight disagreed. |

Rules have a weight. When several rules recognize an entry, the heaviest one sets its category; for example, a `WINDOWS` folder is full of program files but is an operating system copy, not an installed program. When two equally heavy rules give different categories, the entry stays `unknown` rather than guessing.

**Traits** describe what an entry holds, whatever its category: `contains_user_material` (photos named by a camera, office documents, saved games, profiles, or mail somewhere inside), `contains_credentials` (keys or password files), `contains_database`, `contains_vcs` (version control history), and `possible_generated_content` (folders where build tools usually put what they make). A trait is an observation, not proof that something matters or can be rebuilt.

### Triage

Each category suggests a triage:

| Triage | Categories | Meaning |
|---|---|---|
| keep | `personal_media`, `documents`, `source_project`, `application_user_data` | Most likely yours and hard to replace. |
| discard | `system_junk`, `cache`, `temporary_data`, `generated_artifacts`, `installer_download`, `application_installation` | Most likely made again, downloaded again, or reinstalled when needed. |
| review | all other categories, including `unknown` | Look before deciding. |

Two exceptions always ask for review: what a disk check recovered (`found.000` and `*.CHK` files) is system junk, but it may hold pieces of your own files; and a folder held back by the veto below.

### Groups

A group is a folder that is best reviewed as one item: an installed program, a copy of Windows, a project, a program's saved games or profile, a cache, build output, or a drive backup. Folders in the categories `application_installation`, `os_installation`, `source_project`, `application_user_data`, `cache`, `generated_artifacts`, and `backup` are groups, unless you unmark them; you can also mark any other folder as a group (see [Your own category and groups](#your-own-category-and-groups)). Groups can sit inside other groups; the outermost one is the item to review. Being in a group never hides anything: the Map and Search still reach every file and folder inside, with their own sizes.

### What a folder is made of

A folder's category is one word for the whole folder; what it holds can be more varied. So every folder also carries:

- **Its composition:** its bytes by family, counted from what is inside it, not from its own category. A file counts under its category's family; a file no rule recognized counts by its type (photos, videos, music, documents, and source code as Personal and valuable; installers and programs as Programs and system; system files as Disposable; archives and anything else as Containers). A group outside the Containers family counts whole under its own family, because it is reviewed as one item. Every other folder, a drive backup included, adds up what it holds. A folder of photos that also holds a downloaded disk image and an installed program therefore shows, for example, 98% Personal and valuable and 2% Programs and system, and Home counts those bytes the same way.
- **What is notable inside it:** up to ten entries below it, largest first, that are worth a look on their own: groups, folders made mostly of another family than the folder, and files of another family. Nothing inside a listed entry is listed again. Starting from a source's top folder, this list points straight at the installed programs, downloads, or caches buried among the photos, without opening folder after folder.

Both are computed by the scan, so they are as current as the last scan.

### The veto: user material inside disposable folders

A folder whose suggestion would be discard, but which holds your own material anywhere inside, is suggested for review instead, and its details say why. This is the veto. For example, `Arquivos de programas/Microsoft Office` is an installed program, which alone would be discarded, but the spreadsheet `OFFICE11/Meu orcamento casamento.xls` inside it holds it back for review.

What counts as your own material:

- office documents (`.doc`, `.docx`, `.xls`, `.xlsx`, `.ppt`, `.odt`, and similar, but not the `~$` lock files);
- photos and videos named by a camera or phone (`DSC*`, `IMG_*`, `MVI_*`, `VID_*`, a `P` and seven characters such as `P1010001.JPG`, and WhatsApp images), and camera folders (`DCIM`);
- saved games, program profiles, mailboxes, databases, and key or password files;
- office autosaves (`*.asd`, `*.wbk`) and what a disk check recovered (`*.CHK`, `found.000`).

The icons, skins, splash screens, and other images that programs ship do not count, or every installed program would be held back. The details list up to 20 of the files or folders that caused the veto.

### Reading the explanations

The detail panel shows each rule behind an entry's classification with a one-sentence explanation, such as "A copy of the Windows folder: the operating system itself, with its system32 folder inside." The rules that set the category come first, then the rules that only added traits.

- **No rule matched:** the category is `unknown` and nothing is listed for it. The entry simply looks like nothing the rules know; review it yourself.
- **Rules disagree:** the category is `unknown`, and the disagreeing rules are both listed, so you can see what each one saw.
- **Veto:** the triage is review, and the files or folders that hold the folder back are listed with it.

The rules are versioned, and each scan records the version it used (`rules-v2+markers-v3` in this release).

### Your own category and groups

When the rules get an entry wrong, you can correct them. Your choice always wins over the rules, and no rescan, rules change, or later release ever changes it; an item that goes missing and comes back keeps it.

- **Category:** any file or folder can get one of the 16 categories. Its family and suggestion follow from it as for a rule category, and so does the veto: a folder you put in a disposable category that holds your own material is still suggested for review. **Back to the rules** removes your category.
- **Review as one item:** any folder can be marked as a group, and a group the rules made can be unmarked; **As the rules say** gives the choice back to the rules. Without a mark, a folder is a group when its category (yours or the rules') is a group category, so a project you put in `documents` is no longer a group, and the folders above it count its files one by one under their own families.
- **Archive members** cannot be changed: they are classified with their archive.

The item itself reads its new category or group at once. The figures of the folders above it (their size by category and what stands out inside them) and the review lists follow when the scan of its disk that the change starts ends; if that disk is already being scanned, the scan runs once more. A disk that is not connected keeps the change, and its next scan applies it. The detail panel marks each value you set with **set by you** and says what the rules would set.

Through the command API (see [Commands](#commands)): `set-category` with `{"entry_id":"12","category":"documents"}`, `{"entry_ids":[…]}` (1 to 1,000), or `{"selection_id":"…"}`, and `"category":"rules"` to remove yours; `set-group` with the same targets and `"group":true`, `false`, or `"rules"`. Both answer `{"applied":n,"scan":{"job_id","coalesced"}}`, or `"scan":null` when the disk is not connected. An archive member, a `set-group` on anything but a folder, and a category on a symbolic link or special file are refused with `invalid_request`, and a refused request changes nothing. Each accepted request writes one audit event, `category_set` or `group_set`, with the entries or selection, the old values (`rules` when the rules decided), and the new value.

## Duplicates and Compare

Precious finds copies by content, never by name: two files are copies when their SHA-256 digests are equal. Hashing (see [Hashing](#hashing)) fills in the digests in the background, and everything below follows it.

### Duplicate groups

A **duplicate group** is one content held by at least two copies among the present files of every source, offline sources included, and the members of opened archives (see [Archives](#archives)). Hard links to one file are one copy, and so are a tar hard link and the member it points to. A member is a copy; the archive holding it is not.

A group's **redundant bytes** are its size times the number of copies minus one: what deleting every copy but one would give back. Precious never picks that one copy for you; each copy keeps its own decision.

A file is said to have **no other copy** only when that is known: no other file anywhere has its size, its 64 KiB samples differ from every file of its size, or it was read in full and no other file has its digest. The claim always comes with the share of the content that could have a copy and was checked, across every source, because a copy could sit on any of them. Archives that are not opened (7z, rar, encrypted zips, archives inside archives, and archives that went over a budget) count as plain files: a copy inside one is not seen.

### Folder relations

Folders and opened archives are related by the digests of the files they hold, whatever the names and layout:

| Relation | Meaning |
|---|---|
| `same` | Each side's content all exists on the other side. |
| `inside` | Side A's content all exists on side B, which holds more. |
| `overlap` | At least half of one side's bytes have their content on the other side. |

Each relation records its matched bytes, its redundant bytes, and the files and bytes found only on each side, and the read API serves all of them (`matched_bytes`, `redundant_bytes`, `only_here`, `only_there`). On screen, the detail panel and [Similar folders](#similar-folders) show only the bytes in common, with a link to Compare: Compare alone counts the files found only on one side, and its groups are the counts to act on (ADR 0010). The relation's own counts go by content and can differ from Compare's: a file edited in place under the same name counts in the relation as only on one side, where Compare puts it in same path, different content; and the files inside an opened archive count in the relation one by one, where Compare on a folder lists the archive as one file. Side A is the contained side of `inside`, the archive (or else the later path) of `same`, and the side with the larger matched share of `overlap`. An archive that is the `same` as a folder counts its packed size as redundant: it can go, and the folder keeps everything.

A folder that holds a file not checked yet, a file that could not be read, an unreadable folder, or a mount point is never claimed `same` or `inside`: something in it might exist nowhere else. It can still `overlap`. Files that are empty count for nothing, and symbolic links match by their target text.

Each copy is listed once, at its highest related folder: when `Fotos - Copia` overlaps `Fotos` and their `2004` folders are the same, the list holds those two relations, and none for the folders inside `2004`. Folders that hold nothing but one folder are named by it in a `same` relation, so a copied program folder pairs with the original even when it sits alone in its parent; a folder that holds nothing but an archive is named by the archive.

### Percent duplicated

Every folder carries:

- its **duplicated bytes**: the bytes of the files in its subtree that have another copy anywhere;
- its **candidate bytes**: the bytes of its files whose size another file shares, the only ones that can have a copy, leaving out the files that could not be read;
- its **checked bytes**: the part of the candidate bytes whose content is known.

Its **percent duplicated** is its duplicated bytes over its total bytes; a file is 0% or 100%. The interface names it **Has copies** (ADR 0010): it counts every copy, the one you would keep included, so it is not the space you could free. That space is on [Opportunities](#opportunities), whose duplicates card counts the redundant bytes. The Map colors folders by it in five bands (0%, under 25%, under 50%, under 75%, and 75% or more), and as **not checked** while its checked bytes are below its candidate bytes, because the figure can still grow. A file that could not be read never gets checked, so it does not keep its folders "not checked"; Home still counts it under **Could not be read**. An archive counts in its folder at its packed size, by its own file, never by its members.

### Compare

Compare takes two folders, opened archives, or folders inside an archive, and lists their files in five groups:

| Group | Meaning |
|---|---|
| Only on the left / only on the right | The file's content is proven absent from the other side. |
| Identical | The content exists on both sides, whatever the names. |
| Same path, different content | Both sides have a file at that relative path, and their contents are proven different. |
| Not checked yet | It cannot be said yet. |

A size that the other side does not have at all proves "only here" without reading anything. **Not checked yet** appears for a file that hashing has not read (or could not read, or that changed while it was read) whose size exists on the other side, and for a checked file whose size exists on the other side only among such files: either could be the same content. Click **Check now** to have both sides hashed first; the group empties as hashing proceeds. Unreadable files stay in it.

Compare opens on the first group that holds files, in this order: only on the left, only on the right, same path with different content, not checked yet, and identical. A folder inside another therefore opens on what only the larger one holds, and two copies that are the same open on identical. The server picks that group in the same request that computes the comparison, so opening a comparison computes it once.

Each file shows its path inside each side whenever the two paths differ: when `fotos-b/2002/12/img_0001.jpg` is identical to `fotos/2014/celular/IMG_0001.jpg`, the item reads `2002/12/img_0001.jpg` on the left and `2014/celular/IMG_0001.jpg` on the right. When one side holds more copies of a content than the other, the identical group pairs them in path order and lists each copy left over on its own as an **extra copy**, naming the file on the other side that holds the same content ("Extra copy, same as … on the left"). The groups' counts and bytes are unchanged by this: an item counts once, at its left file's size, else its right file's.

When one side holds nothing but a single folder, such as `emule-0.47c/` inside a zip, and dropping it lines up the paths with the other side, Compare drops it. The two sides cannot contain each other: comparing `Fotos` with `Fotos/2005`, or a file, is refused with `400 invalid_request`.

### When relations are recomputed

The `relate` job recomputes every relation and every folder's figures from the index, for all sources at once. It runs in a pool of its own, one at a time, and never holds a disk: it only reads the database. It starts after each scan, every `duplicates.refresh_interval` while hashing runs and when hashing ends, and at server start when a run was requested but never done. A request while it runs makes it run again when it finishes. The relations shown switch to the new ones all at once, so a page never mixes two runs; the folder figures are updated in place and settle within the run. Its progress shows `phase` (1 loading the index, 2 relating, 3 writing, 4 review lists, 5 cleaning up) and `folders`.

On a development machine, a run over 2 million entries takes seconds and well under 1.5 GB of memory; a Compare of two folders of 100,000 files each answers within 2 seconds.

## Opportunities

Opportunities answers "what should I look at first?" with eight cards, built from the index, the rules' classification, and the duplicates. None of them decides anything for you.

### The cards

| Card | Rows | Bytes | Basis |
|---|---|---|---|
| Your files inside programs (`rescue`) | Your own material the rules found inside installed programs, system copies, and disposable groups, each under its outermost such group | Total bytes | Rules |
| Exact duplicates (`duplicates`) | Folder and archive relations of kind same or inside, and duplicate files outside every listed relation | Redundant bytes | Same content |
| Archives already unpacked (`unpacked_archives`) | Archives whose whole content is the same as, or inside, a folder | The archive file's size | Same content |
| System junk (`system_junk`) | Entries of category `system_junk` | Total bytes | Rules |
| Old installers and disk images (`installers`) | Entries of category `installer_download`: installers and disk images, wherever they are | Total bytes | Rules |
| Programs and system copies (`programs`) | Entries of category `application_installation` or `os_installation` | Total bytes | Rules |
| Caches and generated files (`caches`) | Entries of category `cache`, `temporary_data`, or `generated_artifacts`, except unfinished downloads | Total bytes | Rules |
| Leftovers (`leftovers`) | Unfinished downloads (`*.part`, `*.partial`, `*.crdownload`), empty folders, and zero-byte files | Total bytes | Rules |

Cards are ranked by bytes, largest first, except that **Your files inside programs** comes first while it has open rows, however few bytes they hold (see [Your files inside programs](#your-files-inside-programs)). Cards can show all sources or one. With one source, a card counts only the rows that touch it: its entries, and the duplicates rows with a copy on it.

How the bytes are counted:

- **A row is the outermost match.** A row is a group, folder, archive, or file that matches its card while no folder above it does. `Backup_PC_2004/C/WINDOWS` is one row of the programs card; `system32` inside it adds no bytes of its own. So no byte counts twice in one card. Different cards can overlap: a zero-byte `desktop.ini` is both system junk and a leftover.
- **A downloads folder is not a row.** A `Downloads` folder (category `download_collection`) holds your own files beside what you fetched, so it is not a row of **Old installers and disk images**: the installers and disk images inside it are, each on its own, such as `Downloads/Setup.exe`. The folder itself stays in the Map and in Search. Sorting the rest of it is organizing, which a later release adds.
- **An empty folder** holds no file at any depth, is readable, and is not where another filesystem is mounted. Folders below an unreadable folder or a mount boundary are never called empty.
- **A duplicates row** is a relation (its bytes are one side's worth of redundant bytes), or a group of identical files with at least one copy outside every listed relation. A group's bytes are its size times its copies outside the listed relations, less one when none of its copies is inside a relation: the relation already counts the copies inside it. Hard links to one file are one copy. Files inside archives count as copies; the archive itself does not.
- **Only open rows count.** A row is open while its entry's effective decision is undecided. A duplicates row is open while at least two of its copies are undecided (for a relation, both sides). A row of **Your files inside programs** is open until you decide the file itself or it is kept (see below). Deciding an entry, or the folder above it, closes its row at once and shrinks the card by the row's bytes. A card's bytes are always the sum of its list's open rows, read through every page.
- **Decided rows show as progress.** Besides its open rows, each card and each list's header shows how many rows are no longer open and what they hold, such as "20 decided (4.4 GiB)"; these equal the list of decided rows. A card with no open row left reads **Nothing left to review** instead of zero.

The rows are recomputed by the `relate` job (see Duplicates and Compare), after each scan and as hashing advances, so the classification and duplicates they show are as current as that job's last run. Decisions are never stored in them: they are read live.

### Review lists

Opening a card shows its review list, largest row first. Each row shows its size, dates, suggestion, and a one-line summary of what it holds: category, years, files (counted as in the Map, without the members of archives), bytes, and up to two notable signals, such as a spreadsheet inside an installed program. Duplicates rows expand into their copies, each with its own decision controls; a row of copies of one file names how many copies it has instead of a file count. A row of **Archives already unpacked** names the folder that holds the archive's content and opens Compare on the two.

The list works from the keyboard: `K` keep, `D` discard, `L` later, `J` or `↓` next row, `↑` previous row, and `Enter` opens the detail panel. In the duplicates list, the keys act on the focused copy. When a decision makes the row leave the list, the row that takes its place is selected, so pressing `D` again decides it. `J` on the last row loaded brings the next page. The keys are ignored while typing in a field and inside a dialog.

A decided row leaves the list. Choose to show decided rows to list the rows that are no longer open, with their decisions.

### Selecting a whole list

Every list except duplicates can select all of its open rows (the `select-list` command, `{"list":"system_junk","source_id":"…"}`; `source_id` is optional). This makes an ordinary selection of the rows' entries, exactly like a search's select-all: the confirmation shows the count, the bytes, and the kept entries, and the bulk decision skips every kept entry and reports it. The selection holds the entries open when it was made; a later refresh of the lists does not change it, and an entry kept in the meantime is skipped.

The duplicates list has no select-all (`400 invalid_request`): Precious never chooses which copy stays. Decide copies one by one, or use Search's duplicate filter ("copies outside this folder") and select its results.

### Similar folders

**Similar folders**, linked under the cards, lists the folders and archives related as `overlap` (see [Folder relations](#folder-relations)), largest bytes in common first, for all sources or the chosen one (a pair shows when either side is on it). Each pair shows both sides and the bytes they have in common, with a link that opens Compare on the two. It does not count the files found only on each side: Compare does, in its groups, such as `Fotos - Copia` against `Fotos` with the one edited photo only in the copy (see [Folder relations](#folder-relations) for why the read API's counts can differ).

The list is read-only: it has no decision controls and no card, because similar folders are not copies, and deleting either side can lose what only it holds. Compare them, then decide in Compare or in the Map.

### Your files inside programs

**Your files inside programs** lists your own documents, photos, saved games, and mail that the rules found inside installed programs, system copies, and disposable folders: the files that hold back a discard suggestion (see [The veto: user material inside disposable folders](#the-veto-user-material-inside-disposable-folders)). Each row names the outermost program or disposable folder that holds it, such as `OFFICE11/Meu orcamento casamento.xls` inside `Backup_PC_2004/C/Arquivos de programas/Microsoft Office`, with a link to that folder on the Map. Rescue them before you clean those folders.

- **First while open.** The card comes before every other card while it has open rows, and leads with how many rows it holds rather than their bytes, which are usually small.
- **Deciding the program does not hide your file.** A row stays open until you decide the file itself, or until it is kept. Discarding `Microsoft Office`, or marking it later, leaves the spreadsheet on the card, reading the discard it inherits; keeping `Microsoft Office` keeps the file and closes its row. To close a row, keep the file or give it its own decision. Setting the file to Undecided on its own leaves it open.
- **Selecting the whole list** keeps or decides every open row at once, like any other card.

Whether a file has **no other copy** is not a card. The detail panel's **Copies** says it for any file, together with the share of the content that could have a copy that was checked (see [The detail panel](#the-detail-panel)), and Search's duplicate filter (`dup=unique`, **No other copy**) lists such files by category, folder, or type (see [Search parameters](#search-parameters)). "No other copy" means the file is unique by size, its sample is distinct among files of its size, or it was hashed and found once (hard links count once). A file not checked yet, or one that could not be read, is never said to have no other copy; it waits until hashing checks it. Archives Precious does not open (7z, rar, and archives over budget) count as plain files, so a copy inside one is not seen.

## Search, viewer, and read API

The interface reads the index through a small JSON API under `/api`. The same endpoints serve scripts, for example to export a search. Every endpoint below needs a signed-in session, like the interface; without one it answers `401 unauthenticated`. None of them change anything, and none of them accept a path: an entry is named only by its ID, which comes from an earlier answer. Entry IDs are decimal strings, such as `"812"`, and tag IDs are numbers. Treat both as opaque.

### Read endpoints

| Endpoint | Answers |
|---|---|
| `GET /api/home`, `GET /api/home?source=ID` | The figures of Home for every source, or for one: totals, bytes and files by family, by file kind, and by year, the decision totals, whether the figures are partial, and the scans in progress. |
| `GET /api/entries/{id}` | One entry with the folders above it, its classification with the explanation of each rule, its own and effective decision and tags with where they come from, and, for a folder, its counts, its breakdowns by kind and by year, and its notable entries inside (`stats.inside`). |
| `GET /api/entries/{id}/children` | A folder's items, one page at a time, in every state (present, missing, unreadable); with `kind=directory`, only its folders, archives left out. |
| `GET /api/entries/{id}/treemap` | A folder's 300 largest items by bytes, and the count and bytes of the rest as one `other` area. Missing items take no space, so they appear in neither. |
| `GET /api/search?…` | One page of search results; with `count=only`, the match count instead. See [Search parameters](#search-parameters). |
| `GET /api/tags` | Every tag with the number of entries carrying it as their own. |
| `GET /api/entries/{id}/content`, `GET /api/entries/{id}/text` | A file's content, and its text decoded. See [Viewer safety](#viewer-safety). |
| `GET /api/history`, `GET /api/history/{id}`, `GET /api/history/{id}/items` | The changes Precious made or planned on the disks, and their items. See [Organizing](#organizing). |

Every entry row carries its name and path twice: `name` and `path` are the escaped display form, and `name_b64` and `path_b64` are the exact bytes on disk in base64. A name that is not valid UTF-8 is therefore never lost. For example, a Latin-1 `fé.txt` shows as `f\xE9.txt`, and its raw bytes are `ZukudHh0`. Times are in UTC, in RFC 3339 form, or `null` when unknown.

Every entry row also carries `composition`, its bytes and files by family as a list such as `[{"family":"personal","bytes":400000000000,"files":7},{"family":"programs","bytes":6000000000,"files":5}]`: a folder's composition, or for a file one element under its family. Families with nothing in them are left out. See [What a folder is made of](#what-a-folder-is-made-of).

The detail of `GET /api/entries/{id}` also helps the Map shorten paths and explain archives. Each folder in `ancestors` has `only_child`, true when it holds nothing but the next one (the entry itself for its parent). `only_folder` is the ID of a folder's only item when that item is a folder, and `null` otherwise. `archive_note` says why an archive file has no `archive`: `unsupported` for a format Precious recognizes but does not open (7z, rar, xz, cab, jar, and the like), `nested` for an archive inside an archive, `not_listed` for a format it opens that was not listed yet, and `null` for anything else. Items in every state count, as in the children list.

Children are sorted with `sort=bytes`, `files`, `newest` (the newest change inside a folder), or `name`, and with `order=desc` or `asc`. By default the sort is by bytes, largest first. A sort by name defaults to ascending and compares the raw bytes of the names, so `Zeta` comes before `alfa`. `kind=directory` lists only the folders, in every state, sorted and paged the same way; an archive is a file, so it is left out, even one Precious can open as a folder, and inside such an archive only the member folders are listed. It is the only `kind` accepted. A page holds 200 rows unless `limit` asks for another number, and never more than 1,000. A page with more after it carries `next_cursor`, and the same request with `cursor=` set to it gives the next page. A cursor belongs to the folder's order and `kind`: changing `sort`, `order`, or `kind` needs a new first page. A cursor holds the position of the last row, not a row count. So a row added or removed while you page does not shift the other rows: a new row is listed only when it sorts after the current page. A row whose size or date a scan changes may move to a page already read.

Errors use the usual envelope, `{"error":{"code","message"}}`:

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | An unknown or repeated parameter, a bad value (such as `sort=color`, `limit=0`, or a `kind` other than `directory`), or a cursor the server did not give for this order and `kind`. |
| 404 | `not_found` | An entry, source, or folder (`within`) that does not exist, or an ID that is not a valid one. |
| 409 | `invalid_entry_state` | The content or text of a folder, of a missing file, or of a file that changed on disk since the last scan. |
| 409 | `source_offline` | The content or text of a file whose source is not online. |

### Search parameters

`GET /api/search` takes the filters below as URL parameters. The Search screen keeps the same parameters in its address, so the address of a search on screen is also the API request. Filters combine with AND. A parameter marked "repeats" can be given several times, such as `ext=jpg&ext=png`, and then matches any of its values. An empty value is the same as leaving the parameter out.

| Parameter | Matches |
|---|---|
| `source=ID` | Entries of one source. |
| `name=TEXT` | Names containing the text, ignoring letter case, matched against the displayed name. A text of three characters or more also ignores accents: `confraternizacao` finds `Confraternização 2018`. A shorter one matches its accented letters only as typed (`ão` finds `Leilão`, `ao` does not). |
| `ext=X` (repeats) | Files with the extension, with or without its dot, ignoring letter case. |
| `file_kind=K` (repeats) | `image`, `video`, `audio`, `document`, `source`, `archive`, `installer`, `executable`, `system`, or `other`. |
| `min_size=N`, `max_size=N` | Size in bytes, both limits included. A folder's size is everything inside it. |
| `year_from=Y`, `year_to=Y` | Year of the last change, in UTC, both years included. |
| `category=C` (repeats) | One of the 16 [categories](#categories-and-families). |
| `triage=T` (repeats) | `keep`, `discard`, or `review`. |
| `decision=D` (repeats) | The effective decision: `undecided`, `keep`, `discard`, or `later`, set on the entry or followed from a folder above it. |
| `tag=ID` (repeats) | Entries carrying the tag, and everything inside them. |
| `within=ID` | Everything inside the folder, not the folder itself. |
| `state=unreadable` | The folders and files that could not be read, which make Home's figures partial. It is the only state to search for. |
| `sort`, `order` | As for children; by default by bytes, largest first. |
| `cursor`, `limit` | As for children: 200 rows by default, at most 1,000. |
| `count=only` | Answer `{"count":…}`, the number of matches, instead of a page; `cursor` and `limit` are then ignored. |

Speed depends on the filters:

- A `name` of three characters or more is looked up in a name index, and is fast on any index size.
- A `name` of one or two characters cannot use that index. It is tested on each entry that the other filters select, so on its own it reads every entry, which takes seconds on millions of entries. Add a `within`, `tag`, `source`, or `decision` filter to narrow it.
- `within`, `tag`, `decision`, `dup`, and `state=unreadable` also use indexes. Extension, file kind, size, year, category, and triage do not: on their own they read every entry of the source, or of every source.
- On 2 million entries, the first page by bytes (the default order) arrives within a second, and its count within two, with no filter, with any `dup` value, and with or without a source. Other orders on a broad search sort every match, which takes longer.

Results include entries in every state, and each row says whether it is present, missing, or unreadable. A page does not carry the number of matches: ask for it with the same parameters and `count=only`, which answers `{"count":1234}`, exact up to 10,000, and `{"count":"10000+"}` beyond that. The Search screen sends both requests at once and shows the results as soon as they arrive, with "Counting…" until the count follows. A search that lists more than you need is best narrowed rather than paged to the end. "Select all results" works on up to 1,000,000 matches; see [Selecting all the results of a search](#selecting-all-the-results-of-a-search).

### Viewer safety

Files on a source come from anywhere, so the viewer treats each one as untrusted content and never lets it run inside Precious:

- **Precious decides the type** from the file's extension, using its own short table, and never from the file's content. Every viewer answer, errors included, carries `X-Content-Type-Options: nosniff`, so the browser does not guess either, and `Cross-Origin-Resource-Policy: same-origin`, so other sites cannot embed the files.

  | Extensions | Served as |
  |---|---|
  | `jpg`, `jpeg`, `jpe`, `png`, `gif`, `webp`, `avif`, `bmp` | the image type |
  | `svg` | `image/svg+xml` |
  | `mp4`, `m4v`, `webm`, `mov` | the video type (`m4v` as `video/mp4`) |
  | `mp3`, `m4a`, `aac`, `ogg`, `opus`, `wav`, `flac` | the audio type |
  | `pdf` | `application/pdf` |
  | anything else, including `html`, `htm`, `xml`, scripts, and executables | `application/octet-stream`, as a download (`Content-Disposition: attachment`) |

- **Everything runs in a sandbox.** Images, video, audio, and downloads are sent with `Content-Security-Policy: sandbox; default-src 'none'`: no script runs and nothing else is loaded. An SVG gets the same policy and may keep only its inline styles, so a script inside it never runs. A PDF is not sandboxed, because Chrome refuses to show a PDF in a sandbox. It gets `default-src 'none'; frame-ancestors 'self'` instead, so it loads nothing and only Precious can frame it.
- **HTML and XML are never pages.** Their content is served only as a download. The interface shows them through the text endpoint, as text.
- **Text** (`/text`) is the first 1 MiB of the file, decoded: a byte-order mark gives UTF-8 or UTF-16, then valid UTF-8 is read as UTF-8, and anything else as Windows-1252. The answer names the encoding, says whether the text was cut at 1 MiB, and gives a syntax hint from the extension and whether the file is Markdown. The interface cleans Markdown before showing it; see [The viewer](#the-viewer).
- **Only the indexed file is read.** The viewer opens the file read-only, starting from the source's folder and going down one name at a time. It never follows a symbolic link and never crosses into another mounted filesystem. It reads only a regular file that still matches the index: the same size and modification time (within the filesystem's time resolution, or one hour off on FAT) and, where the filesystem has stable file numbers, the same inode. A file changed, replaced, or moved since the last scan is refused with `invalid_entry_state` until a rescan. Content answers range requests, so video and audio can seek.
- **Offline sources** stay browsable, but their files cannot be viewed: content and text answer `source_offline` until the disk is connected again.

### Content, duplicates, and archive endpoints

What hashing learns (see [Hashing](#hashing), [Duplicates and Compare](#duplicates-and-compare), and [Opportunities](#opportunities)) is read through the same API. Like the rest, these endpoints need a session, change nothing, and name entries only by ID.

**Archive members.** A member of an archive Precious read completely is named `m` followed by its number, such as `"m45"`. Every endpoint below `/api/entries/` accepts it where it accepts an entry ID. A member row has `"id":"m45"`, `"archive_id"` (the archive's entry ID), the path `archive path!path inside`, such as `Downloads/fotos.zip!Carnaval/DSC01001.JPG`, `"decision":null`, `"tag_ids":[]`, and the archive's effective decision: a member has no decision or tags of its own and follows its archive. `set-decision` and `set-tags` naming a member answer `invalid_request`; decide the archive instead. Members cannot be searched by name.

**New fields of every entry row** (children, treemap, search, and the detail), each `null` where it does not apply:

| Field | Of | Meaning |
|---|---|---|
| `content_state` | files and file members | `unique_size` (no other file of that size), `pending` (not read yet), `sampled` (unique by its samples), `hashed`, `changed` (changed while read), or `unreadable` |
| `copies` | files and file members | how many physical copies the content has, this one included: hard links of one file count once, and so do a tar hard link and its target; `1` for a unique size or sample; `null` while not checked |
| `candidate_bytes`, `checked_bytes`, `duplicated_bytes` | folders and member folders | the bytes inside that could have a copy, those checked, and those with another copy anywhere (computed by the last relations pass, and on read for a member folder) |
| `archive_state` | archive files | the archive's [outcome](#outcomes), `null` while it was never listed or is being listed |
| `archive_id` | members | the archive's entry ID |

**Endpoints.**

| Endpoint | Answers |
|---|---|
| `GET /api/entries/{id}` | Also `content` for a file or file member: its state, its SHA-256 in hex once read in full, when it was last read, up to 20 other copies, and their count; `relations`: up to 20 [relations](#folder-relations) of a folder, archive, or member folder, each with `self` (`a` or `b`, which side this entry is), the `other` side's row, the matched and redundant bytes, and the files and bytes only here and only there; `archive` for an archive file: its format, outcome, detail, members, and unpacked bytes; and `coverage`, the share checked over every source. A member's `ancestors` run from the source root through the archive to the member folder above it. |
| `GET /api/entries/{id}/copies?cursor=&limit=` | Every other copy of a file or file member, files first, 100 per page by default and at most 1,000, with their count. A copy names its source, path, archive (for a member), whether it is a hard link of the same file, whether its source is offline, and its effective decision. |
| `GET /api/entries/{id}/children`, `GET /api/entries/{id}/treemap` | Also for an archive read completely, whose items are its top members, and for a member folder. They sort and page as for folders; a member file has no items. |
| `GET /api/home` | Also `coverage` (of the chosen source, or of all), `cards` (the eight opportunity cards, in the order of `GET /api/opportunities`), and `hashing` (hashing jobs in progress, like the scans, with their kind and progress). |
| `GET /api/opportunities?source=` | The eight [cards](#the-cards), `rescue` first while it has open rows, then largest first, with the bytes and rows still open (`bytes`, `rows`) and those of the rows no longer open (`decided_bytes`, `decided_rows`), the coverage of every source, and when the lists were last computed (`computed_at`, `null` before the first pass). |
| `GET /api/opportunities/{list}?source=&decided=&cursor=&limit=` | One page of a card's open rows (50 by default, at most 500), largest first, with the card; `decided=1` lists the rows no longer open. A row of a rules card has its entry; a `rescue` row also has its `group`, the row of the outermost program or disposable group holding it (`null` on every other list). A duplicates row is either a relation, whose `entry` is one side and whose `relation.other` is the other, or a group of copies of one file, which lists its `copies` (up to 101). Each row has the `summary` the interface writes its line from: category, oldest and newest year, files, bytes, and up to two signals. An unknown list answers `404 not_found`. |
| `GET /api/compare?left=&right=&bucket=&cursor=&limit=` | Both sides' rows, the files and bytes of the five [Compare](#compare) groups (`only_left`, `only_right`, `identical`, `different`, `unchecked`), and one page of a group's items (100 by default, at most 1,000), with the group listed in `bucket`: the one asked for, or without `bucket` the first holding files in the order Compare opens on. Each item has its `path`, each side's path inside that side (`left_path`, `right_path`, `null` for a side without the file), its row on each side, and for an extra copy `twin`: the `path` and `entry` row of the file on the other side holding the same content (else `null`). A side is a folder, an archive read completely, or a member folder. A file, two sides of which one holds the other, an unknown group, or a malformed ID answers `invalid_request`; a side that does not exist answers `not_found`. |
| `GET /api/relations?kind=overlap&source=&cursor=&limit=` | One page of the [similar folders](#similar-folders) (50 by default, at most 500): the `overlap` relations of the last relate pass with a side on the source, by bytes in common, then ID, largest first. Each item is a relation as in `GET /api/entries/{id}`, seen from side A (`self` `a`), with side A's row in `a` beside `other`. A missing or other `kind`, or a bad cursor, answers `invalid_request`; an unknown source answers `not_found`. |

**Every claim of no other copy carries the share checked.** A file reads "no other copy" only when its state is `unique_size` or `sampled`, or when it is `hashed` with `copies` 1. A copy can be on any source, so the claim always comes with `coverage` over every source: when 80% of the candidate bytes are checked, a file whose copy sits among the other 20% still reads unique. Archives Precious does not open (7z, rar, and partial ones) count as plain files: a file inside one is never seen as a copy.

**Search's `dup` filter** (repeats) matches present files by what hashing found:

| Value | Matches |
|---|---|
| `dup=copies` | Files with another physical copy anywhere, members of archives included. |
| `dup=elsewhere` | Files with a copy outside the `within` folder: on another source, outside the folder's paths, or in an archive outside it. It needs `within`, otherwise `invalid_request`. With `within` set to a source's top folder it means a copy on another source. |
| `dup=unique` | Files with no other copy: a unique size, a unique sample, or hashed with one copy. |
| `dup=unchecked` | Files not checked yet, changed while read, or unreadable. |

The filter uses the index of each file's content state (`unique`, `unchecked`, and pages by bytes) or of the contents with more than one copy (`copies`, `elsewhere`), and tests the exact copies of those files only. A selection stores the filter like the others, so "Select all results" on, for example, `within=ID&dup=elsewhere` selects the copies of that folder that have another copy outside it, ready to be discarded in one confirmed change.

**Viewing a member.** `/content` and `/text` serve a file member under the [viewer's rules](#viewer-safety): the type comes from the member's own name, the same sandbox applies, and HTML is only a download. The member is read from the archive in memory: nothing is written to the state directory, to `TMPDIR`, or to the source. The archive file must still match the index and its listing, else `409 invalid_entry_state` until a rescan; its source must be online, else `source_offline`. A member stored without compression in a zip, and a compressed one up to `archives.view_max_bytes`, answers range requests; a larger one, and every member of a tar-family or gzip archive, is streamed whole without ranges (see [Viewing members](#viewing-members)). A member folder answers `invalid_entry_state`, like a folder.

## Decisions and tags

Decisions and tags are yours: no scan, rule, or job ever sets or changes them. A triage suggestion of discard is only a suggestion until you decide.

### Decisions

Every file and folder has a decision: **undecided** (the default), **keep**, **discard**, or **later**. A decision on a folder applies to everything inside it, except where something inside has a decision of its own. So each entry shows two things:

- its **own** decision, which is none when you never decided it;
- its **effective** decision: its own one when it has one, otherwise that of the nearest folder above it that has one, otherwise undecided. The detail panel names the folder the effective decision comes from.

For example, with `Fotos` kept and `Fotos/2006/rejeitadas` discarded, `Fotos/2006` reads keep (from `Fotos`) and `Fotos/2006/rejeitadas/IMG_0001.JPG` reads discard (from `Fotos/2006/rejeitadas`).

**Inherit** removes an entry's own decision. The entry, and everything inside it without a decision of its own, then reads the decision of the nearest decided folder above it, or undecided.

Decisions are stored with each entry and take effect at once: Home's decided and undecided bytes (present files only), the decision filter in Search, and every entry read reflect them as soon as the change is accepted. Discarding a folder of half a million entries updates them all in one change, which takes a few seconds; a scan running at the same time waits for it.

Decisions survive rescans. An entry that goes missing and comes back at the same path keeps its decision. A file a rescan finds for the first time has no decision of its own and reads its folder's, so a new file inside a discarded folder reads discard.

### Keep is the protection

A keep protects an entry, and everything inside a kept folder, from changes made in bulk:

- **One entry at a time** (the detail panel, or a request naming one `entry_id`), you can set any decision, including changing a keep, explicit or inherited.
- **In bulk** (several selected entries, or all the results of a search), any decision other than keep skips every entry whose effective decision is keep, explicit or inherited, and applies to the rest. Inherit in bulk skips them too, so it never clears a keep. The result says how many entries were changed and how many were skipped, and lists up to 100 of the skipped ones with their paths. Keep in bulk applies to every entry.

Discarding a folder that holds a kept entry discards the folder and leaves the kept entry, and everything inside it, kept. Cleanup plans (a later release) never remove a kept entry.

A bulk request is checked against the decisions in force when it is applied: a keep set a moment before it, on an entry or on a folder above it, is honored.

### Tags

Tags are your own labels, such as `familia`, `livro`, or `scan`. A name has 1 to 64 characters (spaces around it are trimmed), and no two tags share a name ignoring letter case: `Familia` is refused while `familia` exists, and so is `FAMÍLIA` while `Família` exists. Renaming a tag keeps it on every entry that carries it; deleting a tag removes it from every entry.

A tag on a folder applies to everything inside it. Each entry shows its **own** tags and the tags it **inherits**, each inherited tag naming the nearest folder that carries it. Searching by a tag finds the entries that carry it and everything inside them.

Removing a tag from entries removes only their own tags. An inherited tag stays until you remove it from the folder that carries it; to have it on only part of a folder, tag the subfolders instead.

### Selecting all the results of a search

"Select all results" turns the search into a fixed list of entries at that moment, up to 1,000,000 of them; a broader search is refused, so a selection is never a silent part of the results. Before you apply a decision or tags to it, the confirmation shows how many entries it holds, their bytes, and how many of them, with their bytes, are kept: a bulk decision other than keep will skip those. A folder's bytes include everything inside it, and bytes are counted once: a file selected together with its folder adds nothing more.

Files a scan adds afterwards never join the selection. A selection can be used for one hour; after that a request naming it is refused with `selection_expired` and changes nothing, and you select again. Expired selections are cleared from the database when the next selection is made; a day after expiry one is forgotten entirely, and naming it answers `not_found`.

### Commands

Decisions and tags change through the command API, `POST /api/commands/{name}` with an `Idempotency-Key` header, like every other command. Entry IDs are strings, tag IDs numbers.

| Command | Request | Response |
|---|---|---|
| `set-decision` | `{"entry_id":"12","decision":"keep"}` for one entry; `{"entry_ids":["12","13"],"decision":"discard"}` (1 to 1,000 IDs) or `{"selection_id":"…","decision":"later"}` in bulk. `"decision":"inherit"` removes the own decision. | `{"applied":n,"skipped_count":k,"skipped":[{"entry_id","path","path_b64"}]}` |
| `set-tags` | `{"entry_ids":[…]}` or `{"selection_id":"…"}`, with `"add":[tag IDs]` and `"remove":[tag IDs]` | `{"applied":n}` |
| `create-tag`, `rename-tag`, `delete-tag` | `{"name":"familia"}`, `{"tag_id":3,"name":"família"}`, `{"tag_id":3}` | `{"tag":{"id","name"}}` |
| `create-selection` | `{"query":{…}}`, with the parameters of a search | `{"selection_id","count","bytes","kept":{"count","bytes"},"expires_at"}` |

A request naming both one entry and several, or neither, or more than 1,000 IDs, is refused with `invalid_request`. A request naming any unknown entry, tag, or selection is refused whole with `not_found`, and a duplicate tag name with `tag_exists`. A refused request changes nothing.

Each accepted request writes one audit event (`decision_set`, `tags_set`, `tag_created`, `tag_renamed`, or `tag_deleted`) with the time, the client address, the entries or selection it named with the counts of changed and skipped entries, and the old and new values.

## Changing disks

Precious changes a disk only to organize it, when you ask: it moves and renames files and folders, creates folders, and removes an empty folder that one of its own changes created, when you undo that change. It never deletes a file, never writes into one, and never changes a file's times or permissions. Every change is listed in [History](#history). How the index follows a move, without a rescan, is described under [Scanning and the index](#scanning-and-the-index).

### Organizing

Every change Precious makes on a disk is an **action** that you plan first and then run. Planning reads only the index, never the disk, and changes nothing anywhere: it lists every step the action would take, its **items**, each with the path before and after. Running it queues an `organize` job that does exactly the steps that were planned, in order (see [How Precious changes a disk](#how-precious-changes-a-disk)). A plan can be run for one hour; after that it reads `expired`, running it is refused with `action_expired`, and you plan again. Expired plans are deleted a day later.

**The actions:**

- **Move** (`plan-move`) moves one entry, several ticked entries (up to 1,000), or all the results of a selection into one folder, under their names. An entry inside another entry of the same move goes with it, as one item. An action holds at most 10,000 items; a larger one is refused with `invalid_request`, so move a folder instead of its contents.
- **Rename** (`plan-rename`) gives one entry a new name in its folder. **New folder** (`plan-create-folder`) creates an empty folder. When the name is already taken in that folder, by an entry the last scan saw or by a missing one that carries your decision, tags, or category, both are refused at once with `name_taken` and no action is made. A name is refused with `invalid_request` when it is empty, `.` or `..`, holds `/` or a NUL character, or is longer than 255 bytes, and so is the current name. On a disk that does not tell letter case apart (FAT, exFAT, NTFS), a rename that changes only the letter case, such as `FOTO.JPG` to `foto.jpg`, is refused too: that disk sees both as the same name and cannot make the change in one step. On a FAT, exFAT, or NTFS disk (filesystem type `vfat`, `exfat`, `ntfs3`, `ntfs`, or `fuseblk`), a new name with any of `" * : < > ? \ |`, a control character, or a space or dot at its end is refused with `invalid_request` as well, since that disk cannot hold it: `Recibos: 2023` or `Novo.` there needs another name.
- **Rescue kept items** (`plan-rescue`) moves the items kept on their own inside a folder into another folder, so the folder can be discarded: only the outermost ones (a kept folder goes with everything in it), keeping their names, all into the chosen folder. A folder that is itself kept has nothing to rescue and is refused with `invalid_entry_state`, as is one with nothing kept on its own inside; a destination inside the folder is refused with `invalid_request`.
- **Merge** (`plan-merge`) moves the files Compare lists as only on one side into the other folder, each to the same place it has on its side: `2006/Praia/DSC_editada.JPG` only in `Fotos - Copia` goes to `Fotos/2006/Praia/DSC_editada.JPG`. When Compare left out a single wrapper folder on the receiving side, such as `Fotos (copia)/Fotos/` against `Fotos/`, the files go inside that wrapper. Each folder missing on the way is created first, as its own item, but only when at least one file is planned to go into it or below it: a folder whose files are all refused or in conflict is not created. Files with the same name and different content, and files not checked yet, are never moved. Both sides must be folders of the same source; an archive, or a folder inside one, is refused with `invalid_request`, since its files cannot be moved.
- **Undo** (`plan-undo`) is described below.

**What a plan leaves out.** An item that cannot be done is planned as **refused** (not included) or **conflict** (left as it is), with its reason, and the rest of the action still runs. Refused:

| Reason | Meaning |
|---|---|
| `other_source` | The entry is on another source than the destination. Moves stay inside one source. |
| `inside_archive` | The entry is a member of an archive. |
| `missing` | The last scan did not find the entry. |
| `source_root` | The entry is a source's top folder. |
| `into_itself` | A folder would go into itself, or into a folder inside it. |
| `already_there` | The entry is already in the destination under that name. |
| `other_filesystem` | The entry is where another filesystem is mounted, or the destination is on another filesystem. |
| `contains_mount` | Another filesystem is mounted somewhere inside the folder; moving it would leave that mount behind. |
| `would_lose_keep` | A move of many entries would take away an entry's keep (see below). |

In conflict:

| Reason | Meaning |
|---|---|
| `name_taken` | An entry with that name is in the destination. On a disk that does not tell letter case apart, `Foto.jpg` and `foto.jpg` are the same name. For a merge, it also marks the files below a place where a folder is needed and a file is. |
| `name_taken_in_plan` | An earlier item of the same action takes that name. |
| `name_taken_by_missing` | A missing entry with that name carries your decision, tags, or category. Precious never lets a move take that place, so your intent is never lost; a missing entry without any gives way. |
| `previous_folder_gone` | (Undo) the folder the item came from is no longer there. |

A name taken on the disk but not yet indexed is found when the step runs: the item then ends `conflict`, and nothing is replaced. Items that changed between the plan and the run end `changed`, so a plan is only ever a preview of what the index shows.

**Decisions follow the place, not the move.** A move never changes a decision. An entry with a decision of its own keeps it; one without takes the decision of its new folder, which the plan shows for each item as `decision_after`. A move of one entry, a rename, and an undo may take away a keep the entry had through its folder; the plan counts those items in `kept_lost`, and the interface warns "N kept items would no longer be kept" before it runs. A move of many entries (ticked entries, a selection, a rescue, or a merge) never takes away a keep: such an item is refused with `would_lose_keep`, and if the destination's decision changes before the item runs, it ends `changed` with that reason.

**Undo.** Every action that ran can be undone from History while some of its done items are not undone yet. `plan-undo` plans the reverse of those items, last first: each moved or renamed entry goes from wherever it is now back to its previous folder and name, and each folder the action created is removed, if it is still empty (an `rmdir` item, which ends `not_empty` otherwise). An item whose previous name is taken now is a conflict (`name_taken`), as is one whose previous folder is gone (`previous_folder_gone`); planned again with `destination_id`, those items go into that folder under their previous names, still in conflict if taken there too. An item counts as undone only once its undo step is done, so an undo that stopped early, was cancelled, or expired leaves the rest undoable, and an undo planned twice does each item once (the second ends `changed`, `already_undone`). An undo is itself an action, which can be undone in turn. A move of many entries is one action and is undone as a whole.

**Run and cancel.** `run-action` queues a planned action. It is refused with `action_expired` after the hour, with `action_not_runnable` when the action is not planned any more or has no item to run, and with the source's own refusals. `cancel-action` stops an action that is waiting or running: a waiting one stops at once and none of its items runs; a running one stops after the step in progress, which is confirmed and recorded. Its items not yet attempted end `not_attempted`, and a stopped action never runs later.

**What every plan and run checks.** The source must allow changes now: refused with `source_offline` while its disk is not connected, `writes_unavailable` while changes cannot be allowed (see [Changes by Precious](#changes-by-precious)), and `writes_disabled` while its **Changes by Precious** is off. While one of the source's items needs your check (see [Recovery after an interruption](#recovery-after-an-interruption)), nothing can be planned or run on it: `recovery_needed`. Resolve it with `resolve-recovery`, which marks the item resolved and starts a scan of the source, so the index shows what you left on the disk; the source must be online for that scan.

**Commands** (`POST /api/commands/{name}` with an `Idempotency-Key`; IDs are strings):

| Command | Request | Response |
|---|---|---|
| `plan-move` | exactly one of `{"entry_id":"12"}`, `{"entry_ids":["12","13"]}` (1 to 1,000), or `{"selection_id":"…"}`, with `"destination_id":"40"` | 201 `{"action","items","next_cursor"}` |
| `plan-rename` | `{"entry_id":"12","name":"curriculo 2005.doc"}` | 201, as above |
| `plan-create-folder` | `{"parent_id":"40","name":"2006"}` | 201, as above |
| `plan-rescue` | `{"folder_id":"30","destination_id":"40"}` | 201, as above |
| `plan-merge` | `{"left_id":"50","right_id":"51","from":"right"}`: the files only on the `from` side go into the other one | 201, as above |
| `plan-undo` | `{"action_id":"7"}`, optionally with `"destination_id":"40"` | 201, as above |
| `run-action` | `{"action_id":"8"}` | 202 `{"action","job_id","state"}` |
| `cancel-action` | `{"action_id":"8"}` | 200 `{"action"}` |
| `resolve-recovery` | `{"item_id":"77"}` | 200 `{"action","scan":{"job_id","coalesced"}}` |

A plan answers with the action and the first 200 of its items; `next_cursor` continues them through `GET /api/history/{id}/items`. A plan of one entry is individual; one of `entry_ids` or of a selection is bulk (`"bulk": true`), as are rescues and merges. An unknown entry, folder, action, item, or selection is `not_found`; a destination or parent that is not a folder the last scan saw (a file, an archive, a missing folder, an archive member) is `invalid_request`. `run-action`, `cancel-action`, and `resolve-recovery` each write an audit event (`action_run`, `action_cancelled`, `recovery_resolved`) with the action, its kind and source, and the job; plans write none, since they change nothing.

**Read endpoints** (each needs a session, like every endpoint):

| Endpoint | Answers |
|---|---|
| `GET /api/history?source=&cursor=&limit=` | The actions that ran (waiting, running, done, or stopped), newest first, 50 per page by default and at most 200: `{"items":[Action],"next_cursor"}`. An unknown `source` is `not_found`. |
| `GET /api/history/{id}` | One action in any state, planned and expired ones included. |
| `GET /api/history/{id}/items?state=&cursor=&limit=` | The action's items in order, 200 per page by default and at most 1,000; `state` repeats, such as `state=manual_recovery` or `state=done&state=conflict`. |

An **action** has its `id`, `kind` (`move`, `rename`, `create_folder`, `rescue`, `merge`, or `undo`), `source_id`, `state` (`planned`, `queued`, `running`, `done`, `stopped`, or `expired`), `created_at`, `expires_at`, `started_at`, and `finished_at`, its `destination` as an entry row (`null` for a rename or an undo to the previous places), `job_id`, `undo_of` (the action an undo reverses), `bulk`, `counts` (its items in each state, every state listed), `bytes` and `files` (of the items planned, under way, or done), `kept_lost`, `reversed` (its items undone), and `undo`: `{"possible":true,"reason":null}`, or `possible` false with `not_done` (it did not run), `nothing_done` (no item was done), or `already_undone`.

An **item** has its `id`, `seq`, `op` (`rename` for a move or rename, `mkdir`, or `rmdir`), `entry` (the entry's row as it is now, or `null`), `from` and `to` (`{"path","path_b64"}` or `null`), `state`, `reason` (from the tables above), `decision_after`, `detail` (the system's message of a `failed` item), `found` (for an item that needs your check, what was at each name: `{"from","to"}`, each `absent`, `same`, or `other`), `reversed`, `bytes`, and `files`. Item states are `planned`, `refused`, `conflict`, `intent` (started and not yet confirmed), `done`, `not_permitted`, `offline`, `changed`, `failed`, `no_safe_rename`, `not_empty`, `manual_recovery`, `not_attempted`, and `resolved`.

**Errors** of the commands above, besides `invalid_request` and `not_found`:

| Status | Code | When |
|---|---|---|
| 409 | `name_taken` | A rename or new folder whose name is taken in its folder. |
| 409 | `action_expired` | Running a plan made over an hour ago. |
| 409 | `action_not_runnable` | Running an action that is not planned, or has nothing to run; cancelling one that is not waiting or running. |
| 409 | `action_not_undoable` | Undoing an action that did not run, or has nothing left to undo. |
| 409 | `recovery_needed` | Planning or running on a source with an item that needs your check. |
| 409 | `writes_disabled` | The source's **Changes by Precious** is off. |
| 409 | `writes_unavailable` | Changes cannot be allowed on the source (see [Changes by Precious](#changes-by-precious)). |
| 409 | `source_offline` | The source's disk is not connected. |
| 409 | `selection_expired` | A selection older than an hour. |
| 409 | `invalid_entry_state` | A rescue of a kept folder or of one with nothing kept inside; resolving an item that does not need your check. |

### How Precious changes a disk

- **Only on sources you allowed.** A source changes only while its **Changes by Precious** is on (off for every new source), the configuration allows it (`sources.allow_writes`), the disk is online and mounted writable, and its filesystem has the no-replace rename (see [Filesystem capabilities](#filesystem-capabilities)). Precious checks all of these again just before each step, in the same database transaction that records the step, so turning changes off stops a change before its next step: that item ends `not_permitted` (`offline` when the disk went away), and the rest of the change is not attempted.
- **Never replaces a file.** Every move and rename uses the filesystem's no-replace rename (`renameat2` with `RENAME_NOREPLACE` on Linux). When the new name is taken, even by something that appeared after the preview, the item ends `conflict` (Name taken), nothing is replaced, and the change goes on with its next item. Should the filesystem refuse the flag at run time (old OpenZFS releases do), the item ends `no_safe_rename`, nothing moves, the rest is not attempted, and Precious turns that source's **Changes by Precious** off, with a `source_writes_set` audit event whose actor is `system` and whose reason is `no_replace_rename`.
- **Checked before each step.** Precious goes down from the source's top folder one name at a time, never by path: it never follows a link and never crosses into another mounted filesystem, and each folder must still be the one the index holds. The item itself must still be what the last scan saw: the same kind, the same size and modification time for anything but a folder (within the filesystem's time resolution), and the same device and inode where the filesystem keeps them stable. Anything else ends the item `changed` (Changed on disk since the last scan). An item that is itself a mount point, or the two folders on different filesystems, ends `refused` with `other_filesystem`, and a folder with a mount point inside it with `contains_mount`.
- **Recorded first, then done, flushed, and confirmed.** Before each step Precious records what it is about to do: both folders and names, and the identity it expects. Only then does it rename, create, or remove. Afterwards it flushes every folder the step changed to the disk (`fsync`), looks at both names again, and records the result together with the index update in one transaction. When the index cannot be updated, the item ends `manual_recovery` (Needs your check) instead of staying half recorded, and the change stops. When a flush fails, the item stays recorded as started, the change stops, and a check like the one after an interruption (below) decides it.
- **Errors.** Permission denied ends the item `failed` with the system's message, and the change goes on. So does "invalid argument" on a rename on any filesystem but ZFS: the disk refused the name, and changes stay on. A filesystem that became read-only ends it `failed` and stops the change; a folder to remove that is not empty ends it `not_empty`. Any other error (an I/O error, for example) is looked at as after an interruption: `failed` when the step clearly did not happen, done when it clearly did, `manual_recovery` otherwise, which stops the change.
- **One at a time.** The changes of one source run one at a time, oldest first, each in the order of its items, as an `organize` job whose progress counts `items` and `done`.
- **Changes wait for scans, and scans for changes.** A change waits while a scan of its source is running, deferring by one second, and runs when the scan ends. A scan waits while a change of its source is waiting or running, or one of its steps is recorded as started and not yet confirmed. Hashing goes on meanwhile: a result it read through a path that has since moved is dropped, and the file is read again.
- **Cancel.** Cancelling a change in History, or cancelling its job, stops a waiting change at once, and a running one after the step in progress, which is confirmed and recorded. The rest of the change is not attempted and never runs later.

### Recovery after an interruption

When Precious stops between recording a step and recording its result (a crash, a power cut, or the service stopped at the wrong moment), the step is checked before anything else changes on that source. At the next start Precious queues that check for every source that needs it, without touching a disk; every change of the source also checks first. The check runs only while no scan of the source is running, and it only looks at the two names:

- the item still at its old name with the identity recorded, and the new name free: the step did not happen, and it runs once (or ends `not_attempted` when its change was stopped meanwhile);
- the item at its new name with that identity, and the old name free: the step happened, and Precious records it and updates the index without renaming again;
- anything else, such as both names taken or neither: the item ends `manual_recovery`, **Needs your check**, with what was found at each name (`absent`, `same`, or `other`).

While a source has an item that needs your check, no change can be planned or run on it. Put things right on the disk, then choose **I fixed it** in History: Precious marks the item resolved and scans the source again. A source that is offline keeps its unchecked step until it is back, and its scans wait until then.

### Allowing changes in the deployment

The shipped deployments keep every disk read-only at the operating-system level: the systemd unit lists the disks in `ReadOnlyPaths=`, and the Compose file binds them read-only. Precious sees that as a read-only mount, so **Changes by Precious** on those sources reads that the disk is mounted read-only. To allow changes on one source only:

- **systemd:** in the drop-in, move that disk from `ReadOnlyPaths=` to `ReadWritePaths=`; `ProtectSystem=strict` keeps everything else read-only. Give the `precious` account write permission on the folders to organize, through its group, and restart the service:

  ```ini
  [Service]
  ReadOnlyPaths=/media/backup-2003
  ReadWritePaths=/srv/old-disk
  ```

- **Compose:** make only that disk's bind writable (`read_only: false` in its long form), keep the others read-only, and recreate the container. UID 65532 needs write permission on the folders to organize.
- **Host:** mount that filesystem read-write (without `ro`), and leave the block device writable.

Then turn **Changes by Precious** on for that source in Sources. The service's `UMask=0077` does not make new folders private: a folder Precious creates gets its parent's permission bits.

## Interface

Precious is used through a web interface served by `precious serve` at the address in `server.external_origin`. Everything it needs is built into the binary: it loads nothing from the internet and works on a network with no outside access. The top bar links the screens, Home, Map, Search, Opportunities, History, and Sources, and has the Sign out button. Sizes are shown in binary units (KiB, MiB, GiB, where 1 GiB is 1,024 MiB). The interface is in English, the only language in this release, and shows numbers and dates in English formats.

Decisions and tags are recorded in Precious's database only. The interface changes a disk only when you organize it: moving, renaming, or creating folders on a source where you allowed changes (see [The Sources screen](#the-sources-screen)). Precious never replaces a file, and every change is listed in [History](#history), where it can be undone.

### Signing in

Open the address and enter the administrator password, which is set on the server with `precious admin set-password` (see [First run](#first-run)). Until it is set, the sign-in page says so. When a session ends, for example after the password is reset, the next action returns you to the sign-in page. If sign-in fails from another machine, check that the browser's address is exactly `server.external_origin` (see [Plain HTTP on a local network](#plain-http-on-a-local-network)).

### Home

Home answers "how is my disk?" for all sources together, or for the one chosen in the **Source** list at the top:

- **Totals:** the size, files, and folders counted.
- **Scans in progress,** with folders, files, and bytes so far, updating as they run.
- **Decisions:** the bytes and files you have decided to keep, discard, or look at later, and the undecided rest. The four add up to the total. They count files present on the disk at the last scan.
- **Size by category, by file type, and by year of last change.** Categories are grouped into four families, counted from what each folder holds; see [What a folder is made of](#what-a-folder-is-made-of).

When some folder could not be read, Home says its figures are incomplete rather than presenting them as complete.

### The Sources screen

The Sources screen lists each source with its state (online, offline, or unavailable, with the reason), its location and disk, whether the disk will be recognized if it is mounted at another path, what its file system can and cannot record, its totals, its last scan, and its rescan schedule with the next scheduled scan and the last skipped one. The location is the source's folder where its disk is mounted now, such as `/run/media/you/FOTOS/Fotos`; while the disk is not connected it is the folder inside the disk and the disk's label (or identity), such as `Fotos on FOTOS (not connected)`. Each source has these actions:

- **Scan now** reads the disk and updates the index. Progress shows on the source and on Home while it runs. A disk that is not connected cannot be scanned.
- **Open in Map** browses the source, also while it is offline.
- **Change schedule** sets the rescan schedule: off, daily, or weekly on a day, at a time in your browser's time zone. See [Rescan schedule](#rescan-schedule).
- **Rename** changes the label only.
- **Remove** asks first. It forgets the source with its decisions and tag assignments; no file on the disk is changed.
- **Changes by Precious** says whether Precious may change the source. Every source starts with it off, and Precious only reads it. **Allow changes…** asks first, saying that Precious will then move, rename, and create folders on that source only when you ask, never replaces a file, and keeps a history you can undo; nothing changes until you confirm. **Turn off** stops it at once, with no question. Where changes cannot be allowed, the row says why instead of offering the button: the server's configuration forbids changes on every source (`sources.allow_writes = false`), the disk is mounted read-only, or its file system cannot rename without the risk of replacing a file. This is separate from the file system's own **Writable** or **Read-only** line above it.

**Add source** opens the folder picker. It starts at the locations Precious may use and lists folders only; open folders until the one you want is current, optionally give it a name, and add it. Then choose Scan now. See [Adding a source](#adding-a-source), [Allowed roots](#allowed-roots), and [Offline and unavailable sources](#offline-and-unavailable-sources).

### Map

The Map answers "where is my space?" for one folder at a time. The Map link opens the source in the address, or else the source you last chose on Home, Opportunities, or Search, in this browser, or else the first source; with no source yet, it points to the Sources screen. When a source's top folder holds only one folder, which holds only one folder, and so on, the Map opens on the first folder that holds more, and Back leaves the Map. The folder's path is shown above, each part a link back up; a chain of folders that each hold only the next is one part, such as `old-disk/home`, which opens the deepest of them.

- **The treemap** draws each item of the folder as an area sized by its bytes, folders counting everything inside them. It draws the 300 largest items; the rest of a large folder is one gray area labeled with how many items it holds, such as "700 smaller items: see the table". Clicking it sorts the table by size and moves the keyboard focus to it, where every item is listed.
- **The table** lists every item of the folder with its name, size, file count (for folders, everything inside), type or category, the range of modification dates, whether it has copies (**Has copies**, see [Opportunities and Compare](#opportunities-and-compare)), the suggestion of the rules, and the decision. Under each folder's size, a thin bar shows its composition in the family colors; when at least 1% of a folder's bytes belongs to another family, the category also gives the main family's share, such as "Personal media · 98% personal" (see [What a folder is made of](#what-a-folder-is-made-of)). A decision followed from a folder above reads like "Keep (inherited)". Click a column title (Name, Size, Files, Changed) to sort by it, and click it again to reverse the order. Scrolling down loads more rows; a Load more button does the same. In a narrow window the table hides the Changed column first, then Suggestion, Decision, Has copies, and Files, and scrolls sideways inside its frame if it still does not fit.
- **The two follow each other:** pointing at a row outlines its area, and pointing at an area highlights its row.
- **Clicking a folder's area or its name** opens that folder in both. Clicking a file's area or any row opens the [detail panel](#the-detail-panel) for it.
- **Color by** paints the areas by category family, file type, age (time since the last change), decision, whether they have copies, or tag (choose the tag next to it). By family, each area takes the family holding most of its bytes, so a photo no rule recognized is still Personal and valuable. The legend names each color.
- **Search in this folder** opens Search limited to the folder.
- **The keyboard** works in the table, as in the review lists: the up and down arrows move the selected row and open its details, scrolling the table; Enter opens the selected folder, or shows the selected file in the viewer; Escape closes the details. With no row selected, the down arrow selects the first row and the up arrow the last. The keys do nothing while you type in a field, inside a dialog, or in the detail panel, which handles its own keys.

The address keeps the folder, the order, the coloring, and the open details, so it can be bookmarked or reloaded.

### Search

Search finds files and folders anywhere in the index, including inside groups (see [Groups](#groups)). The filters are:

- **Name contains:** part of the name, ignoring letter case, and accents too from three characters on: `confraternizacao` finds `Confraternização 2018`. One or two characters match accented letters only as typed.
- **Extensions:** one or more, separated by spaces, such as `jpg png`.
- **Size:** at least and at most, in B, KiB, MiB, or GiB. A folder's size includes everything inside it.
- **Year of last change:** from and to.
- **File type, Category, Decision, Suggestion, and Tags:** tick any number in each list. Decision and Tags match what an item follows from its folders too: searching for the tag `familia` finds the folders tagged `familia` and everything inside them.
- **Only inside a folder,** set by Search in this folder on the Map or in the detail panel. **Search everywhere** removes it.
- **Could not be read:** only the folders and files the scan could not read. Home's notice that its figures are partial links here, with Home's source.

The **Source** choice above the results limits the search to one source, or searches all of them. It is remembered in the browser: Home, Opportunities, the review lists, Search, and the Map's start use the source you chose last on any of them, until you choose another or all sources. A link that names a source, such as Home's partial notice, opens on that source without changing your choice.

Choose **Search** to apply the filters, or **Clear filters** to start again. The filters live in the address, so a search can be bookmarked. The results appear first, and the count follows: "Counting…" shows until it arrives, then how many items match, exactly up to 10,000 and as "More than 10,000 results" beyond. Each result shows under its name the folder that holds it, cut from the left when long, after its source's name when all sources are searched; a source's top folder is listed under the source's name. The **Has copies** column says, for a checked file, "3 copies" (this one included) or "No other copy", and for a folder the share of its bytes that also exists elsewhere, as on the Map. An item that could not be read says "Could not be read" and shows "—" for its size, files, and share that has copies. Sort the results by clicking a column title, and click a row to open its details.

To change many items at once:

1. Tick the items one by one (up to 1,000), or choose **Select all results**. Selecting all asks first, showing how many items the search holds, their size, and how many of them are kept, with their size. It holds exactly those items for one hour; see [Selecting all the results of a search](#selecting-all-the-results-of-a-search).
2. Choose a decision: Follow folder, Undecided, Keep, Discard, or Later. Or choose a tag and **Add tag** or **Remove tag**.
3. Precious reports what it did: how many items changed and, for a decision, which kept items it skipped. Up to 100 skipped items are listed by path, with how many more there were.

A decision applied to many items never changes a kept one, whether it was kept itself or inside a kept folder: Discard, Later, Undecided, and Follow folder skip it. Only Keep applies to all. To change a kept item, open it and decide it on its own. See [Keep is the protection](#keep-is-the-protection).

**Manage tags** renames a tag, keeping it on every item, or deletes it from every item after asking.

**Move to…** in the same bulk section moves the ticked items, or all the results you selected, into one folder of a source where you allowed changes. Choose the folder in the destination chooser (see [The detail panel](#the-detail-panel)). A preview then lists every item with its path before and after, the items that cannot go there (a name already taken in that folder, or by another item of the same move) and those not included, each with its reason, such as an archive member, a source's top folder, or a kept item that would no longer be kept there: a move of many items never takes away a keep. Nothing moves until you choose **Confirm**; **Cancel** leaves everything as it is. Items indexed after the preview never join the move. The result then shows, with a link to History.

### The detail panel

Clicking an item on the Map or in Search opens its details beside the screen, or over it in a window narrower than 1,600 pixels (Escape or × closes it). They show:

- **Where it is,** each folder above it a link to its own details, and its kind, size, file and folder counts, and dates of last change (for a folder, its newest and oldest change inside). A notice says when the item was not found in the last scan or could not be read.
- **A preview of a file,** without choosing Open: a photo scaled to the panel (click it to see it full size), a video or audio player, a PDF, or the first 40 lines of a text, source, or Markdown file. Other types offer the download. A file changed on disk since the last scan, or on a disk that is not connected, says so instead.
- **For a folder, its size by category** (its composition) and **Inside this folder**, the notable entries below it with their category and size, each opening its own details. See [What a folder is made of](#what-a-folder-is-made-of).
- **Size by file type and by year** for a folder.
- **Classification:** the category, family, and suggestion, the traits, and one sentence per rule explaining why. A file no rule recognized reads "Not classified" and says which family its type counts under. When discard was held back because the folder holds your own material, the panel says so and lists the files that caused it, each a link to its details. See [Reading the explanations](#reading-the-explanations). **Change category** sets your own category or goes back to the rules, and, for a folder, **Review as one item** offers As the rules say, Yes, and No. A value you set reads **set by you**, with what the rules would set beside it, and after a change the panel notes that the figures of the folders above update when the scan ends. See [Your own category and groups](#your-own-category-and-groups).
- **Decision:** the item's own decision ("None: follows its folder" when it has none) and the one in force, with where it comes from: set on this item, inherited from a named folder (a link), or undecided because no folder above has a decision. The buttons Follow folder, Undecided, Keep, Discard, and Later set this item's own decision, even when it is kept; Follow folder removes it. See [Decisions](#decisions).
- **Tags:** the item's own tags, each with a button to remove it, and the tags it inherits, each naming the folder it comes from. An inherited tag can be removed only at that folder. Add an existing tag from the list, or type a new name and choose **Create and add**; a name that already exists, in any letter case, is refused with a message.
- **Organize,** for anything but an archive member or a missing item: **Rename** and **Move to…** (except for a source's top folder), and for a folder **New folder** and **Rescue kept items…** (when the folder itself is not kept), which moves the items kept inside it, the outermost ones, into a folder you choose, keeping their names. A rename, a new folder, or a single move without a conflict runs at once and shows its result with **Undo**; a name already taken is refused with a message, and nothing changes. A move that would take away an item's keep, and every rescue, opens the preview first, with the warning "N kept items would no longer be kept" where it applies. While Precious may not change the item's source, the section says so and links to the Sources screen; while the source's disk is not connected, it asks to connect it.
- **Technical details,** collapsed until opened: the entry ID, the source, the raw bytes of the name, and the raw path.

**The destination chooser** of Move to… and Rescue kept items… browses the folders Precious indexed, starting where the item is, or at the source's top folder; no path is typed. The path above the list goes back up; **Load more** shows more folders of a large one. **New folder here** creates a folder in the current one, which appears in the list once it is made. **Move here** chooses the current folder; it is off on the item being moved and on any folder inside it.

**The preview** names the change and its destination, and counts the items that will be changed, those that cannot go there, and those not included, with their total size. It lists every item, with **Load more** for long lists, and its path before and after, the decision it would take from its new place, and the reason of each item left out. **Confirm** runs the change; **Cancel** leaves everything as it is, and the plan is forgotten after an hour.

**Show in Map** opens the item's folder on the Map, **Search in this folder** limits Search to a folder, and **Open** shows a file in the viewer. For an archive Precious read completely, the main button is **Open as a folder**, which browses its items on the Map; Open comes second, and Show in Map shows the folder that holds the archive.

**Archives that were not opened** say why in their **Archive** section: their format is one Precious does not open, such as 7z or rar; they are inside another archive; they were not read yet; or reading stopped (encrypted, damaged, over the limits). Either way, what is inside is not checked for copies.

Closing the viewer or a confirmation, with Escape or a button, puts the keyboard focus back on the control that opened it. So Escape closes the viewer, and a second Escape closes the details.

### The viewer

**Open** in the detail panel shows a file without copying or extracting anything:

- photos and pictures (JPEG, PNG, GIF, WebP, AVIF, BMP, and SVG);
- video (MP4, M4V, WebM, and MOV when the browser can play it) and audio (MP3, M4A, AAC, Ogg, Opus, WAV, FLAC), with seeking;
- PDF, in the browser's own PDF viewer;
- text and source code, with syntax coloring for common languages. The text's encoding is recognized (UTF-8, UTF-16, or Windows-1252) and named, and only the first 1 MiB of a larger file is shown;
- Markdown, rendered as formatted text.

Any other file is offered as a **Download**, which every file also has. Viewing needs the source's disk to be connected.

Files from a disk are never run as part of Precious:

- The type a file is shown as comes from its extension in Precious's own list, never from its content, so a `.jpg` that holds a web page is still only an image.
- HTML and XML files are shown as text, never as pages, and an SVG is shown only as a picture, so any script inside it does not run. Executables are never run.
- Markdown is cleaned before it is shown: scripts, event handlers, styles, forms, and embedded frames are removed, and images are replaced by their description ("[image not shown: …]"), so nothing is fetched from the internet. Links to web pages open in a new browser tab; other links do nothing.

### Opportunities and Compare

**Home** also shows **Checked for copies**: how many bytes of the files that could have a copy (those sharing their size with another file) have been read, with the files not checked yet and those that could not be read. Each running hashing job shows what it is doing (listing archives, reading large files, reading small files) and its checked bytes, live. The opportunity cards follow, in the order of Opportunities; each opens its review list. All of these follow the source chosen at the top.

**Opportunities** (in the main menu) lists the eight cards: **Your files inside programs** first while it has open rows, then the others largest first. Each card shows its bytes, how many items it holds, and whether it rests on the rules or on the same content found by hashing; **Your files inside programs**, and a card whose open items hold no bytes, lead with their count instead. A card counts only what is still open, so it shrinks as you decide, and reads **Nothing left to review** once nothing is.

**A review list** shows one card's items, largest first. Each row gives its path, size, dates, suggestion, and a one-line summary (category, years, files, size, and up to two notable things inside, such as an Office document or version history), with the decision buttons. A row of **Your files inside programs** also says which program or disposable folder it is inside, with a link to that folder on the Map. **Show decided rows** also lists the rows you already decided, with their decision. **Select all rows** works as Search's select all: it confirms the count, size, and kept items, then a decision skips kept items and reports them. The duplicates list has no select all: show a row's copies and decide each copy on its own (Precious never picks a copy for you); a folder pair also offers **Compare**.

Review lists work from the keyboard: **K** keep, **D** discard, **L** later, **J** or **↓** next row, **↑** previous row, and **Enter** opens the row's details (in the duplicates list, Enter shows or hides a row's copies, and the keys then act on the selected copy). The keys do nothing while you type in a field or while a dialog or the detail panel has the focus.

**Compare** shows two folders or archives side by side. Open it from a relation in the detail panel, a pair of Similar folders, or the duplicates list, or choose **Compare with…** in a folder's or archive's details, then open the second one on the Map or in Search and choose **Compare with <first>**. The address names both sides (`/compare?left=…&right=…&bucket=…`), so a comparison can be bookmarked. Its five groups (only on the left, only on the right, identical, same name with different content, and not checked yet) show their files and size; each file has the decision buttons for each side that holds it. **Check now** reads the files of both sides that are not checked yet before any other hashing on their disks; the groups update when it ends. Two folders where one is inside the other cannot be compared.

**Move these files into "…"** appears above the files of the only-on-the-left and only-on-the-right groups when both sides are folders, not archives, of the same source, and Precious may change that source. It moves each file only on that side to the same place inside the other folder, creating the folders missing on the way, after the preview. Files with the same name and different content, and files not checked yet, are never moved.

**On the Map,** the **Has copies** column shows the share of each row's bytes that also exists elsewhere (a file is 0% or 100%), "so far" while its folder is not fully checked, and "Not checked" before anything is. It counts every copy, the one you would keep included, so it is not the space you could free: Opportunities shows that, on its duplicates card. Pointing at the column title says so, and so does its description for a screen reader (see [Percent duplicated](#percent-duplicated)). It hides after Changed, Suggestion, and Decision in a narrow window. **Color by → Has copies** paints the treemap in bands (no other copy, less than 25%, 25% to 50%, 50% to 75%, 75% or more has copies), with "Not checked yet" and "Nothing to check" colors, named in the legend. An archive Precious read completely opens like a folder, in the table and the treemap: its items show their sizes and copies, open in the viewer, and are decided with the archive, so their details show the archive's decision with a link to it and no decision or tag buttons.

**Search** has a **Copies** filter: has another copy, no other copy, not checked yet, and, when searching inside a folder, has a copy outside this folder. Select all of the last one, then Discard, to discard the copies a folder holds of files kept elsewhere.

**The detail panel** adds **Copies** for a file (its other copies, each with its path and decision, or why there is none: no other file of its size, different from every file of its size, or not checked yet), **Related folders** for a folder or archive (same content, contained in, or mostly shared, each with its bytes in common and **Compare**, which is where the files only on one side are counted), **Archive** for an archive file (format, what was read, items, size unpacked, and **Open as a folder**), the **Has copies** share, with a line under it saying that it counts every copy and is not the space you could free, which Opportunities shows, and the SHA-256 in the technical details. Every "no other copy" statement carries the share checked on all disks, because a copy can be on any of them; archives Precious does not open (7z, rar, and those over the limits) count as plain files.

### History

**History** (in the main menu) lists every change Precious made on a disk, newest first: what it was (such as "Move into “Documentos”" or "Rename"), its source, when it ran, its size, its state (waiting for its turn, in progress, done, or stopped), and how many of its items were done, left as they were because their place was taken, not included, not done, or not attempted. The list follows running changes live.

- **Undo** puts a done change back: each moved or renamed item returns to its previous folder and name, and each folder the change created is removed if it is still empty. When an item's previous name is taken, or its previous folder is gone, the preview opens and offers to choose a folder for those items, where they go under their previous names. An undo is itself a change in the list, and an item already undone reads Undone.
- **Cancel**, on a change waiting for its turn or running, stops it after the step in progress; nothing more of it runs.
- **Show items** lists every item with its paths and what became of it, such as Done, Name taken, or Changed on disk since the last scan.
- **Needs your check** marks an item whose step Precious could not confirm, for example after a power cut. It shows the item's path before and after and what was found at each (nothing, the item, or something else). Put things right on the disk, then choose **I fixed it**: Precious marks the item resolved and scans the source again. Until then, no other change can be planned on that source.

### Common tasks

- **Find what fills a disk:** open the Map, keep the default order (largest first), and open the largest folders in the treemap or the table until the space is accounted for. Every folder shows its total size and file count.
- **Decide a folder:** open its details and choose Keep, Discard, or Later. Everything inside follows, except items with a decision of their own. Home's decision figures update at once.
- **Decide many files:** search for them, select all results (or tick some), check the confirmation, and choose the decision. Read the report for the kept items that were skipped.
- **Label things:** tag a folder in its details, and everything inside it carries the tag. Search by the tag, or color the Map by it, to see them.
- **Tidy up a disk:** allow changes on its source on the Sources screen, then rename or move items from their details, or move many search results at once with Move to…. Check History to undo a change.
