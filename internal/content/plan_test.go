package content

import (
	"fmt"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess/instrument"
)

var fileTime = time.Date(2020, 5, 1, 10, 0, 0, 0, time.UTC)

// 990 files of 990 distinct sizes are unique_size, and hashing opens none.
func TestUniqueSizesCostNoReads(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	d := root.Dir("d")
	for i := 1; i <= 990; i++ {
		d.File(fmt.Sprintf("f%04d.jpg", i), int64(i), fileTime)
	}
	e.scan("fotos")
	e.rec.Reset()
	e.hash("fotos")
	if n := e.count(`SELECT count(*) FROM file_content WHERE state = 'unique_size'`); n != 990 {
		t.Errorf("%d unique_size rows, want 990", n)
	}
	if got := e.opened(); len(got) != 0 || e.rec.Count(instrument.OpReadAt) != 0 {
		t.Errorf("hashing opened %d files and read %d times", len(got), e.rec.Count(instrument.OpReadAt))
	}
	c := e.coverage("fotos")
	if c.CandidateFiles != 0 {
		t.Errorf("coverage %+v; unique sizes are no candidates", c)
	}
}

// Two hard links to one file are one copy: alone in their size they are
// unique_size and unread; with a third file of their content, the inode is
// read once and all three get the digest.
func TestHardLinksAreOneCopy(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	data := []byte("the same bytes under two names")
	a := root.Dir("a").File("foto.jpg", 0, fileTime).Content(data)
	root.Dir("b").HardLink("foto.jpg", a)
	e.scan("fotos")
	e.rec.Reset()
	e.hash("fotos")
	for _, p := range []string{"a/foto.jpg", "b/foto.jpg"} {
		if s := e.state("fotos", p); s != domain.ContentUniqueSize {
			t.Errorf("%s is %s, want unique_size", p, s)
		}
	}
	if got := e.opened(); len(got) != 0 {
		t.Errorf("opened %q", got)
	}
	root.Dir("c").File("foto.jpg", 0, fileTime).Content(data)
	e.scan("fotos")
	e.rec.Reset()
	e.hash("fotos")
	if got := e.opened(); len(got) != 2 {
		t.Errorf("opened %q; want the inode once and the third file", got)
	}
	want := e.digest("fotos", "c/foto.jpg")
	for _, p := range []string{"a/foto.jpg", "b/foto.jpg"} {
		if s, d := e.state("fotos", p), e.digest("fotos", p); s != domain.ContentHashed || d != want || want == "" {
			t.Errorf("%s: %s %s; want hashed %s", p, s, d, want)
		}
	}
}

// A size shared across sources makes both files pending, and both are
// hashed by their sources' jobs.
func TestSizeSharedAcrossSources(t *testing.T) {
	e := newEnv(t)
	e.disk("fotos", "/mnt/fotos", posix).File("x.jpg", 777, fileTime)
	e.disk("usb", "/mnt/usb", posix).File("y.jpg", 777, fileTime)
	e.scan("fotos")
	e.scan("usb")
	e.plan()
	if a, b := e.state("fotos", "x.jpg"), e.state("usb", "y.jpg"); a != domain.ContentPending || b != domain.ContentPending {
		t.Fatalf("states %s, %s; want pending", a, b)
	}
	e.hash("fotos")
	e.hash("usb")
	if a, b := e.state("fotos", "x.jpg"), e.state("usb", "y.jpg"); a != domain.ContentHashed || b != domain.ContentHashed {
		t.Errorf("states %s, %s; want hashed", a, b)
	}
	if e.digest("fotos", "x.jpg") == e.digest("usb", "y.jpg") {
		t.Error("files of different content got one digest")
	}
}

// A sampled file whose size group gains a file with an equal sample goes
// back to pending and is read in full; a file whose sample stays distinct
// is never read again.
func TestSampledFileBackToPending(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	var pair []corpus.LargeFile
	for _, lf := range corpus.LargeFiles() {
		if lf.Case == corpus.LargeDifferentSamples {
			pair = append(pair, lf)
		}
	}
	addLarge(root, pair...)
	e.scan("fotos")
	e.hash("fotos")
	a, b := pair[0].Name, pair[1].Name
	for _, p := range []string{a, b} {
		if s := e.state("fotos", p); s != domain.ContentSampled {
			t.Fatalf("%s is %s, want sampled", p, s)
		}
	}
	// A copy of a: equal samples.
	copyOfA := pair[0]
	copyOfA.Name = "copia.bin"
	addLarge(root, copyOfA)
	e.scan("fotos")
	e.plan()
	if s := e.state("fotos", a); s != domain.ContentPending {
		t.Errorf("%s is %s once its group gained a file; want pending", a, s)
	}
	e.rec.Reset()
	e.hash("fotos")
	if s := e.state("fotos", b); s != domain.ContentSampled {
		t.Errorf("%s is %s, want sampled", b, s)
	}
	if n := e.readOf(b); n != 0 {
		t.Errorf("%s was read again (%d bytes)", b, n)
	}
	if da, dc := e.digest("fotos", a), e.digest("fotos", "copia.bin"); da != pair[0].SHA256 || dc != da {
		t.Errorf("digests %s and %s; want both %s", da, dc, pair[0].SHA256)
	}
}
