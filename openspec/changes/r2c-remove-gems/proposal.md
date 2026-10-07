# Proposal

## Why

After R2b, the owner judged Gems (§11.7) unworkable on his 780 GiB archive and asked for it to go. Only metadata and copies are known, so the system cannot tell what is valuable:
- "unique" is 48,000 rows on his disk, one per file. Among them are WhatsApp statuses, stickers, and voice notes, `.thumbnails`, and downloaded films;
- "only in a copy" repeats about 70% of "unique" and starts with `.DS_Store`.

Both answer a narrower question that other screens already answer better:
- "this file has no other copy" is in the detail panel (R2.7) and in Search's duplicate filter;
- "what is only on one side" is in Compare and in Similar folders (r2b).

One section is useful: rescue. It lists the owner's own files inside program or disposable folders, such as the wedding budget inside `Microsoft Office`. It is 3 rows on his disk and names a danger nothing else lists. The owner chose to remove Gems and to keep rescue as an opportunity card. ADR 0009 records this deviation from §11.7 and from R2.4 and R2.6.

## What Changes

- **BREAKING** Gems is removed: the page, its menu entry, `GET /api/gems`, the "unique" and "only in a copy" lists, and their computation.
- A new opportunity card, **Your files inside programs** (list `rescue`), holds the former rescue rows. Each row is a file of the owner's inside its outermost program or disposable group, and names that group.
- A rescue row stays open until the owner decides the file itself, or until it is kept. An inherited discard keeps it open: deciding a program folder must not hide the files of the owner's inside it.
- While the rescue card has open rows, it comes first among the cards and leads with its file count.
- Migration `0005_rescue` rebuilds `review_rows`. It keeps the rescue rows under the new list name and drops the two removed lists. This is schema version 5, one way like 0003 and 0004.
- The ground truth (`internal/corpus`, `gencorpus`) keeps only the rescue list. Its Gems fields and declarations are removed.
- Docs: the operator manual, the README, and the project context in `openspec/config.yaml` drop Gems and describe the rescue card.

## Acceptance

Milestone R2, as amended by ADR 0009:
- **R2.6** becomes "the rescue card lists `Backup_PC_2004/C/Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls`, inside `Microsoft Office`".
- **R2.4** drops Gems from the places a copy is decided from.
- **R2.5** (card bytes equal the review list) holds for the new card.

Deferred: a "valuable" list built on media metadata (camera, dates, events) waits for R5 (§14). A model-assisted one waits for R6. Neither is in scope.

## Capabilities

### New Capabilities
None.

### Modified Capabilities
- `review-lists`: remove the Gems requirement; add the rescue card to the cards, its ordering, and its open rule.
- `inventory-explorer`: drop Gems from the navigation, the read API, the offline workflow, the layout check, and the remembered source.
- `duplicates`: an unchecked file is no longer described through Gems.
- `classification`: overrides feed composition, notable entries, and review lists. Gems is gone.
- `owner-intent`: an override's effect no longer names Gems.

## Impact

- **Go:**
  - `internal/review` (lists, refresh, open rule, card order);
  - `internal/web/api` (gems handler removed, card JSON);
  - `internal/corpus` and `tools/gencorpus` (ground truth);
  - `cmd/precious` (R2 serve test);
  - a new `migrations/0005_rescue.sql` and its upgrade test.
- **UI:**
  - remove `web/ui/src/gems/*`, `api/gems.ts`, the route, the menu entry, and the i18n keys;
  - add the rescue card's copy and row rendering;
  - update the references in events, the source choice, and bulk selection.
- **e2e:** `precious.spec.ts` R2.6 becomes the rescue card test; `layout.spec.ts` drops Gems; `env.ts` truth types.
- **Docs and specs:**
  - `docs/operator.md`, `README.md`, `openspec/config.yaml`;
  - `docs/adr/0009-remove-gems.md`;
  - delta specs for the five capabilities above.
- **No change** to I1–I9, the decisions model, or any other card.
