# Proposal

## Why

R2 was accepted on the owner's 780 GiB archive on 2026-10-06. The product spec puts three owner features in a short step after R2, before R3 (§14): owner category overrides (§6.6), owner-marked groups (§6.5), and scheduled rescans (§7). Without them:
- the owner cannot correct a rule that is wrong about his folders;
- he cannot group a photo event or a project for review;
- the index ages between manual scans.

The R2 walkthrough on his dataset also found Search, Map, and Compare problems. The owner chose to fix them in this same step ("Search and Map go to the short change after R2"). The most important ones:
- a search for `confraternizacao` misses `Confraternização`;
- results do not say where a file is;
- duplicate filters take 1.5–3 s;
- unreadable folders look empty;
- the 280 similar-folder relations appear in no list;
- Compare hides the other copy's location.

## What Changes

- **Owner category overrides.**
  - The owner sets an entry's category to one of the sixteen fixed categories, or returns it to the rules.
  - The override wins over the rules (§6.6 precedence); family, triage, and veto follow from it.
  - It survives rescans and rule changes (I4), is audited, and is shown with its source ("set by you" versus the rule that would apply).
- **Owner group marks.** The owner marks a folder as one item for review, or unmarks a group that the rules made. Composition, notable entries, and Gems follow the mark, exactly as they follow a rule group today.
- **Scheduled rescans.**
  - Each source gets an optional schedule (off, daily, or weekly, at a chosen time).
  - A scheduler starts the scan when it is due, skips offline sources, and catches up once after downtime.
  - The source card shows the schedule and the next scan. Hashing and relations follow every scan, as they already do.
- **Search:**
  - names match without accents;
  - results show the folder each one is in, and a file's copy count;
  - a source filter, and the last source chosen is remembered across screens;
  - a filter for entries that could not be read, linked from Home's warning;
  - faster duplicate filters and default pages, against a 2-million-entry target.
- **Map and detail panel:**
  - keyboard row navigation;
  - quieter Decision column, which no longer hides Duplicated first;
  - unreadable folders marked as such, not shown as empty;
  - single-child folder chains collapsed in the path;
  - unopened archives (7z, rar, nested zips) said to be unopened, with "Open as a folder" as the main action for listed ones;
  - focus kept after the viewer closes;
  - a clearer remainder block in flat-folder treemaps.
- **Compare:**
  - each item shows both sides' paths;
  - an extra copy says it is one;
  - opening a pair computes it once, not twice.
- **Opportunities:**
  - a read-only "similar folders" list of `overlap` relations, each with Compare;
  - cards show how much was already decided.
- **Startup:** the duplicate API reads that the event stream caused on load go away.

Not in this change. These wait for the owner's decisions (design Open Questions), one at a time:
- Gems' sections and what counts as valuable;
- the installers card holding whole download folders;
- the Map's "Duplicated" wording;
- the panel's relation counts versus Compare's.

## Capabilities

### New Capabilities
None.

### Modified Capabilities
- `classification`: owner overrides take precedence over rules for the category and the group flag, and explanations name the owner as the source.
- `owner-intent`: category overrides and group marks are owner intent. They are audited and survive rescans and rule changes.
- `file-index`: rescans run on an optional per-source schedule.
- `source-registry`: a source carries its rescan schedule and next scheduled scan.
- `inventory-explorer`:
  - Search ignores accents, shows locations, filters by source and by unreadable state, and meets a speed target;
  - the Map works from the keyboard and marks unreadable folders;
  - the remembered source;
  - the path collapses single-child chains;
  - dialogs return focus.
- `duplicates`: Compare shows both paths of an item and marks extra copies.
- `review-lists`: a read-only list of similar folders, and cards that show decided progress.
- `archive-contents`: archives that were not opened say so.

## Impact

- **Schema.** Migration `0003`:
  - `entry_overrides`;
  - source schedule columns;
  - the name index rebuilt with diacritic folding (repopulated through a Go SQL function, so non-UTF-8 names index exactly as the scanner writes them);
  - search indexes.
- **Go:**
  - `internal/rules` (owner precedence);
  - `internal/index` (applying overrides in the walk, a rescan after an override);
  - `internal/decisions` or a new `internal/overrides` (commands, audit);
  - `internal/sources` (schedule command, JSON);
  - a scheduler started by `serve`;
  - `internal/search` (folding, drivers, two-stage page, state filter);
  - `internal/relations` (Compare's first group, similar-folders read);
  - `internal/review` (decided totals);
  - `internal/web/api`.
- **UI:**
  - detail panel classification controls;
  - Sources schedule form;
  - Search columns and filters;
  - Map keyboard and cells;
  - Compare item rows;
  - Opportunities similar folders and card figures;
  - the event stream's first refresh;
  - Dialog focus;
  - remembered source.
- **Docs.** `docs/operator.md` gets overrides, groups, schedules, Search, and the similar folders list.
- **Tests:**
  - Go unit and API tests;
  - Vitest;
  - Playwright additions;
  - a slow search target at 2 million entries;
  - an owner smoke test on the regression corpus and his dataset.
- **No change** to I1–I9. A scheduled scan is a scan like any other (I1, I4).
