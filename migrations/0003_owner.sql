-- R2b schema (r2b design D1, D3, D6, D7, Interfaces): the owner's category
-- overrides and group marks, per-source rescan schedules, and the name index
-- rebuilt to fold accents. Conventions as in 0001_baseline.sql.

-- One row per entry the owner classified (D1). NULL follows the rules; a row
-- with neither value set is deleted instead. entries keeps the effective
-- values, which the scan writes through rules.ApplyOwner.
CREATE TABLE entry_overrides (
  entry_id   INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  category   TEXT CHECK (category IN (
               'personal_media','documents','source_project','application_user_data',
               'application_installation','application_configuration','os_installation','installer_download',
               'system_junk','cache','temporary_data','generated_artifacts',
               'download_collection','backup','mixed','unknown')),
  group_mark INTEGER CHECK (group_mark IN (0,1)),   -- 1 marks a folder as a group, 0 unmarks a rule group
  updated_at INTEGER NOT NULL,
  CHECK (category IS NOT NULL OR group_mark IS NOT NULL)
);

-- Rescan schedules (D6) and the scan an override waits for (D3).
ALTER TABLE sources ADD COLUMN scan_schedule TEXT;           -- JSON domain.Schedule, NULL when off
ALTER TABLE sources ADD COLUMN next_scan_at INTEGER;         -- the next due time, NULL when off
ALTER TABLE sources ADD COLUMN schedule_skipped_at INTEGER;  -- the last due time that was skipped
ALTER TABLE sources ADD COLUMN schedule_skip_reason TEXT;    -- why: the source's state then ('offline', 'unavailable')
ALTER TABLE sources ADD COLUMN rescan_requested INTEGER NOT NULL DEFAULT 0 CHECK (rescan_requested IN (0,1));

-- The name index, rebuilt to fold accents (D7): the tokenizer folds the
-- indexed names and the searched text alike. precious_display_name is
-- domain.DisplayName, registered by the store, so every name is indexed
-- exactly as the scanner writes it (every entry but a source's root).
DROP TABLE entry_names;
CREATE VIRTUAL TABLE entry_names USING fts5(name, content='', contentless_delete=1,
  tokenize='trigram case_sensitive 0 remove_diacritics 1');      -- rowid = entries.id, name = display name
INSERT INTO entry_names (rowid, name)
  SELECT id, precious_display_name(name) FROM entries WHERE parent_id IS NOT NULL;
