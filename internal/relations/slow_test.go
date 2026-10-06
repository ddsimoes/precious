//go:build slow

package relations

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/jobs"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// Task 4.6 (design D20): relate over a 2,000,000-entry index within 3
// minutes and 1.5 GB resident, and Compare of two 100,000-file sides within
// 2 s. The tree, under source "big": tops t00..t19, each with 90 mids
// holding 10 leaves; every mid and leaf holds 101 hashed files, so a top
// holds 99,990 files and the index 2,019,621 entries. t05 is a copy of t04;
// in each other pair of tops (t00 and t01, ...) every 13th file has the
// same small content, far below an overlap.
const (
	bigTops   = 20
	bigMids   = 90
	bigLeaves = 10
	bigFiles  = 101
)

func seedBig(t *testing.T) (*store.Store, *indextest.Seeded) {
	t.Helper()
	st := storetest.Open(t)
	s := indextest.Seed(t, st, indextest.Tree{Source: "big", CreateSource: true, MountPoint: "/mnt/big"})
	start := time.Now()
	ctx := context.Background()
	tx, err := st.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) {
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Folders (id, parent, path, top, folder ordinal within the top).
	exec(`CREATE TEMP TABLE big_dirs (id INTEGER PRIMARY KEY, parent INTEGER, path BLOB, top INTEGER, ord INTEGER)`)
	dir, err := tx.Prepare(`INSERT INTO big_dirs VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	next := int64(s.Root) + 1
	add := func(parent int64, path string, top, ord int) int64 {
		id := next
		next++
		if _, err := dir.Exec(id, parent, []byte(path), top, ord); err != nil {
			t.Fatal(err)
		}
		return id
	}
	for top := range bigTops {
		tp := fmt.Sprintf("t%02d", top)
		tid := add(int64(s.Root), tp, top, -1)
		ord := 0
		for m := range bigMids {
			mp := fmt.Sprintf("%s/m%03d", tp, m)
			mid := add(tid, mp, top, ord)
			ord++
			for l := range bigLeaves {
				add(mid, fmt.Sprintf("%s/l%02d", mp, l), top, ord)
				ord++
			}
		}
	}
	dir.Close()
	now := indextest.DefaultNow.UnixMilli()
	exec(`INSERT INTO entries (id, source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		SELECT id, 'big', parent, CAST(substr(CAST(path AS TEXT), length(rtrim(CAST(path AS TEXT), 'abcdefghijklmnopqrstuvwxyz0123456789'))+1) AS BLOB),
			path, 'directory', 'present', ?, ?, 1 FROM big_dirs ORDER BY id`, now, now)
	// Files: content key k = (source top)*1e6 + ord*bigFiles + n, shared
	// within a pair of tops for every 13th file, t05 taking t04's.
	exec(`CREATE TEMP TABLE big_files AS
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i + 1 < ?)
		SELECT d.id AS parent, CAST(CAST(d.path AS TEXT) || '/f' || printf('%03d', n.i) AS BLOB) AS path,
			printf('f%03d', n.i) AS name,
			CASE WHEN d.top NOT IN (4, 5) AND (d.ord * ? + n.i) % 13 = 0
				THEN 900000000 + (d.top / 2) * 1000000 + d.ord * ? + n.i
				ELSE (CASE d.top WHEN 5 THEN 4 ELSE d.top END) * 1000000 + d.ord * ? + n.i END AS k
		FROM big_dirs d CROSS JOIN n WHERE d.ord >= 0`, bigFiles, bigFiles, bigFiles, bigFiles)
	exec(`INSERT INTO contents (id, sha256, size) SELECT k + 1, randomblob(32), 1000 + (k * 7919) % 100000
		FROM (SELECT DISTINCT k FROM big_files)`)
	exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, size, total_bytes, total_files, state, first_seen, last_seen, scan_gen)
		SELECT 'big', parent, CAST(name AS BLOB), path, 'file', 1000 + (k * 7919) % 100000, 1000 + (k * 7919) % 100000, 1,
			'present', ?, ?, 1 FROM big_files`, now, now)
	exec(`INSERT INTO file_content (entry_id, source_id, state, size, content_id)
		SELECT e.id, 'big', 'hashed', e.size, f.k + 1 FROM big_files f JOIN entries e ON e.source_id = 'big' AND e.path = f.path`)
	exec(`DROP TABLE big_files`)
	exec(`DROP TABLE big_dirs`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var entries int64
	if err := st.Reader().QueryRow(`SELECT count(*) FROM entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %d entries in %v", entries, time.Since(start))
	if entries < 2_000_000 {
		t.Fatalf("%d entries, want at least 2,000,000", entries)
	}
	return st, s
}

func maxRSS(t *testing.T) int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatal(err)
	}
	return ru.Maxrss * 1024 // KiB on Linux
}

func TestRelateAtScale(t *testing.T) {
	if raceEnabled {
		t.Skip("time and memory targets are measured without the race detector; make test-slow runs this test in its second pass")
	}
	st, _ := seedBig(t)
	runtime.GC()
	rssBefore := maxRSS(t)

	h := NewHandler(st, nil, configDuplicates(), nil)
	start := time.Now()
	if err := h.Run(context.Background(), jobs.Job{Kind: KindRelate}, &fakeRT{}); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	rss := maxRSS(t)
	t.Logf("relate: %v; max resident %d MiB (%d MiB before relate, seeding included)", took, rss>>20, rssBefore>>20)
	if took > 3*time.Minute {
		t.Errorf("relate took %v, target 3 min", took)
	}
	if rss > 1500<<20 {
		t.Errorf("max resident %d MiB, target 1.5 GB", rss>>20)
	}
	rels := visible(t, st)
	t04, t05 := domain.Ref{Entry: entryID(t, st, "t04")}, domain.Ref{Entry: entryID(t, st, "t05")}
	found := false
	for _, r := range rels {
		if r.A == t05 && r.B == t04 && r.Kind == KindSame {
			found = true
		}
	}
	if !found || len(rels) != 1 {
		t.Errorf("%d relations; want one, t05 same t04: %+v", len(rels), rels)
	}
	var dd DirDups
	if err := st.Reader().QueryRow(`SELECT candidate_bytes, checked_bytes, duplicated_bytes, duplicated_files FROM dir_dups
		WHERE entry_id = ?`, int64(t05.Entry)).Scan(&dd.CandidateBytes, &dd.CheckedBytes, &dd.DuplicatedBytes, &dd.DuplicatedFiles); err != nil {
		t.Fatal(err)
	}
	if dd.DuplicatedFiles != bigMids*(bigLeaves+1)*bigFiles || dd.DuplicatedBytes != dd.CandidateBytes {
		t.Errorf("t05 dir_dups %+v", dd)
	}

	// Compare two 100,000-file sides.
	start = time.Now()
	res, err := Compare(context.Background(), st.Reader(), t04, t05, BucketIdentical, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	took = time.Since(start)
	t.Logf("compare of %d and %d files: %v", bigMids*(bigLeaves+1)*bigFiles, bigMids*(bigLeaves+1)*bigFiles, took)
	if n := res.Summary[BucketIdentical].Files; n != bigMids*(bigLeaves+1)*bigFiles {
		t.Errorf("identical %d", n)
	}
	if took > 2*time.Second {
		t.Errorf("compare took %v, target 2 s", took)
	}
}

func entryID(t *testing.T, st *store.Store, path string) domain.EntryID {
	t.Helper()
	var id int64
	if err := st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = 'big' AND path = ?`, []byte(path)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return domain.EntryID(id)
}
