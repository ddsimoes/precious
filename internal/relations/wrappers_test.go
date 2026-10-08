package relations

import (
	"context"
	"slices"
	"testing"

	"precious/internal/domain"
)

// Wrappers names the folder Compare drops from one side, on the side it is
// dropped from, and nil for a pair with nothing dropped; the paths Compare
// lists are the side's paths with that folder cut.
func TestWrappersNameTheDroppedFolder(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("copia/Fotos/2007/a.jpg", 100, "a")
	w.file("fotos/2007/a.jpg", 100, "a")
	w.file("fotos/2007/b.jpg", 200, "b")
	w.file("fotos/2008/c.jpg", 300, "c")
	w.file("plain/2007/a.jpg", 100, "a")
	w.file("plain/top.txt", 10, "t")
	w.file("emule-0.47c/emule.exe", 1000, "e")
	w.archive(wf{path: "dl/emule.zip", size: 900}, mem("emule-0.47c/emule.exe", 1000, "e"))
	w.seed()
	ctx := context.Background()
	wraps := func(l, r string) (string, string, bool, bool) {
		t.Helper()
		lw, rw, err := Wrappers(ctx, w.st.Reader(), w.ref(l), w.ref(r))
		if err != nil {
			t.Fatalf("Wrappers(%s, %s): %v", l, r, err)
		}
		return string(lw), string(rw), lw != nil, rw != nil
	}
	for _, tc := range []struct {
		l, r     string
		lw, rw   string
		lok, rok bool
	}{
		{l: "copia", r: "fotos", lw: "Fotos", lok: true},
		{l: "fotos", r: "copia", rw: "Fotos", rok: true},
		{l: "fotos", r: "plain"},                                            // neither side has a single top folder
		{l: "dl/emule.zip", r: "emule-0.47c", lw: "emule-0.47c", lok: true}, // an archive side
	} {
		lw, rw, lok, rok := wraps(tc.l, tc.r)
		if lw != tc.lw || rw != tc.rw || lok != tc.lok || rok != tc.rok {
			t.Errorf("Wrappers(%s, %s) = %q (%v), %q (%v); want %q (%v), %q (%v)", tc.l, tc.r, lw, lok, rw, rok,
				tc.lw, tc.lok, tc.rw, tc.rok)
		}
	}

	// The paths Compare lists are those below the wrapper: copia's
	// Fotos/2007/a.jpg pairs with fotos' 2007/a.jpg, and fotos' other files
	// are only on the right, at the paths a merge joins to copia/Fotos.
	res, err := Compare(ctx, w.st.Reader(), w.ref("copia"), w.ref("fotos"), BucketOnlyRight, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemPaths(res.Items); !slices.Equal(got, []string{"2007/b.jpg", "2008/c.jpg"}) ||
		res.Summary[BucketIdentical].Files != 1 {
		t.Errorf("only_right %q, summary %+v", got, res.Summary)
	}

	// The sides Compare refuses, Wrappers refuses alike.
	if _, _, err := Wrappers(ctx, w.st.Reader(), w.ref("fotos"), w.ref("fotos/2007")); domain.CodeOf(err) != domain.CodeInvalidRequest {
		t.Errorf("a side inside the other: %v, want invalid_request", err)
	}
	if _, _, err := Wrappers(ctx, w.st.Reader(), w.ref("fotos"), domain.Ref{Entry: 999999}); domain.CodeOf(err) != domain.CodeNotFound {
		t.Errorf("an unknown side: %v, want not_found", err)
	}
}
