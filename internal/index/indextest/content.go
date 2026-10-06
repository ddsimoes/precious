package indextest

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/store"
)

// Content seeding (R2 design D3, D7, D8, schema 0002_content.sql). These
// helpers write what hashing writes, for the tests of the code that reads it
// (relations, review lists, the read API):
//
//   - Seeded.SeedContent writes a whole source's content rows from a
//     corpus.GroundTruth, as a complete hashing run leaves them.
//   - Seeded.SetContent writes one file's file_content row, and
//     Seeded.SeedArchive one archive's archives and archive_members rows,
//     for hand-built worlds.
//   - RecomputeCoverage rewrites content_coverage.
//
// Identity: a file_content row in a state that a read produced (sampled,
// hashed, changed, unreadable) copies the entry row's size, mtime_ns,
// ctime_ns, and ino, and is checked at the seeded scan time; unique_size and
// pending rows have no identity and no checked_at. An archives row always
// copies the entry row's identity and is listed at the seeded scan time.
//
// Coverage (D8) counts, per source, the file_content rows and the file
// members of its complete archives (under the archive entry's source):
// candidate = every row but unique_size, checked = hashed and sampled,
// unchecked = pending and changed, unreadable = unreadable. A source
// without rows gets zeros; updated_at is the newest sources.last_scan_at.
// SetContent and SeedArchive leave coverage alone; call RecomputeCoverage
// after them. SeedContent recomputes it.

// Content is the content state of a file or a file member.
type Content struct {
	State domain.ContentState
	// SHA256 is the content digest (32 bytes), required for hashed and
	// refused otherwise.
	SHA256 []byte
	// Sample is the 64 KiB-sample digest (32 bytes), required for sampled
	// and optional for the other read states of a file. Members have none.
	Sample []byte
}

// Member is one member of a seeded archive. Folders above a listed path
// that are not listed themselves are created as directory members.
type Member struct {
	// Path is the raw names inside the archive joined by '/'.
	Path string
	// Kind defaults to domain.MemberFile.
	Kind domain.MemberKind
	Size int64
	// MTime is the time the archive records; zero writes NULL.
	MTime time.Time
	// LinkText is a symlink member's text.
	LinkText string
	// Locator is a zip member's central-directory index; nil writes NULL.
	Locator *int
	// Stored marks a zip member stored without compression.
	Stored bool
	// Content is a file member's state: unique_size, pending, hashed, or
	// unreadable. It must be zero for other kinds.
	Content Content
}

// Archive is the listing of one seeded archive file.
type Archive struct {
	Format domain.ArchiveFormat
	// State defaults to domain.ArchiveComplete.
	State domain.ArchiveState
	// Detail is the detail JSON; "" writes NULL.
	Detail  string
	Members []Member
}

// SeededArchive is a seeded archive and its member IDs.
type SeededArchive struct {
	ID      domain.EntryID
	members map[string]domain.MemberID
	t       testing.TB
}

// Member returns the ID of the member at path (created implied folders
// included) and fails the test for any other.
func (a *SeededArchive) Member(path string) domain.MemberID {
	a.t.Helper()
	id, ok := a.members[path]
	if !ok {
		a.t.Fatalf("indextest: archive member %q was not seeded", path)
	}
	return id
}

// entryRow is the identity of a seeded file entry.
type entryRow struct {
	id         domain.EntryID
	path       string
	size       int64
	mtime      int64
	ctime, ino sql.NullInt64
	source     domain.SourceID
}

func loadEntry(tx *sql.Tx, id domain.EntryID) (entryRow, error) {
	var r entryRow
	var p []byte
	var kind, state string
	err := tx.QueryRow(`SELECT id, path, size, mtime_ns, ctime_ns, ino, source_id, kind, state FROM entries WHERE id = ?`,
		int64(id)).Scan(&r.id, &p, &r.size, &r.mtime, &r.ctime, &r.ino, &r.source, &kind, &state)
	if err != nil {
		return r, err
	}
	r.path = string(p)
	if kind != string(domain.EntryFile) || state != "present" {
		return r, fmt.Errorf("%q is a %s entry in state %s, not a present file", r.path, kind, state)
	}
	return r, nil
}

// SetContent writes (or replaces) the file_content row of the seeded file at
// path. The file must be present and non-empty. A hashed state adds its
// contents row (sha256, the file's size) when missing.
func (s *Seeded) SetContent(st *store.Store, path string, c Content) {
	s.t.Helper()
	id := s.ID(path)
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		e, err := loadEntry(tx, id)
		if err != nil {
			return err
		}
		return writeFileContent(tx, e, c, s.now)
	})
	if err != nil {
		s.t.Fatalf("indextest: content of %q: %v", path, err)
	}
}

// SeedArchive writes the archives row of the seeded file at path, with the
// entry's identity, and its members.
func (s *Seeded) SeedArchive(st *store.Store, path string, a Archive) *SeededArchive {
	s.t.Helper()
	id := s.ID(path)
	var members map[string]domain.MemberID
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		e, err := loadEntry(tx, id)
		if err != nil {
			return err
		}
		members, err = writeArchive(tx, e, a, s.now)
		return err
	})
	if err != nil {
		s.t.Fatalf("indextest: archive %q: %v", path, err)
	}
	return &SeededArchive{ID: id, members: members, t: s.t}
}

// SeedContent writes the content rows of the seeded source from truth (D3,
// D7, D8), as a complete hashing run leaves them, and recomputes coverage.
// truth must describe the seeded tree (corpus.Corpus().GroundTruth() seeded
// with CorpusNodes). It returns the archives by raw path.
//
//   - Every archive of truth.Members is complete, with its members. Their
//     locator, stored flag, and mtime come from the ground truth.
//   - An archive of a streamed format (domain.ArchiveFormat.Streamed: the tar
//     family, gzip, and bzip2) was read whole: its file is hashed with its
//     own digest even when its size is unique, and every non-empty file
//     member is hashed.
//   - Every other present non-empty file, and every non-empty member of a
//     zip, follows the size rule: unique_size when no other present file or
//     file member of a complete archive has its size, index-wide (other
//     sources included, a hard-link set counted once by source, dev, and
//     ino), else hashed with the ground truth's digest.
//   - Empty files have no file_content row; an empty file member is
//     unique_size (it has no content).
//
// Rows of other sources are not revisited, so seed sources whose sizes meet
// before seeding their content.
func (s *Seeded) SeedContent(st *store.Store, truth corpus.GroundTruth) map[string]*SeededArchive {
	s.t.Helper()
	digests := make(map[string][]byte, len(truth.Entries))
	for _, e := range truth.Entries {
		if e.Kind != domain.EntryFile {
			continue
		}
		raw, err := e.RawPath()
		if err != nil {
			s.t.Fatalf("indextest: %v", err)
		}
		sum, err := hex.DecodeString(e.SHA256)
		if err != nil || len(sum) != 32 {
			s.t.Fatalf("indextest: %s: sha256 %q", e.Path, e.SHA256)
		}
		digests[string(raw)] = sum
	}
	archives := make(map[string]Archive, len(truth.Members))
	for _, a := range truth.Members {
		raw := s.raw(a.PathB64)
		arc := Archive{Format: domain.ArchiveFormat(a.Format)}
		for _, m := range a.Members {
			x := Member{Path: s.raw(m.PathB64), Kind: domain.MemberKind(m.Kind), Locator: m.Locator, Stored: m.Stored}
			if m.Size != nil {
				x.Size = *m.Size
			}
			if m.MTime != nil {
				x.MTime = *m.MTime
			}
			if x.Kind == domain.MemberFile && x.Size > 0 {
				sum, err := hex.DecodeString(m.SHA256)
				if err != nil || len(sum) != 32 {
					s.t.Fatalf("indextest: %s!%s: sha256 %q", a.Path.Path, m.Path.Path, m.SHA256)
				}
				x.Content.SHA256 = sum
			}
			arc.Members = append(arc.Members, x)
		}
		archives[raw] = arc
	}

	out := make(map[string]*SeededArchive, len(archives))
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		sizes, err := sharedSizes(tx)
		if err != nil {
			return err
		}
		for _, a := range archives {
			for _, m := range a.Members {
				if m.Kind == domain.MemberFile && m.Size > 0 {
					sizes[m.Size]++
				}
			}
		}
		rows, err := tx.Query(`SELECT id FROM entries WHERE source_id = ? AND kind = 'file' AND state = 'present' AND size > 0 ORDER BY id`,
			string(s.Source))
		if err != nil {
			return err
		}
		var ids []domain.EntryID
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, domain.EntryID(id))
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			e, err := loadEntry(tx, id)
			if err != nil {
				return err
			}
			a, isArchive := archives[e.path]
			c := Content{State: domain.ContentUniqueSize}
			if sizes[e.size] > 1 || isArchive && a.Format.Streamed() {
				sum, ok := digests[e.path]
				if !ok {
					return fmt.Errorf("%q has no digest in the ground truth", e.path)
				}
				c = Content{State: domain.ContentHashed, SHA256: sum}
			}
			if err := writeFileContent(tx, e, c, s.now); err != nil {
				return err
			}
			if !isArchive {
				continue
			}
			for i := range a.Members {
				m := &a.Members[i]
				if m.Kind != domain.MemberFile {
					continue
				}
				if m.Size > 0 && (a.Format.Streamed() || sizes[m.Size] > 1) {
					m.Content.State = domain.ContentHashed
				} else {
					m.Content = Content{State: domain.ContentUniqueSize}
				}
			}
			members, err := writeArchive(tx, e, a, s.now)
			if err != nil {
				return err
			}
			out[e.path] = &SeededArchive{ID: e.id, members: members, t: s.t}
		}
		for p := range archives {
			if out[p] == nil {
				return fmt.Errorf("archive %q is not a present non-empty file of the source", p)
			}
		}
		return recomputeCoverage(tx)
	})
	if err != nil {
		s.t.Fatalf("indextest: seed content of %s: %v", s.Source, err)
	}
	return out
}

func (s *Seeded) raw(b64 string) string {
	s.t.Helper()
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		s.t.Fatalf("indextest: path_b64 %q: %v", b64, err)
	}
	return string(b)
}

// sharedSizes counts, per size, the present non-empty files of every source
// (a hard-link set once) and the non-empty file members of complete archives
// already in the index.
func sharedSizes(tx *sql.Tx) (map[int64]int, error) {
	sizes := map[int64]int{}
	rows, err := tx.Query(`SELECT size, count(*) FROM (
		SELECT DISTINCT size, source_id, CASE WHEN dev IS NULL OR ino IS NULL THEN 'e' || id ELSE dev || ':' || ino END
		FROM entries WHERE kind = 'file' AND state = 'present' AND size > 0
		UNION ALL
		SELECT m.size, NULL, 'm' || m.id FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id
		WHERE a.state = 'complete' AND m.kind = 'file' AND m.size > 0) GROUP BY size`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var size int64
		var n int
		if err := rows.Scan(&size, &n); err != nil {
			return nil, err
		}
		sizes[size] = n
	}
	return sizes, rows.Err()
}

// contentID returns the contents row of sum, adding it when missing.
func contentID(tx *sql.Tx, sum []byte, size int64) (int64, error) {
	if len(sum) != 32 {
		return 0, fmt.Errorf("sha256 of %d bytes", len(sum))
	}
	if _, err := tx.Exec(`INSERT INTO contents (sha256, size) VALUES (?, ?) ON CONFLICT (sha256) DO NOTHING`, sum, size); err != nil {
		return 0, err
	}
	var id, got int64
	if err := tx.QueryRow(`SELECT id, size FROM contents WHERE sha256 = ?`, sum).Scan(&id, &got); err != nil {
		return 0, err
	}
	if got != size {
		return 0, fmt.Errorf("sha256 %x has size %d and %d", sum, got, size)
	}
	return id, nil
}

func writeFileContent(tx *sql.Tx, e entryRow, c Content, now int64) error {
	if !c.State.Valid() {
		return fmt.Errorf("%q: content state %q", e.path, c.State)
	}
	if e.size <= 0 {
		return fmt.Errorf("%q is empty: it has no content row", e.path)
	}
	if (c.State == domain.ContentHashed) != (c.SHA256 != nil) {
		return fmt.Errorf("%q: a sha256 is required for hashed and refused otherwise", e.path)
	}
	if c.State == domain.ContentSampled && c.Sample == nil {
		return fmt.Errorf("%q: sampled needs a sample", e.path)
	}
	var cid, mtime, ctime, ino, checked any
	if c.State == domain.ContentHashed {
		id, err := contentID(tx, c.SHA256, e.size)
		if err != nil {
			return fmt.Errorf("%q: %w", e.path, err)
		}
		cid = id
	}
	if c.State != domain.ContentUniqueSize && c.State != domain.ContentPending {
		mtime, ctime, ino, checked = e.mtime, e.ctime, e.ino, now
	}
	var sample any
	if c.Sample != nil {
		sample = c.Sample
	}
	_, err := tx.Exec(`INSERT INTO file_content (entry_id, source_id, state, size, mtime_ns, ctime_ns, ino, sample, content_id, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entry_id) DO UPDATE SET state = excluded.state, size = excluded.size, mtime_ns = excluded.mtime_ns,
		ctime_ns = excluded.ctime_ns, ino = excluded.ino, sample = excluded.sample, content_id = excluded.content_id,
		checked_at = excluded.checked_at`,
		int64(e.id), string(e.source), string(c.State), e.size, mtime, ctime, ino, sample, cid, checked)
	return err
}

// memberNode is one member row to write, with its subtree totals. An
// implied folder was created for a deeper path; an explicit member of the
// same path replaces it.
type memberNode struct {
	Member
	implied      bool
	bytes, files int64
}

func writeArchive(tx *sql.Tx, e entryRow, a Archive, now int64) (map[string]domain.MemberID, error) {
	if !a.Format.Valid() {
		return nil, fmt.Errorf("%q: archive format %q", e.path, a.Format)
	}
	if a.State == "" {
		a.State = domain.ArchiveComplete
	}
	if !a.State.Valid() {
		return nil, fmt.Errorf("%q: archive state %q", e.path, a.State)
	}
	nodes := map[string]*memberNode{}
	for _, m := range a.Members {
		if m.Kind == "" {
			m.Kind = domain.MemberFile
		}
		if !m.Kind.Valid() || m.Path == "" || strings.HasPrefix(m.Path, "/") || strings.HasSuffix(m.Path, "/") {
			return nil, fmt.Errorf("%q: member %q of kind %q", e.path, m.Path, m.Kind)
		}
		if nodes[m.Path] != nil && !nodes[m.Path].implied {
			return nil, fmt.Errorf("%q: member %q listed twice", e.path, m.Path)
		}
		if m.Kind == domain.MemberFile {
			switch m.Content.State {
			case domain.ContentUniqueSize, domain.ContentPending, domain.ContentHashed, domain.ContentUnreadable:
			default:
				return nil, fmt.Errorf("%q: file member %q in content state %q", e.path, m.Path, m.Content.State)
			}
			if (m.Content.State == domain.ContentHashed) != (m.Content.SHA256 != nil) || m.Content.Sample != nil {
				return nil, fmt.Errorf("%q: file member %q: sha256 only and always when hashed, and no sample", e.path, m.Path)
			}
		} else if m.Content.State != "" || m.Size != 0 {
			return nil, fmt.Errorf("%q: %s member %q with a size or content", e.path, m.Kind, m.Path)
		}
		nodes[m.Path] = &memberNode{Member: m}
		for dir := path.Dir(m.Path); dir != "."; dir = path.Dir(dir) {
			if nodes[dir] == nil {
				nodes[dir] = &memberNode{Member: Member{Path: dir, Kind: domain.MemberDirectory}, implied: true}
			}
		}
	}
	paths := make([]string, 0, len(nodes))
	var unpacked int64
	for p, n := range nodes {
		paths = append(paths, p)
		if n.Kind == domain.MemberDirectory {
			continue
		}
		if n.Kind == domain.MemberFile {
			n.bytes, n.files = n.Size, 1
			unpacked += n.Size
		}
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			d := nodes[dir]
			if d.Kind != domain.MemberDirectory {
				return nil, fmt.Errorf("%q: member %q is both a file and a folder", e.path, dir)
			}
			d.bytes += n.bytes
			d.files += n.files
		}
	}
	// Parents before children: by depth, then by path.
	slices.SortFunc(paths, func(x, y string) int {
		return cmp.Or(cmp.Compare(strings.Count(x, "/"), strings.Count(y, "/")), strings.Compare(x, y))
	})

	var detail any
	if a.Detail != "" {
		detail = a.Detail
	}
	if _, err := tx.Exec(`INSERT INTO archives (entry_id, format, state, detail, size, mtime_ns, ctime_ns, ino,
		members, unpacked_bytes, listed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(e.id), string(a.Format), string(a.State), detail, e.size, e.mtime, e.ctime, e.ino,
		len(nodes), unpacked, now); err != nil {
		return nil, err
	}
	ids := make(map[string]domain.MemberID, len(nodes))
	for _, p := range paths {
		n := nodes[p]
		var parent, mtime, link, locator, state, content any
		if dir := path.Dir(p); dir != "." {
			parent = int64(ids[dir])
		}
		if !n.MTime.IsZero() {
			mtime = n.MTime.UnixNano()
		}
		if n.Kind == domain.MemberSymlink {
			link = blob(n.LinkText)
		}
		if n.Locator != nil {
			locator = *n.Locator
		}
		if n.Kind == domain.MemberFile {
			state = string(n.Content.State)
			if n.Content.State == domain.ContentHashed {
				id, err := contentID(tx, n.Content.SHA256, n.Size)
				if err != nil {
					return nil, fmt.Errorf("%q!%q: %w", e.path, p, err)
				}
				content = id
			}
		}
		res, err := tx.Exec(`INSERT INTO archive_members (archive_id, parent_id, name, path, kind, size, mtime_ns, link_text,
			total_bytes, total_files, locator, stored, state, content_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			int64(e.id), parent, blob(path.Base(p)), blob(p), string(n.Kind), n.Size, mtime, link,
			n.bytes, n.files, locator, boolInt(n.Stored), state, content)
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids[p] = domain.MemberID(id)
	}
	return ids, nil
}

// RecomputeCoverage rewrites content_coverage for every source (see the
// package doc).
func RecomputeCoverage(t testing.TB, st *store.Store) {
	t.Helper()
	if err := st.Write(context.Background(), recomputeCoverage); err != nil {
		t.Fatalf("indextest: coverage: %v", err)
	}
}

func recomputeCoverage(tx *sql.Tx) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO content_coverage (source_id, candidate_files, candidate_bytes,
		checked_files, checked_bytes, unchecked_files, unchecked_bytes, unreadable_files, unreadable_bytes, updated_at)
		WITH rows(source_id, state, size) AS (
			SELECT source_id, state, size FROM file_content
			UNION ALL
			SELECT e.source_id, m.state, m.size FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id
				JOIN entries e ON e.id = m.archive_id WHERE a.state = 'complete' AND m.kind = 'file')
		SELECT s.id,
			count(r.state) FILTER (WHERE r.state <> 'unique_size'), coalesce(sum(r.size) FILTER (WHERE r.state <> 'unique_size'), 0),
			count(r.state) FILTER (WHERE r.state IN ('hashed','sampled')), coalesce(sum(r.size) FILTER (WHERE r.state IN ('hashed','sampled')), 0),
			count(r.state) FILTER (WHERE r.state IN ('pending','changed')), coalesce(sum(r.size) FILTER (WHERE r.state IN ('pending','changed')), 0),
			count(r.state) FILTER (WHERE r.state = 'unreadable'), coalesce(sum(r.size) FILTER (WHERE r.state = 'unreadable'), 0),
			(SELECT coalesce(max(last_scan_at), 0) FROM sources)
		FROM sources s LEFT JOIN rows r ON r.source_id = s.id GROUP BY s.id`)
	return err
}

// CorpusNodes returns the nodes of a corpus ground truth for Seed: every
// entry with its kind and size, unreadable folders marked, and symlinks
// with the text "target". Classification and times are left to defaults.
func CorpusNodes(t testing.TB, truth corpus.GroundTruth) []Node {
	t.Helper()
	nodes := make([]Node, 0, len(truth.Entries))
	for _, e := range truth.Entries {
		raw, err := e.RawPath()
		if err != nil {
			t.Fatalf("indextest: %v", err)
		}
		n := Node{Path: string(raw), Kind: e.Kind, Unreadable: e.Unreadable}
		if e.Size != nil {
			n.Size = *e.Size
		}
		if e.Kind == domain.EntrySymlink {
			n.LinkText = "target"
		}
		nodes = append(nodes, n)
	}
	return nodes
}
