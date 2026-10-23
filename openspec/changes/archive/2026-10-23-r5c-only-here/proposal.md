# Proposal

## Why

On 2026-10-22 the owner asked for an easy way to plug in an external disk and see whether it holds files that exist nowhere else. He also asked to tell copies on the same source apart from copies on other sources, and he wants the mode for any source.

Precious knows every file's copies (§6.4). The Map shows only "Has copies", a share counted over every source (ADR 0010); nothing says which content would be lost with one source.

A read-only count on the reference server shows what the answer looks like. Counting archives by their own bytes, the owner's 487 GB external disk holds:
- 351 GB with no other copy;
- 132 GB found only on that disk, repeated inside it and counting each copy. Of this, 94 GB sits beside its own zip or tar on the same disk;
- 3.5 GB with a copy on his other source.

The Gems list of unique files answered part of this. It was removed because it put 48,000 files in one list (ADR 0009).

On 2026-10-22 the owner chose:
- a mode of the Map rather than a new screen ("A");
- copies on the same source in a class of their own, each naming its other copy ("A").

This change is milestone **R5c**. It sits between R5 and R6, and is not in §14's list; ADR 0013 records it.

## What Changes

- **Where copies are.** Each file gets a place relative to its own source:
  - only copy;
  - copies only on this source;
  - a copy on another source;
  - not checked yet;
  - unreadable.

  Copies are counted as for duplicate groups:
  - a hard-link set is one copy;
  - members of complete archives count;
  - sources that are not connected count, and are marked;
  - the quarantine counts for nothing.
- **A complete archive is placed by its members.** Its bytes split by its members' places. A copy of the archive file itself counts for every member.
- **Folder figures.** Every folder carries its subtree's files and bytes per place, recomputed together with its "Has copies" figures. System and temporary files are counted apart, so they do not make a folder an "only copy".
- **The Map's "Only here" mode**, kept in the address:
  - rows: the only copy highlighted, only on this source less, a copy on another source dimmed, and not checked marked as not known. Each row says its place in words;
  - folder rows: a bar of their places and their bytes only on this source. The open folder shows its totals, and the share checked so far across all sources;
  - an option to hide what has a copy on another source, and a sort by bytes only on this source;
  - a treemap color "Where copies are";
  - a file with copies names one, with the count of the rest: on another source, on this source, or inside an archive;
  - a folder whose content all exists in another folder names that folder, with a link to Compare;
  - inside an archive, members follow the same rules.
- **Detail panel.** A folder's or archive's files and bytes per place, and a file's place above its copies.

## Acceptance

R1.2, R1.10, and R2.7 keep passing. The new scenarios in the delta specs are checked:
- by unit tests over a synthetic index of three sources, one of them offline;
- by a test that compares the stored folder figures with an independent computation over the corpus and a second source;
- in a real browser, on the corpus plus a second source holding copies of some corpus files;
- on the reference server, where the external disk's root figures must equal an independent count, with the owner's sign-off.

Deferred:
- **pt-BR strings for the mode:** R7, with the rest of pt-BR.
- **A Search filter by place:** not planned; Search keeps `copies`, `elsewhere`, `unique`, and `unchecked`.
- **Copying the only copies onto another source:** not planned.
- **A per-folder figure for copies on unconnected sources:** not planned. File rows and the panel show it (design D11).

## Capabilities

### New Capabilities
None.

### Modified Capabilities
- `duplicates`:
  - a file's place relative to its source;
  - archives placed by their members;
  - folder figures per place;
  - disposable files counted apart.
- `inventory-explorer`:
  - the Map table and treemap, which gain the mode's sort and color;
  - the Only here mode and its hide option;
  - rows that name another copy;
  - the detail panel's figures per place.

## Impact

- **Migration `0009_places.sql`:** it adds `entry_places`, one row per folder and complete archive, and marks relations dirty, so the first start runs one relate pass.
- **Go:**
  - `internal/relations`: the snapshot keeps each content's sources and writes `entry_places`;
  - `internal/search`: the place SQL for files and members;
  - `internal/content`: one preferred copy per file;
  - `internal/web/api`: the `places`, `hide`, and `sort=only_here` parameters, and the row fields;
  - `internal/domain`: the place type and the archive split.
- **UI:** the Map toolbar, the `EntryTable` place column and row styles, `map/colors.ts`, the detail panel, and `en.ts`.
- **Docs:** `docs/operator.md` (Map, Duplicates and Compare, read endpoints, upgrade) and ADR 0013.
- **No change** to I1–I9. Nothing is written to sources, and no response changes when the mode is off.
