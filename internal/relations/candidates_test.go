package relations

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
)

// Candidates (design D4): the folder pairs hashing checks first, on a
// provisional snapshot keyed by size.

func (w *world) candidates(src domain.SourceID) [][2]Range {
	w.t.Helper()
	w.seed()
	var out [][2]Range
	err := w.st.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = Candidates(context.Background(), tx, src)
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func folder(src domain.SourceID, p string) Range { return descendants(src, []byte(p)) }

// Copied folder of small files is checked: the provisional pass selects
// code and code-copy, not notes. code's sizes are shared only with
// code-copy, and notes' sizes only within notes, so notes' files have size
// peers (they are pending) but no candidate partner.
func TestCopiedFolderOfSmallFilesIsACandidate(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for i := range 200 {
		size := int64(3000 + i)
		w.add(wf{path: fmt.Sprintf("code/src/f%04d.go", i), size: size, state: domain.ContentPending})
		w.add(wf{path: fmt.Sprintf("code-copy/f%04d.go", i), size: size, state: domain.ContentPending})
		w.add(wf{path: fmt.Sprintf("notes/n%04d.txt", i), size: int64(5000 + i/2), state: domain.ContentPending})
	}
	// code/src holds nothing else, so code and code/src are one folder: the
	// pair names the outer one.
	want := [][2]Range{{folder("disk", "code"), folder("disk", "code-copy")}}
	if got := w.candidates("disk"); !slices.EqualFunc(got, want, rangePairEqual) {
		t.Fatalf("Candidates = %s, want %s", fmtPairs(got), fmtPairs(want))
	}
	if got := w.candidates("other"); len(got) != 0 {
		t.Errorf("an unknown source has candidates: %s", fmtPairs(got))
	}
	// The final snapshot treats the same pending files as gaps.
	if s, rels := w.relate(); len(rels) != 0 {
		t.Errorf("pending files matched in a final snapshot: %v", s.lines(rels, false))
	}
}

// A pair nested in a listed pair is not listed again; hashed large files
// key by content, small hashed ones by size.
func TestCandidatesListOuterPairs(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for i := range 4 {
		w.add(wf{path: fmt.Sprintf("p/q/f%d", i), size: int64(100 + i), state: domain.ContentPending})
		w.add(wf{path: fmt.Sprintf("z/f%d", i), size: int64(100 + i), content: fmt.Sprint("z", i)})
	}
	w.file("p/other/big.bin", 2<<20, "big-a")
	w.file("z/x/big.bin", 2<<20, "big-b") // same size, other content: no match
	w.file("p/other/u.bin", 7, "")
	got := w.candidates("disk")
	want := [][2]Range{{folder("disk", "p"), folder("disk", "z")}}
	if len(got) != 0 && slices.EqualFunc(got, want, rangePairEqual) {
		t.Fatalf("big files of other content made p a candidate: %s", fmtPairs(got))
	}
	want = [][2]Range{{folder("disk", "p/q"), folder("disk", "z")}}
	if !slices.EqualFunc(got, want, rangePairEqual) {
		t.Fatalf("Candidates = %s, want %s", fmtPairs(got), fmtPairs(want))
	}
}

// Pending zip members key by size, so a zip of small members whose sizes
// match a folder's small files is a candidate: its range is the archive
// file's own path. A pair with no side on the source is not listed.
func TestPendingMembersMakeCandidates(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var members []indextest.Member
	for i := range 4 {
		w.add(wf{path: fmt.Sprintf("usb:code/f%d.go", i), size: int64(3000 + i), state: domain.ContentPending})
		members = append(members, indextest.Member{Path: fmt.Sprintf("src/f%d.go", i), Size: int64(3000 + i),
			Content: indextest.Content{State: domain.ContentPending}})
	}
	w.archive(wf{path: "backup/code.zip", size: 9000}, members...)
	w.file("backup/old/notes.txt", 1_000_000, "")
	w.file("usb:music/x.mp3", 5_000_000, "") // usb's root holds more than code
	want := [][2]Range{{itself("disk", []byte("backup/code.zip")), folder("usb", "code")}}
	for _, src := range []domain.SourceID{"disk", "usb"} {
		if got := w.candidates(src); !slices.EqualFunc(got, want, rangePairEqual) {
			t.Fatalf("Candidates(%s) = %s, want %s", src, fmtPairs(got), fmtPairs(want))
		}
	}
	if !want[0][0].Contains([]byte("backup/code.zip")) || want[0][0].Contains([]byte("backup/code.zip.bak")) ||
		!want[0][1].Contains([]byte("code/f1.go")) || want[0][1].Contains([]byte("code")) ||
		want[0][1].Contains([]byte("code-old/x")) {
		t.Error("Range.Contains")
	}
	if s, rels := w.relate(); len(rels) != 0 {
		t.Errorf("pending members matched in a final snapshot: %v", s.lines(rels, false))
	}

	// The source root's range is every non-empty path.
	root := descendants("disk", nil)
	if !root.Contains([]byte("a")) || root.Contains(nil) {
		t.Error("root range")
	}
}

func rangePairEqual(x, y [2]Range) bool { return rangeEqual(x[0], y[0]) && rangeEqual(x[1], y[1]) }

func rangeEqual(x, y Range) bool {
	return x.Source == y.Source && string(x.From) == string(y.From) && (x.To == nil) == (y.To == nil) &&
		string(x.To) == string(y.To)
}

func fmtPairs(ps [][2]Range) string {
	out := ""
	for _, p := range ps {
		out += fmt.Sprintf(" {%s:%q..%q %s:%q..%q}", p[0].Source, p[0].From, p[0].To, p[1].Source, p[1].From, p[1].To)
	}
	return "[" + out + " ]"
}
