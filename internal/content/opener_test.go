package content

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// r5 task 1.6: the exported Opener walks each folder once for files that
// share it, answers OpenAt's errors, and closes the folders it opened.
func TestOpener(t *testing.T) {
	sfs := synthfs.New()
	root := sfs.Root("/mnt/fotos")
	mtime := time.Date(2010, 7, 17, 10, 0, 0, 0, time.UTC)
	viagem := root.Dir("Viagens").Dir("2010-07 Bahia")
	viagem.File("IMG_0101.JPG", 10, mtime)
	viagem.File("IMG_0102.JPG", 20, mtime)
	viagem.File("IMG_0103.JPG", 30, mtime).Unreadable()
	rec := instrument.Wrap(sfs)
	dir, err := rec.OpenRoot("/mnt/fotos")
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	rowOf := func(n *synthfs.Node, path string) Row {
		info := n.Info()
		return Row{Path: []byte(path), Size: info.Size, MtimeNs: sql.NullInt64{Int64: info.ModTime.UnixNano(), Valid: true},
			CtimeNs: sql.NullInt64{Int64: info.Ctime.UnixNano(), Valid: true},
			Ino:     sql.NullInt64{Int64: int64(info.Ino), Valid: true}}
	}
	const folder = "Viagens/2010-07 Bahia/"
	rec.Reset()
	o := NewOpener(dir, nil)
	for _, name := range []string{"IMG_0101.JPG", "IMG_0102.JPG"} {
		f, info, err := o.Open(rowOf(viagem.Child(name), folder+name), posix)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		if info.Ino != viagem.Child(name).Info().Ino {
			t.Errorf("%s: opened inode %d", name, info.Ino)
		}
		f.Close()
	}
	if n := rec.Count(instrument.OpOpenDir); n != 2 {
		t.Errorf("%d folders opened for two files in one folder, want 2", n)
	}

	// A file whose size changed since its row: invalid_entry_state, as
	// OpenAt answers.
	stale := rowOf(viagem.Child("IMG_0101.JPG"), folder+"IMG_0101.JPG")
	stale.Size++
	if _, _, err := o.Open(stale, posix); domain.CodeOf(err) != domain.CodeInvalidEntryState {
		t.Errorf("a changed file: %v, want invalid_entry_state", err)
	}
	_, _, want := OpenAt(dir, stale, posix)
	if _, _, err := o.Open(stale, posix); err == nil || err.Error() != want.Error() {
		t.Errorf("a changed file: %v, OpenAt answers %v", err, want)
	}
	// A gone file keeps its outcome.
	gone := Row{Path: []byte(folder + "IMG_0109.JPG"), Size: 1, MtimeNs: sql.NullInt64{Valid: true}}
	_, _, err = o.Open(gone, posix)
	if oc, _ := fsaccess.OutcomeOf(err); domain.CodeOf(err) != domain.CodeInvalidEntryState || oc != domain.OutcomeAbsent {
		t.Errorf("a gone file: %v, want invalid_entry_state with outcome absent", err)
	}
	// An unreadable file carries its outcome.
	_, _, err = o.Open(rowOf(viagem.Child("IMG_0103.JPG"), folder+"IMG_0103.JPG"), posix)
	if oc, _ := fsaccess.OutcomeOf(err); domain.CodeOf(err) != domain.CodeInvalidEntryState ||
		oc != domain.OutcomeUnreadable || !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("an unreadable file: %v, want invalid_entry_state with outcome unreadable", err)
	}
	// Close drops the chain: the next open walks from the root again.
	o.Close()
	rec.Reset()
	f, _, err := o.Open(rowOf(viagem.Child("IMG_0102.JPG"), folder+"IMG_0102.JPG"), posix)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	o.Close()
	if n := rec.Count(instrument.OpOpenDir); n != 2 {
		t.Errorf("%d folders opened after Close, want 2", n)
	}
}
