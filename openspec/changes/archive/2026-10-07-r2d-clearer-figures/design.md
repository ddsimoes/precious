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
- **U1. The column hint's description is `hidden`, beside the header row.** The Has copies column is not sortable, so it has no header button: the `title` and `aria-describedby` go on its `columnheader`. The description it points to is a `hidden` span outside the header row. Inside the header it would join the header's name and text ("Has copies Files here…"), and a visually hidden span outside it would be read twice, once as table text and once as the description. A `hidden` element still gives its text to `aria-describedby`.
- **U2. Two hint keys.** `map.columns.duplicatedHint` is plain text, for the `title` and the description. `detail.duplicatedHint` carries the same words, with "Opportunities" a link to `/opportunities` (Trans), shown as a muted line under the figure in the panel.
- **U3. The panel line shows wherever the figure does,** for a file (0% or 100%) as for a folder, like the column header, which hints for every row.
- **U4. Keys replaced.** `detail.relationFigures` (bytes in common and both only-side counts) became `detail.inCommon` ("{{bytes}} in common"), and `similar.onlyIn` is gone. `units.files` stays, used elsewhere.
- **U5. The catalog test matches the word.** It refuses `duplicated` as a whole word, in any case, in every string of the English catalog. That catches the old label and legend bands, and leaves "duplicates" and "Duplicate folders and files".
- **U6. The installers card's help** reads "Setup programs and disk images, wherever they are." It said "and download folders".
- **U7. Docs.** `docs/operator.md` keeps the heading "Percent duplicated", the duplicates capability's term, so its anchor holds, and says the interface names it Has copies. The Map paragraph now says the column hides after Decision, as the table does (it said "after Changed and Suggestion"). The installers card's row of the cards table is left to task 2.1.
- **C1. Executables inside a program folder stay rows of the installers card.** A file of category `installer_download` inside a folder of category `application_installation` or `os_installation` was already a row of the installers card before r2d, and it remains one: the cards are independent, and a program folder is not an installer. On the reference index, after D1, there are 94 rows (about 4.6 GB, down from 64.6 GB). Of those, 21 lie inside a program folder (1.09 GB). The largest is a real 1.08 GB installer whose folder a rule takes for an installed application, and the rest of the real ones add up to about 4 MB of program parts. Leaving out rows inside program folders would hide that installer, so D1 stays as designed. The corpus's program executables (for example `WINWORD.EXE` under `Arquivos de programas`) are rows, as in r2c.
- **C2. Smoke (4.3).** An r2c binary scanned and hashed the corpus. Its installers card held 20 rows, `Downloads` among them. On a copy of that database, the r2d binary started with the old rows, and after a scan and its relate pass, the card held 30 rows. All of them are files of category `installer_download`, `Downloads/Setup.exe` and the three ISOs among them, with no `Downloads` row, and the card's bytes equal the sum of its rows. The relation of `Fotos - Copia` with `Fotos` still carries `only_here` and `only_there` in `GET /api/entries/{id}`. The embedded UI carries "Has copies", the hint, and "Old installers and disk images", and no "Duplicated" label. The script and its files were deleted.
- **C3. Owner sign-off (4.4), 2026-10-07.** Deployed on the reference server, where a scan and relate pass of both sources rewrote the installers card from 25 rows (64.6 GB, downloads folders first) to 94 file rows (90 open, 3.5 GB, plus 4 decided), largest first: two ISOs and an `.msi`. The owner looked at the installers card, the Map's "Has copies" with its hint, and a folder's relations, and accepted.
