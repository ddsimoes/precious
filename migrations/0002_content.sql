-- R2 content schema (R2 design D3, D6-D12, Interfaces): content digests, the
-- per-file content state, coverage, listed archives and their members, folder
-- relations, folder duplication figures, and review rows. Conventions as in
-- 0001_baseline.sql. Every foreign key into entries is indexed (R1 A2).

CREATE TABLE contents (
  id     INTEGER PRIMARY KEY,
  sha256 BLOB NOT NULL UNIQUE CHECK (length(sha256) = 32),
  size   INTEGER NOT NULL CHECK (size > 0)
);
CREATE TABLE file_content (
  entry_id   INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  source_id  TEXT NOT NULL,                       -- the entry's source (denormalized for coverage)
  state      TEXT NOT NULL CHECK (state IN ('unique_size','pending','sampled','hashed','changed','unreadable')),
  size       INTEGER NOT NULL,
  mtime_ns   INTEGER, ctime_ns INTEGER, ino INTEGER,   -- identity the last read observed
  sample     BLOB CHECK (sample IS NULL OR length(sample) = 32),
  content_id INTEGER REFERENCES contents(id),
  checked_at INTEGER,
  CHECK ((state = 'hashed') = (content_id IS NOT NULL)),
  CHECK (state <> 'sampled' OR sample IS NOT NULL)
);
CREATE INDEX file_content_by_content ON file_content(content_id) WHERE content_id IS NOT NULL;
CREATE INDEX file_content_by_source  ON file_content(source_id, state, size);
CREATE TABLE content_coverage (
  source_id TEXT PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
  candidate_files INTEGER NOT NULL, candidate_bytes INTEGER NOT NULL,
  checked_files INTEGER NOT NULL,   checked_bytes INTEGER NOT NULL,
  unchecked_files INTEGER NOT NULL, unchecked_bytes INTEGER NOT NULL,
  unreadable_files INTEGER NOT NULL, unreadable_bytes INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE archives (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  format   TEXT NOT NULL CHECK (format IN ('zip','tar','tar_gzip','tar_bzip2','gzip','bzip2')),
  state    TEXT NOT NULL CHECK (state IN ('listing','complete','partial','rejected','encrypted','corrupt','unsupported','changed','unreadable')),
  detail   TEXT,                                   -- {"budget":"ratio"} | {"member_b64":"…"} | {"error":"…"}
  size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, ctime_ns INTEGER, ino INTEGER,
  members INTEGER NOT NULL DEFAULT 0, unpacked_bytes INTEGER NOT NULL DEFAULT 0,
  listed_at INTEGER
);
CREATE TABLE archive_members (
  id          INTEGER PRIMARY KEY,
  archive_id  INTEGER NOT NULL REFERENCES archives(entry_id) ON DELETE CASCADE,
  parent_id   INTEGER REFERENCES archive_members(id) ON DELETE CASCADE,   -- NULL: top level
  name        BLOB NOT NULL,
  path        BLOB NOT NULL,                       -- '/'-joined inside the archive
  kind        TEXT NOT NULL CHECK (kind IN ('directory','file','symlink','special')),
  size        INTEGER NOT NULL DEFAULT 0, mtime_ns INTEGER, link_text BLOB,
  total_bytes INTEGER NOT NULL DEFAULT 0, total_files INTEGER NOT NULL DEFAULT 0,
  locator     INTEGER,                             -- zip central-directory index
  stored      INTEGER NOT NULL DEFAULT 0,          -- zip method store (ranges, D17)
  link_member INTEGER REFERENCES archive_members(id) ON DELETE CASCADE,  -- tar hard link target
  state       TEXT CHECK (state IN ('unique_size','pending','hashed','unreadable')),  -- file members only
  content_id  INTEGER REFERENCES contents(id),
  UNIQUE (archive_id, path),
  CHECK ((kind = 'file') = (state IS NOT NULL))
);
CREATE INDEX archive_members_children ON archive_members(archive_id, parent_id, name);
CREATE INDEX archive_members_parent   ON archive_members(parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX archive_members_link     ON archive_members(link_member) WHERE link_member IS NOT NULL;
CREATE INDEX archive_members_by_size  ON archive_members(size) WHERE kind = 'file';
CREATE INDEX archive_members_content  ON archive_members(content_id) WHERE content_id IS NOT NULL;
CREATE TABLE relations (
  id   INTEGER PRIMARY KEY,
  gen  INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('same','inside','overlap')),
  a_entry  INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  a_member INTEGER REFERENCES archive_members(id) ON DELETE CASCADE,
  b_entry  INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  b_member INTEGER REFERENCES archive_members(id) ON DELETE CASCADE,
  matched_bytes INTEGER NOT NULL, redundant_bytes INTEGER NOT NULL,
  a_bytes INTEGER NOT NULL, a_files INTEGER NOT NULL, b_bytes INTEGER NOT NULL, b_files INTEGER NOT NULL,
  a_only_files INTEGER NOT NULL, a_only_bytes INTEGER NOT NULL,
  b_only_files INTEGER NOT NULL, b_only_bytes INTEGER NOT NULL
);
CREATE INDEX relations_a        ON relations(a_entry);
CREATE INDEX relations_b        ON relations(b_entry);
CREATE INDEX relations_a_member ON relations(a_member) WHERE a_member IS NOT NULL;
CREATE INDEX relations_b_member ON relations(b_member) WHERE b_member IS NOT NULL;
CREATE TABLE dir_dups (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  candidate_bytes INTEGER NOT NULL, checked_bytes INTEGER NOT NULL,
  duplicated_bytes INTEGER NOT NULL, duplicated_files INTEGER NOT NULL
);
CREATE TABLE review_rows (
  id       INTEGER PRIMARY KEY,
  gen      INTEGER NOT NULL,
  list     TEXT NOT NULL CHECK (list IN ('duplicates','unpacked_archives','system_junk','installers',
             'programs','caches','leftovers','gems_unique','gems_rescue','gems_only_in_copy')),
  source_id   TEXT REFERENCES sources(id) ON DELETE CASCADE,         -- NULL for duplicates rows
  entry_id    INTEGER REFERENCES entries(id) ON DELETE CASCADE,
  relation_id INTEGER REFERENCES relations(id) ON DELETE CASCADE,
  content_id  INTEGER REFERENCES contents(id),
  group_id    INTEGER REFERENCES entries(id) ON DELETE CASCADE,      -- gems_rescue: the group
  bytes INTEGER NOT NULL, files INTEGER NOT NULL,
  sort_key INTEGER NOT NULL,                       -- bytes (cards) or mtime_ns (gems_unique)
  CHECK ((entry_id IS NOT NULL) + (relation_id IS NOT NULL) + (content_id IS NOT NULL) = 1)
);
CREATE INDEX review_rows_list     ON review_rows(gen, list, sort_key, id);
CREATE INDEX review_rows_source   ON review_rows(gen, list, source_id, sort_key, id) WHERE source_id IS NOT NULL;
CREATE INDEX review_rows_entry    ON review_rows(entry_id) WHERE entry_id IS NOT NULL;
CREATE INDEX review_rows_group    ON review_rows(group_id) WHERE group_id IS NOT NULL;
CREATE INDEX review_rows_relation ON review_rows(relation_id) WHERE relation_id IS NOT NULL;
CREATE INDEX review_rows_content  ON review_rows(content_id) WHERE content_id IS NOT NULL;
CREATE TABLE review_row_sources (
  row_id INTEGER NOT NULL REFERENCES review_rows(id) ON DELETE CASCADE,
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  PRIMARY KEY (source_id, row_id)) WITHOUT ROWID;
CREATE INDEX review_row_sources_row ON review_row_sources(row_id);
CREATE TABLE review_state (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  gen INTEGER NOT NULL, dirty INTEGER NOT NULL, computed_at INTEGER);
INSERT INTO review_state VALUES (1, 0, 1, NULL);
