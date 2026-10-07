# Design

## Context

See proposal.md for why. Gems is three `review_rows` lists, written by `review.Refresh` (the relate job's after hook) and read through `GET /api/gems`:
- `gems_unique` and `gems_only_in_copy` are removed with Gems;
- `gems_rescue` becomes the eighth card.

`review_rows.list` has a `CHECK` that names every list (`0002_content.sql`). `review_row_sources` references `review_rows(id)` with `ON DELETE CASCADE`. Connections run with `foreign_keys` on.

## Decisions

### D1. The list is renamed `rescue`, through a table rebuild (§11.4)

`0005_rescue.sql` gives `review_rows` a `CHECK` without the Gems lists and with `rescue`. A `CHECK` cannot be altered in place, so the migration:
1. creates `review_rows_v5` and `review_row_sources_v5`, the latter referencing `review_rows_v5`;
2. copies every row except `gems_unique` and `gems_only_in_copy`, mapping `gems_rescue` to `rescue`, and copies the source rows of the rows kept;
3. drops `review_row_sources`, then `review_rows`. With the dependent table gone first, no cascade fires;
4. renames `review_rows_v5` to `review_rows` (SQLite rewrites the reference in `review_row_sources_v5`), then `review_row_sources_v5` to `review_row_sources`;
5. recreates the seven indexes.

`review_state` is untouched: the copied rows are the current generation, so the cards read exactly as before, minus Gems. This is schema version 5. The r2b binary refuses it (Q8), and the upgrade notes say so.

Rejected alternatives:
- Keeping `gems_rescue` as the stored name, with an API alias `rescue`, keeps a dead name and two spellings of one list.
- Leaving the `CHECK` as it is keeps values that nothing may write.
- Emptying the tables and marking `review_state.dirty` is simpler SQL. But the cards would read empty until the relate job finished (about 64 s on the reference server).

### D2. The open rule for rescue rows (spec "Decided rows leave the list")

A rescue row is open while `e.eff_decision <> 'keep' AND coalesce(e.decision, 'undecided') = 'undecided'`:

| File's own decision | Effective decision | Row |
|---|---|---|
| none | undecided | open |
| none | discard or later, inherited | open |
| none | keep, inherited | closed |
| own keep, discard, or later | (its own) | closed |
| own `undecided` | undecided | open |

`openSQL` gets one more `WHEN` arm, so `Cards` and `Rows` share it, and R2.5 holds for the card.

Rejected alternatives:
- The common rule (open while undecided) closes the row when the owner discards the program folder around the file, which is exactly the danger this card names.
- "Always listed", as Gems did, cannot let the card reach "Nothing left to review".

### D3. Card order and headline (spec "Opportunity cards")

`review.Cards` sorts the rescue card first while it has open rows. Otherwise all cards rank by open bytes, with `CardLists` order as the tie break; `CardLists` lists `rescue` first. The UI leads the rescue card with its row count, the way it leads a card whose open rows hold no bytes. The card's basis is `rules`.

Rejected: ranking it by bytes. A few KiB of files would put it last, buried as Gems' rescue section was.

### D4. Row order and the group of a row

A rescue row's `sort_key` is the file's bytes, and the list pages largest first, as every card does. Each row carries its outermost group, as `review_rows.group_id` already does. The review list shows "inside <group path>", with a link to that folder on the Map.

Rejected: ordering by group, as Gems did (groups largest first, then path, ascending). That would keep an ascending paging case for this list alone.

### D5. What is removed, and what stays

Removed:
- `GET /api/gems`. With no route, it is a 404 like any unknown API path, with no redirect or alias;
- `review.GemLists`, `IsGems`, `ListGemsUnique`, `ListGemsOnlyInCopy`, `uniqueGems`, `onlyInCopyGems`, `gemKinds`, and the ascending Gems paging;
- the UI's `gems/` module, `api/gems.ts`, the `/gems` route and menu entry, and the `gems` query root and i18n keys. An old `/gems` link lands on the app's not-found page;
- in the ground truth, the Gems sections and the `personal` declarations that only "unique" used.

Stays:
- the panel's "No other copy" with its checked share (R2.7);
- Search's `dup=unique`;
- Compare and Similar folders;
- `rescueGems`, renamed `rescueRows`.

### D6. Ground truth shape

`ground_truth.json` replaces `gems: {unique, rescue, only_in_copy}` with a top-level `rescue: [{path, group}]`, in the card's order (file bytes descending, then path). The e2e `env.ts` follows.

## Interfaces

- `internal/review`:
  - `ListRescue List = "rescue"`;
  - `CardLists = []List{ListRescue, ListDuplicates, …, ListLeftovers}`;
  - `List.Valid() == List.IsCard()`;
  - `Cards(ctx, q, src) ([]Card, error)` returns eight cards, ordered per D3;
  - `Row.Group` is set for rescue rows, as well as for unpacked-archive rows.
- **`GET /api/opportunities`:** `cards[]` gains `{"list":"rescue", …}` with the same fields as every card (`bytes`, `rows`, `decided_rows`, `decided_bytes`, `basis:"rules"`).
- **`GET /api/opportunities/rescue?source=&cursor=&limit=&decided=`:**
  - pages as every card does;
  - each item gains `group`, the group's entry row. `group` is `null` on the other lists' items;
  - errors are unchanged: 404 `not_found` for an unknown list or source, 400 `invalid_request` for a bad cursor or limit.
- **`GET /api/gems`:** 404.
- **Bulk decisions:** "select all" on the rescue list uses the existing selection path for card lists (`selection_id` of the list's open rows). Rows are files; no member can be a rescue row.
- **Ground truth:** `rescue: [{"path": string, "group": string}]`.

## Concurrency

- **Refresh:** it writes a new generation in its own transaction and flips `review_state.gen` after, as today. Only the list name of the rescue rows changes.
- **Open rule:** it is evaluated at read time against live decisions. A decision committed just before a read shows in it; one committed just after shows in the next read, as for every card.
- **Migration:** it runs at startup, before the job runner starts, so no refresh overlaps it.

## Risks / Trade-offs

- [Schema 5 is one-way] → The deploy takes a `precious backup` first, as for 0003. The upgrade notes say so.
- [Table rebuild with foreign keys on] → The migration test upgrades a v4 database that holds duplicates rows with source rows, rescue rows, and Gems rows. It checks:
  - the row counts and the renamed list;
  - that `PRAGMA foreign_key_check` is empty;
  - that the indexes exist.
- [Owner bookmarks of `/gems`] → They land on not-found; there is one owner, who chose the removal.

## Addendum: decisions made during implementation

- **U1.** `CardList` (Home and Opportunities) keeps ranking the cards itself, as D3 does: the rescue card first while it has open rows, then by open bytes. The server's order and the UI's agree, and the UI test still feeds the cards unordered.
- **U2.** The rescue card heads with "1 item" / "N items" through the zero-bytes path, since a row can be a saves folder. Like those cards, it shows no rows line and no open bytes. Its decided line keeps its bytes.
- **U3.** "Inside <group path>" links to `/map/<group>?entry=<group>`, the folder's place on the Map with its details, as the panel's "Show in Map" does for a folder. The row's own path still opens the detail panel in place.
- **U4.** The e2e R2.7 test read a file with no other copy from the Gems truth. It now picks a file of `Documentos` that no duplicate group of the ground truth holds.
- **B1. Migrated rescue rows take their bytes as sort key.** `0005_rescue` sets `sort_key = bytes` on the `gems_rescue` rows it keeps, so they page per D4 before the next refresh rewrites them.
- **B2. Only outermost indicators are rows.** `rescueRows` drops an indicator that lies inside another listed indicator, as every card keeps only its outermost matches ("A row SHALL be the outermost entry that matches its card"; "No byte SHALL be counted twice in one card"). On the corpus the folder `Jogos/Need for Speed Underground 2/save` is a row and `save/Joao/profile.sav` inside it is not; the ground truth lists 2 rows from its 3 declarations. A row can therefore be a folder. 0005 copies existing rows as they are, and the next refresh rewrites them. Rejected: listing nested indicators, as `rescueGems` did, which counts the bytes inside a listed folder twice.
- **B3. Ties in bytes page by path.** `rescueRows` writes its rows in reverse card order, so among rows of equal bytes the higher ID, which pages first, has the smaller path. The list then pages exactly in the ground truth's order (bytes descending, then path).
- **B4. Ground-truth paths are display paths.** `rescue: [{path, group}]` holds the display form of each path (as `Entry.path`), with no base64; every rescue path in the corpus is valid UTF-8.
- **B5. `group` only on rescue rows.** `Row.Group` is still set on `unpacked_archives` rows (the folder the archive was unpacked into, used for their relation), but the review row JSON serves `group` only for `rescue` rows and `null` elsewhere, per the Interfaces.
