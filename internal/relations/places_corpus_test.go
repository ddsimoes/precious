package relations

import (
	"context"
	"database/sql"
	"encoding/hex"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index/indextest"
	"precious/internal/jobs"
	"precious/internal/store/storetest"
)

// R5c task 2.1, design D13: entry_places, archive_places, and member_places
// equal a direct computation on the seeded corpus beside a second source,
// "copies", that holds copies of every third corpus file, a byte-identical
// copy of one corpus zip, and a copy of another corpus zip as a plain file,
// which covers its members, and a complete zip hard-linked to a name that
// is never listed.
// Plain SQL places every file and member by D1: its content state, and for
// a hashed one its physical copies (a hard-link set, the members of one
// archive identity, and a tar link with its target count once) and their
// sources. Go then sums the places per folder and per member folder,
// member classification by the default rules (D14), applies each complete
// archive's own coverage to its unpacked sums, and splits it by D5. An
// unchanged index then writes no row of these tables.
func TestEntryPlacesEqualADirectComputation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := storetest.Open(t)
	gt := corpusTruth()
	corpusSeed := indextest.Seed(t, st, indextest.Tree{Source: "corpus", CreateSource: true, MountPoint: "/mnt/corpus",
		Nodes: indextest.CorpusNodes(t, gt)})

	const zipPath, plainPath = "Downloads/fotos_2005_do_pendrive.zip", "Downloads/eMule0.47c-Installer.zip"
	archives := map[string]bool{}
	for _, a := range gt.Members {
		archives[raw(t, a.Path)] = true
	}
	digests := map[string][]byte{}
	var nodes []indextest.Node
	n := 0
	for _, e := range gt.Entries {
		if e.Kind != domain.EntryFile || e.Size == nil || *e.Size == 0 {
			continue
		}
		p, err := e.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		path := string(p)
		if archives[path] && path != zipPath && path != plainPath {
			continue
		}
		if n++; n%3 != 0 && path != zipPath && path != plainPath {
			continue
		}
		sum, err := hex.DecodeString(e.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		digests["copia/"+path] = sum
		nodes = append(nodes, indextest.Node{Path: "copia/" + path, Size: *e.Size})
	}
	const linkedZip, linkedDat = "copia/hl/bkp.zip", "copia/hl/bkp.dat"
	for _, p := range []string{linkedZip, linkedDat} {
		nodes = append(nodes, indextest.Node{Path: p, Lstat: &fsaccess.EntryInfo{Kind: domain.EntryFile, Size: 250,
			ModTime: indextest.DefaultNow, Dev: 1, Ino: 9, Nlink: 2}})
	}
	copiesSeed := indextest.Seed(t, st, indextest.Tree{Source: "copies", CreateSource: true, MountPoint: "/mnt/copies",
		Nodes: nodes})
	corpusSeed.SeedContent(st, gt)
	for path, sum := range digests {
		copiesSeed.SetContent(st, path, indextest.Content{State: domain.ContentHashed, SHA256: sum})
	}
	for _, p := range []string{linkedZip, linkedDat} {
		copiesSeed.SetContent(st, p, indextest.Content{State: domain.ContentHashed, SHA256: digest("hard-linked bkp")})
	}
	copiesSeed.SeedArchive(st, linkedZip, indextest.Archive{Format: domain.ArchiveZip, Members: []indextest.Member{
		{Path: "u.txt", Size: 100, Content: indextest.Content{State: domain.ContentHashed, SHA256: digest("hard-linked u")}}}})
	// The zip's copy lists the same members in the same states.
	var members []indextest.Member
	err := scanAll(ctx, st.Reader(), `SELECT m.path, m.kind, m.size, COALESCE(m.link_text, X''), COALESCE(m.state, ''),
			COALESCE(c.sha256, X'') FROM archive_members m LEFT JOIN contents c ON c.id = m.content_id
		WHERE m.archive_id = ? ORDER BY m.id`, []any{int64(corpusSeed.ID(zipPath))}, func(r *sql.Rows) error {
		var m indextest.Member
		var path, link []byte
		var state string
		var sum []byte
		if err := r.Scan(&path, &m.Kind, &m.Size, &link, &state, &sum); err != nil {
			return err
		}
		m.Path, m.LinkText = string(path), string(link)
		if m.Kind == domain.MemberFile {
			m.Content = indextest.Content{State: domain.ContentState(state)}
			if len(sum) > 0 {
				m.Content.SHA256 = sum
			}
		}
		members = append(members, m)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	copiesSeed.SeedArchive(st, "copia/"+zipPath, indextest.Archive{Format: domain.ArchiveZip, Members: members})

	h := NewHandler(st, nil, configDuplicates(), nil)
	classify := policyClassifier()
	h.SetClassifier(classify)
	if err := h.Run(ctx, jobs.Job{Kind: KindRelate}, &fakeRT{}); err != nil {
		t.Fatal(err)
	}
	got := storedPlaces(t, st)

	q := st.Reader()
	// Physical copies of each content, with their sources.
	type copyOf struct{ phys, src string }
	copies := map[int64]map[copyOf]bool{}
	err = scanAll(ctx, q, `SELECT f.content_id, CASE WHEN e.nlink > 1 AND e.dev IS NOT NULL
				THEN 'i' || e.source_id || ':' || e.dev || ':' || e.ino ELSE 'e' || e.id END, e.source_id
			FROM file_content f JOIN entries e ON e.id = f.entry_id
			WHERE f.state = 'hashed' AND e.state = 'present' AND `+notQuarantinedE+`
		UNION ALL
		SELECT COALESCE(t.content_id, m.content_id), CASE WHEN e.nlink > 1 AND e.dev IS NOT NULL
				THEN 'i' || e.source_id || ':' || e.dev || ':' || e.ino ELSE 'e' || e.id END || '/' || hex(COALESCE(t.path, m.path)),
				e.source_id
			FROM archive_members m LEFT JOIN archive_members t ON t.id = m.link_member
			JOIN archives a ON a.entry_id = m.archive_id AND a.state = 'complete'
			JOIN entries e ON e.id = a.entry_id AND e.state = 'present'
			WHERE m.kind = 'file' AND COALESCE(t.state, m.state) = 'hashed' AND `+notQuarantinedE, nil,
		func(r *sql.Rows) error {
			var cid int64
			var c copyOf
			if err := r.Scan(&cid, &c.phys, &c.src); err != nil {
				return err
			}
			if copies[cid] == nil {
				copies[cid] = map[copyOf]bool{}
			}
			copies[cid][c] = true
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	// place is D1's place of a file or member on source src.
	place := func(state string, cid int64, src string) domain.Place {
		switch domain.ContentState(state) {
		case domain.ContentUniqueSize, domain.ContentSampled:
			return domain.PlaceOnlyCopy
		case domain.ContentUnreadable:
			return domain.PlaceUnreadable
		case domain.ContentHashed:
			phys := map[string]bool{}
			elsewhere := false
			for c := range copies[cid] {
				phys[c.phys] = true
				elsewhere = elsewhere || c.src != src
			}
			switch {
			case len(phys) <= 1:
				return domain.PlaceOnlyCopy
			case elsewhere:
				return domain.PlaceOtherSource
			}
			return domain.PlaceThisSource
		}
		return domain.PlaceUnchecked
	}
	at := func(p *domain.PlaceSums, pl domain.Place) *domain.PlaceAmount {
		switch pl {
		case domain.PlaceOnlyCopy:
			return &p.OnlyCopy
		case domain.PlaceThisSource:
			return &p.ThisSource
		case domain.PlaceOtherSource:
			return &p.OtherSource
		case domain.PlaceUnchecked:
			return &p.Unchecked
		}
		return &p.Unreadable
	}
	add := func(p *domain.PlaceSums, pl domain.Place, disposable bool, size int64) {
		a := at(p, pl)
		if disposable {
			a = &p.Disposable
		}
		a.Files++
		a.Bytes += size
	}

	// Every folder outside the quarantine has a row.
	parent := map[int64]int64{}
	want := map[int64]*domain.PlaceSums{}
	err = scanAll(ctx, q, `SELECT id, COALESCE(parent_id, 0) FROM entries e
		WHERE kind = 'directory' AND state <> 'missing' AND `+notQuarantinedE, nil, func(r *sql.Rows) error {
		var id, p int64
		err := r.Scan(&id, &p)
		parent[id], want[id] = p, &domain.PlaceSums{}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	up := func(dir int64, p domain.PlaceSums) {
		for ; dir != 0; dir = parent[dir] {
			addSums(want[dir], p)
		}
	}
	type file struct {
		id, dir, size, cid     int64
		src, state             string
		category, kind, triage string
		complete               bool
	}
	var files []file
	err = scanAll(ctx, q, `SELECT e.id, e.parent_id, e.size, COALESCE(f.content_id, 0), e.source_id, COALESCE(f.state, ''),
			COALESCE(e.category, ''), COALESCE(e.file_kind, ''), COALESCE(e.triage, ''),
			EXISTS (SELECT 1 FROM archives a WHERE a.entry_id = e.id AND a.state = 'complete')
		FROM entries e LEFT JOIN file_content f ON f.entry_id = e.id
		WHERE e.kind = 'file' AND e.state = 'present' AND e.size > 0 AND `+notQuarantinedE, nil, func(r *sql.Rows) error {
		var f file
		err := r.Scan(&f.id, &f.dir, &f.size, &f.cid, &f.src, &f.state, &f.category, &f.kind, &f.triage, &f.complete)
		files = append(files, f)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	completes := 0
	wantArchives, wantMembers := map[int64]domain.PlaceSums{}, map[int64]domain.PlaceSums{}
	for _, f := range files {
		disposable := domain.IsDisposable(domain.Category(f.category), domain.FileKind(f.kind), domain.Triage(f.triage))
		own := place(f.state, f.cid, f.src)
		if !f.complete {
			var p domain.PlaceSums
			add(&p, own, disposable, f.size)
			up(f.dir, p)
			continue
		}
		completes++
		// The members' sums, whole and per member folder (each member adds
		// to every folder above it), then the archive file's own coverage.
		var m domain.PlaceSums
		folderOf := map[int64]int64{} // member folder -> its parent folder, 0 at the top
		err := scanAll(ctx, q, `SELECT id, COALESCE(parent_id, 0) FROM archive_members
			WHERE archive_id = ? AND kind = 'directory'`, []any{f.id}, func(r *sql.Rows) error {
			var id, p int64
			err := r.Scan(&id, &p)
			folderOf[id] = p
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		folders := map[int64]*domain.PlaceSums{}
		for id := range folderOf {
			folders[id] = &domain.PlaceSums{}
		}
		err = scanAll(ctx, q, `SELECT m.name, COALESCE(m.parent_id, 0), COALESCE(t.size, m.size), COALESCE(t.state, m.state),
				COALESCE(t.content_id, m.content_id, 0)
			FROM archive_members m LEFT JOIN archive_members t ON t.id = m.link_member
			WHERE m.archive_id = ? AND m.kind = 'file' AND COALESCE(t.size, m.size) > 0`, []any{f.id},
			func(r *sql.Rows) error {
				var name []byte
				var dir, size, cid int64
				var state string
				if err := r.Scan(&name, &dir, &size, &state, &cid); err != nil {
					return err
				}
				pl, disp := place(state, cid, f.src), domain.IsDisposable(classify(name, size))
				add(&m, pl, disp, size)
				for ; dir != 0; dir = folderOf[dir] {
					add(folders[dir], pl, disp, size)
				}
				return nil
			})
		if err != nil {
			t.Fatal(err)
		}
		cover := func(m domain.PlaceSums) domain.PlaceSums {
			if f.state != string(domain.ContentHashed) {
				return m
			}
			switch own {
			case domain.PlaceOtherSource:
				m = domain.PlaceSums{OtherSource: pa(
					m.OnlyCopy.Files+m.ThisSource.Files+m.OtherSource.Files+m.Unchecked.Files+m.Unreadable.Files,
					m.OnlyCopy.Bytes+m.ThisSource.Bytes+m.OtherSource.Bytes+m.Unchecked.Bytes+m.Unreadable.Bytes),
					Disposable: m.Disposable}
			case domain.PlaceThisSource:
				m.ThisSource = pa(m.ThisSource.Files+m.OnlyCopy.Files, m.ThisSource.Bytes+m.OnlyCopy.Bytes)
				m.OnlyCopy = domain.PlaceAmount{}
			}
			return m
		}
		m = cover(m)
		wantArchives[f.id] = m
		for id, p := range folders {
			wantMembers[id] = cover(*p)
		}
		var row domain.PlaceSums
		switch {
		case disposable:
			row.Disposable = pa(1, f.size)
		case m.OnlyCopy.Bytes+m.ThisSource.Bytes+m.OtherSource.Bytes+m.Unchecked.Bytes+m.Unreadable.Bytes > 0:
			row = domain.SplitArchive(f.size, m)
		default:
			add(&row, own, false, f.size)
		}
		want[f.id] = &row
		up(f.dir, row)
	}

	if len(got) != len(want) {
		t.Errorf("%d entry_places rows, want one per folder and complete archive (%d)", len(got), len(want))
	}
	for id, w := range want {
		if g, ok := got[id]; !ok || g != *w {
			t.Errorf("entry %d: entry_places %+v (stored %v), want %+v", id, g, ok, *w)
		}
	}
	// archive_places and member_places: one row per complete archive and
	// per member folder, its unpacked members under the coverage.
	for _, c := range []struct {
		table string
		got   map[int64]domain.PlaceSums
		want  map[int64]domain.PlaceSums
	}{
		{"archive_places", storedRows(t, st, archivePlacesTable), wantArchives},
		{"member_places", storedRows(t, st, memberPlacesTable), wantMembers},
	} {
		if len(c.got) != len(c.want) {
			t.Errorf("%d %s rows, want %d", len(c.got), c.table, len(c.want))
		}
		for id, w := range c.want {
			if g, ok := c.got[id]; !ok || g != w {
				t.Errorf("%s %d: %+v (stored %v), want %+v", c.table, id, g, ok, w)
			}
		}
	}
	// Not vacuous: member folders, some covered by a copy of their archive.
	covered := 0
	for _, p := range wantMembers {
		if p.OtherSource.Files > 0 && p.OnlyCopy.Files == 0 {
			covered++
		}
	}
	if len(wantMembers) < 4 || covered == 0 {
		t.Errorf("%d member folders, %d wholly elsewhere", len(wantMembers), covered)
	}
	// The figures are not vacuous: both sources, copies on each side, and
	// both copies of the zip wholly elsewhere.
	if completes < 4 {
		t.Errorf("%d complete archives, want the corpus's and the copy", completes)
	}
	var total domain.PlaceSums
	for _, root := range []int64{int64(corpusSeed.Root), int64(copiesSeed.Root)} {
		addSums(&total, got[root])
	}
	if total.OnlyCopy.Files == 0 || total.ThisSource.Files == 0 || total.OtherSource.Files == 0 {
		t.Errorf("the roots' places %+v lack a place", total)
	}
	for _, id := range []int64{int64(corpusSeed.ID(zipPath)), int64(copiesSeed.ID("copia/" + zipPath))} {
		if g := got[id]; g.OtherSource.Files != 1 || g.OnlyHereBytes() != 0 || g.Unchecked != (domain.PlaceAmount{}) {
			t.Errorf("zip %d: %+v, want wholly other_source", id, g)
		}
	}
	// The hard-linked pair is one copy: both names stay the only copy.
	if g := got[int64(copiesSeed.ID("copia/hl"))]; g != only(2, 500) {
		t.Errorf("copia/hl: %+v, want %+v", g, only(2, 500))
	}

	// A second pass with nothing changed writes no row of any places table
	// (temporary triggers on the single writer connection count them).
	stmts := []string{`CREATE TEMP TABLE ep_writes (n INTEGER)`}
	for _, table := range []string{"entry_places", "archive_places", "member_places"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			stmts = append(stmts, `CREATE TEMP TRIGGER `+table+`_`+op+` AFTER `+op+` ON main.`+table+`
				BEGIN INSERT INTO ep_writes VALUES (1); END`)
		}
	}
	for _, stmt := range stmts {
		if _, err := st.Writer().Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Run(ctx, jobs.Job{Kind: KindRelate}, &fakeRT{}); err != nil {
		t.Fatal(err)
	}
	var writes int64
	if err := st.Writer().QueryRow(`SELECT count(*) FROM ep_writes`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Errorf("an unchanged index wrote %d places rows", writes)
	}
}
