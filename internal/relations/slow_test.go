//go:build slow

package relations

import (
	"context"
	"database/sql"
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
//
// R5c task 2.1 adds what the places of the relate pass read:
//   - Source "big2": tops u00..u08, each with 10 mids holding 10 leaves of
//     101 hashed files (99,990 files). In every folder the even files are
//     copies of files of big (every 39th of them) and the odd ones are
//     unique and larger, so no folder of big2 reaches an overlap with big.
//   - In leaves l00 and l05 of tops t06..t16 of big, a complete zip
//     "pack.zip" (1,980 archives) of 50 file members in folders docs and
//     pics. Two members of each are copies of unique files of big2, eight
//     are copies of files of big, and the rest are unique, so no archive
//     reaches an overlap either.
//   - Every 495th archive (4) has small unique members and a byte-identical
//     copy on big2, zips/pack-NNNN.zip, beside 50 large unique files: each
//     copy is the same as its original, the only relations besides t05's.
const (
	bigTops   = 20
	bigMids   = 90
	bigLeaves = 10
	bigFiles  = 101

	big2Tops   = 9
	big2Mids   = 10
	big2Leaves = 10
	big2Files  = 101
	big2Stride = 39 // big2's copies are every 39th file of big
	zipsFiles  = 50

	arcFirstTop = 6
	arcLastTop  = 16
	arcMembers  = 50
	arcCopied   = 495 // archive a has a copy on big2 when a % arcCopied == 0
	memberBig2  = 12  // members copy every 12th unique file of big2
	memberBig   = 100 // and every 100th file of big

	// Content ids of the contents seeded beyond big's keys (below 1e9).
	big2Unique   = 1_000_000_000
	memberUnique = 1_100_000_000
	zipContent   = 1_200_000_000
	zipsUnique   = 1_300_000_000
)

// seedBig seeds sources big and big2 and returns the store and each
// archive of big that has a byte-identical copy on big2, with that copy.
func seedBig(t *testing.T) (*store.Store, [][2]domain.EntryID) {
	t.Helper()
	st := storetest.Open(t)
	s := indextest.Seed(t, st, indextest.Tree{Source: "big", CreateSource: true, MountPoint: "/mnt/big"})
	s2 := indextest.Seed(t, st, indextest.Tree{Source: "big2", CreateSource: true, MountPoint: "/mnt/big2"})
	start := time.Now()
	ctx := context.Background()
	tx, err := st.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	now := indextest.DefaultNow.UnixMilli()
	exec(`CREATE TEMP TABLE big_dirs (id INTEGER PRIMARY KEY, source_id TEXT, parent INTEGER, path BLOB, top INTEGER, ord INTEGER)`)
	var next int64
	if err := tx.QueryRow(`SELECT max(id) + 1 FROM entries`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	next = seedFolders(t, tx, "big", int64(s.Root), next, "t", bigTops, bigMids, bigLeaves)
	next = seedFolders(t, tx, "big2", int64(s2.Root), next, "u", big2Tops, big2Mids, big2Leaves)
	zips := next
	exec(`INSERT INTO big_dirs VALUES (?, 'big2', ?, CAST('zips' AS BLOB), -1, -1)`, zips, int64(s2.Root))
	exec(`INSERT INTO entries (id, source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		SELECT id, source_id, parent, CAST(substr(CAST(path AS TEXT), length(rtrim(CAST(path AS TEXT), 'abcdefghijklmnopqrstuvwxyz0123456789'))+1) AS BLOB),
			path, 'directory', 'present', ?, ?, 1 FROM big_dirs ORDER BY id`, now, now)

	// Files of big: content key k = (source top)*1e6 + ord*bigFiles + n,
	// shared within a pair of tops for every 13th file, t05 taking t04's.
	exec(`CREATE TEMP TABLE big_files AS
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i + 1 < ?)
		SELECT d.id AS parent, CAST(CAST(d.path AS TEXT) || '/f' || printf('%03d', n.i) AS BLOB) AS path,
			printf('f%03d', n.i) AS name,
			CASE WHEN d.top NOT IN (4, 5) AND (d.ord * ? + n.i) % 13 = 0
				THEN 900000000 + (d.top / 2) * 1000000 + d.ord * ? + n.i
				ELSE (CASE d.top WHEN 5 THEN 4 ELSE d.top END) * 1000000 + d.ord * ? + n.i END AS k
		FROM big_dirs d CROSS JOIN n WHERE d.source_id = 'big' AND d.ord >= 0`, bigFiles, bigFiles, bigFiles, bigFiles)
	exec(`INSERT INTO contents (id, sha256, size) SELECT k + 1, randomblob(32), 1000 + (k * 7919) % 100000
		FROM (SELECT DISTINCT k FROM big_files)`)
	exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, size, total_bytes, total_files, state, first_seen, last_seen, scan_gen)
		SELECT 'big', parent, CAST(name AS BLOB), path, 'file', 1000 + (k * 7919) % 100000, 1000 + (k * 7919) % 100000, 1,
			'present', ?, ?, 1 FROM big_files`, now, now)
	exec(`INSERT INTO file_content (entry_id, source_id, state, size, content_id)
		SELECT e.id, 'big', 'hashed', e.size, f.k + 1 FROM big_files f JOIN entries e ON e.source_id = 'big' AND e.path = f.path`)
	exec(`DROP TABLE big_files`)

	// big's files have consecutive ids: copies pick them by stride.
	var first, count, last int64
	if err := tx.QueryRow(`SELECT min(id), count(*), max(id) FROM entries WHERE source_id = 'big' AND kind = 'file'`).
		Scan(&first, &count, &last); err != nil {
		t.Fatal(err)
	}
	big2Folders := int64(big2Tops * big2Mids * (big2Leaves + 1))
	copies, uniques := int64(big2Files+1)/2, int64(big2Files/2)
	archives := int64((arcLastTop - arcFirstTop + 1) * bigMids * 2)
	if last-first+1 != count || (big2Folders*copies-1)*big2Stride >= count || (archives*10-1)*memberBig >= count ||
		(archives*2-1)*memberBig2 >= big2Folders*uniques {
		t.Fatalf("big's %d files (ids %d..%d) do not hold the copies", count, first, last)
	}

	// Files of big2: in folder g (in order), file n is a copy of big's file
	// g*copies + n/2 (by stride) when n is even, else unique u = g*uniques
	// + n/2. Then the unique files of zips.
	exec(`CREATE TEMP TABLE big2_files AS
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i + 1 < ?1),
		f AS (SELECT d.id AS parent, CAST(CAST(d.path AS TEXT) || '/f' || printf('%03d', n.i) AS BLOB) AS path,
			printf('f%03d', n.i) AS name, n.i % 2 = 0 AS copy,
			(d.top * ?2 + d.ord) * ?3 + n.i / 2 AS q, (d.top * ?2 + d.ord) * ?4 + n.i / 2 AS u
			FROM big_dirs d CROSS JOIN n WHERE d.source_id = 'big2' AND d.ord >= 0)
		SELECT parent, path, name,
			CASE WHEN copy THEN fc.content_id ELSE ?5 + u END AS content,
			CASE WHEN copy THEN fc.size ELSE 200000 + u * 7919 % 100000 END AS size
		FROM f LEFT JOIN file_content fc ON copy AND fc.entry_id = ?6 + q * ?7`,
		big2Files, big2Mids*(big2Leaves+1), copies, uniques, big2Unique, first, big2Stride)
	exec(`INSERT INTO big2_files
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i + 1 < ?)
		SELECT ?, CAST('zips/z' || printf('%02d', i) || '.bin' AS BLOB), 'z' || printf('%02d', i) || '.bin', ? + i, 1000000 + i * 7919 % 1000
		FROM n`, zipsFiles, zips, zipsUnique)
	exec(`INSERT INTO contents (id, sha256, size) SELECT content, randomblob(32), size FROM big2_files WHERE content >= ?`, big2Unique)
	exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, size, total_bytes, total_files, state, first_seen, last_seen, scan_gen)
		SELECT 'big2', parent, CAST(name AS BLOB), path, 'file', size, size, 1, 'present', ?, ?, 1 FROM big2_files`, now, now)
	exec(`INSERT INTO file_content (entry_id, source_id, state, size, content_id)
		SELECT e.id, 'big2', 'hashed', e.size, f.content FROM big2_files f JOIN entries e ON e.source_id = 'big2' AND e.path = f.path`)
	exec(`DROP TABLE big2_files`)

	// Archives: a in big's leaf order, then the copies on big2 (r numbers
	// every archive file, its members taking ids from (r-1)*(members+2)+1).
	exec(`CREATE TEMP TABLE big_arcs (r INTEGER PRIMARY KEY, a INTEGER, source_id TEXT, parent INTEGER, name TEXT, path BLOB, size INTEGER, entry INTEGER)`)
	exec(`INSERT INTO big_arcs (a, source_id, parent, name, path, size)
		SELECT a, 'big', id, 'pack.zip', CAST(CAST(path AS TEXT) || '/pack.zip' AS BLOB),
			CASE WHEN a % ?3 = 0 THEN 30000 + a ELSE 5000000 + a * 7919 % 1000000 END
		FROM (SELECT row_number() OVER (ORDER BY id) - 1 AS a, id, path FROM big_dirs
			WHERE source_id = 'big' AND top BETWEEN ?1 AND ?2 AND ord % ?4 IN (1, 6))`,
		arcFirstTop, arcLastTop, arcCopied, bigLeaves+1)
	exec(`INSERT INTO big_arcs (a, source_id, parent, name, path, size)
		SELECT a, 'big2', ?, 'pack-' || printf('%04d', a) || '.zip', CAST('zips/pack-' || printf('%04d', a) || '.zip' AS BLOB), size
		FROM big_arcs WHERE source_id = 'big' AND a % ? = 0 ORDER BY a`, zips, arcCopied)
	exec(`DROP TABLE big_dirs`)
	exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, size, mtime_ns, total_bytes, total_files, state, first_seen, last_seen, scan_gen)
		SELECT source_id, parent, CAST(name AS BLOB), path, 'file', size, ?, size, 1, 'present', ?, ?, 1 FROM big_arcs ORDER BY r`,
		indextest.DefaultNow.UnixNano(), now, now)
	exec(`UPDATE big_arcs SET entry = (SELECT id FROM entries e WHERE e.source_id = big_arcs.source_id AND e.path = big_arcs.path)`)
	exec(`INSERT INTO contents (id, sha256, size) SELECT ? + a, randomblob(32), size FROM big_arcs WHERE source_id = 'big'`, zipContent)
	exec(`INSERT INTO file_content (entry_id, source_id, state, size, content_id)
		SELECT entry, source_id, 'hashed', size, ? + a FROM big_arcs`, zipContent)

	// Members: j < members/2 in docs, the rest in pics. A copied archive's
	// are small and unique; another's j = 0 and members/2 copy unique files
	// of big2, its other j % 5 = 0 copy files of big, and the rest are
	// unique.
	exec(`CREATE TEMP TABLE big_members AS
		WITH RECURSIVE j(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM j WHERE i + 1 < ?1)
		SELECT x.r, x.entry, j.i AS j, (x.r - 1) * (?1 + 2) + 1 AS base,
			CASE WHEN x.a % ?2 = 0 THEN ?3 + x.a * ?1 + j.i
				WHEN j.i IN (0, ?1 / 2) THEN ?4 + (x.a * 2 + j.i / (?1 / 2)) * ?5
				WHEN j.i % 5 = 0 THEN fc.content_id
				ELSE ?3 + x.a * ?1 + j.i END AS content,
			CASE WHEN x.a % ?2 = 0 THEN 500 + (x.a * ?1 + j.i) * 7919 % 1000
				WHEN j.i IN (0, ?1 / 2) THEN 200000 + (x.a * 2 + j.i / (?1 / 2)) * ?5 * 7919 % 100000
				WHEN j.i % 5 = 0 THEN fc.size
				ELSE 100000 + (x.a * ?1 + j.i) * 7919 % 100000 END AS size
		FROM big_arcs x CROSS JOIN j LEFT JOIN file_content fc ON x.a % ?2 <> 0 AND j.i % 5 = 0 AND j.i NOT IN (0, ?1 / 2)
			AND fc.entry_id = ?6 + (x.a * 10 + j.i / 5) * ?7`,
		arcMembers, arcCopied, memberUnique, big2Unique, memberBig2, first, memberBig)
	exec(`INSERT INTO contents (id, sha256, size) SELECT content, randomblob(32), size
		FROM (SELECT DISTINCT content, size FROM big_members WHERE content >= ?)`, memberUnique)
	exec(`INSERT INTO archives (entry_id, format, state, size, mtime_ns, members, unpacked_bytes, listed_at)
		SELECT x.entry, 'zip', 'complete', x.size, ?, ? + 2, sum(m.size), ?
		FROM big_members m JOIN big_arcs x ON x.r = m.r GROUP BY m.r ORDER BY m.r`,
		indextest.DefaultNow.UnixNano(), arcMembers, now)
	exec(`INSERT INTO archive_members (id, archive_id, parent_id, name, path, kind, total_bytes, total_files)
		SELECT base + j / (?1 / 2), entry, NULL, CAST(CASE j / (?1 / 2) WHEN 0 THEN 'docs' ELSE 'pics' END AS BLOB),
			CAST(CASE j / (?1 / 2) WHEN 0 THEN 'docs' ELSE 'pics' END AS BLOB), 'directory', sum(size), count(*)
		FROM big_members GROUP BY r, j / (?1 / 2) ORDER BY r, j / (?1 / 2)`, arcMembers)
	exec(`INSERT INTO archive_members (id, archive_id, parent_id, name, path, kind, size, total_bytes, total_files, locator, state, content_id)
		SELECT base + 2 + j, entry, base + j / (?1 / 2), CAST(name AS BLOB),
			CAST(CASE j / (?1 / 2) WHEN 0 THEN 'docs/' ELSE 'pics/' END || name AS BLOB),
			'file', size, size, 1, j, 'hashed', content
		FROM (SELECT *, CASE j / (?1 / 2) WHEN 0 THEN printf('d%02d.txt', j) ELSE printf('p%02d.jpg', j) END AS name
			FROM big_members) ORDER BY r, j`, arcMembers)
	exec(`DROP TABLE big_members`)

	rows, err := tx.Query(`SELECT o.entry, c.entry FROM big_arcs o JOIN big_arcs c ON c.a = o.a AND c.source_id = 'big2'
		WHERE o.source_id = 'big' ORDER BY o.a`)
	if err != nil {
		t.Fatal(err)
	}
	var dups [][2]domain.EntryID
	for rows.Next() {
		var d [2]domain.EntryID
		if err := rows.Scan(&d[0], &d[1]); err != nil {
			t.Fatal(err)
		}
		dups = append(dups, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	exec(`DROP TABLE big_arcs`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var entries, entries2, arcs, members int64
	if err := st.Reader().QueryRow(`SELECT count(*), count(*) FILTER (WHERE source_id = 'big2'),
		(SELECT count(*) FROM archives WHERE state = 'complete'), (SELECT count(*) FROM archive_members WHERE kind = 'file')
		FROM entries`).Scan(&entries, &entries2, &arcs, &members); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %d entries (%d on big2), %d complete archives with %d file members, in %v",
		entries, entries2, arcs, members, time.Since(start))
	if entries < 2_000_000 {
		t.Fatalf("%d entries, want at least 2,000,000", entries)
	}
	if arcs != archives+int64(len(dups)) || len(dups) == 0 {
		t.Fatalf("%d archives, %d of them copied; want %d and their copies", arcs, len(dups), archives)
	}
	return st, dups
}

// seedFolders adds source src's folders below root to the temporary table
// big_dirs, with ids from next in pre order: tops <prefix>00..., each with
// mids mNNN holding leaves lNN. ord is a folder's ordinal within its top
// (-1 for the top). It returns the next free id.
func seedFolders(t *testing.T, tx *sql.Tx, src domain.SourceID, root, next int64, prefix string, tops, mids, leaves int) int64 {
	t.Helper()
	dir, err := tx.Prepare(`INSERT INTO big_dirs VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	add := func(parent int64, path string, top, ord int) int64 {
		id := next
		next++
		if _, err := dir.Exec(id, string(src), parent, []byte(path), top, ord); err != nil {
			t.Fatal(err)
		}
		return id
	}
	for top := range tops {
		tp := fmt.Sprintf("%s%02d", prefix, top)
		tid := add(root, tp, top, -1)
		ord := 0
		for m := range mids {
			mp := fmt.Sprintf("%s/m%03d", tp, m)
			mid := add(tid, mp, top, ord)
			ord++
			for l := range leaves {
				add(mid, fmt.Sprintf("%s/l%02d", mp, l), top, ord)
				ord++
			}
		}
	}
	return next
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
	st, dups := seedBig(t)
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
	// One relation per copy: t05 same t04, and each copied archive on big2
	// (path-later) same its original.
	rels := visible(t, st)
	t04, t05 := domain.Ref{Entry: entryID(t, st, "t04")}, domain.Ref{Entry: entryID(t, st, "t05")}
	want := map[[2]domain.Ref]bool{{t05, t04}: true}
	for _, d := range dups {
		want[[2]domain.Ref{{Entry: d[1]}, {Entry: d[0]}}] = true
	}
	found := 0
	for _, r := range rels {
		if r.Kind == KindSame && want[[2]domain.Ref{r.A, r.B}] {
			found++
		}
	}
	if found != len(want) || len(rels) != len(want) {
		t.Errorf("%d relations; want %d, t05 same t04 and each copied archive same its original: %+v", len(rels), len(want), rels)
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
