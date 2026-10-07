-- r2c schema (r2c design D1): Gems is removed and its rescue list becomes
-- the opportunity card `rescue`. review_rows' CHECK names every list and
-- cannot be altered in place, so the table is rebuilt: the rows of the
-- removed lists gems_unique and gems_only_in_copy are dropped, gems_rescue
-- rows are kept as rescue, and every other row and its review_row_sources
-- rows are kept as they are. The copied rows stay the visible generation,
-- so the cards read as before at once; review_state is marked dirty, so the
-- relate job that startup enqueues rewrites them under r2c's rules (a
-- rescue row is an outermost item, design B2). Conventions as in
-- 0001_baseline.sql.

CREATE TABLE review_rows_v5 (
  id       INTEGER PRIMARY KEY,
  gen      INTEGER NOT NULL,
  list     TEXT NOT NULL CHECK (list IN ('rescue','duplicates','unpacked_archives','system_junk','installers',
             'programs','caches','leftovers')),
  source_id   TEXT REFERENCES sources(id) ON DELETE CASCADE,         -- NULL for duplicates rows
  entry_id    INTEGER REFERENCES entries(id) ON DELETE CASCADE,
  relation_id INTEGER REFERENCES relations(id) ON DELETE CASCADE,
  content_id  INTEGER REFERENCES contents(id),
  group_id    INTEGER REFERENCES entries(id) ON DELETE CASCADE,      -- rescue: the group; unpacked_archives: the folder
  bytes INTEGER NOT NULL, files INTEGER NOT NULL,
  sort_key INTEGER NOT NULL,                       -- bytes
  CHECK ((entry_id IS NOT NULL) + (relation_id IS NOT NULL) + (content_id IS NOT NULL) = 1)
);
CREATE TABLE review_row_sources_v5 (
  row_id INTEGER NOT NULL REFERENCES review_rows_v5(id) ON DELETE CASCADE,
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  PRIMARY KEY (source_id, row_id)) WITHOUT ROWID;

INSERT INTO review_rows_v5 (id, gen, list, source_id, entry_id, relation_id, content_id, group_id, bytes, files, sort_key)
  SELECT id, gen, CASE list WHEN 'gems_rescue' THEN 'rescue' ELSE list END,
    source_id, entry_id, relation_id, content_id, group_id, bytes, files,
    CASE list WHEN 'gems_rescue' THEN bytes ELSE sort_key END
  FROM review_rows WHERE list NOT IN ('gems_unique', 'gems_only_in_copy');
INSERT INTO review_row_sources_v5 (row_id, source_id)
  SELECT rs.row_id, rs.source_id FROM review_row_sources rs
  WHERE rs.row_id IN (SELECT id FROM review_rows_v5);

-- The dependent table goes first, so dropping review_rows cascades nowhere.
DROP TABLE review_row_sources;
DROP TABLE review_rows;
-- Renaming rewrites review_row_sources_v5's reference to review_rows.
ALTER TABLE review_rows_v5 RENAME TO review_rows;
ALTER TABLE review_row_sources_v5 RENAME TO review_row_sources;

CREATE INDEX review_rows_list     ON review_rows(gen, list, sort_key, id);
CREATE INDEX review_rows_source   ON review_rows(gen, list, source_id, sort_key, id) WHERE source_id IS NOT NULL;
CREATE INDEX review_rows_entry    ON review_rows(entry_id) WHERE entry_id IS NOT NULL;
CREATE INDEX review_rows_group    ON review_rows(group_id) WHERE group_id IS NOT NULL;
CREATE INDEX review_rows_relation ON review_rows(relation_id) WHERE relation_id IS NOT NULL;
CREATE INDEX review_rows_content  ON review_rows(content_id) WHERE content_id IS NOT NULL;
CREATE INDEX review_row_sources_row ON review_row_sources(row_id);

UPDATE review_state SET dirty = 1 WHERE id = 1;
