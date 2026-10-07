# ADR 0009: Remove Gems; keep its rescue list as an opportunity card

- Status: accepted
- Date: 2026-10-07
- Context: `precious-spec-v0.3.md` §11.4, §11.7, acceptance scenarios R2.4 and R2.6; OpenSpec change `r2c-remove-gems`.

## Context

Gems (§11.7) was meant to answer "what is valuable?" with three lists:
- personal media and documents with no other copy;
- the owner's files inside program or disposable groups (rescue candidates);
- files that exist only on one side of an `overlap` relation.

R2 delivered it. On the owner's 780 GiB archive it did not answer the question:
- **"Unique"** listed about 48,000 files, one row each. It mixed camera photos with WhatsApp statuses, stickers, and voice notes, `.thumbnails`, and downloaded films. The index holds metadata and copies, which say that a file has no other copy, not that it is valuable.
- **"Only in a copy"** repeated about 70% of "unique" and began with `.DS_Store`.
- **"Rescue"** held 3 rows, and the walkthrough found it the most useful part. It names a real danger: the owner's own spreadsheet inside `Microsoft Office`, which a cleanup of programs would take with it.

After R2b, the owner judged Gems unworkable and asked for its removal. He chose to keep the rescue list, as an opportunity card.

## Decision

1. **Gems is removed:** its page, its menu entry, `GET /api/gems`, and the "unique" and "only in a copy" lists with their computation.
2. **The rescue list becomes the opportunity card "Your files inside programs"** (§11.4). It comes first while it has open rows, and leads with its item count, since a row may be a file or a folder such as saved games. A row stays open until the owner decides the item itself or it is kept, so that a decision on the program around it does not hide it.
3. **What "unique" and "only in a copy" answered stays available where it is precise:**
   - the detail panel says whether a file has another copy, with the checked share (R2.7, I7);
   - Search filters files with no other copy;
   - Compare and Similar folders show what is only on one side.
4. **The acceptance scenarios change:**
   - **R2.6** reads "the rescue card lists `Backup_PC_2004/C/Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls`, inside `Microsoft Office`";
   - **R2.4** no longer lists Gems among the places a copy is decided from;
   - §11.7 is withdrawn, its rescue bullet moving to §11.4.

## Alternatives rejected

- **Keep Gems and filter the noise:** hide or demote WhatsApp, `.thumbnails`, and downloaded films. These would be heuristics tuned to one disk, and a different archive brings different noise. The list would still claim a judgment the system cannot make.
- **Restructure Gems:** tabs with counts, grouped by folder, with thumbnails. A better presentation of the same claim is still the same claim.
- **Remove all three lists:** the rescue information would then show only folder by folder in the detail panel, through the veto, with no list across the archive.

## Consequences

- Migration `0005_rescue` rebuilds `review_rows`. It keeps the rescue rows under the list `rescue` and drops the two removed lists. Schema 5 is one-way; an r2b binary refuses it.
- The ground truth keeps only the rescue list.
- A "what is valuable" view may return when the index knows more than metadata and copies: camera and date metadata (R5), or a model's judgment (R6). It would come back as a new decision, not as this list.
