# Proposal

## Why

The R2 walkthrough on the owner's 780 GiB archive found three figures that mislead. On 2026-10-07, after r2c, the owner chose a fix for each:

1. **The installers card** (§11.4) reads 64.6 GB on his server. 52.4 GB of it is one whole `Downloads` folder, which holds 19.2 GB of his own material. The card counts every downloads folder as if it were an installer.
2. **The Map's "Duplicated" column** (§11.2) reads 49% (379 GiB) at his root. That figure counts every file that has a copy elsewhere, every copy included. The owner reads it as space he could free, which Home's duplicates card puts at 125 GiB.
3. **"Only here / only there" counts disagree.** The detail panel and Similar folders count by content: an edited photo with the same name counts as only on one side. Compare puts those in "different". So the same pair reads 11,647 in the panel and 10,223 in Compare.

## What Changes

- **Installers card:** it lists installer files and disk images only, wherever they are. A downloads folder is no longer a row of its own: the installers inside it become rows. The card is renamed "Old installers and disk images". The downloads folders stay in the Map and in Search. Sorting them is organizing, which is R3.
- **"Has copies":** the Map, Search, and the detail panel name a folder's duplicated share "Has copies", and the treemap's color legend follows. A hint on the column says that this counts the files that also exist elsewhere, every copy included, and is not the space you could free, which Opportunities shows. The figure itself is unchanged.
- **Compare alone counts what is only on one side:**
  - the detail panel's relations and the Similar folders list show the bytes in common, with a link to Compare, and no longer show files only on each side;
  - relations still record those counts, and the read API still serves them (§6.4).

## Acceptance

R2 (amended by ADR 0009) keeps passing:
- **R2.5** holds for the narrower installers card;
- the Compare scenarios R2.2 and R2.3 are unchanged.

The changed behaviors are checked by the scenarios in the delta specs. Deferred:
- a card for organizing downloads folders (R3);
- a per-folder "space you could free" figure (not planned).

## Capabilities

### New Capabilities
None.

### Modified Capabilities
- `review-lists`: the installers card's scope; the Similar folders row content.
- `inventory-explorer`: the "Has copies" naming of the duplicated share.
- `duplicates`: Compare is the one place that counts files only on one side.

## Impact

- **Go:**
  - `internal/review` (the category-to-card mapping, and the downloads category leaving the installers card), with its tests;
  - the corpus and API tests that count installers rows.
  - No migration: the next relate pass rewrites the rows.
- **UI:**
  - `i18n/en.ts` (labels, hint, legend, card title);
  - the Map and Search column header hint;
  - the detail panel's relations section;
  - `opportunities/SimilarFoldersPage.tsx`;
  - Vitest updates.
- **e2e:** the Similar folders and R2.5 tests where they read the removed figures.
- **Docs:** the Map, Opportunities, Compare, and detail panel sections of `docs/operator.md`.
- **No change** to I1–I9, the relations data, or the read API's fields.
