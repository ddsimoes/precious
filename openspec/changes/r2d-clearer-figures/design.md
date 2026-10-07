# Design

## Context

See proposal.md for why. The three changes are independent:
- `review.ruleRows` maps categories to rule cards through `ruleLists`. Today `installer_download` (files) and `download_collection` (folders named `Downloads` and the like, rule `downloads_folder`) both go to the installers card, and a card keeps only its outermost matches. So a downloads folder hides every installer inside it.
- The UI labels `dup_bytes / total_bytes` "Duplicated" in the Map and Search tables, the treemap legend, and the detail panel.
- The panel's relations section (`ContentSections.tsx`) and `SimilarFoldersPage.tsx` print `only_here` and `only_there` from the relation JSON.

## Decisions

### D1. Downloads folders leave the installers card; their installers become rows (§11.4, deviation recorded in ADR 0010)

`download_collection` is removed from `ruleLists` and from `ruleRows`' category filter. Rules and categories are unchanged: a downloads folder is still classified `download_collection`, shows as such in the Map, and counts in composition. The installer files inside it (`installer_download`: `setup*.exe`, `*install*.exe`, `*.msi`, disk images, a lone `*.exe`) are now outermost matches, so they become rows. The card's title becomes "Old installers and disk images".

No migration is needed. The current generation of review rows keeps its downloads-folder rows until the next relate pass, which follows every scan, or hashing, or a restart with relations dirty. The deploy restarts the server and then starts a scan, so the rows are rewritten within minutes.

Rejected alternatives:
- A separate "downloads folders to sort" card. That is an organizing task (R3), and the owner chose against it.
- Counting only the installer bytes inside a downloads folder, still as one row. One decision on that row would then decide the whole folder, his own files included.

### D2. "Has copies" is a label change only (§11.2; ADR 0010)

The figure (`duplicated_bytes / total_bytes`, with "so far" while unchecked) is unchanged. The label, the treemap color mode, and the legend bands change wording ("Has copies", "Less than 25% has copies"…). The column header and the panel line gain a hint: "Files here that also exist elsewhere, counting every copy. Not the space you could free: see Opportunities."

Where it is shown:
- **On the table header:** the existing header button gets a `title`, and a visually hidden description goes in `aria-describedby`, so the hint reaches the keyboard and screen readers too;
- **In the panel:** a muted line under the figure.

Rejected: a per-folder "space you could free" figure. Its meaning depends on which copy the owner keeps, so the per-folder sum misleads in a new way. Home's duplicates card already gives the total.

### D3. Only Compare counts files only on one side (§11.6, §11.8; ADR 0010)

The panel's relation rows and the Similar folders rows drop their "only here / only there" figures. They keep the kind, the other side, and `matched_bytes` ("X in common"), plus the Compare link.

The relation JSON keeps `only_here` and `only_there`, and the `relations` table keeps its columns: §6.4 has relations record them, and API clients other than this UI may read them. The operator docs say that Compare's buckets are the counts to act on, and why the API's content-based figures can differ from them (same name with different content, and archive members).

Rejected alternatives:
- Making Compare's summary repeat the content-based total. It would still disagree in a way that needs explaining, because of archive members.
- Removing the API fields. That breaks a documented contract for no gain to the owner.

## Interfaces

- `internal/review`: `ruleLists` loses `download_collection`; no signature changes.
- **Read API:** unchanged. `GET /api/opportunities/installers` serves file rows where it served folder rows.
- **UI:**
  - i18n keys renamed in place (`map.columns.duplicated` and the detail and legend keys keep their names; their English text changes). One new hint key per place.
  - `SimilarFoldersPage` and `ContentSections` stop reading `only_here` and `only_there`. The `Overlap` and `Relation` types keep the fields, because the API serves them.

## Concurrency

There is no new job or write path. D1 changes what the relate job's after hook writes, inside the same transaction as before.

## Risks / Trade-offs

- [The installers card grows from about 25 rows to many file rows on the owner's disk] → Rows are ordered largest first, and select-all applies as on every card. The bytes drop to real installers.
- [A downloads folder full of installers no longer shows its total on the card] → The Map shows its size, and its installers are each a row.

## Addendum: decisions made during implementation

- **B1.** D1 takes `download_collection` off the installers card only. An empty downloads folder is still a row of the leftovers card, which selects empty folders by their emptiness, not by category.
- **B2.** The review test of "A downloads folder is not an installer" asserts more than the scenario names: every installer and disk image the corpus's ground truth asserts is a row with its own size, every row is of category `installer_download`, and no `download_collection` folder is a row, while `Downloads` keeps that category. The API test checks the same rows through `GET /api/opportunities/installers`, and that the card's bytes are the sum of its rows (R2.5).
