-- R3 schema (r3 design D1, D4, D11, D14, Interfaces): each source's write
-- permission, off for every source until the owner turns it on, and the
-- organize actions with their items, which are both the history and the
-- journal of every filesystem step. Conventions as in 0001_baseline.sql.

ALTER TABLE sources ADD COLUMN write_enabled INTEGER NOT NULL DEFAULT 0 CHECK (write_enabled IN (0,1));

-- One row per planned action. organize inserts it 'planned'; run-action and
-- cancel-action move it to 'queued' or 'stopped'; the executor owns every
-- other transition.
CREATE TABLE actions (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('move','rename','create_folder','rescue','merge','undo')),
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  state TEXT NOT NULL CHECK (state IN ('planned','queued','running','done','stopped','expired')),
  bulk INTEGER NOT NULL CHECK (bulk IN (0,1)),                         -- D14
  destination_id INTEGER REFERENCES entries(id) ON DELETE SET NULL,
  undo_of INTEGER REFERENCES actions(id) ON DELETE SET NULL,
  kept_lost INTEGER NOT NULL DEFAULT 0,
  job_id INTEGER,
  created_at INTEGER NOT NULL, expires_at INTEGER, started_at INTEGER, finished_at INTEGER);
CREATE INDEX actions_by_source ON actions(source_id, state, id);
-- Every other foreign key is searched through an index too, so deleting
-- entries (a rescan dropping missing ones) or a source's actions checks the
-- referencing rows by index.
CREATE INDEX actions_destination ON actions(destination_id) WHERE destination_id IS NOT NULL;
CREATE INDEX actions_undo_of ON actions(undo_of) WHERE undo_of IS NOT NULL;

-- One row per filesystem step (D4): planned, then intent, then its outcome.
CREATE TABLE action_items (
  id INTEGER PRIMARY KEY,
  action_id INTEGER NOT NULL REFERENCES actions(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  op TEXT NOT NULL CHECK (op IN ('rename','mkdir','rmdir')),
  entry_id INTEGER REFERENCES entries(id) ON DELETE SET NULL,  -- rename: the entry; mkdir: the folder once done; rmdir: the folder
  from_parent INTEGER REFERENCES entries(id) ON DELETE SET NULL, from_name BLOB, from_path BLOB,
  to_parent INTEGER REFERENCES entries(id) ON DELETE SET NULL, to_dir_seq INTEGER, to_name BLOB, to_path BLOB,
  bytes INTEGER NOT NULL DEFAULT 0, files INTEGER NOT NULL DEFAULT 0,
  kind TEXT, dev INTEGER, ino INTEGER, size INTEGER, mtime_ns INTEGER,   -- identity expected, set at intent
  decision_after TEXT CHECK (decision_after IN ('undecided','keep','discard','later')),
  created INTEGER NOT NULL DEFAULT 0 CHECK (created IN (0,1)),          -- mkdir made the folder
  reverses INTEGER REFERENCES action_items(id) ON DELETE SET NULL,      -- undo item: the item it reverses
  reversed_by INTEGER REFERENCES action_items(id) ON DELETE SET NULL,   -- set when that undo item is done
  state TEXT NOT NULL CHECK (state IN ('planned','refused','conflict','intent','done','not_permitted','offline',
    'changed','failed','no_safe_rename','not_empty','manual_recovery','not_attempted','resolved')),
  reason TEXT,   -- refused/conflict/changed reason, see the item JSON
  detail TEXT,   -- failed: the OS error text; manual_recovery: {"from":"absent|same|other","to":"absent|same|other"}
  finished_at INTEGER,
  UNIQUE (action_id, seq));
CREATE INDEX action_items_open ON action_items(state, action_id) WHERE state IN ('intent','manual_recovery');
CREATE INDEX action_items_entry ON action_items(entry_id) WHERE entry_id IS NOT NULL;
CREATE INDEX action_items_from_parent ON action_items(from_parent) WHERE from_parent IS NOT NULL;
CREATE INDEX action_items_to_parent ON action_items(to_parent) WHERE to_parent IS NOT NULL;
CREATE INDEX action_items_reverses ON action_items(reverses) WHERE reverses IS NOT NULL;
CREATE INDEX action_items_reversed_by ON action_items(reversed_by) WHERE reversed_by IS NOT NULL;
