package content

import (
	"context"
	"slices"
	"sync"
	"testing"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/relations"
	"precious/internal/store"
)

// A second hashing run with no change on disk opens no file, archives
// included (spec "A second run reads nothing", "An unchanged archive is not
// re-read").
func TestSecondRunOpensNoFile(t *testing.T) {
	e := newEnv(t)
	buildCorpus(t, e)
	e.hash("corpus")
	if e.count(`SELECT count(*) FROM file_content WHERE state = 'hashed'`) == 0 {
		t.Fatal("the first run hashed nothing")
	}
	e.rec.Reset()
	e.hash("corpus")
	if got := e.opened(); len(got) != 0 {
		t.Errorf("the second run opened %d files: %q", len(got), got)
	}
	if n := e.rec.Count(instrument.OpReadAt); n != 0 {
		t.Errorf("the second run read %d times", n)
	}
}

// Large files: different samples are told apart reading at most 192 KiB
// each; equal samples with different content are read in full and are not
// one group; identical files are one group.
func TestLargeFilesBySamples(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	files := corpus.AddLargeFiles(root.Dir("grandes"), fileTime)
	e.scan("fotos")
	e.rec.Reset()
	e.hash("fotos")
	digests := map[string][]string{}
	for _, lf := range files {
		p := "grandes/" + lf.Name
		switch lf.Case {
		case corpus.LargeDifferentSamples:
			if n := e.readOf(p); n > Samples*SampleBytes {
				t.Errorf("%s: read %d bytes, want at most %d", p, n, Samples*SampleBytes)
			}
			if s := e.state("fotos", p); s != domain.ContentSampled {
				t.Errorf("%s is %s, want sampled", p, s)
			}
		default:
			if d := e.digest("fotos", p); d != lf.SHA256 {
				t.Errorf("%s: digest %s, want %s", p, d, lf.SHA256)
			}
			digests[lf.Case] = append(digests[lf.Case], e.digest("fotos", p))
		}
	}
	if d := digests[corpus.LargeEqualSamples]; len(d) != 2 || d[0] == d[1] {
		t.Errorf("equal samples, different content: digests %q", d)
	}
	if d := digests[corpus.LargeIdentical]; len(d) != 2 || d[0] != d[1] {
		t.Errorf("identical files: digests %q", d)
	}
	if c := e.coverage("fotos"); c.CheckedFiles != c.CandidateFiles || c.CandidateFiles != 6 {
		t.Errorf("coverage %+v; want 6 candidates, all checked", c)
	}
}

// pairOf adds two files of size under dir, with different content.
func pairOf(dir *synthfs.Node, size int64, a, b string) (*synthfs.Node, *synthfs.Node) {
	return dir.File(a, size, fileTime), dir.File(b, size, fileTime)
}

// onRead runs fn once, before the first read of the file at path.
func (e *env) onRead(path string, fn func()) {
	var once sync.Once
	e.rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpReadAt && string(joinPath(c.Path)) == path {
			once.Do(fn)
		}
	})
	e.t.Cleanup(func() { e.rec.SetBeforeCall(nil) })
}

// A file that grows while it is read gets no digest and counts as not
// checked; hashing goes on with the next file.
func TestFileChangingDuringReadGetsNoDigest(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	f, _ := pairOf(root, 3<<20, "a.avi", "b.avi")
	e.scan("fotos")
	e.onRead("a.avi", func() { f.Size(3<<20 + 10) })
	e.hash("fotos")
	if s, d := e.state("fotos", "a.avi"), e.digest("fotos", "a.avi"); s != domain.ContentChanged || d != "" {
		t.Errorf("a.avi: %s %q; want changed without digest", s, d)
	}
	if s := e.state("fotos", "b.avi"); s != domain.ContentHashed {
		t.Errorf("b.avi is %s, want hashed", s)
	}
	if c := e.coverage("fotos"); c.UncheckedFiles != 1 || c.CheckedFiles != 1 {
		t.Errorf("coverage %+v; want one checked and one not", c)
	}
}

// A file that cannot be opened is counted as unreadable, and coverage stays
// below 100%.
func TestUnreadableFileCounted(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	f, _ := pairOf(root, 5000, "a.doc", "b.doc")
	f.Unreadable()
	e.scan("fotos")
	e.hash("fotos")
	if s := e.state("fotos", "a.doc"); s != domain.ContentUnreadable {
		t.Errorf("a.doc is %s, want unreadable", s)
	}
	c := e.coverage("fotos")
	if c.UnreadableFiles != 1 || c.UnreadableBytes != 5000 || c.CheckedBytes >= c.CandidateBytes {
		t.Errorf("coverage %+v; want one unreadable file and less than everything checked", c)
	}
}

// A rescan that commits while a file is read drops the read's result, and
// the next hashing run reads the file again (I9).
func TestRescanDuringReadDropsResult(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	f, _ := pairOf(root, 2<<20, "a.mov", "b.mov")
	e.scan("fotos")
	e.onRead("a.mov", func() {
		f.Ctime(fileTime.Add(1e9)) // a chmod: same bytes, new change time
		e.scan("fotos")
	})
	// The rescan's deletion drifts coverage until the next plan repairs it.
	if err := e.hashWith(context.Background(), "fotos", &fakeRuntime{}); err != nil {
		t.Fatal(err)
	}
	if d := e.digest("fotos", "a.mov"); d != "" {
		t.Errorf("a.mov got digest %s from a read the rescan overtook", d)
	}
	e.rec.SetBeforeCall(nil)
	e.rec.Reset()
	e.hash("fotos")
	if d := e.digest("fotos", "a.mov"); d == "" {
		t.Error("the next run did not hash a.mov")
	}
	if got := e.opened(); !slices.Equal(got, []string{"a.mov"}) {
		t.Errorf("the next run opened %q, want only a.mov", got)
	}
}

// A cancelled job keeps what it read, and its successor reads only the
// files not checked yet.
func TestCancelledJobsSuccessorReadsOnlyUnchecked(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	for i := range 10 {
		root.File(string(rune('a'+i))+".jpg", 4000, fileTime)
	}
	e.scan("fotos")
	ctx, cancel := context.WithCancel(context.Background())
	opens := 0
	e.rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpOpenFile {
			if opens++; opens == 4 {
				cancel()
			}
		}
	})
	if err := e.hashWith(ctx, "fotos", &fakeRuntime{}); err == nil {
		t.Fatal("the cancelled job succeeded")
	}
	e.rec.SetBeforeCall(nil)
	e.checkCoverage()
	hashed := e.count(`SELECT count(*) FROM file_content WHERE state = 'hashed'`)
	if hashed != 3 {
		t.Errorf("%d files hashed before the cancel, want the 3 read", hashed)
	}
	e.rec.Reset()
	e.hash("fotos")
	if got := e.opened(); len(got) != 10-hashed {
		t.Errorf("the successor opened %d files (%q), want %d", len(got), got, 10-hashed)
	}
	if n := e.count(`SELECT count(*) FROM file_content WHERE state = 'hashed'`); n != 10 {
		t.Errorf("%d hashed after the successor, want 10", n)
	}
}

// The reading order of design D4: large files by size descending, then
// the small files of candidate folder pairs, then every other small file.
func TestReadingOrder(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	g := root.Dir("Grandes")
	pairOf(g, 2<<20, "m1.bin", "m2.bin")
	pairOf(g, 3<<20, "g1.bin", "g2.bin")
	o := root.Dir("Outros")
	pairOf(o, 700, "o1.txt", "o2.txt")
	f, c := root.Dir("Fotos"), root.Dir("Fotos - Copia")
	for _, d := range []*synthfs.Node{f, c} {
		d.File("1.jpg", 1000, fileTime)
		d.File("2.jpg", 2000, fileTime)
	}
	e.scan("fotos")
	e.svc.candidates = func(context.Context, store.Queryer, domain.SourceID) ([][2]relations.Range, error) {
		return [][2]relations.Range{{
			{Source: "fotos", From: []byte("Fotos - Copia/"), To: []byte("Fotos - Copia0")},
			{Source: "fotos", From: []byte("Fotos/"), To: []byte("Fotos0")},
		}}, nil
	}
	e.rec.Reset()
	e.hash("fotos")
	got := e.opened()
	want := []string{"Grandes/g2.bin", "Grandes/g1.bin", "Grandes/m2.bin", "Grandes/m1.bin",
		"Fotos - Copia/1.jpg", "Fotos - Copia/2.jpg", "Fotos/1.jpg", "Fotos/2.jpg", "Outros/o1.txt", "Outros/o2.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("reading order\n got  %q\n want %q", got, want)
	}
}

// hash_now reads the chosen folders' files and nothing else.
func TestHashNowReadsItsFolders(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	f, c, o := root.Dir("Fotos"), root.Dir("Fotos - Copia"), root.Dir("Outros")
	for _, d := range []*synthfs.Node{f, c, o} {
		d.File("1.jpg", 1000, fileTime)
		d.File("2.jpg", 2000, fileTime)
	}
	e.scan("fotos")
	e.rec.Reset()
	e.hashNow("fotos", e.id("fotos", "Fotos"), e.id("fotos", "Fotos - Copia"))
	got := e.opened()
	slices.Sort(got)
	want := []string{"Fotos - Copia/1.jpg", "Fotos - Copia/2.jpg", "Fotos/1.jpg", "Fotos/2.jpg"}
	if !slices.Equal(got, want) {
		t.Errorf("hash_now opened %q, want %q", got, want)
	}
	for _, p := range []string{"Outros/1.jpg", "Outros/2.jpg"} {
		if s := e.state("fotos", p); s != domain.ContentPending {
			t.Errorf("%s is %s, want pending", p, s)
		}
	}
	e.checkCoverage()
	e.rec.Reset()
	e.hash("fotos")
	if got := e.opened(); !slices.Equal(got, []string{"Outros/1.jpg", "Outros/2.jpg"}) {
		t.Errorf("the hash job then opened %q", got)
	}
}
