# ADR 0010: Clearer figures: installers, "Has copies", and where only-side files are counted

- Status: accepted
- Date: 2026-10-07
- Context: `precious-spec-v0.3.md` §6.4, §11.2, §11.4, §11.6, §11.8; ADR 0009; OpenSpec change `r2d-clearer-figures`.

## Context

The R2 walkthrough on the owner's 780 GiB archive found three figures that mislead:

- **The installers card** (§11.4: "old installers, disk images, and downloads") read 64.6 GB. Of that, 52.4 GB was one whole `Downloads` folder, holding 19.2 GB of the owner's own material. A card keeps only its outermost matches, so the downloads folder hid the installers inside it, and the card presented the folder as something to clear.
- **The Map's percent duplicated** (§11.2) read "Duplicated 49% (379 GiB)" at the root. That counts every file that has a copy, every copy included. Home's duplicates card, the space that can be freed, read 125 GiB. Nothing explained the difference, so the owner read 379 GiB as space to free.
- **Files only on one side** of a relation were counted in two ways:
  - the detail panel and Similar folders count by content, so a file whose content is not on the other side counts, the same name with an edit included, and so do archive members;
  - Compare (§11.6) splits those into "only on the left or right" and "same name, different content", and does not count archive members.

  The same pair therefore read 11,647 in the panel and 10,223 in Compare.

The owner decided each fix on 2026-10-07, one at a time.

## Decision

1. **The installers card lists installers and disk images only, wherever they are.** A downloads folder is no longer a row of its own: the installers inside it are. The card is "Old installers and disk images". Downloads folders keep their category and stay visible in the Map and Search. Sorting them is organizing (R3), not cleanup.
2. **The duplicated share is labelled "Has copies".** A hint says that it counts the files that also exist elsewhere, every copy included, and that the space to free is on Opportunities. The figure itself is unchanged.
3. **Compare alone counts the files only on one side.** The detail panel's relations and Similar folders show the bytes in common and a link to Compare. Relations still record their content-based counts (§6.4), and the read API still serves them.

## Alternatives rejected

- **A "downloads folders to sort" card.** It asks for organizing, which belongs to R3.
- **Counting only the installer bytes inside a downloads folder, on one row.** One decision on that row would decide the whole folder, the owner's files included.
- **A per-folder "space you could free" figure.** It depends on which copy the owner keeps, so summed per folder it misleads in a new way. Home's card gives the total.
- **Making the panel and Compare count alike.** Archive members and edited files make the two counts differ for good reasons; aligning them means explaining one in terms of the other on every screen.

## Consequences

- No migration. The next relate pass rewrites the installers rows.
- The owner's installers card shows many file rows instead of a few folder rows, and its bytes drop to real installers.
- The read API is unchanged.
