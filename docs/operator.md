# Precious operator guide

Precious helps you make sense of a disk that has been collecting files for years. It indexes every file and folder on the disks you add, shows where the space goes, lets you find any file, and lets you record what to keep and what to discard, all from a web browser. It only reads your disks: it never writes to, moves, or deletes anything on them. This guide covers building, installing, configuring, and running it. The product specification is [`precious-spec-v0.3.md`](../precious-spec-v0.3.md).

This release, R1, is the full index and the explorer: sources added from the browser, complete scans and rescans with every folder's size, classification rules, Home, Map, Search, the detail panel, the file viewer, and your decisions and tags. Finding duplicates, organizing files into new folders, and cleanup (quarantine and deletion) come in later releases; nothing in R1 changes a file on a disk. Why the product was reset from the earlier `curator` design is recorded in [ADR 0008](adr/0008-product-reset.md).

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
| `ReadOnlyPaths=` | the disks to index, listed explicitly in a drop-in (below) |
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

Precious never writes to the disks it indexes, but present every disk read-only at the operating-system level as well:

- Mount the filesystems read-only on the host, for example `mount -o ro,nosuid,nodev,noexec /dev/sdb1 /srv/old-disk` or `ro,nosuid,nodev,noexec` in `/etc/fstab`. A read-only mount also prevents the access-time updates that reading directories and files would otherwise write.
- For an original disk that must not change at all, set the block device read-only first (`blockdev --setro /dev/sdb`): an `ro` mount of ext3/ext4 with a dirty journal still replays the journal unless you add `noload`.
- Under systemd, list each disk in `ReadOnlyPaths=`; under Compose, bind each disk read-only.
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

Security events and every change you make are written to the `audit_events` table with the time and the client address, never a password, token, or file content: `password_set`, `sessions_revoked`, `login_succeeded`, `login_failed`, `login_throttled`, `logout`, `source_added`, `source_renamed`, `source_removed`, `decision_set`, `tags_set`, `tag_created`, `tag_renamed`, and `tag_deleted`. To read the latest ones, run this as the user that owns the state directory:

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

R2 adds hashing, archives, duplicates, Compare, opportunities, and Gems. Its migration, `0002_content`, only adds tables: every R1 entry, decision, tag, selection, job, and audit event stays as it was, and no rescan is needed.

1. **Back up first** with the R1 binary still running: `precious backup` (see [Taking a backup](#taking-a-backup)).
2. **Check the configuration** with the R2 binary. An R1 configuration stays valid: the new `[hashing]`, `[archives]`, and `[duplicates]` sections have defaults (see [Configuration reference](#configuration-reference)), and `[copies]` is still refused.
3. **Replace and restart.** At startup `0002_content` applies to the R1 database in one transaction, and every online source gets a hashing job, which reads file content in the background (see [Hashing](#hashing)). The first run on a large archive can take hours; scans, pages, and decisions are not blocked while it runs.

To roll back, stop Precious, reinstall the R1 binary or image, [restore](#restoring) the backup taken in step 1, and start it. The R1 binary refuses the migrated database (`database has version 2, binary supports up to 1`) and leaves it unmodified, so the backup is the only way back. Decisions and tags recorded after the upgrade are lost with it.

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

The API and the interface are the same on every system; only the reported volume and capabilities differ. On every system Precious only reads: it never follows a symlink, never opens a FIFO, socket, or device, and never enters another filesystem mounted inside a source.

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

| Filesystem type | Names | Stable file identity | Time resolution | Local time | Read-only |
|---|---|---|---|---|---|
| ext2, ext3, ext4, xfs, btrfs, zfs, f2fs, tmpfs | case-sensitive | yes | 1 ns | no | as mounted |
| vfat (FAT) | case-insensitive | no | 2 s | yes | as mounted |
| exfat | case-insensitive | no | 10 ms | no | as mounted |
| ntfs, ntfs3, NTFS through ntfs-3g (`fuseblk`) | case-insensitive | yes | 100 ns | no | as mounted |
| iso9660, udf (optical discs) | case-sensitive | yes | 1 s | no | always |
| anything else, and every filesystem on macOS and Windows | case-insensitive | no | 2 s | no | as mounted (Linux only) |

The last row is the conservative set, reported with `known: false`: Precious assumes the least it can rely on. NTFS is treated as case-insensitive because Windows treats its names that way. An NTFS volume mounted through ntfs-3g shows the type `fuseblk`, which other drivers use too; Precious recognizes it by its NTFS volume serial (16 hexadecimal digits in `/dev/disk/by-uuid`), and gives any other `fuseblk` filesystem the conservative set. No filesystem normalizes Unicode in names, so two names that differ only in Unicode composition stay two names everywhere.

What the capabilities change:

- **Case sensitivity.** On a case-insensitive filesystem, names that differ only in letter case, such as `Fotos` and `FOTOS`, are the same name wherever Precious compares names. Every entry still keeps its name exactly as the filesystem lists it.
- **Time resolution.** A rescan treats a file as unchanged when its size is the same and its modification time differs by no more than the resolution. A FAT memory card, which stores times in 2-second steps, therefore does not show every file as modified, while on ext4 a change of a microsecond is a change.
- **Local time.** FAT stores times in local time, without a time zone, so after a daylight-saving change every time on the card can move by an hour. On a local-time filesystem a difference of one hour (within the resolution) also counts as unchanged. A card read in another time zone shows its files as changed: Precious does not hide edits made within the same hour.
- **Stable file identity.** Where it is missing (FAT, exFAT, and the conservative set), device and inode numbers change between mounts, so Precious does not rely on them and matches a file by its path, size, and modification time.
- **Read-only.** Follows the mount; optical discs always report read-only. Precious never writes to a source either way.

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

Removing a source deletes its index: its entries, folder totals, decisions, and tag assignments. The tags themselves stay, and no file on the disk is touched. A source cannot be removed while its scan is queued, running, or paused (`job_active`); cancel the scan first.

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

### Volumes that cannot be recognized when moved

A source on a volume with the weak `path` identity, reported with `"strong": false`, is recognized only at the mount point it was added at. Mounted anywhere else, it shows as offline. This applies to filesystems with no UUID, dataset, or btrfs identity (network shares, tmpfs, most FUSE filesystems), to disks whose UUID the service cannot see, and to every source on macOS and Windows in this release. To keep such a source:

- On Linux, let the service see `/dev/disk`, so disks with a filesystem UUID get a strong identity. The shipped systemd unit and Compose file already do; see [Volume identity](#volume-identity). A source added while `/dev/disk` was hidden keeps its weak identity and shows as offline once its disk is identified by UUID; remove it and add the folder again.
- Mount the filesystem at the same mount point every time: an `/etc/fstab` entry or a systemd mount unit for a network share, or the same drive letter or volume path on macOS and Windows.
- If the volume has moved for good, remove the source and add the folder at its new location as a new source, then scan it. Decisions and tags of the removed source are not carried over.

## Scanning and the index

A scan reads a source's folders and records every file, folder, symbolic link, and special file in it, with the folder totals, breakdowns, and classification the screens show. Start one with **Scan now** on the Sources screen, or with the `start-scan` command; adding a source does not scan it.

### What a scan reads and records

A scan lists every folder in batches of `scan.list_batch` entries and reads each entry's metadata (`lstat`): kind, size, modification and change times, permissions, link count, and file identity. It reads no file content. It never writes to the source, never follows a symbolic link (the link is recorded with its target text), never opens a FIFO, socket, or device (recorded as a special file), and never enters a folder where another filesystem is mounted: that folder is recorded as a mount boundary, with no contents. Names are kept byte for byte, including names that are not valid UTF-8, which the interface shows with `\xNN` escapes.

Each folder's totals are complete when its last entry is done, in the same pass: its total bytes and files, the newest and oldest file time, its main file kind, its counts of folders, files, links, special files, unreadable folders, and mount boundaries below it, its bytes by file kind, by year (of the files' modification times, UTC), and by family (its [composition](#what-a-folder-is-made-of)), and the [notable entries inside it](#what-a-folder-is-made-of). Links and special files count no bytes. The rules classify every file as it is listed and every folder once it is complete (see [Classification rules](#classification-rules)); a folder whose discard suggestion is vetoed keeps up to 20 examples of the user material below it.

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

<!-- owner: H -->
This section will document how Precious reads file content to find copies: which files are read and in what order, samples of large files, reading safety, the hashing jobs and commands (`start-hash`, `check-now`), coverage, progress, cancelling, and the `[hashing]` settings.

## Archives

<!-- owner: A -->
This section will document the archives Precious opens (zip, tar, tar.gz, tar.bz2, gzip, and bzip2), their budgets and outcomes, what stays unopened, and that nothing is ever unpacked to disk.

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

A group is a folder that is best reviewed as one item: an installed program, a copy of Windows, a project, a program's saved games or profile, a cache, build output, or a drive backup. Folders in the categories `application_installation`, `os_installation`, `source_project`, `application_user_data`, `cache`, `generated_artifacts`, and `backup` are groups. Groups can sit inside other groups; the outermost one is the item to review. Being in a group never hides anything: the Map and Search still reach every file and folder inside, with their own sizes.

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

## Duplicates and Compare

<!-- owner: R -->
This section will document duplicate groups and redundant bytes, what a "no other copy" claim means and the coverage it carries, folder relations (same, inside, overlap), the duplication figures of folders, Compare and its buckets, the `relate` job, and the `[duplicates]` settings.

## Opportunities and Gems

<!-- owner: V -->
Opportunities answers "what should I look at first?" with seven cards, and Gems answers "what is valuable and has no other copy?". Both are built from the index, the rules' classification, and the duplicates; neither decides anything for you.

### The cards

| Card | Rows | Bytes | Basis |
|---|---|---|---|
| Exact duplicates (`duplicates`) | Folder and archive relations of kind same or inside, and duplicate files outside every listed relation | Redundant bytes | Same content |
| Archives already unpacked (`unpacked_archives`) | Archives whose whole content is the same as, or inside, a folder | The archive file's size | Same content |
| System junk (`system_junk`) | Entries of category `system_junk` | Total bytes | Rules |
| Installers and downloads (`installers`) | Entries of category `installer_download` or `download_collection` | Total bytes | Rules |
| Programs and system copies (`programs`) | Entries of category `application_installation` or `os_installation` | Total bytes | Rules |
| Caches and generated files (`caches`) | Entries of category `cache`, `temporary_data`, or `generated_artifacts`, except unfinished downloads | Total bytes | Rules |
| Leftovers (`leftovers`) | Unfinished downloads (`*.part`, `*.partial`, `*.crdownload`), empty folders, and zero-byte files | Total bytes | Rules |

Cards are ranked by bytes, largest first, and can show all sources or one. With one source, a card counts only the rows that touch it: its entries, and the duplicates rows with a copy on it.

How the bytes are counted:

- **A row is the outermost match.** A row is a group, folder, archive, or file that matches its card while no folder above it does. `Backup_PC_2004/C/WINDOWS` is one row of the programs card; `system32` inside it adds no bytes of its own. So no byte counts twice in one card. Different cards can overlap: a zero-byte `desktop.ini` is both system junk and a leftover.
- **An empty folder** holds no file at any depth, is readable, and is not where another filesystem is mounted. Folders below an unreadable folder or a mount boundary are never called empty.
- **A duplicates row** is a relation (its bytes are one side's worth of redundant bytes), or a group of identical files with at least one copy outside every listed relation. A group's bytes are its size times its copies outside the listed relations, less one when none of its copies is inside a relation: the relation already counts the copies inside it. Hard links to one file are one copy. Files inside archives count as copies; the archive itself does not.
- **Only open rows count.** A row is open while its entry's effective decision is undecided. A duplicates row is open while at least two of its copies are undecided (for a relation, both sides). Deciding an entry, or the folder above it, closes its row at once and shrinks the card by the row's bytes. A card's bytes are always the sum of its list's open rows, read through every page.

The rows are recomputed by the `relate` job (see Duplicates and Compare), after each scan and as hashing advances, so the classification and duplicates they show are as current as that job's last run. Decisions are never stored in them: they are read live.

### Review lists

Opening a card shows its review list, largest row first. Each row shows its size, dates, suggestion, and a one-line summary of what it holds: category, years, files, bytes, and up to two notable signals, such as a spreadsheet inside an installed program. Duplicates rows expand into their copies, each with its own decision controls.

The list works from the keyboard: `K` keep, `D` discard, `L` later, `J` or `↓` next row, `↑` previous row, and `Enter` opens the detail panel. In the duplicates list, the keys act on the focused copy. The keys are ignored while typing in a field and inside a dialog.

A decided row leaves the list. Choose to show decided rows to list the rows that are no longer open, with their decisions.

### Selecting a whole list

Every list except duplicates can select all of its open rows (the `select-list` command, `{"list":"system_junk","source_id":"…"}`; `source_id` is optional). This makes an ordinary selection of the rows' entries, exactly like a search's select-all: the confirmation shows the count, the bytes, and the kept entries, and the bulk decision skips every kept entry and reports it. The selection holds the entries open when it was made; a later refresh of the lists does not change it, and an entry kept in the meantime is skipped.

The duplicates list has no select-all (`400 invalid_request`): Precious never chooses which copy stays. Decide copies one by one, or use Search's duplicate filter ("copies outside this folder") and select its results.

### Gems

Gems has three sections, for all sources or one:

- **Unique personal files:** photos, videos, music, and documents of the personal family with no other copy anywhere, outside every installed program, system copy, or disposable group, oldest first.
- **To rescue:** the user material the rules found inside programs and disposable groups (the indicators that trigger the veto, such as `OFFICE11/Meu orcamento casamento.xls` inside Microsoft Office), each under its outermost group, with its copy state.
- **Only in one copy:** files with no other copy that sit on one side of an overlap relation, such as a photo edited in a copied folder, grouped by relation. Files inside archives are not listed here.

"No other copy" means the file is unique by size, its sample is distinct among files of its size, or it was hashed and found once (hard links count once). A file not checked yet, or one that could not be read, is never listed as having no other copy; it waits until hashing checks it. Archives Precious does not open (7z, rar, and archives over budget) count as plain files, so a copy inside one is not seen. Each section states the share of the content that could have a copy that was checked, over every source, because a copy can be anywhere. Gems lists files whatever their decision.

## Search, viewer, and read API

The interface reads the index through a small JSON API under `/api`. The same endpoints serve scripts, for example to export a search. Every endpoint below needs a signed-in session, like the interface; without one it answers `401 unauthenticated`. None of them change anything, and none of them accept a path: an entry is named only by its ID, which comes from an earlier answer. Entry IDs are decimal strings, such as `"812"`, and tag IDs are numbers. Treat both as opaque.

### Read endpoints

| Endpoint | Answers |
|---|---|
| `GET /api/home`, `GET /api/home?source=ID` | The figures of Home for every source, or for one: totals, bytes and files by family, by file kind, and by year, the decision totals, whether the figures are partial, and the scans in progress. |
| `GET /api/entries/{id}` | One entry with the folders above it, its classification with the explanation of each rule, its own and effective decision and tags with where they come from, and, for a folder, its counts, its breakdowns by kind and by year, and its notable entries inside (`stats.inside`). |
| `GET /api/entries/{id}/children` | A folder's items, one page at a time, in every state (present, missing, unreadable). |
| `GET /api/entries/{id}/treemap` | A folder's 300 largest items by bytes, and the count and bytes of the rest as one `other` area. Missing items take no space, so they appear in neither. |
| `GET /api/search?…` | One page of search results, with the match count. See [Search parameters](#search-parameters). |
| `GET /api/tags` | Every tag with the number of entries carrying it as their own. |
| `GET /api/entries/{id}/content`, `GET /api/entries/{id}/text` | A file's content, and its text decoded. See [Viewer safety](#viewer-safety). |

Every entry row carries its name and path twice: `name` and `path` are the escaped display form, and `name_b64` and `path_b64` are the exact bytes on disk in base64. A name that is not valid UTF-8 is therefore never lost. For example, a Latin-1 `fé.txt` shows as `f\xE9.txt`, and its raw bytes are `ZukudHh0`. Times are in UTC, in RFC 3339 form, or `null` when unknown.

Every entry row also carries `composition`, its bytes and files by family as a list such as `[{"family":"personal","bytes":400000000000,"files":7},{"family":"programs","bytes":6000000000,"files":5}]`: a folder's composition, or for a file one element under its family. Families with nothing in them are left out. See [What a folder is made of](#what-a-folder-is-made-of).

Children are sorted with `sort=bytes`, `files`, `newest` (the newest change inside a folder), or `name`, and with `order=desc` or `asc`. By default the sort is by bytes, largest first. A sort by name defaults to ascending and compares the raw bytes of the names, so `Zeta` comes before `alfa`. A page holds 200 rows unless `limit` asks for another number, and never more than 1,000. A page with more after it carries `next_cursor`, and the same request with `cursor=` set to it gives the next page. A cursor belongs to the folder's order: changing `sort` or `order` needs a new first page. A cursor holds the position of the last row, not a row count. So a row added or removed while you page does not shift the other rows: a new row is listed only when it sorts after the current page. A row whose size or date a scan changes may move to a page already read.

Errors use the usual envelope, `{"error":{"code","message"}}`:

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | An unknown or repeated parameter, a bad value (such as `sort=color` or `limit=0`), or a cursor the server did not give for this order. |
| 404 | `not_found` | An entry, source, or folder (`within`) that does not exist, or an ID that is not a valid one. |
| 409 | `invalid_entry_state` | The content or text of a folder, of a missing file, or of a file that changed on disk since the last scan. |
| 409 | `source_offline` | The content or text of a file whose source is not online. |

### Search parameters

`GET /api/search` takes the filters below as URL parameters. The Search screen keeps the same parameters in its address, so the address of a search on screen is also the API request. Filters combine with AND. A parameter marked "repeats" can be given several times, such as `ext=jpg&ext=png`, and then matches any of its values. An empty value is the same as leaving the parameter out.

| Parameter | Matches |
|---|---|
| `source=ID` | Entries of one source. |
| `name=TEXT` | Names containing the text, ignoring letter case, matched against the displayed name. |
| `ext=X` (repeats) | Files with the extension, with or without its dot, ignoring letter case. |
| `file_kind=K` (repeats) | `image`, `video`, `audio`, `document`, `source`, `archive`, `installer`, `executable`, `system`, or `other`. |
| `min_size=N`, `max_size=N` | Size in bytes, both limits included. A folder's size is everything inside it. |
| `year_from=Y`, `year_to=Y` | Year of the last change, in UTC, both years included. |
| `category=C` (repeats) | One of the 16 [categories](#categories-and-families). |
| `triage=T` (repeats) | `keep`, `discard`, or `review`. |
| `decision=D` (repeats) | The effective decision: `undecided`, `keep`, `discard`, or `later`, set on the entry or followed from a folder above it. |
| `tag=ID` (repeats) | Entries carrying the tag, and everything inside them. |
| `within=ID` | Everything inside the folder, not the folder itself. |
| `sort`, `order` | As for children; by default by bytes, largest first. |
| `cursor`, `limit` | As for children: 200 rows by default, at most 1,000. |

Speed depends on the filters:

- A `name` of three characters or more is looked up in a name index, and is fast on any index size.
- A `name` of one or two characters cannot use that index. It is tested on each entry that the other filters select, so on its own it reads every entry, which takes seconds on millions of entries. Add a `within`, `tag`, `source`, or `decision` filter to narrow it.
- `within`, `tag`, and `decision` also use indexes. Extension, file kind, size, year, category, and triage do not: on their own they read every entry of the source, or of every source.

Results include entries in every state, and each row says whether it is present, missing, or unreadable. `count` is the exact number of matches up to 10,000, and the string `"10000+"` beyond that. A search that lists more than you need is best narrowed rather than paged to the end. "Select all results" works on up to 1,000,000 matches; see [Selecting all the results of a search](#selecting-all-the-results-of-a-search).

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

<!-- owner: Q -->
This section will document what R2 adds to the read API: the content fields of every entry row, member refs (`m45`), the detail's content, relations, archive, and coverage, the copies, opportunities, Gems, and Compare endpoints, Search's `dup` filter, and viewing archive members.

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

## Interface

Precious is used through a web interface served by `precious serve` at the address in `server.external_origin`. Everything it needs is built into the binary: it loads nothing from the internet and works on a network with no outside access. The top bar links the four screens, Home, Map, Search, and Sources, and has the Sign out button. Sizes are shown in binary units (KiB, MiB, GiB, where 1 GiB is 1,024 MiB). The interface is in English, the only language in this release, and shows numbers and dates in English formats.

Nothing in the interface changes a file on a disk. Decisions and tags are recorded in Precious's database only.

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

The Sources screen lists each source with its state (online, offline, or unavailable, with the reason), its location and disk, whether the disk will be recognized if it is mounted at another path, what its file system can and cannot record, its totals, and its last scan. The location is the source's folder where its disk is mounted now, such as `/run/media/you/FOTOS/Fotos`; while the disk is not connected it is the folder inside the disk and the disk's label (or identity), such as `Fotos on FOTOS (not connected)`. Each source has these actions:

- **Scan now** reads the disk and updates the index. Progress shows on the source and on Home while it runs. A disk that is not connected cannot be scanned.
- **Open in Map** browses the source, also while it is offline.
- **Rename** changes the label only.
- **Remove** asks first. It forgets the source with its decisions and tag assignments; no file on the disk is changed.

**Add source** opens the folder picker. It starts at the locations Precious may use and lists folders only; open folders until the one you want is current, optionally give it a name, and add it. Then choose Scan now. See [Adding a source](#adding-a-source), [Allowed roots](#allowed-roots), and [Offline and unavailable sources](#offline-and-unavailable-sources).

### Map

The Map answers "where is my space?" for one folder at a time. The Map link opens the top folder of the first source; with no source yet, it points to the Sources screen. The folder's path is shown above, each part a link back up.

- **The treemap** draws each item of the folder as an area sized by its bytes, folders counting everything inside them. It draws the 300 largest items; the rest of a large folder is one gray area labeled with how many items it holds and their size, such as "700 more items, 3 GiB". That area does not open: the table lists every item.
- **The table** lists every item of the folder with its name, size, file count (for folders, everything inside), type or category, the range of modification dates, the suggestion of the rules, and the decision. Under each folder's size, a thin bar shows its composition in the family colors; when at least 1% of a folder's bytes belongs to another family, the category also gives the main family's share, such as "Personal media · 98% personal" (see [What a folder is made of](#what-a-folder-is-made-of)). A decision followed from a folder above reads like "Keep (inherited)". Click a column title (Name, Size, Files, Changed) to sort by it, and click it again to reverse the order. Scrolling down loads more rows; a Load more button does the same. In a narrow window the table hides the Changed column first, then Suggestion, Decision, and Files, and scrolls sideways inside its frame if it still does not fit.
- **The two follow each other:** pointing at a row outlines its area, and pointing at an area highlights its row.
- **Clicking a folder's area or its name** opens that folder in both. Clicking a file's area or any row opens the [detail panel](#the-detail-panel) for it.
- **Color by** paints the areas by category family, file type, age (time since the last change), decision, or tag (choose the tag next to it). By family, each area takes the family holding most of its bytes, so a photo no rule recognized is still Personal and valuable. The legend names each color.
- **Search in this folder** opens Search limited to the folder.

The address keeps the folder, the order, the coloring, and the open details, so it can be bookmarked or reloaded.

### Search

Search finds files and folders anywhere in the index, including inside groups (see [Groups](#groups)). The filters are:

- **Name contains:** part of the name, ignoring letter case.
- **Extensions:** one or more, separated by spaces, such as `jpg png`.
- **Size:** at least and at most, in B, KiB, MiB, or GiB. A folder's size includes everything inside it.
- **Year of last change:** from and to.
- **File type, Category, Decision, Suggestion, and Tags:** tick any number in each list. Decision and Tags match what an item follows from its folders too: searching for the tag `familia` finds the folders tagged `familia` and everything inside them.
- **Only inside a folder,** set by Search in this folder on the Map or in the detail panel. **Search everywhere** removes it.

Choose **Search** to apply the filters, or **Clear filters** to start again. The filters live in the address, so a search can be bookmarked. The results show how many items match, exactly up to 10,000 and as "More than 10,000 results" beyond. Sort them by clicking a column title, and click a row to open its details.

To change many items at once:

1. Tick the items one by one (up to 1,000), or choose **Select all results**. Selecting all asks first, showing how many items the search holds, their size, and how many of them are kept, with their size. It holds exactly those items for one hour; see [Selecting all the results of a search](#selecting-all-the-results-of-a-search).
2. Choose a decision: Follow folder, Undecided, Keep, Discard, or Later. Or choose a tag and **Add tag** or **Remove tag**.
3. Precious reports what it did: how many items changed and, for a decision, which kept items it skipped. Up to 100 skipped items are listed by path, with how many more there were.

A decision applied to many items never changes a kept one, whether it was kept itself or inside a kept folder: Discard, Later, Undecided, and Follow folder skip it. Only Keep applies to all. To change a kept item, open it and decide it on its own. See [Keep is the protection](#keep-is-the-protection).

**Manage tags** renames a tag, keeping it on every item, or deletes it from every item after asking.

### The detail panel

Clicking an item on the Map or in Search opens its details beside the screen, or over it in a window narrower than 1,600 pixels (Escape or × closes it). They show:

- **Where it is,** each folder above it a link to its own details, and its kind, size, file and folder counts, and dates of last change (for a folder, its newest and oldest change inside). A notice says when the item was not found in the last scan or could not be read.
- **A preview of a file,** without choosing Open: a photo scaled to the panel (click it to see it full size), a video or audio player, a PDF, or the first 40 lines of a text, source, or Markdown file. Other types offer the download. A file changed on disk since the last scan, or on a disk that is not connected, says so instead.
- **For a folder, its size by category** (its composition) and **Inside this folder**, the notable entries below it with their category and size, each opening its own details. See [What a folder is made of](#what-a-folder-is-made-of).
- **Size by file type and by year** for a folder.
- **Classification:** the category, family, and suggestion, the traits, and one sentence per rule explaining why. A file no rule recognized reads "Not classified" and says which family its type counts under. When discard was held back because the folder holds your own material, the panel says so and lists the files that caused it, each a link to its details. See [Reading the explanations](#reading-the-explanations).
- **Decision:** the item's own decision ("None: follows its folder" when it has none) and the one in force, with where it comes from: set on this item, inherited from a named folder (a link), or undecided because no folder above has a decision. The buttons Follow folder, Undecided, Keep, Discard, and Later set this item's own decision, even when it is kept; Follow folder removes it. See [Decisions](#decisions).
- **Tags:** the item's own tags, each with a button to remove it, and the tags it inherits, each naming the folder it comes from. An inherited tag can be removed only at that folder. Add an existing tag from the list, or type a new name and choose **Create and add**; a name that already exists, in any letter case, is refused with a message.
- **Technical details,** collapsed until opened: the entry ID, the source, the raw bytes of the name, and the raw path.

**Show in Map** opens the item's folder on the Map, **Search in this folder** limits Search to a folder, and **Open** shows a file in the viewer.

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

### Opportunities, Compare, and Gems

<!-- owner: U -->
This section will document the R2 screens and controls: hashing coverage on Home, the Opportunities screen and review lists, Compare, Gems, duplicate figures on the Map, the duplicate filter in Search, and archives opened as folders.

### Common tasks

- **Find what fills a disk:** open the Map, keep the default order (largest first), and open the largest folders in the treemap or the table until the space is accounted for. Every folder shows its total size and file count.
- **Decide a folder:** open its details and choose Keep, Discard, or Later. Everything inside follows, except items with a decision of their own. Home's decision figures update at once.
- **Decide many files:** search for them, select all results (or tick some), check the confirmation, and choose the decision. Read the report for the kept items that were skipped.
- **Label things:** tag a folder in its details, and everything inside it carries the tag. Search by the tag, or color the Map by it, to see them.
