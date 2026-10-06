# ADR 0005: Copy searches list and read files inside atomic units without cataloging them

- Status: accepted
- Date: 2026-10-03
- Context: `directory-first-curator-spec-v0.2.md` §3 (directory first), §4.1, §5.2 (selective analysis), §11.1, §11.2; OpenSpec change `m4-find-copies`, design D2 and D3.

## Context

§5.2 limits hashing to "explicitly selected expanded scopes", and §11.1 says "Hash only owner-selected expanded scopes". Atomic nodes stay excluded "until the user changes their inventory mode or requests a specific analysis".

The owner's archive shows where redundancy actually lives. A metadata survey of the 1.17 TB, 1.3-million-file backup tree that motivates this product found about 210 GB of redundancy in three photo trees: the same photos arranged by event, by month, and by day, with renamed files. Most of the rest of the redundancy sits in machine backups. Directory-first triage keeps exactly these trees atomic, because they are recognizable units. Under the rule above, the owner could compare them only by refining each one, which catalogs hundreds of thousands of entries into the active inventory. That is the outcome directory-first exists to avoid.

## Decision

- A copy search may cover atomic units. It lists every entry under its folders, including inside atomic units, into a snapshot of its own (`copy_dirs`, `copy_files`). It reads and hashes the regular files whose size is shared.
- The snapshot is analysis data, not inventory. It creates no node, never appears in the explorer, search results, dashboard totals, or review queues, and never changes an inventory mode. It is kept only for the latest complete search of each source.
- A snapshot entry inside an atomic unit can be shown on the Copies page, by path below its unit. It cannot receive an owner decision until the owner refines the unit, because dispositions belong to nodes.
- A copy search is an explicit owner request, like an aggregate walk. Nothing starts one automatically.

## Alternatives rejected

- Hashing only expanded scopes, as §11.1 says: it cannot see the copies the owner most needs to find without refining every backup into the inventory.
- Refining atomic units automatically for a search: it would turn a read-only question into a permanent catalog change, against §3.
- Storing per-file digests on inventory nodes only: atomic interiors have no nodes, and creating them is the refinement rejected above.

## Consequences

- The invariant "an atomic node has zero active descendant inventory rows" still holds. Snapshot rows are not inventory rows, and the docs say so.
- A snapshot can be large: about one row per file, so roughly 1.3 million rows for the surveyed archive. Older searches keep only their summary and result rows.
- Freshness inside an atomic unit is limited. Reconciliation sees only a unit's top level, so a deep change inside a unit is seen only by running the search again. The Copies page states this.
