# cleanup Specification

## Purpose
Defines how the owner cleans up a source (§10.1–§10.6): cleanup plans drafted from discard decisions, which move items to a quarantine folder at the source's top where nothing is deleted and everything can be restored; the pre-delete check, which reads every file of a chosen set again and looks for a verified copy outside it; the confirmations the check asks for; the purge, which deletes a checked set for good and reports the space freed; and the CSV export of every action.

## Requirements

### Requirement: A cleanup plan is drafted from discard decisions
`plan-cleanup` SHALL draft a plan for one source:
- **Scope.** The plan covers the source's explicit discard decisions, or only the rows of one review list whose entries are explicitly discarded.
- **Items.** Each item is a topmost entry with its own `discard`; discards nested inside it are folded into it (§10.1).
- **The plan's answer.** It reports, from the index only:
  - its items with their bytes and files, and its totals;
  - the bytes with a known copy outside the plan and outside quarantine;
  - the bytes with no known copy;
  - the items that hold personal material.
- **Identity.** Each item records the identity its entry had when drafted (§6.8).
- **Changes.** Drafting changes nothing on disk or in the index.
- **The reserved name.** When a folder named `.precious-quarantine` that Precious did not create sits at the top of the source, drafting SHALL fail with `409 quarantine_name_taken`.

#### Scenario: Drafting from the discards of a source
- **WHEN** a source has a discarded folder, a discarded file inside it, and a discarded file elsewhere, and the owner drafts a cleanup plan for it
- **THEN** the plan holds two items, the folder and the file elsewhere, with their bytes and files, and nothing has moved

### Requirement: A discarded folder that holds a kept entry is blocked
An item whose subtree holds an entry whose effective decision is `keep` SHALL be `blocked` with reason `holds_kept`. The plan SHALL list the item with the kept entries that block it, 100 to a page, and their count. No part of a blocked item SHALL be quarantined, and the rest of the plan SHALL proceed (§10.1, I5). Before it moves anything of an item, the executor SHALL check again that:
- the item's own decision is still `discard`;
- nothing in its inclusive subtree is effectively `keep`.

When either fails, the item SHALL end `blocked` or `changed`, with nothing moved.

#### Scenario: R4.1 A kept file inside a discarded folder
- **WHEN** a discarded folder holds a kept file, another discarded file exists elsewhere, and the owner drafts and runs a cleanup plan
- **THEN** the folder is listed as blocked, with the kept file named. No entry of the folder is quarantined, and the other file is quarantined.

#### Scenario: A keep set after drafting
- **WHEN** the owner keeps a file inside a planned folder, or keeps the planned folder itself, after drafting and before running the plan
- **THEN** that item ends `blocked` or `changed` with nothing moved, and the plan's other items run

### Requirement: Duplicates are never removed together
A plan drafted from the duplicates list SHALL give its items the ground `duplicate`. It SHALL refuse any item that would leave a content group with no copy outside the plan and outside quarantine (`last_copy`), or put both sides of a relation in the plan (`both_sides`). Before quarantining such an item, the executor SHALL re-read and verify a copy of each of its files that stays outside the plan and outside quarantine, and is not being quarantined by another plan. It SHALL end the item `changed` with reason `no_verified_copy`, and nothing moved, when one cannot be verified (§10.1, §10.2).

#### Scenario: R4.4 The last copy of a group stays
- **WHEN** the owner discards both copies of `curriculo.doc` listed on the duplicates card and drafts a plan from that list
- **THEN** one copy is planned and the other is refused as `last_copy`, and running the plan leaves one copy outside quarantine

#### Scenario: A copy that changed before the run
- **WHEN** the copy that stays outside a duplicates plan changes on disk before the plan runs
- **THEN** the item that relied on it ends `no_verified_copy`, and its file is not quarantined

### Requirement: Approval freezes the plan
A cleanup plan SHALL be immutable once drafted. Running it is the owner's approval, and a changed scope needs a new plan. A plan SHALL be runnable for 24 hours. Running it SHALL quarantine exactly its planned items, in order, through the executor, and SHALL answer `202` with the job. An item whose entry differs from its draft-time identity (location, kind, size, times, or, for a folder, its totals), on disk or in the index, SHALL be skipped as `changed` and reported. The rest of the plan SHALL continue (§10.1, §10.2).

#### Scenario: R4.2 A changed entry is skipped
- **WHEN** a planned file is modified on disk after drafting, and the plan runs
- **THEN** that item ends `changed` with the file left in place, it is reported in the plan's results, and the other items are quarantined

#### Scenario: A rescan between draft and run
- **WHEN** a planned file is modified and the source is rescanned before the plan runs
- **THEN** that item still ends `changed`, because it no longer matches its draft

### Requirement: Quarantine keeps items whole and restorable
Quarantining an item SHALL move it, through the executor's no-replace rename, to `.precious-quarantine/<plan>/<item>/<original name>` at the top of its source. It SHALL then write a record of its origin beside it: the source, the original path, the entry, the plan, and the time. A quarantined entry and everything inside it SHALL keep their IDs, decisions, tags, overrides, and digests. Quarantine SHALL free no space (§10.3).

#### Scenario: A quarantined folder keeps its intent
- **WHEN** a discarded folder with a tagged file inside it is quarantined
- **THEN** it sits under `.precious-quarantine` with its origin record beside it, its entries keep their IDs, own decisions, and tags, and Home shows its bytes as in quarantine

### Requirement: Restore never merges or overwrites
`plan-restore` SHALL plan quarantined items back to their original paths.
- **Conflicts.** An item SHALL be a conflict that asks for a destination when its original path is taken, or when its original folder is gone or is itself in quarantine. With `destination_id`, those items go into that folder under their original names.
- **Destinations.** A destination in the quarantine SHALL be refused with `400 invalid_request`.
- **What it keeps.** A restored item SHALL keep its IDs, decisions, tags, and digests.
- **Clean-up.** Its origin record and its item folder SHALL then be removed from the quarantine (§10.3).

#### Scenario: R4.3 Restore to the original path
- **WHEN** the owner restores a quarantined folder whose original path is free
- **THEN** it is back at that path with the same IDs, its origin record is gone, and it is no longer in quarantine

#### Scenario: R4.3 Restore when the path is taken
- **WHEN** a new file now holds a quarantined file's original path, and the owner restores it
- **THEN** the plan shows the item as a conflict and asks for a destination, and with one chosen, the file moves there and the new file is unchanged

### Requirement: The pre-delete check proves what has a copy
`check-purge` SHALL start a check of a purge set, a chosen list of quarantined items of one source:
- **Recording.** The check SHALL record every entry below the items, of every kind, with its identity.
- **Reading.** It SHALL read every file in full, archive members included (§10.6).
- **Safe files.** A file SHALL be `safe` only when an identical copy outside the set and outside quarantine was read and verified during the check.
- **Unverified copies.** A file whose only copies are on offline sources SHALL be `copy_offline`, gated like a unique file.
- **Unique files.** Every other file with content SHALL be `unique`.
- **Unreadable items.** An item holding a folder that could not be read SHALL be reported `unreadable`, and cannot be purged.
- **The report.** It SHALL give the exact count and bytes of each verdict, and of the unique files the rules rank as `possibly_valuable`, `likely_junk`, and `uncertain`.
- **Changes.** The check SHALL change nothing on disk.

#### Scenario: R4.6 A purge set with copies and unique files
- **WHEN** the owner checks a purge set holding a duplicate file whose twin is outside quarantine, a unique photo, and a zip whose members are unique
- **THEN** every file and member is read, the duplicate is safe with its twin named, and the photo and the zip's members are unique, with their exact count and bytes

#### Scenario: A copy on an offline disk
- **WHEN** a quarantined file's only copy is on a source that is offline during the check
- **THEN** the file is reported as `copy_offline`, not safe

### Requirement: Unique files gate the purge
The purge SHALL be allowed only when every file of the checked set is `safe`, or confirmed:
- **Likely junk.** `likely_junk` unique files are confirmed by one confirmation for their whole group.
- **Possibly valuable or uncertain.** `possibly_valuable` and `uncertain` unique files, and `copy_offline` files, are confirmed one by one, or taken out of the set by restoring their item or moving the file out of the quarantine.

`plan-purge` SHALL fail with `409 purge_not_allowed`, listing what is still unconfirmed, until then (§10.6).

#### Scenario: R4.7 Unique photos block the purge
- **WHEN** a checked set holds unique photos and unique system junk, and the owner confirms the junk group but not the photos
- **THEN** the purge is refused and names the photos. After the owner confirms each photo, or restores or moves it out and checks the set again, the purge is allowed.

### Requirement: A check is valid only for what it checked
A check SHALL become stale when:
- **The set changes.** An item is restored, a file is moved out, or a file in it differs from what the check read.
- **A verified copy changes.** Its identity differs, it is moved, quarantined, or decided again, or its source is offline at purge time.

Changes the index records SHALL make the check stale at once, and `plan-purge` and `run-action` SHALL then refuse it with `409 check_stale`. A purge's first step SHALL compare every recorded entry and every relied-on copy with the disk before deleting anything. On any difference it SHALL make the check stale and stop with nothing deleted (§10.6).

#### Scenario: R4.8 A verified copy changes before the purge
- **WHEN** the twin that made a quarantined file safe is modified on disk after the check, and the owner runs the purge
- **THEN** nothing is deleted, the purge stops at its first step, and the check is stale

#### Scenario: R4.8 A copy decided again after the check
- **WHEN** the owner discards the twin's folder after the check
- **THEN** `plan-purge` fails with `409 check_stale`, and nothing is deleted

### Requirement: Purge deletes for good and reports the space
`purge` SHALL delete the checked set through the executor.
- **Scope.** The deletion SHALL be limited to the entries of the set and their origin records inside `.precious-quarantine`.
- **Before the first deletion.** It SHALL verify each item's whole tree against the check before deleting any of it. An item that differs SHALL end `changed` with nothing of it deleted.
- **The index.** It SHALL remove the index rows of what it deleted.
- **The report.** It SHALL report the files and bytes deleted, and the space freed: the allocated bytes of the files whose last link it removed. On ZFS, it SHALL say that space held by snapshots returns only when they expire (§10.3).

#### Scenario: R4.5 Purge on ZFS
- **WHEN** the owner purges a checked set on a ZFS source
- **THEN** the files are gone, the report gives the bytes deleted and the space freed, and it says that snapshots may keep the space until they expire

### Requirement: Every plan exports as CSV
`GET /api/history/{id}/export.csv` SHALL return one row per item of an action, cleanup plans included. Each row has the path, size in bytes, operation, state, and reason. Paths SHALL use the escaped display form (I6). Cells SHALL be quoted so that no spreadsheet reads them as formulas (§10.4).

#### Scenario: R4.9 Exporting a plan
- **WHEN** the owner exports a drafted cleanup plan with a blocked item
- **THEN** the CSV has a header and one row per item, with the blocked one's reason `holds_kept`

### Requirement: The Cleanup screen
The interface SHALL offer Cleanup in its main navigation. It SHALL show (§11.9):
- **Plans.** Draft a plan for a source, and from a review list.
- **The plan's preview.** It lists the planned, blocked, and refused items, with the kept entries that block them, the totals, and the summary of copies. It offers approve and run, and export.
- **Quarantine.** The browser lists each source's quarantined items, with restore and the selection of a purge set.
- **The check report.** It shows each verdict, the confirmations, move out, and purge.

Execution SHALL show live in History.

#### Scenario: From a review list to quarantine
- **WHEN** the owner discards the rows of the system junk list, drafts a plan from it, and approves it
- **THEN** the items move to quarantine, and the Cleanup screen lists them with Restore
