-- R4 schema (r4 design D1, D3, D7, D10, D11, Interfaces): cleanup plans,
-- restores, and purges are actions of the R3 executor, so the CHECKs of
-- actions and action_items widen. A CHECK cannot be altered in place, so
-- both tables are rebuilt together as 0005 rebuilt review_rows: the new
-- tables are created, the actions and then their items are copied with their
-- IDs (so undo_of, reverses, and reversed_by still point at the same rows),
-- the old items and then the old actions are dropped (foreign keys are on, so
-- the dependent table goes first and the drops cascade nowhere), both are
-- renamed, and every index of 0006 is created again. Each source's
-- quarantine folder, and the pre-delete checks with their sets and recorded
-- files, are new. Conventions as in 0001_baseline.sql.

-- Each source's quarantine folder at its top, once Precious made it (D1).
ALTER TABLE sources ADD COLUMN quarantine_entry_id INTEGER REFERENCES entries(id) ON DELETE SET NULL;
CREATE INDEX sources_quarantine ON sources(quarantine_entry_id) WHERE quarantine_entry_id IS NOT NULL;

-- One pre-delete check of a set of quarantined items of one source (D7, D10).
-- It exists before actions, which reference it.
CREATE TABLE purge_checks (
  id INTEGER PRIMARY KEY,
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  state TEXT NOT NULL CHECK (state IN ('running','ready','failed','stale')),
  job_id INTEGER, created_at INTEGER NOT NULL, finished_at INTEGER,
  junk_confirmed_at INTEGER, stale_reason TEXT);
CREATE INDEX purge_checks_by_source ON purge_checks(source_id, state, id);

-- The set: the quarantined top entries a check covers.
CREATE TABLE purge_check_items (
  check_id INTEGER NOT NULL REFERENCES purge_checks(id) ON DELETE CASCADE,
  entry_id INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  path BLOB NOT NULL,                        -- its quarantine path at check time
  readable INTEGER NOT NULL CHECK (readable IN (0,1)),
  PRIMARY KEY (check_id, entry_id)) WITHOUT ROWID;
CREATE INDEX purge_check_items_entry ON purge_check_items(entry_id);

-- Every entry the check recorded below its set: files, archive members,
-- folders, and links, each with its identity, verdict, and relied-on copy.
CREATE TABLE purge_check_files (
  id INTEGER PRIMARY KEY,
  check_id INTEGER NOT NULL REFERENCES purge_checks(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL,
  entry_id INTEGER, member_id INTEGER,       -- exactly one; member_id belongs to entry_id's archive
  kind TEXT NOT NULL, path BLOB NOT NULL, size INTEGER NOT NULL,
  mtime_ns INTEGER, ctime_ns INTEGER, ino INTEGER, dev INTEGER, nlink INTEGER, alloc INTEGER,
  sha256 BLOB,
  verdict TEXT NOT NULL CHECK (verdict IN ('safe','copy_offline','unique','unreadable','opaque_archive','no_content')),
  class TEXT CHECK (class IN ('possibly_valuable','likely_junk','uncertain')),
  copy_source TEXT, copy_path BLOB, copy_entry INTEGER, copy_member INTEGER,
  copy_size INTEGER, copy_mtime_ns INTEGER, copy_ctime_ns INTEGER, copy_ino INTEGER, copy_dev INTEGER,
  copy_hard_link INTEGER NOT NULL DEFAULT 0,
  confirmed_at INTEGER);
CREATE INDEX purge_check_files_by_check ON purge_check_files(check_id, verdict, class, id);
CREATE INDEX purge_check_files_copy ON purge_check_files(copy_source, copy_path) WHERE copy_path IS NOT NULL;
CREATE INDEX purge_check_files_path ON purge_check_files(check_id, path);

-- 0006's actions, with the cleanup kinds, the ground of a cleanup plan, the
-- check a purge acts on, the list a plan was drafted from, and a purge's
-- report (D11). New columns come last.
CREATE TABLE actions_v7 (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('move','rename','create_folder','rescue','merge','undo','cleanup','restore','purge')),
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  state TEXT NOT NULL CHECK (state IN ('planned','queued','running','done','stopped','expired')),
  bulk INTEGER NOT NULL CHECK (bulk IN (0,1)),
  destination_id INTEGER REFERENCES entries(id) ON DELETE SET NULL,
  undo_of INTEGER REFERENCES actions_v7(id) ON DELETE SET NULL,
  kept_lost INTEGER NOT NULL DEFAULT 0,
  job_id INTEGER,
  created_at INTEGER NOT NULL, expires_at INTEGER, started_at INTEGER, finished_at INTEGER,
  ground TEXT CHECK (ground IN ('discard','duplicate')),
  check_id INTEGER REFERENCES purge_checks(id) ON DELETE SET NULL,
  list TEXT,
  deleted_files INTEGER NOT NULL DEFAULT 0,
  deleted_bytes INTEGER NOT NULL DEFAULT 0,
  freed_bytes INTEGER NOT NULL DEFAULT 0);

-- 0006's action_items, with the cleanup ops, the blocked state, and the rest
-- of the draft-time identity (D3): for cleanup items kind, dev, ino, size,
-- mtime_ns, ctime_ns, draft_bytes, and draft_files are set at draft.
CREATE TABLE action_items_v7 (
  id INTEGER PRIMARY KEY,
  action_id INTEGER NOT NULL REFERENCES actions_v7(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  op TEXT NOT NULL CHECK (op IN ('rename','mkdir','rmdir','record','unlink','purge','verify')),
  entry_id INTEGER REFERENCES entries(id) ON DELETE SET NULL,
  from_parent INTEGER REFERENCES entries(id) ON DELETE SET NULL, from_name BLOB, from_path BLOB,
  to_parent INTEGER REFERENCES entries(id) ON DELETE SET NULL, to_dir_seq INTEGER, to_name BLOB, to_path BLOB,
  bytes INTEGER NOT NULL DEFAULT 0, files INTEGER NOT NULL DEFAULT 0,
  kind TEXT, dev INTEGER, ino INTEGER, size INTEGER, mtime_ns INTEGER,
  decision_after TEXT CHECK (decision_after IN ('undecided','keep','discard','later')),
  created INTEGER NOT NULL DEFAULT 0 CHECK (created IN (0,1)),
  reverses INTEGER REFERENCES action_items_v7(id) ON DELETE SET NULL,
  reversed_by INTEGER REFERENCES action_items_v7(id) ON DELETE SET NULL,
  state TEXT NOT NULL CHECK (state IN ('planned','refused','conflict','intent','done','not_permitted','offline',
    'changed','failed','no_safe_rename','not_empty','manual_recovery','not_attempted','resolved','blocked')),
  reason TEXT,
  detail TEXT,
  finished_at INTEGER,
  ctime_ns INTEGER, draft_bytes INTEGER, draft_files INTEGER,
  UNIQUE (action_id, seq));

-- Each INSERT is one statement, so a reference to a row copied later in the
-- same statement (an item's reversed_by) is checked once all are in.
INSERT INTO actions_v7 (id, kind, source_id, state, bulk, destination_id, undo_of, kept_lost, job_id,
    created_at, expires_at, started_at, finished_at)
  SELECT id, kind, source_id, state, bulk, destination_id, undo_of, kept_lost, job_id,
    created_at, expires_at, started_at, finished_at
  FROM actions;
INSERT INTO action_items_v7 (id, action_id, seq, op, entry_id, from_parent, from_name, from_path,
    to_parent, to_dir_seq, to_name, to_path, bytes, files, kind, dev, ino, size, mtime_ns,
    decision_after, created, reverses, reversed_by, state, reason, detail, finished_at)
  SELECT id, action_id, seq, op, entry_id, from_parent, from_name, from_path,
    to_parent, to_dir_seq, to_name, to_path, bytes, files, kind, dev, ino, size, mtime_ns,
    decision_after, created, reverses, reversed_by, state, reason, detail, finished_at
  FROM action_items;

-- The dependent table goes first, so dropping actions cascades nowhere.
DROP TABLE action_items;
DROP TABLE actions;
-- Renaming rewrites the references to actions_v7 and action_items_v7.
ALTER TABLE actions_v7 RENAME TO actions;
ALTER TABLE action_items_v7 RENAME TO action_items;

CREATE INDEX actions_by_source ON actions(source_id, state, id);
CREATE INDEX actions_destination ON actions(destination_id) WHERE destination_id IS NOT NULL;
CREATE INDEX actions_undo_of ON actions(undo_of) WHERE undo_of IS NOT NULL;
CREATE INDEX actions_check ON actions(check_id) WHERE check_id IS NOT NULL;
CREATE INDEX action_items_open ON action_items(state, action_id) WHERE state IN ('intent','manual_recovery');
CREATE INDEX action_items_entry ON action_items(entry_id) WHERE entry_id IS NOT NULL;
CREATE INDEX action_items_from_parent ON action_items(from_parent) WHERE from_parent IS NOT NULL;
CREATE INDEX action_items_to_parent ON action_items(to_parent) WHERE to_parent IS NOT NULL;
CREATE INDEX action_items_reverses ON action_items(reverses) WHERE reverses IS NOT NULL;
CREATE INDEX action_items_reversed_by ON action_items(reversed_by) WHERE reversed_by IS NOT NULL;
