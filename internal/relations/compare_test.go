package relations

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"precious/internal/domain"
)

// Tasks 4.4 and 4.5: Compare (design D11).

// bucket pages through one bucket, 7 items at a time, and returns its
// items and summary count.
func (c *corpusWorld) bucket(t *testing.T, left, right domain.Ref, b Bucket) ([]CompareItem, Count) {
	t.Helper()
	var items []CompareItem
	cursor := ""
	var sum Count
	for {
		res, err := Compare(context.Background(), c.st.Reader(), left, right, b, cursor, 7)
		if err != nil {
			t.Fatal(err)
		}
		sum = res.Summary[b]
		items = append(items, res.Items...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if int64(len(items)) != sum.Files {
		t.Errorf("%s: %d items paged, summary %d", b, len(items), sum.Files)
	}
	return items, sum
}

func itemPaths(items []CompareItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = string(it.Path)
	}
	return out
}

// R2.2: Fotos against Fotos - Copia: only on the right
// 2006/Praia/DSC_editada.JPG, only on the left the three photos missing
// from the copy, identical every other photo, and nothing unchecked.
func TestR2_2FotosAgainstItsCopy(t *testing.T) {
	t.Parallel()
	c := seedCorpus(t)
	left, right := c.ref("Fotos"), c.ref("Fotos - Copia")
	var rel *struct{ aOnly, bOnly []string }
	for _, r := range c.truth.Relations {
		if raw(t, r.A) == "Fotos - Copia" && raw(t, r.B) == "Fotos" {
			rel = &struct{ aOnly, bOnly []string }{}
			for _, p := range r.AOnly {
				rel.aOnly = append(rel.aOnly, strings.TrimPrefix(raw(t, p), "Fotos - Copia/"))
			}
			for _, p := range r.BOnly {
				rel.bOnly = append(rel.bOnly, strings.TrimPrefix(raw(t, p), "Fotos/"))
			}
		}
	}
	if rel == nil || len(rel.bOnly) != 3 || !slices.Equal(rel.aOnly, []string{"2006/Praia/DSC_editada.JPG"}) {
		t.Fatalf("ground truth relation Fotos - Copia/Fotos: %+v", rel)
	}
	onlyR, _ := c.bucket(t, left, right, BucketOnlyRight)
	if got := itemPaths(onlyR); !slices.Equal(got, rel.aOnly) {
		t.Errorf("only on the right %q, want %q", got, rel.aOnly)
	}
	onlyL, _ := c.bucket(t, left, right, BucketOnlyLeft)
	slices.Sort(rel.bOnly)
	if got := itemPaths(onlyL); !slices.Equal(got, rel.bOnly) {
		t.Errorf("only on the left %q, want %q", got, rel.bOnly)
	}
	for _, b := range []Bucket{BucketUnchecked, BucketDifferent} {
		if items, _ := c.bucket(t, left, right, b); len(items) != 0 {
			t.Errorf("%s: %q", b, itemPaths(items))
		}
	}
	// Identical holds every other file of both sides.
	ident, _ := c.bucket(t, left, right, BucketIdentical)
	var lf, rf int
	for _, it := range ident {
		if it.Left != nil {
			lf++
		}
		if it.Right != nil {
			rf++
		}
	}
	files := func(p string) int {
		return int(count(t, c.st, `SELECT total_files FROM entries WHERE id = ?`, int64(c.seeded.ID(p))))
	}
	if lf != files("Fotos")-3 || rf != files("Fotos - Copia")-1 {
		t.Errorf("identical: %d left and %d right files, want %d and %d", lf, rf, files("Fotos")-3, files("Fotos - Copia")-1)
	}
}

// The zip against Fotos/2005: every member identical to a file of
// Fotos/2005, nothing only on either side.
func TestCompareZipAgainstFotos2005(t *testing.T) {
	t.Parallel()
	c := seedCorpus(t)
	left, right := c.ref("Downloads/fotos_2005_do_pendrive.zip"), c.ref("Fotos/2005")
	res, err := Compare(context.Background(), c.st.Reader(), left, right, BucketIdentical, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	members := c.arcs["Downloads/fotos_2005_do_pendrive.zip"]
	for _, it := range res.Items {
		if it.Left == nil || it.Right == nil || it.Left.Entry != members.ID || it.Left.Member == 0 {
			t.Errorf("identical item %+v", it)
		}
	}
	if n := res.Summary[BucketIdentical].Files; n == 0 || n != int64(len(res.Items)) {
		t.Errorf("identical %d", n)
	}
	for _, b := range []Bucket{BucketOnlyLeft, BucketOnlyRight, BucketUnchecked, BucketDifferent} {
		if n := res.Summary[b]; n != (Count{}) {
			t.Errorf("%s = %+v", b, n)
		}
	}
}

// site_antigo against its copy: contato.php has the same path and other
// content; every other file is identical.
func TestCompareSameNameDifferentContent(t *testing.T) {
	t.Parallel()
	c := seedCorpus(t)
	left, right := c.ref("Projetos/site_antigo"), c.ref("Projetos/site_antigo_copia")
	diff, _ := c.bucket(t, left, right, BucketDifferent)
	if got := itemPaths(diff); !slices.Equal(got, []string{"contato.php"}) || diff[0].Left == nil || diff[0].Right == nil {
		t.Errorf("different %q", got)
	}
	res, err := Compare(context.Background(), c.st.Reader(), left, right, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	total := count(t, c.st, `SELECT total_files FROM entries WHERE id = ?`, int64(c.seeded.ID("Projetos/site_antigo")))
	if s := res.Summary; s[BucketIdentical].Files != total-1 || s[BucketOnlyLeft] != (Count{}) ||
		s[BucketOnlyRight] != (Count{}) || s[BucketUnchecked] != (Count{}) || res.Items != nil {
		t.Errorf("summary %+v, want %d identical and nothing else", s, total-1)
	}
	// The tar.gz against the folder lines up its wrapper-free members.
	res, err = Compare(context.Background(), c.st.Reader(), c.ref("Projetos/site_antigo_2006.tar.gz"), left, BucketIdentical, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if s := res.Summary; s[BucketIdentical].Files != total || s[BucketDifferent] != (Count{}) {
		t.Errorf("tar.gz summary %+v", s)
	}
}

// A gap is unchecked when its size occurs on the other side, and so is a
// checked file whose size occurs there only among gaps; sizes absent from
// the other side prove "only here" without a read.
func TestCompareGapIsUnchecked(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.add(wf{path: "l/a.txt", size: 100, state: domain.ContentPending})
	w.file("l/same.txt", 300, "s")
	w.add(wf{path: "l/alone.bin", size: 777, state: domain.ContentChanged})
	w.add(wf{path: "l/w.txt", size: 50, state: domain.ContentUnreadable})
	w.file("l/name.txt", 60, "n1")
	w.file("l/sized.bin", 400, "x1")
	w.file("r/b.txt", 100, "b")
	w.file("r/same-renamed.txt", 300, "s")
	w.add(wf{path: "r/name.txt", size: 60, state: domain.ContentPending})
	w.file("r/sized.bin", 400, "x2")
	w.file("r/u.txt", 9, "")
	w.seed()
	res, err := Compare(context.Background(), w.st.Reader(), w.ref("l"), w.ref("r"), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[Bucket]Count{
		BucketUnchecked: {Files: 4, Bytes: 100 + 100 + 60 + 60}, // a.txt, b.txt, and name.txt on both sides
		BucketIdentical: {Files: 1, Bytes: 300},
		BucketDifferent: {Files: 1, Bytes: 400}, // sized.bin: two checked contents
		BucketOnlyLeft:  {Files: 2, Bytes: 777 + 50},
		BucketOnlyRight: {Files: 1, Bytes: 9},
	}
	for _, b := range Buckets {
		if res.Summary[b] != want[b] {
			t.Errorf("%s = %+v, want %+v", b, res.Summary[b], want[b])
		}
	}
	unchecked, err := Compare(context.Background(), w.st.Reader(), w.ref("l"), w.ref("r"), BucketUnchecked, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemPaths(unchecked.Items); !slices.Equal(got, []string{"a.txt", "b.txt"}) || unchecked.NextCursor != "2" {
		t.Errorf("unchecked page %q next %q", got, unchecked.NextCursor)
	}
}

// When one side holds a single top folder, it is dropped from the paths
// when that lines them up with the other side.
func TestCompareDropsAWrapperFolder(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("emule-0.47c/emule.exe", 1000, "e")
	w.file("emule-0.47c/config/a.ini", 20, "a")
	w.file("emule-0.47c/config/b.ini", 30, "b1")
	w.archive(wf{path: "dl/emule.zip", size: 900},
		mem("emule-0.47c/emule.exe", 1000, "e"), mem("emule-0.47c/config/a.ini", 20, "a"),
		mem("emule-0.47c/config/b.ini", 31, "b2"))
	w.seed()
	res, err := Compare(context.Background(), w.st.Reader(), w.ref("dl/emule.zip"), w.ref("emule-0.47c"), BucketDifferent, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemPaths(res.Items); !slices.Equal(got, []string{"config/b.ini"}) || res.Summary[BucketIdentical].Files != 2 {
		t.Errorf("different %q, summary %+v", got, res.Summary)
	}
	// A member folder side.
	res, err = Compare(context.Background(), w.st.Reader(), w.ref("dl/emule.zip!emule-0.47c/config"), w.ref("emule-0.47c/config"),
		BucketIdentical, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemPaths(res.Items); !slices.Equal(got, []string{"a.ini"}) {
		t.Errorf("identical %q", got)
	}
}

// Sides: a folder against its subfolder, a file, an archive against its
// member folder, and an unknown ref.
func TestCompareRefusesContainmentAndFiles(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("fotos/2005/a.jpg", 100, "a")
	w.file("fotos/2006/b.jpg", 200, "b")
	w.file("other/c.jpg", 300, "")
	w.archive(wf{path: "other/x.zip", size: 90}, mem("d/a.jpg", 100, "a"))
	w.archive(wf{path: "other/y.zip", size: 90}, mem("d/b.jpg", 200, "b"))
	w.seed()
	code := func(l, r domain.Ref) domain.ErrorCode {
		_, err := Compare(context.Background(), w.st.Reader(), l, r, "", "", 0)
		var de *domain.Error
		if errors.As(err, &de) {
			return de.Code
		}
		if err != nil {
			return domain.ErrorCode(err.Error())
		}
		return ""
	}
	for _, tc := range []struct {
		l, r string
		want domain.ErrorCode
	}{
		{"fotos", "fotos/2005", domain.CodeInvalidRequest},
		{"fotos/2005", "fotos", domain.CodeInvalidRequest},
		{"fotos", "fotos", domain.CodeInvalidRequest},
		{"other", "other/x.zip", domain.CodeInvalidRequest},
		{"other/x.zip", "other/x.zip!d", domain.CodeInvalidRequest},
		{"other", "other/y.zip!d", domain.CodeInvalidRequest},
		{"fotos", "other/c.jpg", domain.CodeInvalidRequest},
		{"fotos", "fotos/2005/a.jpg", domain.CodeInvalidRequest},
		{"fotos/2005", "fotos/2006", ""},
		{"other/x.zip", "other/y.zip", ""},
		{"other/x.zip!d", "fotos/2005", ""},
		{"other/x.zip!d", "other/y.zip!d", ""},
	} {
		if got := code(w.ref(tc.l), w.ref(tc.r)); got != tc.want {
			t.Errorf("Compare(%s, %s) = %q, want %q", tc.l, tc.r, got, tc.want)
		}
	}
	if got := code(domain.Ref{Entry: 99999}, w.ref("fotos")); got != domain.CodeNotFound {
		t.Errorf("unknown entry: %q", got)
	}
	if got := code(domain.Ref{Member: 99999}, w.ref("fotos")); got != domain.CodeNotFound {
		t.Errorf("unknown member: %q", got)
	}
	if got := code(w.ref("other/x.zip!d/a.jpg"), w.ref("fotos")); got != domain.CodeInvalidRequest {
		t.Errorf("file member: %q", got)
	}
	_, err := Compare(context.Background(), w.st.Reader(), w.ref("fotos/2005"), w.ref("fotos/2006"), "nope", "", 0)
	_, err2 := Compare(context.Background(), w.st.Reader(), w.ref("fotos/2005"), w.ref("fotos/2006"), BucketIdentical, "x", 0)
	for _, e := range []error{err, err2} {
		var de *domain.Error
		if !errors.As(e, &de) || de.Code != domain.CodeInvalidRequest {
			t.Errorf("bad bucket or cursor: %v", e)
		}
	}
}
