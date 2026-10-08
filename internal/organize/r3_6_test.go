package organize

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/relations"
)

// compare reads every item of one Compare group of two folders.
func (w *world) compare(left, right string, b relations.Bucket) (relations.CompareResult, []relations.CompareItem) {
	w.t.Helper()
	l, r := domain.Ref{Entry: domain.EntryID(mustID(w.t, left))}, domain.Ref{Entry: domain.EntryID(mustID(w.t, right))}
	var (
		first relations.CompareResult
		all   []relations.CompareItem
	)
	w.readTx(func(tx *sql.Tx) error {
		for cursor := ""; ; {
			res, err := relations.Compare(context.Background(), tx, l, r, b, cursor, 1000)
			if err != nil {
				return err
			}
			if cursor == "" {
				first = res
			}
			all = append(all, res.Items...)
			if res.NextCursor == "" {
				return nil
			}
			cursor = res.NextCursor
		}
	})
	return first, all
}

// R3.6: on the corpus, after hashing, the owner moves the files only in
// Fotos - Copia into Fotos, and hashing and relations run again:
// Fotos/2006/Praia/DSC_editada.JPG exists, Compare of the two shows no file
// only in Fotos - Copia, and every file left in Fotos - Copia has a copy in
// Fotos.
func TestR3_6MergingFotosCopiaIntoFotos(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	root, _ := corpus.BuildSynth(w.sfs, "/corpus", corpus.Corpus())
	w.add("corpus", "/corpus", root, posix)
	fotos, copia := w.id("corpus", "Fotos"), w.id("corpus", "Fotos - Copia")
	sum, only := w.compare(fotos, copia, relations.BucketOnlyRight)
	if len(only) != 1 || string(only[0].RightPath) != "2006/Praia/DSC_editada.JPG" {
		t.Fatalf("before the merge only_right is %v (summary %+v)", paths(only), sum.Summary)
	}

	a, items := w.plan("plan-merge", fmt.Sprintf(`{"left_id":%q,"right_id":%q,"from":"right"}`, fotos, copia))
	wantItems(t, items, "rename planned Fotos - Copia/2006/Praia/DSC_editada.JPG -> Fotos/2006/Praia/DSC_editada.JPG")
	if a.Kind != "merge" || !a.Bulk || a.Destination == nil || a.Destination.Path != "Fotos" {
		t.Fatalf("merge action %+v", a)
	}
	if got := w.run(a.ID); got.State != "done" || got.Counts["done"] != 1 {
		t.Fatalf("merge %+v", got)
	}
	w.scan("corpus") // hashing and relations again
	if !w.exists("/corpus", "Fotos/2006/Praia/DSC_editada.JPG") {
		t.Fatal("Fotos/2006/Praia/DSC_editada.JPG is not on the disk")
	}
	if p, state := w.pathOf(w.id("corpus", "Fotos/2006/Praia/DSC_editada.JPG")); state != "present" || p == "" {
		t.Fatalf("the index holds it %s", state)
	}
	sum, only = w.compare(fotos, copia, relations.BucketOnlyRight)
	if len(only) != 0 || sum.Summary[relations.BucketUnchecked].Files != 0 || sum.Summary[relations.BucketDifferent].Files != 0 {
		t.Fatalf("after the merge only_right %v, summary %+v", paths(only), sum.Summary)
	}
	// Every file left in the copy is paired with, or a copy of, one in Fotos.
	_, same := w.compare(fotos, copia, relations.BucketIdentical)
	right := 0
	for _, it := range same {
		if it.Right != nil {
			right++
			if it.Left == nil && it.Twin == nil {
				t.Errorf("%s has no copy in Fotos", it.RightPath)
			}
		}
	}
	if files := w.count(`SELECT count(*) FROM entries WHERE source_id = 'corpus' AND kind = 'file' AND state = 'present'
		AND path >= ? AND path < ?`, []byte("Fotos - Copia/"), []byte("Fotos - Copia0")); right != files || files == 0 {
		t.Errorf("%d of the %d files in Fotos - Copia have a copy in Fotos", right, files)
	}
}

func paths(items []relations.CompareItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, string(it.Path))
	}
	return out
}

// R3.6: Fotos (copia) holds Fotos/2007; comparing it with Fotos, the files
// only in Fotos merge into Fotos (copia)/Fotos/…, under the wrapper Compare
// left out, not one level higher; a missing folder is made first, and an
// entry in the way of a folder makes the items below it conflicts.
func TestR3_6MergeIntoAWrapperSide(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		f := root.Dir("Fotos")
		f07 := f.Dir("2007")
		f07.File("a.jpg", 100, mtime).Content([]byte("photo a"))
		f07.File("b.jpg", 201, mtime)
		f.Dir("2008").Dir("Praia").File("c.jpg", 302, mtime)
		f.Dir("2009").File("d.jpg", 403, mtime)
		c := root.Dir("Fotos (copia)").Dir("Fotos")
		c.Dir("2007").File("a.jpg", 100, mtime).Content([]byte("photo a"))
		c.File("2009", 7, mtime) // a file where a folder is needed
	})
	copia, fotos := w.id("disk", "Fotos (copia)"), w.id("disk", "Fotos")
	a, items := w.plan("plan-merge", fmt.Sprintf(`{"left_id":%q,"right_id":%q,"from":"right"}`, copia, fotos))
	wantItems(t, items,
		"rename planned Fotos/2007/b.jpg -> Fotos (copia)/Fotos/2007/b.jpg",
		"mkdir planned -> Fotos (copia)/Fotos/2008",
		"mkdir planned -> Fotos (copia)/Fotos/2008/Praia",
		"rename planned Fotos/2008/Praia/c.jpg -> Fotos (copia)/Fotos/2008/Praia/c.jpg",
		"rename conflict name_taken Fotos/2009/d.jpg -> Fotos (copia)/Fotos/2009/d.jpg")
	var toSeq []string
	w.readTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT seq, COALESCE(to_dir_seq, 0), to_parent IS NOT NULL FROM action_items
			WHERE action_id = ? ORDER BY seq`, a.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var seq, dirSeq int
			var parent bool
			if err := rows.Scan(&seq, &dirSeq, &parent); err != nil {
				return err
			}
			toSeq = append(toSeq, fmt.Sprintf("%d:%d:%v", seq, dirSeq, parent))
		}
		return rows.Err()
	})
	if got := strings.Join(toSeq, " "); got != "1:0:true 2:0:true 3:2:false 4:3:false 5:0:false" {
		t.Errorf("destinations by seq: %s", got)
	}
	got := w.run(a.ID)
	if got.State != "done" || got.Counts["done"] != 4 || got.Counts["conflict"] != 1 {
		t.Fatalf("merge ended %s with %v", got.State, got.Counts)
	}
	for _, p := range []string{"Fotos (copia)/Fotos/2007/b.jpg", "Fotos (copia)/Fotos/2008/Praia/c.jpg", "Fotos/2009/d.jpg"} {
		if !w.exists("/disk", p) {
			t.Errorf("%s is not on the disk", p)
		}
	}
	if w.exists("/disk", "Fotos (copia)/2007/b.jpg") {
		t.Error("a file landed one level too high")
	}

	// The merge undoes: files back, and the folders it made removed.
	undo, items := w.plan("plan-undo", fmt.Sprintf(`{"action_id":%q}`, a.ID))
	wantItems(t, items,
		"rename planned Fotos (copia)/Fotos/2008/Praia/c.jpg -> Fotos/2008/Praia/c.jpg",
		"rmdir planned Fotos (copia)/Fotos/2008/Praia",
		"rmdir planned Fotos (copia)/Fotos/2008",
		"rename planned Fotos (copia)/Fotos/2007/b.jpg -> Fotos/2007/b.jpg")
	if got := w.run(undo.ID); got.State != "done" || got.Counts["done"] != 4 {
		t.Fatalf("undo ended %s with %v", got.State, got.Counts)
	}
	if w.exists("/disk", "Fotos (copia)/Fotos/2008") || !w.exists("/disk", "Fotos/2008/Praia/c.jpg") {
		t.Error("the undo did not restore the disk")
	}

	// Sides that cannot merge.
	w.refuse(400, domain.CodeInvalidRequest, "plan-merge",
		fmt.Sprintf(`{"left_id":%q,"right_id":%q,"from":"up"}`, copia, fotos))
	w.refuse(400, domain.CodeInvalidRequest, "plan-merge",
		fmt.Sprintf(`{"left_id":%q,"right_id":%q,"from":"left"}`, w.id("disk", "Fotos/2009/d.jpg"), fotos))
	w.refuse(404, domain.CodeNotFound, "plan-merge", fmt.Sprintf(`{"left_id":"999999","right_id":%q,"from":"left"}`, fotos))
}
