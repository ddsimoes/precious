package relations

import (
	"context"
	"database/sql"
	"encoding/base64"
	"sync"
	"testing"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/jobs"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// corpusWorld is the regression corpus seeded as source "corpus" with its
// content as a complete hashing run leaves it.
type corpusWorld struct {
	st     *store.Store
	seeded *indextest.Seeded
	arcs   map[string]*indextest.SeededArchive
	truth  corpus.GroundTruth
}

var corpusTruth = sync.OnceValue(func() corpus.GroundTruth { return corpus.Corpus().GroundTruth() })

func seedCorpus(t testing.TB) *corpusWorld {
	t.Helper()
	st := storetest.Open(t)
	gt := corpusTruth()
	s := indextest.Seed(t, st, indextest.Tree{Source: "corpus", CreateSource: true, MountPoint: "/mnt/corpus",
		Nodes: indextest.CorpusNodes(t, gt)})
	return &corpusWorld{st: st, seeded: s, arcs: s.SeedContent(st, gt), truth: gt}
}

// raw decodes a ground-truth path.
func raw(t testing.TB, p corpus.Path) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(p.PathB64)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ref is the ref of a corpus folder or archive path.
func (c *corpusWorld) ref(path string) domain.Ref { return domain.Ref{Entry: c.seeded.ID(path)} }

// relate runs the relate job once and returns the visible relations.
func (c *corpusWorld) relate(t testing.TB) []Relation {
	t.Helper()
	h := NewHandler(c.st, nil, configDuplicates(), nil)
	if err := h.Run(context.Background(), jobs.Job{Kind: KindRelate}, &fakeRT{}); err != nil {
		t.Fatal(err)
	}
	return visible(t, c.st)
}

// visible reads every relation of the visible generation.
func visible(t testing.TB, st *store.Store) []Relation {
	t.Helper()
	var out []Relation
	err := st.Read(context.Background(), func(tx *sql.Tx) error {
		return scanAll(context.Background(), tx, `SELECT `+relationColumns+` FROM relations
			WHERE gen = (SELECT gen FROM review_state WHERE id = 1) ORDER BY id`, nil, func(r *sql.Rows) error {
			rel, err := scanRelation(r)
			out = append(out, rel)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// R2.3: on the corpus seeded with SeedContent, the two zips are the same
// as their unpacked folders, and so is the Winamp copy as the original;
// every relation the ground truth declares is found with its sides
// ordered as design D9 says.
func TestR2_3ZipsAndWinampAreTheSame(t *testing.T) {
	t.Parallel()
	c := seedCorpus(t)
	rels := c.relate(t)
	find := func(a, b string) *Relation {
		for i, r := range rels {
			if r.A == c.ref(a) && r.B == c.ref(b) {
				return &rels[i]
			}
		}
		return nil
	}
	for _, pair := range [][2]string{
		{"Downloads/fotos_2005_do_pendrive.zip", "Downloads/fotos_2005_do_pendrive"},
		{"Downloads/eMule0.47c-Installer.zip", "Downloads/emule-0.47c"},
	} {
		if r := find(pair[0], pair[1]); r == nil || r.Kind != KindSame || r.AOnlyFiles != 0 || r.BOnlyFiles != 0 {
			t.Errorf("%s same %s: got %+v", pair[0], pair[1], r)
		}
	}
	for _, want := range c.truth.Relations {
		a, b := raw(t, want.A), raw(t, want.B)
		r := find(a, b)
		if r == nil || r.Kind != want.Kind || r.AOnlyFiles != int64(len(want.AOnly)) || r.BOnlyFiles != int64(len(want.BOnly)) {
			t.Errorf("declared %s %s <- %s (only %d/%d): got %+v", want.Kind, want.A.Path, want.B.Path,
				len(want.AOnly), len(want.BOnly), r)
		}
		if want.Kind == KindSame && r != nil && (r.AOnlyBytes != 0 || r.BOnlyBytes != 0) {
			t.Errorf("same %s <- %s lists bytes on one side: %+v", want.A.Path, want.B.Path, r)
		}
	}
	// One relation per copy: nothing inside a related pair of 2004 folders.
	f2004, c2004 := c.ref("Fotos/2004"), c.ref("Fotos - Copia/2004")
	paths := c.pathsByID(t)
	for _, r := range rels {
		for _, side := range []domain.Ref{r.A, r.B} {
			p := paths[side.Entry]
			if side != f2004 && side != c2004 && (under(p, "Fotos/2004") || under(p, "Fotos - Copia/2004")) {
				t.Errorf("a relation below the 2004 pair: %+v (%q)", r, p)
			}
		}
	}
}

func under(p, dir string) bool { return len(p) > len(dir) && p[:len(dir)] == dir && p[len(dir)] == '/' }

func (c *corpusWorld) pathsByID(t testing.TB) map[domain.EntryID]string {
	t.Helper()
	out := map[domain.EntryID]string{}
	err := c.st.Read(context.Background(), func(tx *sql.Tx) error {
		return scanAll(context.Background(), tx, `SELECT id, path FROM entries`, nil, func(r *sql.Rows) error {
			var id int64
			var p []byte
			err := r.Scan(&id, &p)
			out[domain.EntryID(id)] = string(p)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Task 4.2: dir_dups equals a direct computation on the seeded corpus:
// per folder, over the present non-empty files of its subtree with a
// file_content row, candidate bytes (all but unique_size), checked bytes
// (hashed and sampled), and the files with another physical copy among
// present files and members of complete archives (a hard-link set, or a
// tar hard link with its target, is one copy).
func TestDirDupsEqualADirectComputation(t *testing.T) {
	t.Parallel()
	c := seedCorpus(t)
	c.relate(t)
	ctx := context.Background()
	q := c.st.Reader()

	type key struct{ src, path string }
	dirs := map[key]int64{}
	want := map[int64]*DirDups{}
	err := scanAll(ctx, q, `SELECT id, source_id, path FROM entries WHERE kind = 'directory' AND state <> 'missing'`, nil,
		func(r *sql.Rows) error {
			var id int64
			var src string
			var p []byte
			err := r.Scan(&id, &src, &p)
			dirs[key{src, string(p)}] = id
			want[id] = &DirDups{}
			return err
		})
	if err != nil {
		t.Fatal(err)
	}
	copies := map[int64]map[string]bool{}
	err = scanAll(ctx, q, `SELECT f.content_id, CASE WHEN e.nlink > 1 AND e.dev IS NOT NULL THEN 'i' || e.dev || ':' || e.ino ELSE 'e' || e.id END
			FROM file_content f JOIN entries e ON e.id = f.entry_id WHERE f.content_id IS NOT NULL AND e.state = 'present'
		UNION ALL
		SELECT m.content_id, 'm' || COALESCE(m.link_member, m.id) FROM archive_members m
			JOIN archives a ON a.entry_id = m.archive_id AND a.state = 'complete'
			JOIN entries ae ON ae.id = a.entry_id AND ae.state = 'present'
			WHERE m.content_id IS NOT NULL`, nil, func(r *sql.Rows) error {
		var cid int64
		var phys string
		err := r.Scan(&cid, &phys)
		if copies[cid] == nil {
			copies[cid] = map[string]bool{}
		}
		copies[cid][phys] = true
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = scanAll(ctx, q, `SELECT e.source_id, e.path, e.size, f.state, COALESCE(f.content_id, 0) FROM entries e
		JOIN file_content f ON f.entry_id = e.id WHERE e.state = 'present' AND e.kind = 'file' AND e.size > 0`, nil,
		func(r *sql.Rows) error {
			var src, state string
			var p []byte
			var size, cid int64
			if err := r.Scan(&src, &p, &size, &state, &cid); err != nil {
				return err
			}
			path := string(p)
			for {
				i := len(path) - 1
				for i >= 0 && path[i] != '/' {
					i--
				}
				if i < 0 {
					path = ""
				} else {
					path = path[:i]
				}
				d := want[dirs[key{src, path}]]
				if state != string(domain.ContentUniqueSize) {
					d.CandidateBytes += size
				}
				if state == string(domain.ContentHashed) || state == string(domain.ContentSampled) {
					d.CheckedBytes += size
				}
				if cid != 0 && len(copies[cid]) >= 2 {
					d.DuplicatedBytes += size
					d.DuplicatedFiles++
				}
				if path == "" {
					return nil
				}
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]DirDups{}
	err = scanAll(ctx, q, `SELECT entry_id, candidate_bytes, checked_bytes, duplicated_bytes, duplicated_files FROM dir_dups`, nil,
		func(r *sql.Rows) error {
			var id int64
			var d DirDups
			err := r.Scan(&id, &d.CandidateBytes, &d.CheckedBytes, &d.DuplicatedBytes, &d.DuplicatedFiles)
			got[id] = d
			return err
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Errorf("%d dir_dups rows, want one per folder (%d)", len(got), len(want))
	}
	for id, w := range want {
		if got[id] != *w {
			t.Errorf("folder %d: dir_dups %+v, want %+v", id, got[id], *w)
		}
	}
	// Spec: Fotos - Copia is about 97% duplicated, every candidate checked.
	fc := got[int64(c.seeded.ID("Fotos - Copia"))]
	var total int64
	if err := q.QueryRow(`SELECT total_bytes FROM entries WHERE id = ?`, int64(c.seeded.ID("Fotos - Copia"))).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if pct := fc.DuplicatedBytes * 100 / total; pct < 95 || pct > 99 || fc.CheckedBytes != fc.CandidateBytes {
		t.Errorf("Fotos - Copia: %+v of %d bytes (%d%%)", fc, total, pct)
	}

	// A second run with nothing changed writes no dir_dups row (temporary
	// triggers on the single writer connection count the writes).
	for _, stmt := range []string{
		`CREATE TEMP TABLE dd_writes (n INTEGER)`,
		`CREATE TEMP TRIGGER dd_ins AFTER INSERT ON main.dir_dups BEGIN INSERT INTO dd_writes VALUES (1); END`,
		`CREATE TEMP TRIGGER dd_upd AFTER UPDATE ON main.dir_dups BEGIN INSERT INTO dd_writes VALUES (1); END`,
	} {
		if _, err := c.st.Writer().Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	c.relate(t)
	var writes int64
	if err := c.st.Writer().QueryRow(`SELECT count(*) FROM dd_writes`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Errorf("an unchanged index rewrote %d dir_dups rows", writes)
	}
}
