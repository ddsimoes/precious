package indextest_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// seedCorpusContent seeds the corpus as source "corpus" with its content.
func seedCorpusContent(t *testing.T) (*store.Store, *indextest.Seeded, map[string]*indextest.SeededArchive, corpus.GroundTruth) {
	t.Helper()
	st := storetest.Open(t)
	gt := corpus.Corpus().GroundTruth()
	s := indextest.Seed(t, st, indextest.Tree{Source: "corpus", CreateSource: true, MountPoint: "/mnt/corpus",
		Nodes: indextest.CorpusNodes(t, gt)})
	return st, s, s.SeedContent(st, gt), gt
}

func b64(t *testing.T, s string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// copiesSQL lists every copy with content: files, and members of complete
// archives written "archive!member".
const copiesSQL = `WITH copies(content_id, p) AS (
	SELECT f.content_id, e.path FROM file_content f JOIN entries e ON e.id = f.entry_id WHERE f.content_id IS NOT NULL
	UNION ALL
	SELECT m.content_id, CAST(ae.path || X'21' || m.path AS BLOB) FROM archive_members m
		JOIN archives a ON a.entry_id = m.archive_id JOIN entries ae ON ae.id = m.archive_id
		WHERE a.state = 'complete' AND m.content_id IS NOT NULL)`

// 1.7: SeedContent on the seeded corpus gives exactly the ground truth's
// duplicate groups, by SQL.
func TestSeedContentGivesTheDuplicateGroups(t *testing.T) {
	st, _, _, gt := seedCorpusContent(t)
	rows, err := st.Reader().Query(copiesSQL + `
		SELECT hex(c.sha256), c.size, group_concat(hex(copies.p), ',') FROM copies JOIN contents c ON c.id = copies.content_id
		GROUP BY copies.content_id HAVING count(*) > 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][]string{}
	for rows.Next() {
		var sum, list string
		var size int64
		if err := rows.Scan(&sum, &size, &list); err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, h := range strings.Split(list, ",") {
			p, err := hex.DecodeString(h)
			if err != nil {
				t.Fatal(err)
			}
			paths = append(paths, string(p))
		}
		slices.Sort(paths)
		got[strings.ToLower(sum)] = paths
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{}
	for _, d := range gt.Duplicates {
		var paths []string
		for _, c := range d.Copies {
			paths = append(paths, b64(t, c.PathB64))
		}
		slices.Sort(paths)
		want[d.SHA256] = paths
	}
	if len(want) < 50 {
		t.Fatalf("ground truth has %d groups", len(want))
	}
	for sum, paths := range want {
		if !slices.Equal(got[sum], paths) {
			t.Errorf("group %s: got %q, want %q", sum, got[sum], paths)
		}
	}
	for sum, paths := range got {
		if want[sum] == nil {
			t.Errorf("extra group %s: %q", sum, paths)
		}
	}
}

// Every hashed row carries its ground-truth digest; a file is hashed exactly
// when another file or complete-archive member has its size, or it is a
// streamed archive (read whole, D7); coverage equals a direct GROUP BY.
func TestSeedContentStatesAndCoverage(t *testing.T) {
	st, s, _, gt := seedCorpusContent(t)
	digest := map[string]string{}
	for _, e := range gt.Entries {
		if e.Kind == domain.EntryFile {
			digest[b64(t, e.PathB64)] = e.SHA256
		}
	}
	streamed := map[string]bool{}
	for _, a := range gt.Members {
		streamed[b64(t, a.PathB64)] = domain.ArchiveFormat(a.Format).Streamed()
	}
	rows, err := st.Reader().Query(`SELECT e.path, f.state, f.size, e.size, hex(c.sha256),
		f.mtime_ns IS e.mtime_ns AND f.ctime_ns IS e.ctime_ns AND f.ino IS e.ino AND f.checked_at IS NOT NULL,
		EXISTS (SELECT 1 FROM entries o WHERE o.id <> e.id AND o.kind = 'file' AND o.state = 'present' AND o.size = e.size)
		OR EXISTS (SELECT 1 FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id
			WHERE a.state = 'complete' AND m.kind = 'file' AND m.size = e.size)
		FROM file_content f JOIN entries e ON e.id = f.entry_id LEFT JOIN contents c ON c.id = f.content_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var p []byte
		var state string
		var size, entrySize int64
		var sum sql.NullString
		var identity, shared bool
		if err := rows.Scan(&p, &state, &size, &entrySize, &sum, &identity, &shared); err != nil {
			t.Fatal(err)
		}
		n++
		wantState := "unique_size"
		if shared || streamed[string(p)] {
			wantState = "hashed"
		}
		switch {
		case state != wantState:
			t.Errorf("%s: %s, want %s", p, state, wantState)
		case size != entrySize || size == 0:
			t.Errorf("%s: size %d, entry %d", p, size, entrySize)
		case state == "hashed" && (strings.ToLower(sum.String) != digest[string(p)] || !identity):
			t.Errorf("%s: digest %s or identity differs from the ground truth's %s", p, sum.String, digest[string(p)])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var files int
	if err := st.Reader().QueryRow(`SELECT count(*) FROM entries WHERE kind = 'file' AND size > 0`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if n != files {
		t.Errorf("%d file_content rows for %d non-empty files", n, files)
	}

	var cov, direct [8]int64
	if err := st.Reader().QueryRow(`SELECT candidate_files, candidate_bytes, checked_files, checked_bytes,
		unchecked_files, unchecked_bytes, unreadable_files, unreadable_bytes FROM content_coverage WHERE source_id = ?`,
		string(s.Source)).Scan(&cov[0], &cov[1], &cov[2], &cov[3], &cov[4], &cov[5], &cov[6], &cov[7]); err != nil {
		t.Fatal(err)
	}
	if err := st.Reader().QueryRow(`WITH rows(state, size) AS (
			SELECT state, size FROM file_content WHERE source_id = ?1
			UNION ALL
			SELECT m.state, m.size FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id
				JOIN entries e ON e.id = a.entry_id WHERE e.source_id = ?1 AND a.state = 'complete' AND m.kind = 'file')
		SELECT
		count(*) FILTER (WHERE state <> 'unique_size'), coalesce(sum(size) FILTER (WHERE state <> 'unique_size'), 0),
		count(*) FILTER (WHERE state IN ('hashed', 'sampled')), coalesce(sum(size) FILTER (WHERE state IN ('hashed', 'sampled')), 0),
		count(*) FILTER (WHERE state IN ('pending', 'changed')), coalesce(sum(size) FILTER (WHERE state IN ('pending', 'changed')), 0),
		count(*) FILTER (WHERE state = 'unreadable'), coalesce(sum(size) FILTER (WHERE state = 'unreadable'), 0)
		FROM rows`, string(s.Source)).Scan(&direct[0], &direct[1], &direct[2], &direct[3],
		&direct[4], &direct[5], &direct[6], &direct[7]); err != nil {
		t.Fatal(err)
	}
	if cov != direct || cov[0] == 0 || cov[0] != cov[2] || cov[1] != cov[3] {
		t.Errorf("coverage %v, direct %v: want complete", cov, direct)
	}
}

// The archives and their members are the ground truth's listings: a member
// tree with folder totals, zip locators and stored flags, and mtimes.
func TestSeedContentArchives(t *testing.T) {
	st, s, arcs, gt := seedCorpusContent(t)
	for _, a := range gt.Members {
		name := b64(t, a.PathB64)
		arc := arcs[name]
		if arc == nil || arc.ID != s.ID(name) {
			t.Fatalf("%s not seeded", name)
		}
		var format, state string
		var members, unpacked, size, entrySize int64
		if err := st.Reader().QueryRow(`SELECT a.format, a.state, a.members, a.unpacked_bytes, a.size, e.size
			FROM archives a JOIN entries e ON e.id = a.entry_id WHERE a.entry_id = ?`, int64(arc.ID)).
			Scan(&format, &state, &members, &unpacked, &size, &entrySize); err != nil {
			t.Fatal(err)
		}
		var wantUnpacked int64
		totals := map[string][2]int64{}
		for _, m := range a.Members {
			if m.Kind != "file" {
				continue
			}
			wantUnpacked += *m.Size
			for dir := path.Dir(b64(t, m.PathB64)); dir != "."; dir = path.Dir(dir) {
				totals[dir] = [2]int64{totals[dir][0] + *m.Size, totals[dir][1] + 1}
			}
		}
		if format != a.Format || state != "complete" || members != int64(len(a.Members)) || unpacked != wantUnpacked || size != entrySize {
			t.Errorf("%s: archive row %s %s %d members %d bytes", name, format, state, members, unpacked)
		}
		for _, m := range a.Members {
			p := b64(t, m.PathB64)
			var gotPath, name []byte
			var kind string
			var parent, locator, mtime sql.NullInt64
			var mSize, bytes, files int64
			var stored bool
			if err := st.Reader().QueryRow(`SELECT m.path, m.name, m.kind, m.size, m.total_bytes, m.total_files, m.locator,
				m.stored, m.mtime_ns, m.parent_id FROM archive_members m WHERE m.id = ? AND m.archive_id = ?`,
				int64(arc.Member(p)), int64(arc.ID)).Scan(&gotPath, &name, &kind, &mSize, &bytes, &files, &locator,
				&stored, &mtime, &parent); err != nil {
				t.Fatalf("%s!%s: %v", a.Path.Path, p, err)
			}
			wantTotals := totals[p]
			if m.Kind == "file" {
				wantTotals = [2]int64{*m.Size, 1}
			}
			wantParent := sql.NullInt64{}
			if dir := path.Dir(p); dir != "." {
				wantParent = sql.NullInt64{Int64: int64(arc.Member(dir)), Valid: true}
			}
			wantLocator := sql.NullInt64{}
			if m.Locator != nil {
				wantLocator = sql.NullInt64{Int64: int64(*m.Locator), Valid: true}
			}
			wantMTime := sql.NullInt64{}
			if m.MTime != nil {
				wantMTime = sql.NullInt64{Int64: m.MTime.UnixNano(), Valid: true}
			}
			if string(gotPath) != p || string(name) != path.Base(p) || kind != m.Kind || [2]int64{bytes, files} != wantTotals ||
				parent != wantParent || locator != wantLocator || stored != m.Stored || mtime != wantMTime {
				t.Errorf("%s!%s: row %q %s totals %d/%d parent %v locator %v stored %v mtime %v",
					a.Path.Path, p, gotPath, kind, bytes, files, parent, locator, stored, mtime)
			}
		}
	}
	var zipped int
	if err := st.Reader().QueryRow(`SELECT count(*) FROM archive_members WHERE locator IS NOT NULL AND stored = 1`).Scan(&zipped); err != nil || zipped < 10 {
		t.Errorf("%d stored zip members (%v)", zipped, err)
	}
}

// The low-level builders write what they are given, and coverage follows
// RecomputeCoverage.
func TestContentBuilders(t *testing.T) {
	st := storetest.Open(t)
	s := indextest.Seed(t, st, indextest.Tree{Source: "s", CreateSource: true, MountPoint: "/mnt/s", Nodes: []indextest.Node{
		{Path: "a/x.jpg", Size: 100},
		{Path: "b/x.jpg", Size: 100},
		{Path: "b/y.jpg", Size: 50},
		{Path: "c.zip", Size: 300},
		{Path: "d.bin", Size: 70},
		{Path: "e.bin", Size: 5},
	}})
	sum := func(s string) []byte { b := sha256.Sum256([]byte(s)); return b[:] }
	x := sum("x")
	s.SetContent(st, "a/x.jpg", indextest.Content{State: domain.ContentHashed, SHA256: x})
	s.SetContent(st, "b/x.jpg", indextest.Content{State: domain.ContentHashed, SHA256: x})
	s.SetContent(st, "b/y.jpg", indextest.Content{State: domain.ContentHashed, SHA256: sum("y")})
	s.SetContent(st, "b/y.jpg", indextest.Content{State: domain.ContentPending}) // replaces
	s.SetContent(st, "c.zip", indextest.Content{State: domain.ContentUniqueSize})
	s.SetContent(st, "d.bin", indextest.Content{State: domain.ContentSampled, Sample: sum("sample")})
	s.SetContent(st, "e.bin", indextest.Content{State: domain.ContentUnreadable})
	zero := 0
	when := time.Date(2009, 1, 2, 3, 4, 0, 0, time.UTC)
	arc := s.SeedArchive(st, "c.zip", indextest.Archive{Format: domain.ArchiveZip, Members: []indextest.Member{
		{Path: "f/x.jpg", Size: 100, Locator: &zero, Stored: true, MTime: when, Content: indextest.Content{State: domain.ContentHashed, SHA256: x}},
		{Path: "f/g/z.txt", Size: 10, Content: indextest.Content{State: domain.ContentPending}},
		{Path: "f", Kind: domain.MemberDirectory, MTime: when},
		{Path: "f/link", Kind: domain.MemberSymlink, LinkText: "/etc"},
	}})
	indextest.RecomputeCoverage(t, st)

	var cov [8]int64
	if err := st.Reader().QueryRow(`SELECT candidate_files, candidate_bytes, checked_files, checked_bytes,
		unchecked_files, unchecked_bytes, unreadable_files, unreadable_bytes FROM content_coverage WHERE source_id = 's'`).
		Scan(&cov[0], &cov[1], &cov[2], &cov[3], &cov[4], &cov[5], &cov[6], &cov[7]); err != nil {
		t.Fatal(err)
	}
	// Files: 5 candidates (320 + 5 unreadable bytes); members: f/x.jpg hashed, f/g/z.txt pending.
	if want := [8]int64{7, 435, 4, 370, 2, 60, 1, 5}; cov != want {
		t.Errorf("coverage %v, want %v", cov, want)
	}

	var same bool
	if err := st.Reader().QueryRow(`SELECT (SELECT content_id FROM file_content WHERE entry_id = ?) =
		(SELECT content_id FROM archive_members WHERE id = ?)`, int64(s.ID("b/x.jpg")), int64(arc.Member("f/x.jpg"))).Scan(&same); err != nil || !same {
		t.Errorf("the member and the files do not share a content (%v)", err)
	}
	var members, unpacked int64
	if err := st.Reader().QueryRow(`SELECT members, unpacked_bytes FROM archives WHERE entry_id = ?`, int64(arc.ID)).
		Scan(&members, &unpacked); err != nil || members != 5 || unpacked != 110 {
		t.Errorf("archive: %d members, %d bytes (%v)", members, unpacked, err)
	}
	for p, want := range map[string][2]int64{"f": {110, 2}, "f/g": {10, 1}, "f/x.jpg": {100, 1}, "f/link": {0, 0}} {
		var b, f int64
		var link []byte
		if err := st.Reader().QueryRow(`SELECT total_bytes, total_files, link_text FROM archive_members WHERE id = ?`,
			int64(arc.Member(p))).Scan(&b, &f, &link); err != nil || [2]int64{b, f} != want {
			t.Errorf("%s: totals %d/%d, want %v (%v)", p, b, f, want, err)
		}
		if p == "f/link" && !bytes.Equal(link, []byte("/etc")) {
			t.Errorf("link text %q", link)
		}
	}
	var sampled []byte
	var checked sql.NullInt64
	if err := st.Reader().QueryRow(`SELECT sample, checked_at FROM file_content WHERE entry_id = ?`, int64(s.ID("d.bin"))).
		Scan(&sampled, &checked); err != nil || !bytes.Equal(sampled, sum("sample")) || !checked.Valid {
		t.Errorf("sampled row: %x %v (%v)", sampled, checked, err)
	}
	if err := st.Reader().QueryRow(`SELECT checked_at FROM file_content WHERE entry_id = ?`, int64(s.ID("b/y.jpg"))).
		Scan(&checked); err != nil || checked.Valid {
		t.Errorf("pending row checked at %v (%v)", checked, err)
	}
}
