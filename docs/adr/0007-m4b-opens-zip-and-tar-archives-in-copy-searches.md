# ADR 0007: Copy searches open zip and tar archives and keep their member listings

- Status: accepted
- Date: 2026-10-04
- Context: `directory-first-curator-spec-v0.2.md` §3 (directory first), §5.4, §11.3; ADR 0005; ADR 0006; OpenSpec change `m4b-archive-copies`.

## Context

§11.3 says: "Initially treat ZIP, TAR, and other containers as opaque files. Later expose supported archive entries as virtual nodes only on request. [...] Start with read-only ZIP inspection." Its safety rules (budgets, rejected traversal paths and collisions, distinct encrypted, corrupt, unsupported, and partial containers, no extraction into source roots, no equivalence from names, sizes, or CRCs) apply unchanged.

ADR 0006 moved archive inspection into M4b. A survey of the owner's backup tree on 2026-10-04 (file names and sizes only) found these archives:
- tar.gz and `.tgz`: 185 GB in 173 files, including the 105 GB machine backup that sits next to its unpacked copy;
- zip: 91 GB in 2,648 files;
- single-file gzip: 36 GB in 10,719 files;
- `.tgz.old` and similar: 12 GB;
- 7z: 16 GB in 23 files, and rar: 6 GB in 104 files;
- jar, apk, iso, msi, docx, and similar: 28 GB. These are programs and documents, not backups.

Starting with ZIP alone would leave the largest and most important archive unanswered. The owner chose:
- the formats the Go standard library reads (zip, tar, tar.gz, tar.bz2, single-file gzip and bzip2), 93% of the archive bytes, with no new dependency;
- opening every supported archive in every copy search;
- showing an archive's contents in its inspector as the last search found them;
- in a `same` pair of an archive and a folder, reporting the archive as the copy.

## Decision

- **Formats.** zip and the tar family are supported together. A file is opened when its name ends in a supported extension, optionally followed by `.old`, `.bak`, or `.orig`, and its first bytes confirm the format. 7z, rar, xz, and zstd archives are counted as not opened. Formats that only use zip inside (jar, docx, odt, epub, apk) are plain files.
- **"On request" is the copy search.** Archives are opened only by a copy search, which only the owner starts. Inside a search's scope, every supported archive is opened. The owner does not pick archives one by one.
- **No virtual nodes.** Archive members never become inventory nodes, like the search snapshot of ADR 0005. A member listing (paths, kinds, sizes, modification times, link texts, and digests) is kept per archive file identity, like M4's digest cache. It is shown read-only by the archive's inspector. It is replaced when the archive changes, and removed once no search uses it and a newer listing of that file exists.
- **Listings are kept, bytes never.** The filesystem boundary's rule "content is never stored" is refined. Member listings of opened archives are stored; member bytes are never stored, logged, or written anywhere, not even to a temporary file. Unpacking happens in memory.
- **Nesting.** The nesting budget is fixed at one level. An archive inside an archive is compared as one member by its unpacked bytes and is never opened.

## Alternatives rejected

- **ZIP first, as §11.3 suggests.** It would miss 185 GB of tar.gz, including the 105 GB backup the owner most needs answered.
- **Virtual nodes for members.** They would add hundreds of thousands of inventory rows behind atomic units, which is what directory-first triage exists to avoid (ADR 0005).
- **Opening only archives the owner picks.** The owner's question is whether an archive is already unpacked somewhere. Picking each of 3,000 archives defeats that.
- **Libraries for 7z and rar.** They would add about 6% of the bytes, through third-party parsers of untrusted input, against the standard-library-first stack.
- **Unpacking to a temporary directory.** It writes untrusted content to disk and needs space the size of the largest archive. Streaming in memory does neither.

## Consequences

- A first search of the surveyed tree reads about 320 GB of archives in addition to M4's file reads. Unchanged archives are never read again.
- The database holds one row per archive member: about 2 million rows for the surveyed tree. The upgrade note states it.
- Changes inside an archive are seen when its file changes (size, modification time, or change time), which the catalog observes for cataloged files.
- M5 can start keeper choice from archive results, but it must verify bytes again before acting (§11.1).
- 7z, rar, xz, zstd, and nested archives stay unopened until a later milestone adds them under the same budgets.
