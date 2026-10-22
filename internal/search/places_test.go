package search

import (
	"context"
	"database/sql"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index/indextest"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// placeSize is the size of the files and members of content c (one size per
// content, as the contents table wants).
func placeSize(c byte) int64 { return 1000 + int64(c) }

// placeSum is the digest of content c.
func placeSum(c byte) []byte {
	sum := make([]byte, 32)
	sum[0], sum[31] = c, 1
	return sum
}

// hashed is the content state of a file or member of content c.
func hashed(c byte) indextest.Content {
	return indextest.Content{State: domain.ContentHashed, SHA256: placeSum(c)}
}

// fileOf is a file node of content c's size.
func fileOf(p string, c byte) indextest.Node { return indextest.Node{Path: p, Size: placeSize(c)} }

// linkedOf is a file node of content c's size that is one name of the inode
// (dev, ino) with two links.
func linkedOf(p string, c byte, dev, ino uint64) indextest.Node {
	return indextest.Node{Path: p, Lstat: &fsaccess.EntryInfo{Kind: domain.EntryFile, Size: placeSize(c),
		ModTime: time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC), Dev: dev, Ino: ino, Nlink: 2, Mode: 0o644}}
}

// memberOf is a hashed file member of content c.
func memberOf(p string, c byte) indextest.Member {
	return indextest.Member{Path: p, Size: placeSize(c), Content: hashed(c)}
}

// placeWorld is three sources: "um" and "dois" online, "tres" offline.
// Each letter names one content.
type placeWorld struct {
	st             *store.Store
	um, dois, tres *indextest.Seeded
}

func seedPlaceWorld(t *testing.T) *placeWorld {
	t.Helper()
	st := storetest.Open(t)
	w := &placeWorld{st: st}
	w.um = indextest.Seed(t, st, indextest.Tree{Source: "um", CreateSource: true, MountPoint: "/mnt/um", Nodes: []indextest.Node{
		{Path: "only/size.bin", Size: 11},
		{Path: "only/sampled.bin", Size: 12},
		{Path: "pend.bin", Size: 13},
		{Path: "changed.bin", Size: 14},
		{Path: "unread.bin", Size: 15},
		{Path: "norow.bin", Size: 16},
		{Path: "empty.bin"},
		{Path: "link.lnk", Kind: domain.EntrySymlink, LinkText: "pend.bin"},
		{Path: "fifo", Kind: domain.EntryFIFO},
		fileOf("solo.bin", 'A'),
		fileOf("same/a.bin", 'B'), fileOf("same/b.bin", 'B'),
		fileOf("inarc.bin", 'C'), fileOf("box.zip", 'c'), fileOf("box2.zip", 'r'),
		linkedOf("hl/x.bin", 'D', 7, 100), linkedOf("hl/y.bin", 'D', 7, 100),
		linkedOf("h1.zip", 'Z', 7, 200), linkedOf("h2.zip", 'Z', 7, 200),
		fileOf("twice.zip", 'g'),
		linkedOf("dev.bin", 'H', 9, 300),
		linkedOf("dv.zip", 'Y', 9, 400),
		fileOf("t.tar", 'i'),
		fileOf("off.bin", 'J'),
		fileOf("q.bin", 'K'), fileOf(".precious-quarantine/q.bin", 'K'),
		fileOf(".precious-quarantine/k2.bin", 'k'),
		fileOf(".precious-quarantine/qo.bin", 'J'),
		fileOf("l.bin", 'L'), fileOf(".precious-quarantine/qa.zip", 'l'),
		fileOf("m.bin", 'M'), fileOf("gone.bin", 'M'),
		fileOf("t.bin", 'T'), fileOf("gonearc.zip", 't'),
		fileOf("v.bin", 'V'), fileOf("part.zip", 'v'),
		fileOf("a1.zip", 'W'), fileOf("a2.zip", 'W'),
		fileOf("arc.zip", 'a'),
		fileOf("mix.bin", 'Q'),
		fileOf("unav.bin", 'P'),
	}})
	w.dois = indextest.Seed(t, st, indextest.Tree{Source: "dois", CreateSource: true, MountPoint: "/mnt/dois", Nodes: []indextest.Node{
		linkedOf("dev.bin", 'H', 9, 300),
		linkedOf("dv.zip", 'Y', 9, 400),
		fileOf("n.bin", 'N'),
		fileOf("mix.bin", 'Q'),
		fileOf("unav.bin", 'P'),
	}})
	w.tres = indextest.Seed(t, st, indextest.Tree{Source: "tres", CreateSource: true, Nodes: []indextest.Node{
		fileOf("off.bin", 'J'),
		fileOf("o.bin", 'O'),
		fileOf("mix.bin", 'Q'),
	}})

	um := w.um
	um.SetContent(st, "only/size.bin", indextest.Content{State: domain.ContentUniqueSize})
	um.SetContent(st, "only/sampled.bin", indextest.Content{State: domain.ContentSampled, Sample: placeSum('s')})
	um.SetContent(st, "pend.bin", indextest.Content{State: domain.ContentPending})
	um.SetContent(st, "changed.bin", indextest.Content{State: domain.ContentChanged})
	um.SetContent(st, "unread.bin", indextest.Content{State: domain.ContentUnreadable})
	um.SetContent(st, "twice.zip", indextest.Content{State: domain.ContentUniqueSize})
	for p, c := range map[string]byte{
		"solo.bin": 'A', "same/a.bin": 'B', "same/b.bin": 'B', "inarc.bin": 'C', "box.zip": 'c', "box2.zip": 'r',
		"hl/x.bin": 'D', "hl/y.bin": 'D', "h1.zip": 'Z', "h2.zip": 'Z', "dev.bin": 'H', "dv.zip": 'Y', "t.tar": 'i',
		"off.bin": 'J', "q.bin": 'K', ".precious-quarantine/q.bin": 'K', ".precious-quarantine/k2.bin": 'k',
		".precious-quarantine/qo.bin": 'J', "l.bin": 'L', ".precious-quarantine/qa.zip": 'l', "m.bin": 'M',
		"gone.bin": 'M', "t.bin": 'T', "gonearc.zip": 't', "v.bin": 'V', "part.zip": 'v', "a1.zip": 'W',
		"a2.zip": 'W', "arc.zip": 'a', "mix.bin": 'Q', "unav.bin": 'P',
	} {
		um.SetContent(st, p, hashed(c))
	}
	for p, c := range map[string]byte{"dev.bin": 'H', "dv.zip": 'Y', "n.bin": 'N', "mix.bin": 'Q', "unav.bin": 'P'} {
		w.dois.SetContent(st, p, hashed(c))
	}
	for p, c := range map[string]byte{"off.bin": 'J', "o.bin": 'O', "mix.bin": 'Q'} {
		w.tres.SetContent(st, p, hashed(c))
	}

	zip := func(s *indextest.Seeded, p string, members ...indextest.Member) *indextest.SeededArchive {
		return s.SeedArchive(st, p, indextest.Archive{Format: domain.ArchiveZip, Members: members})
	}
	zip(um, "box.zip", memberOf("c.bin", 'C'), memberOf("r.bin", 'R'))
	zip(um, "box2.zip", memberOf("r.bin", 'R'))
	zip(um, "h1.zip", memberOf("d.bin", 'E'))
	zip(um, "h2.zip", memberOf("d.bin", 'E'))
	zip(um, "twice.zip", memberOf("p.bin", 'G'), memberOf("q.bin", 'G'))
	zip(um, "dv.zip", memberOf("s.bin", 'S'))
	zip(w.dois, "dv.zip", memberOf("s.bin", 'S'))
	tar := um.SeedArchive(st, "t.tar", indextest.Archive{Format: domain.ArchiveTar, Members: []indextest.Member{
		memberOf("orig/f.bin", 'I'), memberOf("copy/g.bin", 'I'),
	}})
	zip(um, ".precious-quarantine/qa.zip", memberOf("l.bin", 'L'))
	zip(um, "gonearc.zip", memberOf("t.bin", 'T'))
	um.SeedArchive(st, "part.zip", indextest.Archive{Format: domain.ArchiveZip, State: domain.ArchivePartial,
		Members: []indextest.Member{memberOf("v.bin", 'V')}})
	zip(um, "a1.zip", memberOf("x.bin", 'X'))
	zip(um, "a2.zip", memberOf("x.bin", 'X'))
	zip(um, "arc.zip",
		memberOf("n.bin", 'N'),
		memberOf("o.bin", 'O'),
		memberOf("k.bin", 'k'),
		memberOf("sub/r.bin", 'R'),
		indextest.Member{Path: "u.bin", Size: 21, Content: indextest.Content{State: domain.ContentUniqueSize}},
		indextest.Member{Path: "pp.bin", Size: 22, Content: indextest.Content{State: domain.ContentPending}},
		indextest.Member{Path: "ur.bin", Size: 23, Content: indextest.Content{State: domain.ContentUnreadable}},
		indextest.Member{Path: "z.bin", Content: indextest.Content{State: domain.ContentUniqueSize}},
		indextest.Member{Path: "sl", Kind: domain.MemberSymlink, LinkText: "n.bin"},
		indextest.Member{Path: "dev", Kind: domain.MemberSpecial},
	)

	execSQL(t, st, `UPDATE archive_members SET link_member = ? WHERE id = ?`,
		int64(tar.Member("orig/f.bin")), int64(tar.Member("copy/g.bin")))
	for _, p := range []string{"gone.bin", "gonearc.zip"} {
		execSQL(t, st, `UPDATE entries SET state = 'missing' WHERE id = ?`, int64(um.ID(p)))
	}
	return w
}

func execSQL(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// placeRow is one file's or member's place and not-connected mark.
type placeRow struct {
	place   sql.NullString
	offline bool
}

// entryPlaces reads PlaceSQL and OfflineOnlySQL for every entry but the
// folders, by "source:path", and checks that every folder is NULL.
func entryPlaces(t *testing.T, st *store.Store) map[string]placeRow {
	t.Helper()
	rows, err := st.Reader().Query(`SELECT e.source_id, e.path, e.kind, ` + PlaceSQL("e", "fc") + `, ` +
		OfflineOnlySQL("e", "fc") + ` FROM entries e LEFT JOIN file_content fc ON fc.entry_id = e.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]placeRow{}
	for rows.Next() {
		var src, kind string
		var p []byte
		var r placeRow
		if err := rows.Scan(&src, &p, &kind, &r.place, &r.offline); err != nil {
			t.Fatal(err)
		}
		key := src + ":" + string(p)
		if kind == string(domain.EntryDirectory) {
			if r.place.Valid || r.offline {
				t.Errorf("folder %s: place %v, offline %v", key, r.place, r.offline)
			}
			continue
		}
		out[key] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// memberPlaces reads MemberPlaceSQL and MemberOfflineOnlySQL for every
// member but the folders, by "source:archive!member", and checks that every
// folder member is NULL.
func memberPlaces(t *testing.T, st *store.Store) map[string]placeRow {
	t.Helper()
	rows, err := st.Reader().Query(`SELECT ae.source_id, ae.path, m.path, m.kind, ` + MemberPlaceSQL("m", "ae") + `, ` +
		MemberOfflineOnlySQL("m", "ae") + ` FROM archive_members m JOIN entries ae ON ae.id = m.archive_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]placeRow{}
	for rows.Next() {
		var src, kind string
		var ap, mp []byte
		var r placeRow
		if err := rows.Scan(&src, &ap, &mp, &kind, &r.place, &r.offline); err != nil {
			t.Fatal(err)
		}
		key := src + ":" + string(ap) + "!" + string(mp)
		if kind == string(domain.MemberDirectory) {
			if r.place.Valid || r.offline {
				t.Errorf("folder member %s: place %v, offline %v", key, r.place, r.offline)
			}
			continue
		}
		out[key] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// checkPlaces compares every row's place with want, where "" is NULL, and
// fails for a row without an expectation or an expectation without a row.
func checkPlaces(t *testing.T, what string, got map[string]placeRow, want map[string]domain.Place) {
	t.Helper()
	for _, k := range slices.Sorted(maps.Keys(got)) {
		w, ok := want[k]
		if !ok {
			t.Errorf("%s %s: place %v, not expected", what, k, got[k].place)
			continue
		}
		if g := domain.Place(got[k].place.String); g != w || got[k].place.Valid != (w != "") {
			t.Errorf("%s %s: place %v, want %q", what, k, got[k].place, w)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if _, ok := got[k]; !ok {
			t.Errorf("%s %s: expected %q, not seeded", what, k, want[k])
		}
	}
}

// checkMarks compares the rows whose not-connected mark is set with want.
func checkMarks(t *testing.T, what string, got map[string]placeRow, want ...string) {
	t.Helper()
	var marked []string
	for k, r := range got {
		if r.offline {
			marked = append(marked, k)
		}
	}
	slices.Sort(marked)
	slices.Sort(want)
	if !slices.Equal(marked, want) {
		t.Errorf("%s marked not connected: %q, want %q", what, marked, want)
	}
}

const (
	only  = domain.PlaceOnlyCopy
	here  = domain.PlaceThisSource
	other = domain.PlaceOtherSource
	unchk = domain.PlaceUnchecked
	unrd  = domain.PlaceUnreadable
	none  = domain.PlaceNone
	null  = domain.Place("")
)

// Every file and member of the three-source world gets its place by D1,
// with copies counted as the relate snapshot counts them (D3).
func TestPlaces(t *testing.T) {
	w := seedPlaceWorld(t)
	checkPlaces(t, "entry", entryPlaces(t, w.st), map[string]domain.Place{
		"um:only/size.bin":               only, // unique by its size
		"um:only/sampled.bin":            only, // unique by its sample
		"um:pend.bin":                    unchk,
		"um:changed.bin":                 unchk,
		"um:unread.bin":                  unrd,
		"um:norow.bin":                   unchk, // no content row yet
		"um:empty.bin":                   none,
		"um:link.lnk":                    none,
		"um:fifo":                        none,
		"um:solo.bin":                    only, // hashed, no other copy
		"um:same/a.bin":                  here,
		"um:same/b.bin":                  here,
		"um:inarc.bin":                   here, // a copy inside box.zip on its source
		"um:box.zip":                     only,
		"um:box2.zip":                    only,
		"um:hl/x.bin":                    only, // a hard link is not a copy
		"um:hl/y.bin":                    only,
		"um:h1.zip":                      only, // hard-linked archive names
		"um:h2.zip":                      only,
		"um:twice.zip":                   only,
		"um:dev.bin":                     other, // the same (dev, ino) on two sources is two copies
		"dois:dev.bin":                   other,
		"um:dv.zip":                      other,
		"dois:dv.zip":                    other,
		"um:t.tar":                       only,
		"um:off.bin":                     other, // a copy on an offline source
		"tres:off.bin":                   other,
		"um:q.bin":                       only, // its copy is in the quarantine
		"um:.precious-quarantine/q.bin":  null,
		"um:.precious-quarantine/k2.bin": null,
		"um:.precious-quarantine/qo.bin": null,
		"um:.precious-quarantine/qa.zip": null,
		"um:l.bin":                       only, // its copy is a member of an archive in the quarantine
		"um:m.bin":                       only, // its copy is missing
		"um:gone.bin":                    null,
		"um:t.bin":                       only, // its copy is a member of a missing archive
		"um:gonearc.zip":                 null,
		"um:v.bin":                       only, // its copy is a member of a partial archive
		"um:part.zip":                    only,
		"um:a1.zip":                      here, // archive files with the same content
		"um:a2.zip":                      here,
		"um:arc.zip":                     only,
		"um:mix.bin":                     other,
		"dois:mix.bin":                   other,
		"tres:mix.bin":                   other,
		"um:unav.bin":                    other,
		"dois:unav.bin":                  other,
		"dois:n.bin":                     other, // a member of um's arc.zip
		"tres:o.bin":                     other, // a member of um's arc.zip
	})
	checkPlaces(t, "member", memberPlaces(t, w.st), map[string]domain.Place{
		"um:box.zip!c.bin":                     here, // a loose copy on its source
		"um:box.zip!r.bin":                     here, // a copy in another archive on its source
		"um:box2.zip!r.bin":                    here,
		"um:arc.zip!sub/r.bin":                 here,
		"um:h1.zip!d.bin":                      only, // hard-linked archive names are one copy
		"um:h2.zip!d.bin":                      only,
		"um:twice.zip!p.bin":                   here, // two members of one archive are two copies
		"um:twice.zip!q.bin":                   here,
		"um:dv.zip!s.bin":                      other, // the same (dev, ino) on two sources is two copies
		"dois:dv.zip!s.bin":                    other,
		"um:t.tar!orig/f.bin":                  only, // a tar link and its target are one copy
		"um:t.tar!copy/g.bin":                  only,
		"um:arc.zip!n.bin":                     other, // a copy on another source
		"um:arc.zip!o.bin":                     other, // a copy on an offline source
		"um:arc.zip!k.bin":                     only,  // its copy is in the quarantine
		"um:arc.zip!u.bin":                     only,
		"um:arc.zip!pp.bin":                    unchk,
		"um:arc.zip!ur.bin":                    unrd,
		"um:arc.zip!z.bin":                     none,
		"um:arc.zip!sl":                        none,
		"um:arc.zip!dev":                       none,
		"um:a1.zip!x.bin":                      here, // the members of two archives with one content are two copies
		"um:a2.zip!x.bin":                      here,
		"um:gonearc.zip!t.bin":                 null,
		"um:part.zip!v.bin":                    here, // the caller lists complete archives only
		"um:.precious-quarantine/qa.zip!l.bin": null,
	})
}

// The not-connected mark (D11) is set when every copy on another source is
// on an offline or unavailable source, and only then.
func TestOfflineOnly(t *testing.T) {
	w := seedPlaceWorld(t)
	checkMarks(t, "entries", entryPlaces(t, w.st), "um:off.bin")
	checkMarks(t, "members", memberPlaces(t, w.st), "um:arc.zip!o.bin")

	execSQL(t, w.st, `UPDATE sources SET state = 'unavailable' WHERE id = 'dois'`)
	checkMarks(t, "entries with dois unavailable", entryPlaces(t, w.st),
		"um:off.bin", "um:mix.bin", "um:unav.bin", "um:dev.bin", "um:dv.zip")
	checkMarks(t, "members with dois unavailable", memberPlaces(t, w.st),
		"um:arc.zip!o.bin", "um:arc.zip!n.bin", "um:dv.zip!s.bin")

	execSQL(t, w.st, `UPDATE sources SET state = 'online' WHERE id = 'tres'`)
	checkMarks(t, "entries with tres online", entryPlaces(t, w.st), "um:unav.bin", "um:dev.bin", "um:dv.zip")
	checkMarks(t, "members with tres online", memberPlaces(t, w.st), "um:arc.zip!n.bin", "um:dv.zip!s.bin")
}

// DisposableSQL agrees with domain.IsDisposable for every category, file
// kind, and triage, and for NULL, empty, and unknown values, which Go reads
// as the empty value; it never holds for a row that is not a file.
func TestDisposableSQLMatchesDomain(t *testing.T) {
	st := storetest.Open(t)
	stmt, err := st.Reader().Prepare(`SELECT ` + DisposableSQL("x") +
		` FROM (SELECT ?1 AS kind, ?2 AS category, ?3 AS file_kind, ?4 AS triage) x`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	strs := func(vs ...string) []any {
		out := []any{nil}
		for _, v := range vs {
			out = append(out, v)
		}
		return append(out, "", "bogus")
	}
	var cats, kinds, triages []string
	for _, c := range domain.Categories {
		cats = append(cats, string(c))
	}
	for _, k := range domain.FileKinds {
		kinds = append(kinds, string(k))
	}
	for _, tr := range domain.Triages {
		triages = append(triages, string(tr))
	}
	str := func(v any) string {
		s, _ := v.(string)
		return s
	}
	seen := map[bool]int{}
	for _, kind := range []string{string(domain.EntryFile), string(domain.EntryDirectory), string(domain.EntrySymlink)} {
		for _, c := range strs(cats...) {
			for _, k := range strs(kinds...) {
				for _, tr := range strs(triages...) {
					var got bool
					if err := stmt.QueryRow(kind, c, k, tr).Scan(&got); err != nil {
						t.Fatal(err)
					}
					want := kind == string(domain.EntryFile) &&
						domain.IsDisposable(domain.Category(str(c)), domain.FileKind(str(k)), domain.Triage(str(tr)))
					if got != want {
						t.Errorf("kind %s, category %v, file kind %v, triage %v: %v, want %v", kind, c, k, tr, got, want)
					}
					seen[want]++
				}
			}
		}
	}
	if seen[true] == 0 || seen[false] == 0 {
		t.Errorf("disposable outcomes %v: both must occur", seen)
	}
}

// Each arm of each copy question starts with one indexed probe by content
// (design D10's cost): file_content_by_content for entries,
// archive_members_content for members, and nothing is scanned.
func TestPlaceProbesUseContentIndexes(t *testing.T) {
	w := seedPlaceWorld(t)
	for name, query := range map[string]string{
		"entry": `SELECT ` + PlaceSQL("e", "fc") + `, ` + OfflineOnlySQL("e", "fc") +
			` FROM entries e LEFT JOIN file_content fc ON fc.entry_id = e.id WHERE e.id = ?`,
		"member": `SELECT ` + MemberPlaceSQL("m", "ae") + `, ` + MemberOfflineOnlySQL("m", "ae") +
			` FROM archive_members m JOIN entries ae ON ae.id = m.archive_id WHERE m.id = ?`,
	} {
		p := plan(t, w.st.Reader(), query, []any{1})
		probes := map[string]int{}
		for _, step := range strings.Split(p, "\n") {
			step = strings.TrimSpace(step)
			switch {
			case strings.HasPrefix(step, "SCAN "):
				t.Errorf("%s: %q scans\n%s", name, step, p)
			case strings.HasPrefix(step, "SEARCH of "):
				probes["of"]++
				if !strings.Contains(step, "file_content_by_content (content_id=?)") {
					t.Errorf("%s: %q\n%s", name, step, p)
				}
			case strings.HasPrefix(step, "SEARCH om "):
				probes["om"]++
				if !strings.Contains(step, "archive_members_content (content_id=?)") {
					t.Errorf("%s: %q\n%s", name, step, p)
				}
			}
		}
		// Two place questions and two mark questions, each with both arms.
		if probes["of"] != 4 || probes["om"] != 4 {
			t.Errorf("%s: probes %v, want 4 of each\n%s", name, probes, p)
		}
	}
}
