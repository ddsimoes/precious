# Proposal

## Why

R1 and R2 show the disk and its copies but cannot change anything on it. R3 (§14) lets the owner move and rename entries safely, which R4's cleanup reuses: one shared executor that journals first, never overwrites, and recovers from a crash (§5 I2, I3; §10.2 mechanics; §10.5).

## What Changes

- **Write permission per source** (§6.1): off by default. The owner turns it on after a confirmation. It is unavailable when the configuration forbids writes (new key `sources.allow_writes`), when the filesystem is read-only, or when it has no no-replace rename.
- **The shared executor** (§5 I2, I3; §10.2): the only code that renames or creates folders in a source. For each step, it journals intent, uses `renameat2` with `RENAME_NOREPLACE`, confirms the result, and records it. After a crash, it reconciles the journal against both paths. An ambiguous item stops for manual recovery.
- **Moves and renames from the interface** (§10.5): a single move or rename runs at once. A bulk move first shows every entry and conflict, then moves only those entries once confirmed (§10.5 Execution).
- **History with undo** (§10.5): every action goes into a history, and undo returns the entries to their previous paths, or asks for a destination when a path is taken.
- **Rescue kept entries** out of a folder (§6.8, §10.1), and **move the files unique to one copy** of a folder into the other copy, from Compare (§10.5, §11.6).
- **The index follows the disk**: a moved entry keeps its ID, decisions, tags, overrides, and digests, and a rescan does not treat it as new (§6.9 Durability; I4).
- **BREAKING (spec):** the server-config requirement "Write mode unavailable in this release" is replaced, as it planned for R3.

Acceptance scenarios this change must make pass: **R3.1, R3.2, R3.3, R3.4, R3.5, R3.6, R3.7**.

## Capabilities

### New Capabilities

- `organizing`: actions that move, rename, and create folders; their preview, execution, history, undo, and manual recovery; rescue and copy merge.
- `source-writes`: per-source write permission, its availability, and the executor's write guarantees (no overwrite, journal, recovery).

### Modified Capabilities

- `server-config`: the new key `sources.allow_writes`, which replaces "Write mode unavailable in this release".
- `filesystem-boundary`: the new capability `no_replace_rename` is detected. Observation stays read-only; the executor is the only writer (source-writes).
- `source-registry`: `remove-source` waits while an action of the source is in flight or needs recovery.
- `inventory-explorer`: the children listing can be limited to folders, for the destination chooser.

## Deferred

- Cleanup plans, quarantine, restore, and purge: R4, on this executor.
- Organizing by date template and writing modification times: R5 (§10.7).
- The no-replace rename on macOS and Windows: R8. Until then, sources there report `no_replace_rename: false`, so writes are unavailable on those platforms.
- Moves across filesystems: out of scope (§13). Moves between two sources on one filesystem are not in R3 either (design D7): R3 moves inside one source.
- A staged organizing mode: §13.
- Multi-select on the Map, and moves from review lists: later, if asked. Search and the detail panel cover bulk and single moves.
