package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"precious/internal/clock"
	"precious/internal/corpus"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/rules"
	"precious/internal/web/clientip"
)

// corpusSpreadsheet is the user file inside the Microsoft Office group.
const corpusSpreadsheet = corpusPrograms + "/Microsoft Office/OFFICE11/Meu orcamento casamento.xls"

// TestR1_3SearchFindsFilesAnywhere searches the scanned corpus through
// /api/search by name, extension, size, year, and tag, alone and combined,
// and checks each search lists exactly the ground truth's matches, files
// inside groups such as Microsoft Office/OFFICE11 included, with their
// count (R1.3). Years come from the modification times on the synthfs
// disk; sizes are a file's size and a folder's sum.
func TestR1_3SearchFindsFilesAnywhere(t *testing.T) {
	e, _, root, tr := scanCorpus(t)
	office := corpusPrograms + "/Microsoft Office"

	idOf := func(p string) domain.EntryID {
		t.Helper()
		var id int64
		if err := e.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = 'corpus' AND path = ?`,
			[]byte(p)).Scan(&id); err != nil {
			t.Fatalf("%q: %v", p, err)
		}
		return domain.EntryID(id)
	}
	tagged := []string{office, "Fotos/2004/Natal"}
	tag := createTag(t, e, "orcamento", idOf(tagged[0]), idOf(tagged[1]))

	year := func(p string) int {
		n := root
		if p != "" {
			for _, name := range strings.Split(p, "/") {
				if n = n.Child(name); n == nil {
					t.Fatalf("%q is not on the synthfs disk", p)
				}
			}
		}
		return n.Info().ModTime.UTC().Year()
	}
	bytesOf := func(p string, g corpus.Entry) int64 {
		switch {
		case g.Kind == domain.EntryDirectory:
			return tr.totals[p][1]
		case g.Size != nil:
			return *g.Size
		}
		return 0
	}
	name := func(p string) string {
		return strings.ToLower(domain.DisplayName([]byte(p[strings.LastIndexByte(p, '/')+1:])))
	}
	inTagged := func(p string) bool {
		for _, f := range tagged {
			if p == f || strings.HasPrefix(p, f+"/") {
				return true
			}
		}
		return false
	}

	cases := []struct {
		query string
		match func(p string, g corpus.Entry) bool
		must  []string
	}{
		{"name=natal", func(p string, _ corpus.Entry) bool { return strings.Contains(name(p), "natal") },
			[]string{"Fotos/2004/Natal"}},
		{"name=ORCAMENTO", func(p string, _ corpus.Entry) bool { return strings.Contains(name(p), "orcamento") },
			[]string{corpusSpreadsheet}},
		{"ext=xls", func(p string, g corpus.Entry) bool { return fileExt(g, p) == "xls" },
			[]string{corpusSpreadsheet}},
		{"ext=.DOC&ext=jpg", func(p string, g corpus.Entry) bool { return fileExt(g, p) == "doc" || fileExt(g, p) == "jpg" }, nil},
		{"min_size=30000&max_size=40000", func(p string, g corpus.Entry) bool {
			b := bytesOf(p, g)
			return b >= 30000 && b <= 40000
		}, []string{corpusSpreadsheet}},
		{"year_from=2004&year_to=2004", func(p string, _ corpus.Entry) bool { return year(p) == 2004 },
			[]string{corpusSpreadsheet}},
		{"year_from=2005", func(p string, _ corpus.Entry) bool { return year(p) >= 2005 }, nil},
		{"tag=" + fmt.Sprint(tag), func(p string, _ corpus.Entry) bool { return inTagged(p) },
			[]string{office, corpusSpreadsheet}},
		{"tag=" + fmt.Sprint(tag) + "&ext=xls&year_from=2004&year_to=2004&min_size=1", func(p string, g corpus.Entry) bool {
			return inTagged(p) && fileExt(g, p) == "xls" && year(p) == 2004 && bytesOf(p, g) >= 1
		}, []string{corpusSpreadsheet}},
		{"name=orcamento&within=" + idOf(office).String(), func(p string, _ corpus.Entry) bool {
			return strings.HasPrefix(p, office+"/") && strings.Contains(name(p), "orcamento")
		}, []string{corpusSpreadsheet}},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			var want []string
			for p, g := range tr.entries {
				if c.match(p, g) {
					want = append(want, p)
				}
			}
			slices.Sort(want)
			for _, m := range c.must {
				if !slices.Contains(want, m) {
					t.Fatalf("the ground truth's matches miss %q; the case is wrong", m)
				}
			}
			if len(want) == 0 {
				t.Fatal("the ground truth has no match; the case is wrong")
			}

			base := "/api/search?source=corpus&" + c.query
			rows, _ := e.pages(t, base, 1000)
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = string(r.PathB64)
				if r.SourceID != "corpus" || r.Path != tr.entries[got[i]].Path && got[i] != "" {
					t.Errorf("row %+v", r)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("found %d entries, ground truth %d\nmissing %q\nextra %q",
					len(got), len(want), difference(want, got), difference(got, want))
			}

			var count struct {
				Count any `json:"count"`
			}
			e.get(t, base+"&count=only", 200, &count)
			if fmt.Sprint(count.Count) != fmt.Sprint(len(want)) {
				t.Errorf("count %v, want %d", count.Count, len(want))
			}
		})
	}
}

// createTag creates the tag name and gives it to ids, through the
// decisions service as the create-tag and set-tags commands do.
func createTag(t *testing.T, e *env, name string, ids ...domain.EntryID) int64 {
	t.Helper()
	svc := decisions.New(clock.Real{}, rules.Default(), nil)
	ctx := clientip.With(context.Background(), clientip.Info{Addr: netip.MustParseAddr("192.0.2.7"), Scheme: "https"})
	var tag decisions.Tag
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if tag, err = svc.CreateTag(ctx, tx, name); err != nil {
			return err
		}
		_, err = svc.SetTags(ctx, tx, decisions.SetTags{EntryIDs: ids, Add: []int64{tag.ID}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tag.ID
}

// fileExt is a file's extension as the index stores it: the ASCII-lowercased
// text after the last '.' of its name, "" for a name without one or whose
// only '.' is its first byte, and "" for anything but a file.
func fileExt(g corpus.Entry, p string) string {
	if g.Kind != domain.EntryFile {
		return ""
	}
	n := p[strings.LastIndexByte(p, '/')+1:]
	i := strings.LastIndexByte(n, '.')
	if i <= 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range []byte(n[i+1:]) {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// difference lists the elements of a not in b, both sorted, displayed.
func difference(a, b []string) []string {
	var out []string
	for _, s := range a {
		if _, ok := slices.BinarySearch(b, s); !ok {
			out = append(out, domain.DisplayName([]byte(s)))
		}
	}
	return out
}
