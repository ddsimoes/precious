# Design

## Context

Copies are counted in two places today:

- **On read, per file.** `search.CopiesSQL` counts the physical copies of a content. A hard-link set counts once, the members of complete archives count, offline sources count, and the quarantine does not. `copiesColumn` serves the result as every row's `copies`.
- **In the relate pass, per folder.** `Snapshot.dirDups` sums, bottom-up, the bytes of the files whose content has two or more physical copies (`dd.copies[k] >= 2`). `writeDirDups` upserts the rows that changed into `dir_dups`.
  - **When it runs:** after each scan, at most every `duplicates.refresh_interval` while hashing runs, and when hashing ends.
  - **Archives:** a complete archive is spliced into the snapshot as a folder of its members. Its own file is kept apart, in `arcFiles`, and `dirDups` counts it by its own bytes.
  - **Physical identity:** members take their archive's (dev, ino) plus their position, so the members of two hard-linked names of one archive count once.

Neither place knows on which source a copy is. `otherCopy` takes a predicate on the copy's entry; Search's `elsewhere` uses it relative to the Within folder, not to the file's own source.

Members of a complete archive are listed in the Map on read (`memberPage`). Their copies come from `memberSelect`, their folders' figures from `memberSubtree`/`fillFolder`, and nothing about members is stored per member.

On the reference server today:
- the largest folder holds 31,163 children, and 15 folders hold more than 5,000;
- there are 18,622 complete archives, with 3.58 million members;
- one source (with stable file identity) holds 20,456 hard-linked file names.

See proposal.md for the motivation.

## Goals / Non-Goals

**Goals:**
- One definition of a place, used by:
  - the stored folder and archive figures;
  - the file rows, read live;
  - archive members, read on request.
- Folder figures that equal the sum over their files, checked against an independent computation over two sources.
- The Map unchanged when the mode is off (R1.10), and a measured, bounded cost when it is on.

**Non-Goals:**
- A stored place per file or per member.
- A per-folder figure for copies on sources that are not connected (D11).
- Any change to "Has copies", the duplicates card, Compare, Search, or the Opportunities lists.

## Decisions

### D1. Places and their order (§6.4, I7)

A present file outside the quarantine has one place, relative to its own source. A member's source is its archive entry's source.

| Place | When |
|---|---|
| `only_copy` | content state `unique_size` or `sampled`; or `hashed` with one physical copy |
| `this_source` | `hashed`, two or more physical copies, all on its source |
| `other_source` | `hashed`, with a physical copy on another source, whatever that source's state |
| `unchecked` | `pending` or `changed`, or a regular file of size > 0 with no content row yet (new or changed since the last content plan) |
| `unreadable` | `unreadable` |
| `none` | an empty file, a symlink, or a special file |

- **No place:** missing and quarantined entries have none (`null`).
- **Nested archives:** a member that is itself an archive is never listed, so it is placed as a file.
- **Physical copies** follow the snapshot's definition (D3):
  - one per hard-link set, on a source with stable identity;
  - a tar hard link and its target are one copy;
  - members of complete archives count, and the members of hard-linked names of one archive are one copy;
  - complete archive files count with the same content;
  - offline sources count, and the quarantine does not.
- **Folders and complete archives** take the first place, in the order `only_copy`, `this_source`, `unchecked`, `unreadable`, `other_source`, whose file count is above zero, over their non-disposable files (D14). They are `none` when no place has a file.
- **A folder not read completely** reads `unreadable` where it would read `other_source` or `none`, because its figures cannot show what it holds. Not read completely means `entries.state = 'unreadable'`, `partial`, or `mount_boundary` on the folder itself, or `dir_stats.unreadable > 0` or `dir_stats.mount_boundaries > 0` (subtree counts) below it.

Rejected:
- **Two classes** ("only on this source", "elsewhere"): the owner chose three ("A").
- **A copy on an unconnected source as no copy**: a file would change class whenever a disk is unplugged. It is `other_source`, with a mark (D11).
- **Dimming unchecked like `other_source`**: it would read as safe.

### D2. Files are placed live; folders and archives are stored (§6.4, §7)

- **Files:** a file row's place is computed in SQL (`search.PlaceSQL`).
  - It reads the content state, then answers two questions about the content's other physical copies: is one on another source, and is there one at all.
  - Each question has an entry arm and a member arm.
  - A complete archive's row reads its stored figures instead (D5).
- **Folders and complete archives:** their figures come from the relate pass, in the new table `entry_places`.

This follows the existing split: `copies` is live, `dir_dups` is stored.

Rejected:
- **A stored place per file, written by the relate pass**: about 2.4 million rows on the reference server, rewritten on every change of copies. File rows would also lag hashing, which `copies` does not.
- **Folder figures computed on read**: a subtree walk per row.

Consequence: a folder's figures lag its files by up to one refresh interval plus the pass's own run time, as "Has copies" does today. D8 shows when a pass is pending or running, and the Map refetches when it ends (`applyJobEventToDuplicates`).

### D3. The snapshot knows each content's sources (§7)

- **`dd.srcOf []int32`**, filled in `countCopies`, gives one value per content key: the source index shared by all its physical copies, or `multiSource` (-1) when they lie on two or more sources.
  - An occurrence counts by its directory's source, `s.src[s.eDir[e]]`, which for a member is the archive's source.
  - A complete archive file counts by the source of its folder.
- **`dd.arcSrc map[int64]int32`** does the same for archive-file contents that have no key (`arcKeys[c] < 0`).
- **`physOf` includes the source in its key.** Today it groups by (dev, ino) alone, so two sources that reuse a device number would share hard-link sets. `copyKey` already includes the source. This corrects `dd.copies` in that case, and the `dir_dups` tests stay as they are.

A hashed file in source `S` with key `k` is:
- `only_copy` when `copies[k] == 1`;
- `this_source` when `srcOf[k] == S`;
- `other_source` otherwise.

Rejected:
- **A bitmask of sources per key**: it caps the number of sources at 64.
- **A set per key**: it costs memory and answers nothing more.

The cost is 4 bytes per content key.

### D4. Folder figures in `entry_places` (§6.4)

The figures are computed only on the final snapshot, never the provisional one.

- **Unhashed files:** `addFile` adds each regular file of size > 0 to its directory's own place sums by content state (D1), after the `arcAt` test, so that a complete archive's file is not counted as a file. Disposable files go to their own sum (D14), and so does a disposable complete archive, with its whole size.
- **Unhashed members:** these are added where member records are built (`keyMembers`).
- **Hashed files and members:** they are placed by D3 in `entryPlaces()`, after `countCopies`. The sums then add up bottom-up, like `dirDups`.
- **Which rows:**
  - one per entry folder (`s.arc[d] < 0`);
  - one per complete archive, keyed by the archive's entry ID (`s.arcDir[s.arc[d]] == d`).

  Member folders do not get `entry_places` rows: their `s.id` values are member IDs. Since A15 they get `member_places` rows, and each complete archive gets an `archive_places` row (D6).
- **Writing:** `writeEntryPlaces` diffs against the stored rows and upserts or deletes the changed ones in batches of `insertBatch`. It shares one generic diff writer with `writeDirDups`.

Rejected:
- **New columns on `dir_dups`**: archive rows would give `dir_dups` rows that are not folders. The positional inserts in `TestSourceDeleteCascadesContent` would also break.

### D5. A complete archive is placed by its members (§6.4)

**Member sums.** An archive's member sums are computed as for any folder (D4).

**Coverage by the archive file itself.** Let `own` be the archive file's place by its own content (D3, through `arcKeys`/`copies` or `arcExtra`/`arcSrc`). `arcFile` keeps the archive's content state for this.
- When `own` is `other_source`, every member byte counts as `other_source`.
- When `own` is `this_source`, member bytes that are `only_copy` count as `this_source`.
- An unhashed archive file adds nothing.

**Split.** The archive's row holds its file size, split among the places in proportion to its member bytes.
- **Arithmetic:** the products are exact in 128 bits (`math/bits.Mul64`, `Div64`), because a 4 GB archive with 4 GB of members in one place overflows int64.
- **Remainder:** it is shared by the largest-remainder method.
- **Minimum:** every place that has member bytes gets at least 1 byte, taken from the largest share, so a place never shows "0 B".
- **Files:** the archive's one file counts in the first place of D1's order that has member bytes.
- **Members with no bytes:** the archive is placed by `own`, as a file.

**Parents and sort.** This split is what the parent folder adds, and what the archive's row and its `only_here` sort key read.

**Opened archives.** When the archive is opened in the Map, or its panel is shown, the figures are the members' unpacked sizes. They come from `memberSubtree` with the coverage above applied (D6). The panel says that these are unpacked sizes.

**Other archives** (partial, encrypted, rejected, corrupt, unsupported, changed, or not listed yet) are placed as files.

Rejected:
- **Placing an archive by its own bytes**: a backup archive beside its unpacked folder would read as the only copy of content that is on the source twice.
- **Counting unpacked member bytes in the parent**: folder figures would no longer add up to `total_bytes`.

### D6. Member files are placed on read; member folders are stored (§11.2, archives in the Map; revised in A15)

**A member file's place** is `search.MemberPlaceSQL`, D2's member variant.
- Its copies are found as `memberCopySQL` finds them, except that the members of archive names in the member's own archive's hard-link set do not count (D1).
- "Another source" means a source other than its archive entry's.

**The archive's own coverage** (D5) is computed live, once per page. `PlaceSQL` and `OfflineOnlySQL` are run on the archive's entry as a file, by its own content, and the result is applied in Go to each member row. The stored split cannot stand in for it: when every member has its own copy on another source, the split reads `other_source` while the archive file itself may be the only copy.
- A member placed `this_source` or `other_source` only through that coverage names the archive's preferred copy ("inside a copy of X.zip at …", `via_archive`) and takes the archive's mark (D11).
- A member with copies of its own names its own copy and keeps its own mark.

**Member folders and whole archives** read stored unpacked sums from the same relate pass: `member_places`, keyed by member, and `archive_places`, keyed by the archive's entry. The pass computes them with the archive's coverage applied. Member files are placed live, but only for the rows shown, or for the whole level when sorting by `only_here` or hiding.

Rejected:
- **Storing the place of every member**: there are 3.58 million members on the reference server.
- **Folder sums of members computed on read** (the first plan): opening a 567,623-member archive in the mode took about 25 s on the reference copy (A15).

### D7. The mode lives in the Map's address (§11.2; owner's choice "A")

| Parameter | Meaning |
|---|---|
| `places=1` | the mode is on |
| `hide=other_source` | hide the rows with nothing that needs keeping here; needs `places=1` |
| `sort=only_here` | sort by bytes only on this source; needs `places=1` |
| `color=places` | the treemap color |

- **Turning the mode on** sets `color=places`.
- **Turning it off:**
  - removes `places` and `hide`;
  - resets `sort=only_here` to `bytes`;
  - resets `color=places` to `family`.
- **Treemap fetches:** the treemap passes `places=1` whenever `color=places`, mode or not.
- **An address with `sort=only_here` and no `places`** falls back to `bytes` in the UI.
- **Drilling** keeps every parameter, as `viewSearch` does.
- **Search** gets no mode.

Rejected:
- **A screen of its own** (B) and **a treemap color alone** (C): the owner chose A.
- **The mode on by default**: it adds reads to every Map page.

### D8. Emphasis, always with words (§11.2, Plain vocabulary)

In the mode, the `duplicated` column gives way to a `place` column. The row class follows the place.

**Width.** In the mode, the `place` column has the track `minmax(12rem, 1fr)` and never hides. The name column gets `minmax(13rem, 3fr)`, and Type or category hides before the place. A row's named copy (D9) goes on a second line under the name, showing file names; the full paths are in its description (A15, A16).

| Place | Row | File text | Folder or archive text |
|---|---|---|---|
| `only_copy` | accent tint, left bar, bold name | "Only copy" | "{{bytes}} only copy" + bar |
| `this_source` | lighter tint, left bar | "{{n}} copies, all on this source", where n is `other_copy.more + 2` (D9) | "{{bytes}} only on this source" + bar |
| `other_source` | muted | "Copy on another source" | "All also on another source", plus "apart from {{n}} disposable files" when it holds some + bar (A16) |
| `unchecked` | dashed left bar | "Not checked yet" | "Not checked yet" + bar |
| `unreadable` | dashed left bar | "Could not be read" | "Partly unreadable" (partial or mount boundary), "Could not be read" (unreadable) + bar |
| `none` | muted | "Empty", or "No content (link)" | "Only disposable files", or "Nothing with content" |
| `not_computed` | dashed left bar | — | "Not computed yet" |

- **Disposable files** (D14) are muted, and their text adds "· disposable".
- **The bar** shows the five places and the disposable share in proportion to bytes, in the treemap's `places` colors.
- **The totals line** above the table shows the open folder's bytes per place.
  - When the all-sources coverage that the entry response already carries is below 100%, it adds that coverage: "On all sources, {{pct}} of the content that could have a copy has been checked; these figures may still change." This is R2.7's checked share. It is the same coverage the panel shows.
  - While `places_updating` is true (Interfaces), the line adds "Updating…". That flag is true while `review_state.dirty` is set or a `relate` job is queued or running.
- **Hover and selection** styles still win.

Rejected:
- **Color alone**: it would not say the place.
- **A folder's own checked share** (`dir_dups.checked_bytes`): it counts an archive by its own state, not by its members, so it disagrees with the places.
- **Hiding by default**: the owner asked to see the dimmed rows.

### D9. Naming another copy (owner's choice "A")

The children handler adds these fields with `places=1`, batched in one query per page for each field.

**`other_copy`** names one copy for a file or member placed `this_source` or `other_source`. It comes from `content.PreferredCopies`, which never names a name of the file's own hard-link set or its tar link group.
- **For `other_source`:** a copy on another source, connected sources first.
- **For `this_source`:** a copy on the file's own source, loose entries before members.
- **Ties** go to the lowest reference.
- **`more`** is the number of physical copies minus 2, counted by D1's definition, so the mode's "{{n}} copies" uses it, not the row's `copies` (which counts the members of hard-linked archive names separately).
- **Members placed by their archive's coverage** name the archive's preferred copy instead (D6).

**`covered_by`**, for a folder or complete archive, comes from `relations.CoveredBy` over the current generation. It is the relation with the most matched bytes, of kind `same` (either side) or `inside` with this entry as side `a`. The Compare link is `/compare?left=<id>&right=<other id>`, with decimal IDs.

Rejected:
- **Browser requests per visible row**: up to 1,000 requests per page.
- **Naming every copy on the row**: the panel already lists them.

### D10. Sort and hide are evaluated on read (R1.10)

**The `only_here` sort key** is decided by the entry's kind and archive state, not by whether a row exists:
- **a present folder, or a present file whose archive is `complete`:**
  - with an `entry_places` row, it is `only_copy_bytes + this_source_bytes`;
  - without one, it is `not_computed`, which the cursor stores as `math.MaxInt64`: first when descending, last when ascending;
- **any other present file:** its size when its place is `only_copy` or `this_source` and it is not disposable, else 0;
- **a missing entry** (folder, archive, or file): place `null` and key 0, so it sorts with `none`. Hide drops it.

The order is descending by default. The next key is the bytes of unchecked plus unreadable, then `e.id`. The cursor's `n` carries the first key, and the cursor gains a field for the second.

**Hide** keeps the rows placed `only_copy`, `this_source`, `unchecked`, `unreadable`, or `not_computed`, except disposable files. It drops `other_source`, `none`, disposable files, and missing entries. The treemap is never filtered, and its "other" area takes the neutral color.

**Inside an archive,** `memberPage` sorts and filters in memory, as it already sorts.

**Without `places=1`,** neither the query nor any response changes, so R1.10 is unaffected.

**With `places=1`, the costs:**
- each row on the page runs `PlaceSQL`: two questions, each with an entry arm and a member arm. Each arm starts with one indexed probe by `content_id` (`file_content_by_content`, `archive_members_content`) and reads the content's other copies until one answers;
- D9's batched queries;
- sort and hide must know the place of every child of the folder before the first row. They do not run `PlaceSQL` per child. One set-based query per request:
  1. takes the folder's child files' content IDs;
  2. looks each distinct content up once through the two content indexes, grouped by content, answering "copy on another source" and "any other copy";
  3. joins the answer back to the children.

  There is no cache, so sort and hide always follow the live places, as file rows do.

**Targets, gated by a slow test on a cold page** (the first page of a folder, with no earlier request to it): a 200-row page answers in under 1 s at p95 with `sort=only_here`, and with `hide` plus `sort=bytes`. The test folder has 30,000 files (the reference maximum is 31,163) in a 2-million-entry index, with:
- a second source holding copies of half the files;
- archive members with the same contents;
- some contents with 50 copies.

Also measured and recorded, but not gated:
- a 100,000-file folder;
- an opened archive level of 50,000 members, where `memberPage` and `memberSubtree` place every member.

**If the gate fails,** work on the slice stops. The measurements go to the owner, with D2's rejected alternative (a place stored per file) as the candidate change. The plan is not changed silently.

Rejected:
- **An index on the key**: the key depends on other rows.
- **A per-folder cache of ordered keys**: it does not help the first page, and it goes stale between relate passes while file rows are live.

### D11. A mark for copies on unconnected sources, on files only (I7)

`place_offline` is true for a file or member placed `other_source` when it has at least one physical copy on another source and none of those copies is on a source whose state is `online`. The test is `search.OfflineOnlySQL`, read live.

- **Wording:** the row says "(the other source is not connected)", and the named copy shows its source as not connected. This covers both `offline` and `unavailable`.
- **Folders** get no such figure.

Rejected:
- **A stored per-folder figure**: source state changes on observation, which runs no relate pass, so it would go stale.

### D12. Migration `0009_places.sql` (§7)

The migration creates `entry_places` (Interfaces). It also sets `review_state.dirty = 1`, so `Handler.Startup` enqueues one relate pass, as `0005_rescue.sql` did.

Until that pass ends, folders and complete archives read `not_computed` in the mode, while file rows are placed at once.

### D13. Truth for the tests (§15)

- **Unit tests:** a synthetic world of three sources, one of them offline, built with the `relations` test helpers. It covers every case of D1, D3, D5, and D14, among them:
  - hard links;
  - hard-linked archive names;
  - tar link members;
  - the quarantine;
  - archives with and without a copy of their own;
  - a 5 GiB archive and a highly compressed one, for the split's arithmetic.
- **Independent computation:**
  - The relations corpus test seeds a second source beside the corpus: copies of part of its files, and a byte-identical copy of one of its zips.
  - Plain SQL places every file and member.
  - Go sums those places per folder and does D5's split. It must equal `entry_places` (`TestEntryPlacesEqualADirectComputation`).
- **Browser:**
  - **The fixture:** the e2e setup writes `disk/copies`. It holds copies of three corpus photos (one of them twice), a byte-identical copy of one corpus photo folder, and one file of its own. These are taken only from corpus files and folders that no test in `precious.spec.ts` moves, renames, quarantines, purges, or redates; task 5.1 lists them after checking.
  - **Isolation:** the new tests sit in `only-here.spec.ts`, in a Playwright project of their own (`testMatch`). That project depends on the existing project, which gets `testIgnore` for it. The new spec adds `disk/copies` as a source, and removes it in `afterAll`.
  - **Covered elsewhere:** R2.7 (partial hashing) and the not-connected mark need states the browser fixture cannot hold steady. They are covered by API tests (3.1, 3.2) and vitest (4.1, 4.2).
- **Reference server:** a read-only count script, kept out of the repository, computes the external disk's root figures by D1, D5, and D14. They must equal the Map's.

### D14. Disposable files are counted apart (§11.2; plan review)

A file is disposable here when `domain.IsDisposable(category, file_kind, triage)` holds:
- its file family (`FileFamily(category, file_kind)`) is `disposable`;
- its triage is not `review`.

Recovered file chunks (triage `review`) are therefore not disposable.

- **Members are classified too.** A member's file kind comes from its name (`Policy.FileKind`), as members' composition already does. Its category and triage come from `Policy.ClassifyFile`, called with its name, kind, and size. So a `Thumbs.db` inside a zip of photos is disposable.
  - The relate pass gets this through `Handler.SetClassifier`, which `cmd/precious/serve.go` wires with the server's policy. No import edge is added; without a classifier, no member is disposable.
  - The API classifies members with the policy it already holds (`members.go`).
- **`search.DisposableSQL`** is generated from `domain.Categories` and `domain.FileKinds` through `IsDisposable`, so the Go and SQL tests cannot disagree. A table test checks every combination.
- **In folder figures,** disposable files count only in their own sum (`disposable`), whatever their place. They do not decide a folder's place, its sort key, or what hide keeps. A disposable complete archive puts its whole size there.
- **On their own rows,** they still show their place in words, muted (D8), and hide drops them.
- **Staleness:** a reclassification takes effect at the next relate pass. On an offline source the owner's override changes the row at once (`decisions/owner.go`), and the folder sums wait for an unrelated pass. This is documented and not fixed: `decisions` would otherwise have to import `relations`.

Without this, a `Thumbs.db` or `desktop.ini` that differs in every folder would light up almost every photo folder of a Windows disk as "only copy".

Rejected:
- **Emphasis by bands of the byte share**: a small unique document in a large folder is exactly what the owner wants to find.
- **Leaving disposable files out of the figures**: the bar would no longer add up.

## Interfaces

### Import direction

`domain` ← `search`, `relations`, `content` ← `web/api`. There is no new package and no new edge.

### Go signatures (foundation adds; slices only add)

```go
// internal/domain/places.go
type Place string

const (
	PlaceOnlyCopy    Place = "only_copy"
	PlaceThisSource  Place = "this_source"
	PlaceOtherSource Place = "other_source"
	PlaceUnchecked   Place = "unchecked"
	PlaceUnreadable  Place = "unreadable"
	PlaceNone        Place = "none"
	PlaceNotComputed Place = "not_computed" // a present folder or complete archive with no entry_places row yet
)

type PlaceAmount struct{ Files, Bytes int64 }

// PlaceSums is a folder's or complete archive's files and bytes per place
// over its non-disposable files, plus its disposable files (D4, D5, D14).
type PlaceSums struct {
	OnlyCopy, ThisSource, OtherSource, Unchecked, Unreadable PlaceAmount
	Disposable                                               PlaceAmount
}

// Place applies D1's order; complete=false (the folder or anything below it
// unreadable, partial, or a mount boundary) turns other_source and none into
// unreadable. The relate pass never knows completeness: the read API alone
// computes it, in one unexported helper (api/rows.go, complete(row)), from
// entries.state, partial, mount_boundary, and dir_stats.unreadable and
// mount_boundaries (D1).
func (p PlaceSums) Place(complete bool) Place
// OnlyHereBytes is OnlyCopy.Bytes + ThisSource.Bytes (D10's key).
func (p PlaceSums) OnlyHereBytes() int64
// SplitArchive splits size by the member sums m (D5): exact 128-bit
// shares, largest remainder, at least 1 byte per place with member bytes,
// the one file in the first place of D1's order with member bytes.
func SplitArchive(size int64, m PlaceSums) PlaceSums
// IsDisposable is D14's test: FileFamily(category, kind) is disposable and
// triage is not review.
func IsDisposable(category Category, kind FileKind, triage Triage) bool

// internal/search/places.go
// PlaceSQL is the place of the file entry aliased e with its LEFT JOINed
// file_content row fc: one of D1's values, or NULL for a missing or
// quarantined entry. Complete archives are the caller's (entry_places).
func PlaceSQL(e, fc string) string
// DisposableSQL is true for a file entry aliased e that D14 counts apart.
func DisposableSQL(e string) string
// MemberPlaceSQL is the place of the file member aliased m of the archive
// entry aliased ae, before D5's archive coverage (D6).
func MemberPlaceSQL(m, ae string) string
// OfflineOnlySQL is D11's test for the entry aliased e; MemberOfflineOnlySQL
// the same for a member.
func OfflineOnlySQL(e, fc string) string
func MemberOfflineOnlySQL(m, ae string) string

// internal/content/preferred.go
type PreferredCopy struct {
	Copy     // ref, source, path, archive, offline (copies.go)
	More int // physical copies minus 2 (D9)
}
// PreferredCopies returns D9's copy for each ref with its place
// (this_source or other_source); refs with no other copy are absent.
func PreferredCopies(ctx context.Context, q store.Queryer, want map[domain.Ref]domain.Place) (map[domain.Ref]PreferredCopy, error)

// internal/relations/read.go
// CoveredBy returns, for each ref (a folder or a whole archive), D9's
// relation in the current generation; refs with none are absent.
func CoveredBy(ctx context.Context, q store.Queryer, refs []domain.Ref) (map[domain.Ref]Relation, error)

// internal/relations/job.go
// SetClassifier gives the relate pass the member classification of D14
// (wired in cmd/precious/serve.go with the server's policy); without it no
// member is disposable.
func (h *Handler) SetClassifier(f func(name []byte, size int64) (domain.Category, domain.FileKind, domain.Triage))

// internal/relations (unexported, owned by the relations slice)
func (s *Snapshot) entryPlaces() map[int64]domain.PlaceSums
func (h *Handler) writeEntryPlaces(ctx context.Context, rt jobs.Runtime, want map[int64]domain.PlaceSums) error
```

### Table (migration `0009_places.sql`; foundation)

```sql
CREATE TABLE entry_places (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,  -- a folder or a complete archive
  only_copy_files    INTEGER NOT NULL CHECK (only_copy_files >= 0),    only_copy_bytes    INTEGER NOT NULL CHECK (only_copy_bytes >= 0),
  this_source_files  INTEGER NOT NULL CHECK (this_source_files >= 0),  this_source_bytes  INTEGER NOT NULL CHECK (this_source_bytes >= 0),
  other_source_files INTEGER NOT NULL CHECK (other_source_files >= 0), other_source_bytes INTEGER NOT NULL CHECK (other_source_bytes >= 0),
  unchecked_files    INTEGER NOT NULL CHECK (unchecked_files >= 0),    unchecked_bytes    INTEGER NOT NULL CHECK (unchecked_bytes >= 0),
  unreadable_files   INTEGER NOT NULL CHECK (unreadable_files >= 0),   unreadable_bytes   INTEGER NOT NULL CHECK (unreadable_bytes >= 0),
  disposable_files   INTEGER NOT NULL CHECK (disposable_files >= 0),   disposable_bytes   INTEGER NOT NULL CHECK (disposable_bytes >= 0)
);
UPDATE review_state SET dirty = 1 WHERE id = 1;
-- Since A15: the unpacked sums of member folders and of whole complete archives.
CREATE TABLE member_places (member_id INTEGER PRIMARY KEY REFERENCES archive_members(id) ON DELETE CASCADE, /* the same 12 columns and CHECKs */ …);
CREATE TABLE archive_places (entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE, /* the same 12 columns and CHECKs */ …);
```

### Read API

**Parameters:**
- `GET /api/entries/{id}/children` gains:
  - `places` (`1`);
  - `hide` (`other_source`, needs `places=1`);
  - `sort=only_here` (needs `places=1`).
- `GET /api/entries/{id}/treemap` and `GET /api/entries/{id}` gain `places` (`1`).

Each of the following returns 400 `invalid_request`:
- any other value of `places` or `hide`;
- `hide` or `sort=only_here` without `places=1`;
- a cursor whose sort or filter differs from the request.

**Without `places=1`,** no response changes; a golden comparison checks this.

**With `places=1`,** every `entryRow` carries the following. IDs are decimal strings, as everywhere: `"123"` for an entry, `"m456"` for a member.

```jsonc
{
  "place": "only_copy" | "this_source" | "other_source" | "unchecked" | "unreadable" | "none" | "not_computed" | null,
  "place_offline": false,       // D11; true only for files and members placed other_source
  "disposable": false,          // D14; files only
  "places": {                   // folders, complete archives (D5 split), member folders (unpacked); null for plain files and not_computed
    "only_copy":    {"files": 0, "bytes": 0},
    "this_source":  {"files": 0, "bytes": 0},
    "other_source": {"files": 0, "bytes": 0},
    "unchecked":    {"files": 0, "bytes": 0},
    "unreadable":   {"files": 0, "bytes": 0},
    "disposable":   {"files": 0, "bytes": 0}
  } | null,
  // children rows only:
  "other_copy": {"ref": "123" | "m456", "source_id": "…", "path": "…", "path_b64": "…",
                 "archive_id": "789" | null, "archive_path": "…", "member_path": "…",  // the two paths only with archive_id
                 "offline": false, "via_archive": false, "more": 2} | null,
  "covered_by": {"kind": "same" | "inside", "other": {"id": "321", "source_id": "…", "path": "…", "path_b64": "…"}} | null
}
```

For a complete archive, `GET /api/entries/{id}?places=1` carries two figures on its entry row (the treemap's `entry` carries only `places`; review R1):
- `places`, the D5 split, as its row in its parent shows it;
- `member_places`, the unpacked member sums with the coverage applied. The Map's totals line and the panel read this one when the archive is open or selected.

With `places=1`, the entry response also carries `places_updating` (boolean). It is true while `review_state.dirty` is set or a `relate` job is queued or running (D8).

`"via_archive": true` marks a member's copy named through its archive's coverage (D6).

### Front end

- **Types (`api/entries.ts`):**
  - `Place`, `PlaceAmount`, and `PlaceSums`, mirroring the JSON;
  - `EntryRow` gains optional `place`, `place_offline`, `disposable`, `places`, `other_copy`, and `covered_by`;
  - `ChildSort` adds `'only_here'`.
- **Fetchers:**
  - `fetchChildren(id, sort, order, cursor, signal, view?: {places: boolean; hide: boolean})`;
  - `fetchTreemap(id, signal, places?: boolean)`;
  - `fetchEntry(id, signal, places?: boolean)`.

  Each query key includes these options.
- **Colors (`map/colors.ts`):** `ColorMode` adds `'places'`, with `placeColors`: one color per place, plus disposable and not computed.
- **Translation keys:** `map.places.*`, `map.place.*`, `map.colorBy.places`, and `detail.places.*`.

## Concurrency

- **The relate pass** writes `entry_places` right after `dir_dups`, in the same pass, in batches. Each upsert runs only while its entry exists (`WHERE EXISTS (SELECT 1 FROM entries WHERE id = ?1)`).
  - **A command that commits before the snapshot is loaded** is seen by the pass.
  - **A scan or executor action that commits after the load** sets `dirty` and enqueues the next relate job, which corrects the rows. Hashing and archive listing ask for a pass at most once per `refresh_interval` while they run, and once when they end, so their commits reach the figures with that delay.
  - **Entries:** a deleted entry's row is skipped by the `EXISTS` test or removed by the cascade, and source removal cascades too.
- **Generation:** `entry_places` is written before the pass flips `review_state.gen`. Until the flip, a page can show new figures with the previous generation's `covered_by`. The flip follows within the same pass.
- **The live reads** (`PlaceSQL`, `PreferredCopies`, `CoveredBy`, member places) run in the request's one read transaction, so they see one index state and one generation.
- **Lag:**
  - **Folder figures:** they lag file rows by up to one refresh interval plus the pass's run time. While that lasts, hide can keep a folder whose new only copy is already shown on its file row, or drop one whose last only copy just got a copy.
  - **Reclassification:** it reaches D14's sums at the next pass. On an offline source that pass may come much later (D14).

  D8's "Updating…" shows while `places_updating` is true.
- **Nothing here writes to a source or to owner data** (I1, I4). There is no asynchronous result to guard (I9): the pass derives its rows from one snapshot, and the rest are reads.

## Risks / Trade-offs

- **[The relate pass grows]** It now carries:
  - 4 bytes per content key;
  - a disposable bit per entry;
  - the per-directory place sums;
  - a second diff writer.

  Its first run writes one row per folder and complete archive. → `relations/slow_test.go` gains a second source and archives with members, and gates:
  - the pass at most 15% slower, with at most 15% more peak RSS;
  - a second unchanged pass that writes nothing.

  6.2 also times the startup pass on a copy of the reference database and records it.
- **[Sort and hide look at every child on every page]** → They use one set-based query, with D10's cold-page gate. If the gate fails, work stops and the owner decides. The mode is off by default.
- **[Folder figures lag files]** → The checked coverage and "Updating…" (D8) say so, and the Map refetches after each pass.
- **[The proposal's figures will not reappear]** D5 and D14 move bytes. → Acceptance compares with an independent count under D1, D5, and D14.
- **[A source without stable identity counts hard links as copies]** There, a hard-linked file reads `this_source`, as its `copies` reads 2 today. → No change; the docs say so.
- **[The D3 correction to `physOf`]** It can change `dd.copies` where two sources reuse a (dev, ino). → Unit test; noted in the docs.
- **[A disposable file that matters]** → Triage `review` keeps it out of D14. The row still states its place, and the bar shows the disposable share.

## Migration Plan

1. On the test server, back up the database and keep the previous binary, as before each deploy.
2. Start the new binary. Migration 0009 runs, and startup enqueues one relate pass. Measure that pass first on a copy of the reference database (6.2).
3. Rollback: stop the server, restore the backup, and start the previous binary. The older binary refuses a newer schema (`store.ErrSchemaTooNew`).

## Addendum: decisions made during implementation

- **A1. `SplitArchive` edge cases** (task 1.2):
  - **Ties:** an equal remainder goes to the earlier place in D1's order. When two shares are equally largest, the earlier place gives up the minimum byte.
  - **Small archives:** an archive smaller than its number of places with member bytes gives 1 byte each to `size` of them; a 0-byte archive still puts its one file in the first place.
  - **Inputs:** only the member bytes are read; file counts and `Disposable` are ignored, and the result's `Disposable` is zero. Negative inputs count as 0.
  - **Overflow:** totals above 64 bits are scaled down before `Div64`.
  - **No member bytes:** gives the empty split, and the caller places the archive by `own`.
- **A2. `member_places` sits on the entry row** (task 1.3): `detail.entry.member_places`. Since review R1, the treemap's `entry` no longer carries it. `places_updating` sits at the top of the entry response. The query keys without options are unchanged, so invalidating them also refreshes the place variants.
- **A3. The upgrade note** (task 1.1) gives no time for the first relate pass; 6.2 measures it.
- **A4. The relate pass** (task 2.1):
  - **Classifier:** `LoadSnapshot` takes the classifier as a third parameter; its only caller is the pass.
  - **D14 in the pass:** the pass computes D14 in its own SQL column, generated from the domain lists. A table test pins it to `IsDisposable`, as 2.2's `DisposableSQL` is pinned.
  - **Odd content states:** a hashed file or member with no content ID, and a tar link member with no file target, are `unchecked`.
  - **Disposable members:** they drop out of an archive's split weights. Only a disposable archive puts `{1 file, its size}` into `Disposable`.
  - **Member classification:** it passes no sibling stems.
  - **Slow gate**, median of 3 runs on the extended fixture: 5.41 s and 493 MiB, against a 4.90 s and 474 MiB baseline (+10.4%, +4.0%).
- **A5. Member copies** (task 2.2): a member's other copies leave out the member itself, its tar link group, and the members of the *other* names in its archive's hard-link set. Other members of its own archive do count, as in the snapshot.
  - **Folders and archives:** `PlaceSQL` is `NULL` for a folder; `MemberPlaceSQL` is `NULL` when the archive entry is not present or is quarantined.
  - **Disposable:** `DisposableSQL` requires `kind = 'file'`.
- **A6. `CoveredBy`** (task 2.3) returns relations as stored, without reversing them, and accepts member-folder refs. `PreferredCopy` leaves the decision fields empty.
- **A7. Panel** (task 4.2): the panel asks for places only when the address has `places=1`. For a file not checked or unreadable, the place sentence replaces the older not-checked line.
- **A8. Map** (task 4.1):
  - **Checked share:** it is capped at 99% until whole.
  - **Member paths:** a member copy's path is split at its first `!`.
  - **Second line:** it adds "on <source>" for another source.
  - **Special files** read "No content".
  - **Treemap:** its "other" area keeps its existing gray.
- **A9. Read API** (tasks 3.1, 3.2):
  - **Parameters:** an empty `places=` or `hide=` counts as absent; `only_here` defaults to descending. The cursor gains `u` (the second key) and `h` (hide).
  - **"Any other copy":** in the set-based query, this means at least two physical identities.
  - **A file's second key** is its size when it is unchecked or unreadable and not disposable.
  - **Folders:** a folder whose own state is `unreadable` still reads its stored figures. A missing complete archive has place `null`.
  - **Archive coverage:** `other_source` lifts every member with content; `this_source` lifts only-copy members. `via_archive` means the coverage changed the member's place. Complete archive rows get no `other_copy`.
  - **`places_updating`** counts queued or running relate jobs, not paused ones.
  - **Speed** (D10 fixture, 2M entries): on a 30,000-file folder the first page takes p95 468 ms (`only_here`) and 492 ms (`hide` + `bytes`). A 100,000-file folder takes 1.6 s, a 50,000-member archive level 0.53–0.61 s, and its treemap 0.87 s. R1.10 pages stay at p95 9.4 ms.
  - **Golden comparison:** the full comparison without `places` (about 3,500 requests, recorded on the base build) runs under `-tags slow` only.
  - **Test time:** `go test -race ./internal/web/api` now takes about 65 s under load. It was about 56 s before, and the new tests take about 10.5 s of it. This is accepted over the 60 s guideline; the package was already near it.
- **A10. Code review, round R1** (task 6.3): two independent reviews, one of the back end and one of the front end. Each finding was fixed with a test that failed before the fix.
  - **Back end:** a complete archive hard-linked to a name that is not a complete archive counted as two copies in the relate pass (the stored figures said `this_source`, the live rows `only_copy`). `dir_dups` had the same overcount since R2. `countCopies` now skips the extra archive copy when its identity is already an entry's.
  - **Front end:**
    - an unreadable folder or mount point read as "Nothing with content" in the totals and the panel (I7); it now reads "Could not be read" or "Partly unreadable";
    - qualifiers and "on <source>" were cut off in narrow columns; they are now their own spans (A15 and A16 reworked how the line shrinks);
    - an opened archive showed "Not computed yet" beside live member figures;
    - member copies were split at the first `!`; `other_copy` now carries `archive_path` and `member_path`;
    - the Map and the panel rounded the checked share differently, and the panel showed it twice; one helper now does it;
    - the place column could hide on narrow screens; it no longer hides in the mode;
    - an archive's row (packed split) and its panel (unpacked sums) looked contradictory; the panel now also gives the split;
    - the hide option gets a hint ("Empty items and system files are hidden too."), and the panel's "Updating…" sentence is reworded.
  - **The treemap** no longer carries `member_places`, which nothing read.
- **A11. A hashing deadlock, older than R5c** (found by task 5.1): since R2, a hash job kept `planMu` while it yielded its device slot. A second hash job then took the slot and waited on `planMu`, and neither ever ended. This happens whenever two hash jobs run at once, for example at startup with two or more online sources. `plan` now creates its rows before it takes the lock, and it no longer yields under it. Test: `TestHashJobsOfTwoSourcesOnOneDeviceBothEnd`, which failed before the fix.
- **A12. Browser fixture** (task 5.1): `disk/copies` (label "Copies") holds:
  - a byte-identical copy of `celular_backup_2009/DCIM/100MEDIA`;
  - copies of three photos, one of them copied twice;
  - one file of its own.

  Every copy is taken from corpus files that `precious.spec.ts` never changes. The `only-here` Playwright project depends on `chromium`, and `chromium` ignores `only-here.spec.ts`. The "not computed yet comes first" case is tested in the API only.
- **A13. Full verification** (task 6.1), on the merged branch:
  - gofmt, `go vet` (plain and `-tags slow`), and `go test -race ./...` pass;
  - the slow tests pass, among them `TestRelateAtScale`, the places speed gate, R1.10, and the golden comparison;
  - `npm test` (352) and `npm run e2e` (64, in 1.9 min) pass;
  - `scripts/e2e-docker.sh` passes;
  - the six builds pass.

  Once, with every slow package running at the same time, `TestReviewPagesStayFastAt2MillionEntries` (`internal/review`, which this change does not touch) read a cards p95 of 1.53 s against 1 s. Run alone, it passes both on this branch and on the previous release (55.5 s and 56.6 s), so this is load, not a regression.
- **A14. Empty disposable files** (task 6.2): an empty file has no place and is in no sum, the disposable sum included, because D4 adds only files of size > 0.
- **A15. Smoke test on a copy of the reference database** (task 6.2; non-root, sources offline on the test machine):
  - **Relate pass on the copy:**

    |Build|Time|Peak RSS|
    |---|---|---|
    |previous release (r5b)|133.5–135.9 s|2.39 GB|
    |this change|about 146 s (144.5–146.6 s, same window)|2.08–2.35 GB|

    That is about +9% in time, within the 15% gate. The first pass writes 157,686 `entry_places`, 256,491 `member_places`, and 18,622 `archive_places` rows; a second pass writes none.
  - **Independent count:** a throwaway program reimplemented D1, D3, D5, and D14 in plain SQL and Go. It matched all 157,686 stored `entry_places` rows.
    - It switched rules off to check what this database tests: tar link groups, archive coverage, and disposable members are exercised; hard-link sets are not.
    - The external disk's root reads 228 GB only copy (152,808 files), 253 GB only on that disk (966,917 files), 5.8 GB on another source (23,121 files), and 12.8 MB disposable.
  - **Found and fixed:**
    - **Opening the largest archive (567,623 members) in the mode took about 25 s.** The children, treemap, and entry calls took 12.2, 11.4, and 12.2 s, against 6.3, 6.9, and 0.014 s without places. Member folder and whole-archive sums are now stored (D6 revised). With places, the calls now take 3.8, 3.8, and 0.014 s. The rest is the older walk for the "Has copies" figures, which R5b already had.
    - **The line under a folder's name overlapped its Compare link,** and archive copies read as "…160)". The line now cannot overlap. It shows the archive's and the member's file names, and the full paths are in the link's description (A16). In the mode, the name column gets more room.
  - **Slow gate in `relations`:** on this busy machine, the fixture's median is 6.34 s with the change and 6.15 s without it, measured interleaved, so the change adds about 3%. Both are above the 4.90 s recorded earlier on a quieter machine, so the gate is judged against the interleaved runs.
- **A16. Code review, round R2** (task 6.3), of the fixes since R1. Each finding was fixed with a test that failed before the fix.
  - **Back end:** the folders inside an archive whose entry is missing or unreadable read "Not computed yet" forever. They now have no place, like the files inside it.
  - **Front end:**
    - **The line under a folder's name dropped the other folder's path.** The words now give way first and keep the last ~10 characters of names; "on <source>" never shrinks. The sentences are shorter: "Same as <path/>" and "All of it is in <path/>".
    - **Extra words in the place column were cut with no hint.** They now end with an ellipsis, and the whole sentence is the tooltip.
    - **"Everything has a copy on another source" did not fit** the 12rem column. It now reads "All also on another source".
    - **The note link's accessible name did not contain its visible text** (WCAG 2.5.3). Its name is now the visible text, and the full-path sentence is its description (`title`).
  - **Remaining limit:** on file rows with a long name, a copy on an unconnected source, and "+N more", the words "Also at" can disappear at 1366 px. The tooltip and the description keep the full sentence.
- **A17. Relate pass speed on real data** (task 6.2). On a quiet machine the first measurements read 83.4 s for r5b against 139.3 s for this change (+67%). Earlier runs had been taken under load, which hid this.
  - **Profile, one pass in-process:** writing the new tables (432,799 rows in random key order, 864 commits) took 16 s. Classifying 3.28 million members took 1.8 s of CPU, one member at a time. The rest was unchanged.
  - **The fix:**
    - `writeFigures` (`dir_dups` and the three places tables) now writes in key order, in batches of `figureBatch` = 5,000 rows, against D4's 500;
    - members are classified on all CPUs before they are processed; a tar link member is still classified by its own name and its target's size.
  - **After the fix, in-process:** the first pass takes 1.04× r5b's time and a second pass 1.06×.
  - **After the fix, server runs** (4 interleaved pairs, noisy because the startup jobs compete): the first pass takes 0.99× (133.8 s against 135.8 s), and a second pass 1.02×. Peak memory is 2.0–2.34 GB, against 2.36–2.54 GB for r5b.
  - **No figure changed:** the independent count matched all 157,686 `entry_places`, 256,491 `member_places`, and 18,622 `archive_places` rows on the final build.
  - **Final build in a browser,** on the copy:
    - the disk's root and folders open in 0.1–0.25 s;
    - the 31,163-file folder opens in 0.84–0.86 s with sort or hide, its API page answering in 0.37 s;
    - the 567,623-member archive opens in 2.8 s.
