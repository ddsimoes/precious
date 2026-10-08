-- R5 schema (r5 design D3, D4, D8-D11, D13, D14, D16, D18, Interfaces):
-- media dates. Setting a file's time and organizing by date are actions of
-- the R3 executor, so the CHECKs of actions and action_items widen again,
-- and both tables are rebuilt together as 0007 rebuilt them: the new tables
-- are created, the actions and then their items are copied with their IDs
-- (so undo_of, reverses, and reversed_by still point at the same rows), the
-- old items and then the old actions are dropped (foreign keys are on, so
-- the dependent table goes first and the drops cascade nowhere), both are
-- renamed, and every index of 0007 is created again. The media metadata
-- cache, the effective dates, the owner's corrections, the cameras, and
-- each source's media state are new. Conventions as in 0001_baseline.sql;
-- every foreign key is searched through an index.

-- 0007's actions, with the date kinds, a date organize's template, and
-- whether it renames (D14, D16). New columns come last.
CREATE TABLE actions_v8 (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('move','rename','create_folder','rescue','merge','undo','cleanup','restore','purge',
    'set_mtime','date_organize')),
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  state TEXT NOT NULL CHECK (state IN ('planned','queued','running','done','stopped','expired')),
  bulk INTEGER NOT NULL CHECK (bulk IN (0,1)),
  destination_id INTEGER REFERENCES entries(id) ON DELETE SET NULL,
  undo_of INTEGER REFERENCES actions_v8(id) ON DELETE SET NULL,
  kept_lost INTEGER NOT NULL DEFAULT 0,
  job_id INTEGER,
  created_at INTEGER NOT NULL, expires_at INTEGER, started_at INTEGER, finished_at INTEGER,
  ground TEXT CHECK (ground IN ('discard','duplicate')),
  check_id INTEGER REFERENCES purge_checks(id) ON DELETE SET NULL,
  list TEXT,
  deleted_files INTEGER NOT NULL DEFAULT 0,
  deleted_bytes INTEGER NOT NULL DEFAULT 0,
  freed_bytes INTEGER NOT NULL DEFAULT 0,
  template TEXT,
  rename INTEGER NOT NULL DEFAULT 0 CHECK (rename IN (0,1)));

-- 0007's action_items, with the set_mtime op, its new and journaled previous
-- times (D13), and the copy an identical_copy refusal names (D17).
CREATE TABLE action_items_v8 (
  id INTEGER PRIMARY KEY,
  action_id INTEGER NOT NULL REFERENCES actions_v8(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  op TEXT NOT NULL CHECK (op IN ('rename','mkdir','rmdir','record','unlink','purge','verify','set_mtime')),
  entry_id INTEGER REFERENCES entries(id) ON DELETE SET NULL,
  from_parent INTEGER REFERENCES entries(id) ON DELETE SET NULL, from_name BLOB, from_path BLOB,
  to_parent INTEGER REFERENCES entries(id) ON DELETE SET NULL, to_dir_seq INTEGER, to_name BLOB, to_path BLOB,
  bytes INTEGER NOT NULL DEFAULT 0, files INTEGER NOT NULL DEFAULT 0,
  kind TEXT, dev INTEGER, ino INTEGER, size INTEGER, mtime_ns INTEGER,
  decision_after TEXT CHECK (decision_after IN ('undecided','keep','discard','later')),
  created INTEGER NOT NULL DEFAULT 0 CHECK (created IN (0,1)),
  reverses INTEGER REFERENCES action_items_v8(id) ON DELETE SET NULL,
  reversed_by INTEGER REFERENCES action_items_v8(id) ON DELETE SET NULL,
  state TEXT NOT NULL CHECK (state IN ('planned','refused','conflict','intent','done','not_permitted','offline',
    'changed','failed','no_safe_rename','not_empty','manual_recovery','not_attempted','resolved','blocked')),
  reason TEXT,
  detail TEXT,
  finished_at INTEGER,
  ctime_ns INTEGER, draft_bytes INTEGER, draft_files INTEGER,
  new_mtime_ns INTEGER,
  prev_mtime_ns INTEGER CHECK (prev_mtime_ns IS NULL OR op = 'set_mtime'),
  copy_of INTEGER REFERENCES entries(id) ON DELETE SET NULL,
  UNIQUE (action_id, seq),
  CHECK ((op = 'set_mtime') = (new_mtime_ns IS NOT NULL)));

-- Each INSERT is one statement, so a reference to a row copied later in the
-- same statement (an item's reversed_by) is checked once all are in.
INSERT INTO actions_v8 (id, kind, source_id, state, bulk, destination_id, undo_of, kept_lost, job_id,
    created_at, expires_at, started_at, finished_at, ground, check_id, list,
    deleted_files, deleted_bytes, freed_bytes)
  SELECT id, kind, source_id, state, bulk, destination_id, undo_of, kept_lost, job_id,
    created_at, expires_at, started_at, finished_at, ground, check_id, list,
    deleted_files, deleted_bytes, freed_bytes
  FROM actions;
INSERT INTO action_items_v8 (id, action_id, seq, op, entry_id, from_parent, from_name, from_path,
    to_parent, to_dir_seq, to_name, to_path, bytes, files, kind, dev, ino, size, mtime_ns,
    decision_after, created, reverses, reversed_by, state, reason, detail, finished_at,
    ctime_ns, draft_bytes, draft_files)
  SELECT id, action_id, seq, op, entry_id, from_parent, from_name, from_path,
    to_parent, to_dir_seq, to_name, to_path, bytes, files, kind, dev, ino, size, mtime_ns,
    decision_after, created, reverses, reversed_by, state, reason, detail, finished_at,
    ctime_ns, draft_bytes, draft_files
  FROM action_items;

-- The dependent table goes first, so dropping actions cascades nowhere.
DROP TABLE action_items;
DROP TABLE actions;
-- Renaming rewrites the references to actions_v8 and action_items_v8.
ALTER TABLE actions_v8 RENAME TO actions;
ALTER TABLE action_items_v8 RENAME TO action_items;

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
CREATE INDEX action_items_copy_of ON action_items(copy_of) WHERE copy_of IS NOT NULL;

-- A media file's header metadata, cached by identity as file_content is
-- (D3): size, mtime_ns, ctime_ns, and ino are the entries identity the read
-- started from. A rescan that sees the file change drops the row; a move by
-- Precious and a written time carry it.
CREATE TABLE media_meta (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  source_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('pending','read','none','unreadable')),
  size INTEGER NOT NULL, mtime_ns INTEGER, ctime_ns INTEGER, ino INTEGER,
  capture_local TEXT, capture_offset_min INTEGER CHECK (capture_offset_min BETWEEN -840 AND 840),
  gps_ns INTEGER, container_ns INTEGER,
  make TEXT, model TEXT, serial TEXT,
  read_at INTEGER,
  CHECK (state = 'read' OR (capture_local IS NULL AND gps_ns IS NULL AND container_ns IS NULL
    AND make IS NULL AND model IS NULL AND serial IS NULL)));
CREATE INDEX media_meta_by_source ON media_meta(source_id, state, entry_id);

-- Each media file's effective date, derived in the writing transaction (D5,
-- D8, D9); inputs_key says which inputs it was derived from.
CREATE TABLE media_dates (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  source_id TEXT NOT NULL,
  effective_ns INTEGER, local TEXT, offset_min INTEGER,
  precision TEXT CHECK (precision IN ('second','day','month','year')),
  source TEXT NOT NULL CHECK (source IN ('owner','exif','gps','container','file_name','folder_name','mtime','none')),
  confidence TEXT NOT NULL CHECK (confidence IN ('high','medium','low','lowest','none')),
  refined INTEGER NOT NULL DEFAULT 0 CHECK (refined IN (0,1)),
  corrected TEXT CHECK (corrected IN ('set','shift','use_name','use_folder')),
  flags INTEGER NOT NULL DEFAULT 0 CHECK (flags BETWEEN 0 AND 15),
  meta_state TEXT NOT NULL CHECK (meta_state IN ('pending','read','none','unreadable')),
  camera_key TEXT,
  inputs_key INTEGER NOT NULL, computed_at INTEGER NOT NULL,
  CHECK ((source = 'none') = (effective_ns IS NULL)),
  CHECK ((effective_ns IS NULL) = (precision IS NULL) AND (effective_ns IS NULL) = (local IS NULL)),
  CHECK (flags & 8 = 0 OR meta_state IN ('read','none')));
CREATE INDEX media_dates_by_time   ON media_dates(source_id, effective_ns, entry_id);
CREATE INDEX media_dates_by_source ON media_dates(source_id, source, effective_ns, entry_id);
CREATE INDEX media_dates_by_camera ON media_dates(source_id, camera_key, effective_ns, entry_id) WHERE camera_key IS NOT NULL;
CREATE INDEX media_dates_mtime_disagrees  ON media_dates(source_id, effective_ns, entry_id) WHERE flags & 1 <> 0;
CREATE INDEX media_dates_implausible      ON media_dates(source_id, effective_ns, entry_id) WHERE flags & 2 <> 0;
CREATE INDEX media_dates_camera_offset    ON media_dates(source_id, effective_ns, entry_id) WHERE flags & 4 <> 0;
CREATE INDEX media_dates_no_date_metadata ON media_dates(source_id, effective_ns, entry_id) WHERE flags & 8 <> 0;

-- The owner's date corrections (D11): owner decisions, so no job writes them,
-- and they survive rescans, moves, and a return from missing (I4).
CREATE TABLE date_corrections (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('set','shift','use_name','use_folder')),
  set_local TEXT CHECK (set_local IS NULL OR length(set_local) IN (4,7,10,19)),
  set_offset_min INTEGER CHECK (set_offset_min BETWEEN -840 AND 840),
  shift_s INTEGER CHECK (shift_s BETWEEN -1577880000 AND 1577880000),
  batch_id TEXT NOT NULL, created_at INTEGER NOT NULL,
  CHECK ((kind = 'set') = (set_local IS NOT NULL)),
  CHECK ((kind = 'shift') = (shift_s IS NOT NULL)),
  CHECK (set_offset_min IS NULL OR (kind = 'set' AND length(set_local) = 19)));

-- Each source's cameras and what detection found (D8); basis is the
-- Interfaces' JSON of events and other cameras.
CREATE TABLE media_cameras (
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  camera_key TEXT NOT NULL,
  make TEXT, model TEXT, serial TEXT,
  photos INTEGER NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('ok','offset','disagrees')),
  suggested_shift_s INTEGER,
  basis TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(basis)),
  computed_at INTEGER NOT NULL,
  PRIMARY KEY (source_id, camera_key),
  CHECK ((state = 'offset') = (suggested_shift_s IS NOT NULL))) WITHOUT ROWID;

-- Each source's media job state and summary (D4, D10): dirty is set by every
-- request and cleared at each loop's start; passes_job is the media job
-- running the passes, if any (D4's mutual exclusion).
CREATE TABLE media_sources (
  source_id TEXT PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
  dirty INTEGER NOT NULL DEFAULT 0 CHECK (dirty IN (0,1)),
  passes_job INTEGER,
  summary TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(summary)),
  summary_at INTEGER, detected_at INTEGER) WITHOUT ROWID;
