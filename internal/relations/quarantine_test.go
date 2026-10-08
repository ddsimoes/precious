package relations

import (
	"context"
	"slices"
	"strings"
	"testing"

	"precious/internal/domain"
)

// The quarantine is out of the relations (r4 design D2): the snapshot holds
// no folder, file, or archive at or below a source's .precious-quarantine,
// so a relate pass relates the copies outside it only, even with exact
// copies of a folder and of an archive inside it; Compare of a source's top
// lists no quarantined file, while a quarantined folder still compares with
// its own files.
func TestRelateAndCompareLeaveTheQuarantineOut(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for _, dir := range []string{"fotos", "fotos-b", quarantineName + "/1/1/fotos-c"} {
		w.file(dir+"/a.jpg", 1001, "a")
		w.file(dir+"/b.jpg", 1002, "b")
	}
	w.archive(wf{path: quarantineName + "/1/2/fotos.zip", size: 1500},
		mem("a.jpg", 1001, "a"), mem("b.jpg", 1002, "b"))
	w.file("pen:P/a.jpg", 1001, "a")

	s, rels := w.relate()
	for d := range s.parent {
		if isQuarantinePath(s.dirPath(int32(d))) {
			t.Errorf("the snapshot holds the quarantined folder %q", s.name(int32(d)))
		}
	}
	for _, line := range s.lines(rels, true) {
		if strings.Contains(line, quarantineName) {
			t.Errorf("relation %s has a quarantined side", line)
		}
	}
	wantShort(t, s, rels, "same fotos-b<-fotos", "inside pen:<-fotos")

	compared := func(left, right domain.Ref) []string {
		t.Helper()
		var out []string
		for _, b := range Buckets {
			res, err := Compare(context.Background(), w.st.Reader(), left, right, b, "", 1000)
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range res.Items {
				for _, p := range [][]byte{it.LeftPath, it.RightPath, it.TwinPath} {
					if p != nil {
						out = append(out, string(p))
					}
				}
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	top := domain.Ref{Entry: w.id("")}
	got := compared(top, w.ref("pen:P"))
	if want := []string{"a.jpg", "fotos-b/a.jpg", "fotos-b/b.jpg", "fotos/a.jpg", "fotos/b.jpg"}; !slices.Equal(got, want) {
		t.Errorf("Compare of the top with pen:P lists %q, want %q", got, want)
	}
	quarantined := w.ref(quarantineName + "/1/1/fotos-c")
	if got, want := compared(quarantined, w.ref("pen:P")), []string{"a.jpg", "b.jpg"}; !slices.Equal(got, want) {
		t.Errorf("Compare of the quarantined folder with pen:P lists %q, want %q", got, want)
	}
}

// The snapshot drops the quarantine folder's own row: an empty quarantine
// leaves the top as the only folder.
func TestSnapshotDropsTheQuarantineFolderRow(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.add(wf{path: quarantineName, kind: domain.EntryDirectory})
	w.file("x.bin", 10, "x")
	s := w.snapshot(false)
	var dirs []string
	for d := range s.parent {
		dirs = append(dirs, s.name(int32(d)))
	}
	if !slices.Equal(dirs, []string{""}) {
		t.Errorf("snapshot folders %q, want the top only", dirs)
	}
}
