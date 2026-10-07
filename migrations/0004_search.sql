-- R2b search index (r2b design D8, addendum Q1). Each index costs every
-- scan's writes, so only what a plan test in internal/search proves
-- necessary is here. The size indexes D8 listed as candidates are not: a
-- page by bytes that the duplicate filter drives merges the size-ordered
-- ranges of file_content_by_source, and the first page with no filter reads
-- entries once within the 1 s target at 2 million entries.

-- The entries that could not be read (state=unreadable): few, so a partial
-- index costs the scan almost nothing. Search names it (INDEXED BY): without
-- statistics SQLite would read the source's entries instead.
CREATE INDEX entries_unreadable ON entries(source_id, path) WHERE state = 'unreadable';
