# ADR 0008: Product reset: full index, task-centered interface, and plans that act

- Status: accepted
- Date: 2026-10-05
- Context: `directory-first-curator-spec-v0.2.md` (all milestones through M4b delivered); ADR 0005, 0006, 0007; `precious-spec-v0.3.md`.

## Context

After M4b, the owner judged that the backend was roughly right but that the product did not solve the problem: the interface was hard to use and did not give a clear view of the files, the problems, the priorities, or the means to deal with them.

An evaluation on 2026-10-05 ran the M4b binary on a synthetic messy disk (3,372 files, 650 MB, shaped after the owner's archive) and used it in a browser as the owner would. It found the following.

**Sizes and classification**

- Right after a scan, the overview stated "Known size: 60.2 MiB … 16 units of unknown size". Sizes stayed unknown until each folder was measured by hand.
- 11 of the 16 top-level folders were classified `unknown`, including `Fotos`, `Musicas`, `Projetos`, `RECYCLER`, and `System Volume Information`. The cause is the depth-0 probe: a folder that holds only folders gives no evidence.
- A measured `Fotos` with 83 images out of 83 files stayed `unknown`, because aggregate walks may add traits but never change a category (`policies/rules/v1.toml`).
- A personal spreadsheet inside an atomic `Microsoft Office` folder carried no indicator.

**Interface**

- There was no search, sort, filter, chart, or largest-folders view.
- At 1440 px, names wrapped letter by letter, and the inspector filled half the screen with digests, base64 names, and absent markers.

**Copy search**

The copy search worked well: in 1 s it found the zip-and-unpacked pairs, the duplicated backups, the copied ISO, and the identical installers. But:

- 17 of its 26 results could not be acted on, because the copy lay inside an atomic unit;
- symmetric pairs were listed twice;
- the files unique to an `overlap` were not shown (an edited photo found only in `Fotos - Copia`);
- the owner could not choose which copy to keep.

**Acting**

Nothing could act on the disk: marks were database labels only, and M5 had not started.

**Root cause**

Invariant §3.2 (an atomic node has no indexed descendants) was the root of most of these gaps. ADR 0005 had already worked around it: a copy search lists every entry, about 1.3 million rows for the owner's archive, into a snapshot that the interface cannot browse, search, or act on.

## Decisions

The owner made these decisions one at a time on 2026-10-05:

1. **Full metadata index.** Every file and folder is indexed. A folder treated as one item (a *group*) is a review aid, not a limit on what is indexed. Hashing stays selective and progressive.
2. **Real, reversible quarantine.** Plans move items to a quarantine on the same filesystem, with restore and a separately confirmed purge. Exporting a plan as CSV is an extra.
3. **Software separate from deployment.** The product needs a login only. Plain HTTP on a local network is allowed. HTTPS and remote exposure are deployment concerns for later. The product runs on a home or small-office file server or locally. The reference server uses OpenZFS (RAIDZ1), which supports `RENAME_NOREPLACE`.
4. **Front end.** A React + TypeScript + Vite single-page app, embedded in the Go binary. Node is a build-time dependency only.
5. **Language.** Translation keys from the start; English first, then Brazilian Portuguese. Code, API, logs, and technical docs stay in English.
6. **Scope.** The managed inbox and the filesystem-notification and per-scope reconciliation machinery are removed. The classifier is kept and repurposed as an assistant over the full index.
7. **Models.** Local and cloud providers work through the same provider-neutral contract. Jev (TypeSafe API) comes first, for fast and cheap classification into predetermined labels with calibrated probabilities, and for routing deeper analysis. Generative text models and image recognition are future work.
8. **A portable product (added 2026-10-05).** Precious is for other people too, on Linux, macOS, and Windows, with internal, external, removable, and optical media on any mountable filesystem. The core is portable from R1: a platform layer, detected filesystem capabilities, volume identity, and offline sources. Linux is delivered first, and macOS, Windows, and removable media are completed in milestone R8.

`precious-spec-v0.3.md` becomes the design authority and replaces v0.2. The product is named Precious everywhere; R1 renames the `curator` binary, command, configuration, and state directory to `precious`.

## Alternatives rejected

- **Keep directory-first and expose the copy-search snapshot (a hybrid).** Two truths about the same disk, one the inventory and one the snapshot, which can disagree and must be explained in the interface. Acting inside a unit stays awkward.
- **Keep the model and only restyle the interface.** It leaves sizes, search, acting inside units, and gems unsolved.
- **Export a script instead of executing plans.** No undo, fragile quoting of decades-old names, and Precious cannot know what was done.
- **Server-rendered pages with htmx.** The treemap, virtualized tables, side-by-side compare, and bulk selection would become a growing pile of hand-written JavaScript.
- **Freeze the inbox and reconciliation code instead of removing it.** It sits on the data model being replaced, so it would either have to be kept compiling without delivering value, or rot.

## Consequences

- R1 starts a new database schema. Databases created by v0.2 builds are not migrated. No owner data needs to be carried over.
- The following packages and pages are removed or rewritten in R1: `internal/discovery`, `internal/reconcile`, `internal/watch`, `internal/inbox`, the inbox parts of `internal/inventory` and `internal/commands`, and the server-rendered `web/templates` with `web/static/app.js`. The git history keeps them.
- The following are reused and adapted: `internal/fsaccess`, `internal/compare`, `internal/jobs`, `internal/auth`, and the provider layer of `internal/classify`.
- `openspec/config.yaml` points to v0.3. The capability specs under `openspec/specs/` describe v0.2 behavior; each milestone's change retires or rewrites the ones it touches.
- `docs/operator.md` describes the M4b binary until R1 rewrites it.
- The scale target changes from avoiding a full catalog to indexing everything quickly. Indexing is bounded relative to a bare metadata walk on the same machine, and interface responses are bounded absolutely at 2 million entries (`precious-spec-v0.3.md` §7, §12).
