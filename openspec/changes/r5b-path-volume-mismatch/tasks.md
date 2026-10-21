# Tasks

## 1. Identity check

- [ ] 1.1 In `internal/sources`, add `ReasonOtherFilesystem` (`other_filesystem`), `Source.RootIno` read with the row (design D3), and the check of D1 in `observe`, returning D2's observation. Verify with unit tests over synthfs:
  - an NTFS-typed `path` source with stable capabilities, scanned, then another filesystem's folder at its mount point: `offline`/`other_filesystem`, `Open` refused with `source_offline`, entries unchanged; and back online once its own filesystem is mounted there;
  - the same type with another root inode: `offline`/`other_filesystem`;
  - another type with the same root inode (the driver change): `online`;
  - a FAT-capability source remounted (inodes renumbered): `online`; a folder of another type there: `offline`/`other_filesystem`;
  - a source with no root inode recorded: decided by type;
  - a strong volume and R1.16: unchanged.
- [ ] 1.2 An end-to-end test with real mounts in `internal/index`, through the OS backend, the registry, and the job runner. A tmpfs holding a few files is bound at a mount point, added as a source, and scanned. Then the mount point holds, in turn:
  - a bind of an empty folder of the test's own filesystem;
  - a bind of an empty folder of another tmpfs, the source's own type;
  - the original tmpfs again.

  Verify, after each refresh:
  - with each stand-in: `offline`/`other_filesystem`; `start-scan` and `Open` refused with `source_offline`; no entry row changes;
  - once the original is back: `online`, and a rescan writes no row.

  Verify with `scripts/e2e-docker.sh`.
- [ ] 1.3 Update `docs/operator.md`:
  - the state reasons table gains `other_filesystem`;
  - Volume identity says when a mount at a `path` source's mount point counts as its volume, and what a container's bind of an empty folder then reads.

  Verify with the docs tests.

## 2. Integration

- [ ] 2.1 Run the full verification:
  - gofmt and both vet runs;
  - `go test -race ./...`;
  - `make cross`;
  - UI lint, Vitest, and build;
  - Playwright;
  - `scripts/e2e-docker.sh`;
  - `openspec validate --all --strict`.

  `make test-slow` is not needed: no query on a slow path changes.
- [ ] 2.2 Smoke-check the built binary with a throwaway script, in a user and mount namespace:
  - the r5 binary adds and scans a `path` source on a tmpfs, and hashes it;
  - the r5b binary runs on a copy of that database. The source is online, and a rescan finds nothing changed.
  - Then an empty folder of another filesystem is bound at the source's mount point. Verify: `offline`/`other_filesystem`; `start-scan` refused; Home's coverage and the entries unchanged.
  - Once the tmpfs is back: `online`.

  Then delete the script.
- [ ] 2.3 Deploy on the reference server, after a database backup. With the hashing of the NTFS source cancelled, unbind its folder from the container, unmount it on the host, and bind the host's empty folder in its place. Verify:
  - the Sources screen shows the source "not connected" with `other_filesystem`;
  - restoring the disk brings it back online, and its hashing resumes.

  The owner signs off, recorded in the design addendum.
