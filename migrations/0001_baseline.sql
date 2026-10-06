-- R1 baseline schema (design D6, Interfaces). Conventions:
--   * `*_at` columns are Unix milliseconds UTC; `*_ns` columns are Unix nanoseconds.
--   * Raw filename bytes and paths are BLOBs and compare bytewise; never TEXT.
--   * Booleans are INTEGER 0/1.

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
  capabilities  TEXT NOT NULL,              -- JSON Capabilities
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

CREATE TABLE jobs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    kind             TEXT NOT NULL,
    payload_version  INTEGER NOT NULL,
    payload          TEXT NOT NULL,
    source_id        TEXT REFERENCES sources (id) ON DELETE CASCADE,
    device_key       TEXT,
    state            TEXT NOT NULL
                     CHECK (state IN ('queued', 'running', 'paused', 'succeeded', 'failed', 'cancelled')),
    cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0, 1)),
    pause_reason     TEXT,
    terminal_code    TEXT,
    terminal_detail  TEXT,
    attempts         INTEGER NOT NULL DEFAULT 0,
    max_attempts     INTEGER NOT NULL,
    lease_owner      TEXT,
    lease_expires_at INTEGER,
    available_at     INTEGER NOT NULL,
    progress         TEXT NOT NULL DEFAULT '{}',
    created_at       INTEGER NOT NULL,
    started_at       INTEGER,
    updated_at       INTEGER NOT NULL,
    finished_at      INTEGER,
    -- Single-flight scope for non-scan jobs.
    scope_key        TEXT
);
CREATE INDEX jobs_state_available ON jobs (state, available_at);
-- One active scan per source (job-runner spec).
CREATE UNIQUE INDEX jobs_one_active_scan ON jobs (source_id)
    WHERE kind = 'scan' AND state IN ('queued', 'running', 'paused');
CREATE UNIQUE INDEX jobs_one_active_scope ON jobs (kind, scope_key)
    WHERE scope_key IS NOT NULL AND state IN ('queued', 'running', 'paused');

CREATE TABLE job_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Cascades so that a source removal, which cascades to its jobs, also
    -- removes their events.
    job_id     INTEGER REFERENCES jobs (id) ON DELETE CASCADE,
    type       TEXT NOT NULL,
    payload    TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX job_events_created ON job_events (created_at);

CREATE TABLE command_requests (
    idempotency_key TEXT PRIMARY KEY,
    command         TEXT NOT NULL,
    payload_digest  BLOB NOT NULL,
    status_code     INTEGER NOT NULL,
    response        TEXT NOT NULL,
    created_at      INTEGER NOT NULL
);

CREATE TABLE users (
    -- Single administrator.
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    username            TEXT NOT NULL,
    password_hash       TEXT NOT NULL,
    password_changed_at INTEGER NOT NULL,
    created_at          INTEGER NOT NULL
);

CREATE TABLE sessions (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    -- SHA-256 of the opaque token; the token itself is never stored.
    token_hash          BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    kind                TEXT NOT NULL CHECK (kind IN ('pre_login', 'authenticated')),
    user_id             INTEGER REFERENCES users (id),
    csrf_token          TEXT NOT NULL,
    created_at          INTEGER NOT NULL,
    last_seen_at        INTEGER NOT NULL,
    idle_expires_at     INTEGER NOT NULL,
    absolute_expires_at INTEGER NOT NULL,
    revoked_at          INTEGER,
    client_addr         TEXT,
    CHECK ((kind = 'authenticated') = (user_id IS NOT NULL))
);
CREATE INDEX sessions_absolute_expiry ON sessions (absolute_expires_at);

CREATE TABLE audit_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at INTEGER NOT NULL,
    kind        TEXT NOT NULL,
    actor       TEXT NOT NULL,
    client_addr TEXT,
    -- JSON; never passwords, tokens, or file content.
    detail      TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_events_time ON audit_events (occurred_at);

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
-- Deleting an entry sets eff_from to NULL where it points at it; the index
-- keeps that lookup from scanning every entry once per deleted entry.
CREATE INDEX entries_eff_from  ON entries(eff_from) WHERE eff_from IS NOT NULL;

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
