# ADR 0013: The Map's "Only here" mode: where each file's copies are

- Status: proposed
- Date: 2026-10-22
- Context: `precious-spec-v0.3.md` §6.4, §11.2, §11.7; ADR 0009; ADR 0010; OpenSpec change `r5c-only-here`.

## Context

The owner plugs external disks into the reference server and asked for an easy way to see whether a disk holds files that exist nowhere else. He also asked to tell copies on the same source apart from copies on other sources, and wants this on any source.

Precious already finds every copy (§6.4), but it says nothing about where those copies are:

- **The Map's "Has copies"** (ADR 0010) counts copies on every source, the file's own included. A folder whose every file is repeated inside the same disk reads 100%, though it would be lost with that disk.
- **Gems** (§11.7) listed personal files with no other copy. On the owner's 780 GiB archive it was a single list of about 48,000 rows, and ADR 0009 removed it as unworkable. That ADR left room for a new decision on "what is valuable".
- **A read-only count of the owner's 487 GB external disk** found most of its bytes with no copy at all, and much of the rest repeated only inside the disk, often as a folder beside its own zip or tar.

On 2026-10-22 the owner chose:

1. a mode of the Map, not a new screen ("A");
2. copies on the same source in a class of their own, each naming its other copy, with cleanup left to the existing screens ("A").

## Decision

1. **Places.** Each file has a place relative to its own source:
   - only copy;
   - copies only on this source;
   - a copy on another source, including sources that are not connected, which are marked;
   - not checked yet;
   - unreadable.

   Copies count as in duplicate groups. Not checked is never an only copy (I7).
2. **Archives.** A complete archive is placed by its members, and a copy of the archive file itself covers them.
3. **Folder figures.** Every folder carries its subtree's files and bytes per place. They are computed with the folder duplication figures and shown with the share checked so far across all sources. System and temporary files (the disposable family, unless the rules ask to review them) are counted apart. That way a `Thumbs.db` that differs in every folder does not make every photo folder an only copy.
4. **The mode.** The Map gets an Only here mode, kept in the address:
   - rows are emphasised by place and say it in words;
   - folders show a bar of their places;
   - the mode can hide what has a copy on another source;
   - it can sort by bytes only on this source;
   - the treemap gets a color for places;
   - rows name another copy, or the folder that holds all their content, with a link to Compare.

   This answers Gems' first question through the tree, one folder at a time, rather than through a flat list.

## Alternatives rejected

- **A screen of its own** that lists only what is unique to a source. It is a second explorer, and it brings back the flat list problem of ADR 0009.
- **A treemap color alone.** It is not precise enough to act on.
- **Counting copies on the same source as copies.** That hides what a lost disk would take with it.
- **A second view of what repeats inside a source.** The duplicates card, Compare, and Cleanup already do that; rows name the other copy instead.
- **Placing archives by their own bytes.** A backup archive beside its unpacked folder would read as the only copy of content that is on the disk twice.

## Consequences

- **Migration 0009** adds `entry_places`, and the first start after the upgrade runs one relate pass.
- **Search, Compare, the duplicates card, and "Has copies" are unchanged.**
- **pt-BR strings** for the mode come with R7.
