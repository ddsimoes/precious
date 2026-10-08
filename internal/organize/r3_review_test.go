package organize

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
)

// V2: an undo leaves a folder its merge made when a missing entry below it
// carries the owner's intent. The file moved into the new folder is tagged,
// then deleted on the disk and marked missing by a scan; the undo's rmdir
// ends not_empty before anything is asked of the disk, the folder stays on
// the disk and in the index, nothing needs the owner's check, and the
// source can still be planned on.
func TestReviewUndoKeepsAFolderAboveAMissingTaggedFile(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	root := w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		a := root.Dir("A")
		a.File("b.jpg", 4, mtime).Content([]byte("same"))
		a.Dir("Novo").File("x.jpg", 5, mtime).Content([]byte("x.jpg"))
		root.Dir("B").File("b.jpg", 4, mtime).Content([]byte("same"))
	})
	merge, items := w.plan("plan-merge", fmt.Sprintf(`{"left_id":%q,"right_id":%q,"from":"left"}`,
		w.id("disk", "A"), w.id("disk", "B")))
	wantItems(t, items, "mkdir planned -> B/Novo", "rename planned A/Novo/x.jpg -> B/Novo/x.jpg")
	if got := w.run(merge.ID); got.State != "done" || got.Counts["done"] != 2 {
		t.Fatalf("merge ended %s with %v", got.State, got.Counts)
	}
	x := w.id("disk", "B/Novo/x.jpg")
	w.tag("familia", x)
	root.Child("B").Child("Novo").Remove("x.jpg")
	w.scan("disk")
	if _, state := w.pathOf(x); state != "missing" {
		t.Fatalf("B/Novo/x.jpg is %s after the rescan", state)
	}

	undo, items := w.plan("plan-undo", fmt.Sprintf(`{"action_id":%q}`, merge.ID))
	wantItems(t, items, "rename refused missing B/Novo/x.jpg -> A/Novo/x.jpg", "rmdir planned B/Novo")
	got := w.run(undo.ID)
	if got.State != "done" || got.Counts["not_empty"] != 1 || got.Counts["manual_recovery"] != 0 {
		t.Fatalf("undo ended %s with %v", got.State, got.Counts)
	}
	if out := outcomes(w.items(undo.ID, "")); out["B/Novo"] != "not_empty" {
		t.Errorf("undo items %v, want B/Novo not_empty", out)
	}
	if !w.exists("/disk", "B/Novo") {
		t.Error("B/Novo was removed from the disk")
	}
	if p, state := w.pathOf(w.id("disk", "B/Novo")); p != "B/Novo" || state != "present" {
		t.Errorf("the index holds B/Novo at %q, %s", p, state)
	}
	if p, state := w.pathOf(x); p != "B/Novo/x.jpg" || state != "missing" {
		t.Errorf("the index holds x.jpg at %q, %s", p, state)
	}
	if n := w.count(`SELECT count(*) FROM action_items WHERE state = 'manual_recovery'`); n != 0 {
		t.Errorf("%d items need the owner's check", n)
	}
	w.plan("plan-create-folder", fmt.Sprintf(`{"parent_id":%q,"name":"Outro"}`, w.id("disk", "B")))
}

// nameBody is a plan-rename (field entry_id) or plan-create-folder (parent_id)
// body naming name.
func nameBody(t *testing.T, field, id, name string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{field: id, "name": name})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// V3: a FAT-family or NTFS disk cannot hold " * : < > ? \ |, a control
// character, or a trailing space or dot; plan-rename and plan-create-folder
// refuse such a name there with 400 invalid_request and plan nothing, while
// another filesystem plans the same names.
func TestReviewNamesTheFilesystemCannotHold(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	build := func(root *synthfs.Node) { root.Dir("Docs").File("recibos.pdf", 3, mtime) }
	w.disk("usb", "/usb", fat, build)
	w.disk("disk", "/disk", posix, build)
	names := []string{"Recibos: 2023", "Novo.", "Novo ", `a"b`, "a*b", "a<b", "a>b", "a?b", `a\b`, "a|b", "a\tb", "a\x7fb"}
	for _, fsType := range []string{"vfat", "exfat", "ntfs3", "ntfs", "fuseblk"} {
		w.exec(`UPDATE sources SET fs_type = ? WHERE id = 'usb'`, fsType)
		for _, name := range names {
			w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-rename",
				nameBody(t, "entry_id", w.id("usb", "Docs/recibos.pdf"), name))
			w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-create-folder",
				nameBody(t, "parent_id", w.id("usb", "Docs"), name))
		}
	}
	if n := w.count(`SELECT count(*) FROM actions WHERE source_id = 'usb'`); n != 0 {
		t.Errorf("%d actions planned on the FAT-family disk", n)
	}
	w.exec(`UPDATE sources SET fs_type = 'exfat' WHERE id = 'usb'`)
	w.plan("plan-rename", nameBody(t, "entry_id", w.id("usb", "Docs/recibos.pdf"), "Recibos 2023.pdf"))
	for _, name := range names {
		for _, body := range []struct{ cmd, field, parent string }{
			{"plan-rename", "entry_id", "Docs/recibos.pdf"}, {"plan-create-folder", "parent_id", "Docs"}} {
			_, items := w.plan(body.cmd, nameBody(t, body.field, w.id("disk", body.parent), name))
			if len(items) != 1 || items[0].State != "planned" {
				t.Errorf("%s %q on ext4: %v", body.cmd, name, summaries(items))
			}
		}
	}
}

// V4: a merge makes only the folders a planned move goes into. When every
// file bound for a missing folder is refused (would_lose_keep), no mkdir is
// planned and nothing runs; in a mixed merge only the needed mkdirs stay,
// numbered without gaps, and the moves name them by their new seq.
func TestReviewMergeMakesOnlyFoldersThatReceiveFiles(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		a := root.Dir("A")
		a.File("b.jpg", 4, mtime).Content([]byte("same"))
		a.Dir("Sub").File("x.jpg", 1, mtime).Content([]byte("x"))
		mid := a.Dir("Mid")
		mid.Dir("Keep").File("k.jpg", 1, mtime).Content([]byte("k"))
		mid.Dir("Free").File("f.jpg", 1, mtime).Content([]byte("f"))
		root.Dir("B").File("b.jpg", 4, mtime).Content([]byte("same"))
	})
	w.decide(w.id("disk", "A"), "keep")
	body := fmt.Sprintf(`{"left_id":%q,"right_id":%q,"from":"left"}`, w.id("disk", "A"), w.id("disk", "B"))

	refused, items := w.plan("plan-merge", body)
	wantItems(t, items,
		"rename refused would_lose_keep A/Mid/Free/f.jpg -> B/Mid/Free/f.jpg",
		"rename refused would_lose_keep A/Mid/Keep/k.jpg -> B/Mid/Keep/k.jpg",
		"rename refused would_lose_keep A/Sub/x.jpg -> B/Sub/x.jpg")
	w.refuse(http.StatusConflict, domain.CodeActionNotRunnable, "run-action", fmt.Sprintf(`{"action_id":%q}`, refused.ID))
	w.idle()

	w.decide(w.id("disk", "A/Mid/Free"), "later")
	mixed, items := w.plan("plan-merge", body)
	wantItems(t, items,
		"mkdir planned -> B/Mid",
		"mkdir planned -> B/Mid/Free",
		"rename planned A/Mid/Free/f.jpg -> B/Mid/Free/f.jpg",
		"rename refused would_lose_keep A/Mid/Keep/k.jpg -> B/Mid/Keep/k.jpg",
		"rename refused would_lose_keep A/Sub/x.jpg -> B/Sub/x.jpg")
	var seqs []string
	w.readTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT seq, COALESCE(to_dir_seq, 0), to_parent IS NOT NULL FROM action_items
			WHERE action_id = ? ORDER BY seq`, mixed.ID)
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
			seqs = append(seqs, fmt.Sprintf("%d:%d:%v", seq, dirSeq, parent))
		}
		return rows.Err()
	})
	if got := strings.Join(seqs, " "); got != "1:0:true 2:1:false 3:2:false 4:0:false 5:0:false" {
		t.Errorf("destinations by seq: %s", got)
	}
	if got := w.run(mixed.ID); got.State != "done" || got.Counts["done"] != 3 || got.Counts["refused"] != 2 {
		t.Fatalf("merge ended %s with %v", got.State, got.Counts)
	}
	if !w.exists("/disk", "B/Mid/Free/f.jpg") {
		t.Error("B/Mid/Free/f.jpg is not on the disk")
	}
	for _, p := range []string{"B/Sub", "B/Mid/Keep"} {
		if w.exists("/disk", p) {
			t.Errorf("%s was made with nothing to hold", p)
		}
	}
}
