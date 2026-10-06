# ADR 0006: M4 compares folders by content; archive inspection moves to a new M4b

- Status: accepted
- Date: 2026-10-03
- Context: `directory-first-curator-spec-v0.2.md` §11.1, §11.2, §11.3, §11.4, §14 (M4, M6); OpenSpec change `m4-find-copies`; ADR 0005.

## Context

§14 gives M4 "explicit-scope file inventory/hash jobs, occurrence/content separation, duplicate report", with A10. Folder relations (`contained_in`, `same_content_tree`, and the others in §11.2), read-only archive inspection (§11.3), and candidate revision families (§11.4) are M6, which the project context says not to build early.

A metadata survey of the owner's 1.17 TB archive, taken on 2026-10-03, found redundancy in four shapes:
- whole folders copied with new layouts and new names: about 210 GB in three photo trees;
- archives: 356 GB, 30% of the bytes, including a 105 GB machine backup next to its extracted copy, and three snapshots of one machine;
- near-copies: one video at two sizes plus an unfinished download, and a `.tgz` next to its `.old` revision;
- unique material with no copies.

A file-level duplicate report would list about 520,000 extra files. The owner cannot act on that list one file at a time in a directory-first tool. The useful answer is about folders: "this folder is entirely inside that one". The owner chose to solve folders first and archives right after.

## Decision

- M4 delivers content-based folder results: `inside` (every file has an identical file in the other folder), `same` (each inside the other), and partial `overlap`. They are computed from file digests, ignoring names and layout. Large files with copies that no folder result explains are listed as file results. These are path-independent set relations. §11.2's path-aware manifests and multiset relations stay in M6.
- M4 lets the owner mark a copy as `cleanup_candidate` with the result as evidence. It still changes nothing on disk.
- A new milestone **M4b**, between M4 and M5, delivers read-only archive inspection (§11.3) and archive-to-folder comparison, plus copy searches across sources. Its acceptance scenarios will be set when it is proposed.
- Near-copies (§11.4) stay in M6. Keepers, byte-for-byte recheck before action, and quarantine of copies stay in M5.

## Alternatives rejected

- The file-level duplicate report of §14 M4: it answers the question the owner cannot act on.
- Archive inspection first: it is the larger pile, but it needs the content index and folder relations built here, plus archive safety budgets (§11.3).
- Waiting for M6: the relations the owner needs most would wait behind controlled organization (M5), which needs them to choose keepers.

## Consequences

- The delivery order becomes M1, M2, M2a, M3, M3a, M4, M4b, M5, M5b, M6. `openspec/config.yaml` still states the old order and its "do not build M6 early" warning. This ADR is the exception to that warning, for folder relations only.
- M6 keeps path-aware manifests, `same_content_tree` versus `same_content_multiset`, `similar_tree`, and revision families. It builds on M4's digests and snapshots.
- M5's keeper selection can start from M4 results, but it must re-verify content at action time (§11.1).
