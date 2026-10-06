# ADR 0003: Special entries in an inbox are inbox issues, not intake items

- Status: accepted
- Date: 2026-10-03
- Context: `directory-first-curator-spec-v0.2.md` §4.5, §5.1, §9.4.1, A11; OpenSpec change `m3a-managed-inbox-intake`, design D5, D6, and addendum D30.

## Context

§4.5 says: "One immediate child is one arrival: a directory is a package, a loose regular file is an individual item, and an archive is an opaque file. Symlinks are review-only; unsupported special entries receive a visible issue rather than being opened." Read literally, a FIFO, socket, or device file in an inbox is an arrival that carries an issue.

An intake item references an ordinary inventory node (§4.5). Discovery never creates a node for a special entry: §5.1 forbids opening it, and `nodes.kind` holds only directories, regular files, and symlinks. A special entry is already recorded as a `special_file` issue of the directory listing that found it, with its raw name bytes. An intake item for it would need either a node kind that M1-M3 deliberately excluded or an item without a node.

## Decision

- An inbox enrolls no intake item for a special entry. It stays the inbox listing's `special_file` issue and is never opened.
- The inbox page shows these entries, and `GET /api/inboxes` lists them as `special_entries` with `{name, name_b64, kind, detail}`.
- Symlinks are still arrivals. They enter `needs_review` directly and are never followed.

## Alternatives rejected

- A `special` node kind: it would add a node type that every catalog, accounting, and cleanup path would have to exclude, only for intake.
- Intake items without a node: every item command, generation check, and proposal is keyed by the node, so these items would need a second identity scheme.

## Consequences

- The visible issue that §4.5 requires is kept, and nothing opens the entry (A11).
- The owner can see special entries but cannot ignore, retry, or mark them ready, and no proposal is computed for them.
