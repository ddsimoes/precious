-- R5c schema (r5c design D4, D5, D6, D12, D14, Interfaces): the Map's Only
-- here mode. The relate pass stores the figures by the place of the files
-- below them, as it stores dir_dups:
--   - entry_places: each folder's, and each complete archive's split (D5);
--   - archive_places: each complete archive's unpacked member sums, under the
--     archive file's own coverage (D5);
--   - member_places: each member folder's unpacked member sums, under its
--     archive's coverage.
-- File and member file rows are placed live and store nothing. review_state
-- is marked dirty, so the relate job that startup enqueues fills the tables
-- once, as after 0005_rescue.sql; until it ends, folders, complete archives,
-- and member folders read not_computed. Conventions as in 0001_baseline.sql;
-- each table's one foreign key is its primary key, so it is searched
-- through an index.

CREATE TABLE entry_places (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,  -- a folder or a complete archive
  only_copy_files    INTEGER NOT NULL CHECK (only_copy_files >= 0),    only_copy_bytes    INTEGER NOT NULL CHECK (only_copy_bytes >= 0),
  this_source_files  INTEGER NOT NULL CHECK (this_source_files >= 0),  this_source_bytes  INTEGER NOT NULL CHECK (this_source_bytes >= 0),
  other_source_files INTEGER NOT NULL CHECK (other_source_files >= 0), other_source_bytes INTEGER NOT NULL CHECK (other_source_bytes >= 0),
  unchecked_files    INTEGER NOT NULL CHECK (unchecked_files >= 0),    unchecked_bytes    INTEGER NOT NULL CHECK (unchecked_bytes >= 0),
  unreadable_files   INTEGER NOT NULL CHECK (unreadable_files >= 0),   unreadable_bytes   INTEGER NOT NULL CHECK (unreadable_bytes >= 0),
  disposable_files   INTEGER NOT NULL CHECK (disposable_files >= 0),   disposable_bytes   INTEGER NOT NULL CHECK (disposable_bytes >= 0)
);

CREATE TABLE archive_places (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,  -- a complete archive
  only_copy_files    INTEGER NOT NULL CHECK (only_copy_files >= 0),    only_copy_bytes    INTEGER NOT NULL CHECK (only_copy_bytes >= 0),
  this_source_files  INTEGER NOT NULL CHECK (this_source_files >= 0),  this_source_bytes  INTEGER NOT NULL CHECK (this_source_bytes >= 0),
  other_source_files INTEGER NOT NULL CHECK (other_source_files >= 0), other_source_bytes INTEGER NOT NULL CHECK (other_source_bytes >= 0),
  unchecked_files    INTEGER NOT NULL CHECK (unchecked_files >= 0),    unchecked_bytes    INTEGER NOT NULL CHECK (unchecked_bytes >= 0),
  unreadable_files   INTEGER NOT NULL CHECK (unreadable_files >= 0),   unreadable_bytes   INTEGER NOT NULL CHECK (unreadable_bytes >= 0),
  disposable_files   INTEGER NOT NULL CHECK (disposable_files >= 0),   disposable_bytes   INTEGER NOT NULL CHECK (disposable_bytes >= 0)
);

CREATE TABLE member_places (
  member_id INTEGER PRIMARY KEY REFERENCES archive_members(id) ON DELETE CASCADE,  -- a member folder of a complete archive
  only_copy_files    INTEGER NOT NULL CHECK (only_copy_files >= 0),    only_copy_bytes    INTEGER NOT NULL CHECK (only_copy_bytes >= 0),
  this_source_files  INTEGER NOT NULL CHECK (this_source_files >= 0),  this_source_bytes  INTEGER NOT NULL CHECK (this_source_bytes >= 0),
  other_source_files INTEGER NOT NULL CHECK (other_source_files >= 0), other_source_bytes INTEGER NOT NULL CHECK (other_source_bytes >= 0),
  unchecked_files    INTEGER NOT NULL CHECK (unchecked_files >= 0),    unchecked_bytes    INTEGER NOT NULL CHECK (unchecked_bytes >= 0),
  unreadable_files   INTEGER NOT NULL CHECK (unreadable_files >= 0),   unreadable_bytes   INTEGER NOT NULL CHECK (unreadable_bytes >= 0),
  disposable_files   INTEGER NOT NULL CHECK (disposable_files >= 0),   disposable_bytes   INTEGER NOT NULL CHECK (disposable_bytes >= 0)
);
UPDATE review_state SET dirty = 1 WHERE id = 1;
