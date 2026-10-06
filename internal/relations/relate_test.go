package relations

import (
	"fmt"
	"slices"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
)

// Task 4.1: the relation tests restored from curator-m4b's relate_test.go
// and relate_archive_test.go, on index-seeded worlds (design D9).

func TestRenamedAndRearrangedPhotosAreFoundInside(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for i := range 6 {
		size := int64(1000 + i)
		w.file(fmt.Sprintf("fotos/2014/21-11-2014/DSC0243%d.JPG", i), size, fmt.Sprint("p", i))
		w.file(fmt.Sprintf("fotos-b/2014/11/dsc0243%d.jpg", i), size, fmt.Sprint("p", i))
	}
	for i := range 4 {
		size := int64(2000 + i)
		w.file(fmt.Sprintf("fotos/2015/03-2015/IMG_%d.JPG", i), size, fmt.Sprint("q", i))
		w.file(fmt.Sprintf("fotos-b/2015-maerz/img-%d.jpg", i), size, fmt.Sprint("q", i))
	}
	for i := range 3 { // 30% more files in fotos
		w.file(fmt.Sprintf("fotos/2016/x%d.jpg", i), int64(3000+i), "")
	}
	s, rels := w.relate()
	// One line for the pair: fotos-b inside fotos. fotos overlaps fotos-b
	// too, but fotos-b has the larger share, so that is the same pair.
	matched := int64(6*1000 + 15 + 4*2000 + 6)
	wantLines(t, s, rels, fmt.Sprintf("inside fotos-b<-fotos matched=%d redundant=%d a=10/%d b=13/%d a_only=0/0 b_only=3/9003",
		matched, matched, matched, matched+9003))
	if r := rels[0]; r.A != w.ref("fotos-b") || r.B != w.ref("fotos") {
		t.Errorf("sides %v %v", r.A, r.B)
	}
}

func TestGapPreventsAnInsideClaim(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("old/a.doc", 100, "a")
	w.file("old/b.doc", 200, "b")
	w.add(wf{path: "old/c.doc", size: 300, state: domain.ContentUnreadable})
	w.file("new/a.doc", 100, "a")
	w.file("new/b.doc", 200, "b")
	w.file("new/z.doc", 50, "")
	s, rels := w.relate()
	// old holds a gap: no inside claim. Its share is 50%, new's 300/350.
	wantLines(t, s, rels, "overlap new<-old matched=300 redundant=300 a=3/350 b=3/600 a_only=1/50 b_only=0/0")

	// A gap directory below a folder blocks it the same way, with no gap
	// file; so do pending and changed files.
	for _, gap := range []wf{
		{path: "old/mnt", kind: domain.EntryDirectory, mount: true},
		{path: "old/locked", kind: domain.EntryDirectory, unreadable: true},
		{path: "old/p.doc", size: 70, state: domain.ContentPending},
		{path: "old/p.doc", size: 70, state: domain.ContentChanged},
		{path: "old/p.doc", size: 70, state: "none"},
	} {
		w := newWorld(t)
		w.file("old/a.doc", 100, "a")
		w.add(gap)
		w.file("new/a.doc", 100, "a")
		w.file("new/z.doc", 50, "")
		s, rels := w.relate()
		if len(rels) != 1 || rels[0].Kind != KindOverlap {
			t.Errorf("old with gap %+v:\n  %v", gap, s.lines(rels, false))
		}
	}
}

func TestTwoIdenticalFolders(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("x/a.txt", 100, "a")
	w.file("x/sub/b.txt", 200, "b")
	w.file("y/renamed.txt", 100, "a")
	w.file("y/other/name.txt", 200, "b")
	s, rels := w.relate()
	// Side a of same is the path-later side.
	wantLines(t, s, rels, "same y<-x matched=300 redundant=300 a=2/300 b=2/300 a_only=0/0 b_only=0/0")
}

func TestHardLinkedCopyFreesNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	// one.bin's size group is its one inode: unique_size, never read.
	// two.bin shares its size with c/other.bin, another inode: hashed.
	w.add(wf{path: "a/one.bin", size: 1000, ino: 11})
	w.add(wf{path: "a/two.bin", size: 2000, content: "2", ino: 12})
	w.file("a/three.bin", 3000, "3")
	w.add(wf{path: "b/one.bin", size: 1000, ino: 11})
	w.add(wf{path: "b/two.bin", size: 2000, content: "2", ino: 12})
	w.file("c/other.bin", 2000, "other")
	s, rels := w.relate()
	wantLines(t, s, rels, "inside b<-a matched=3000 redundant=0 a=2/3000 b=3/6000 a_only=0/0 b_only=1/3000")
}

func TestOneLinePerCopy(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for i := range 120 {
		size := int64(100 + i)
		w.file(fmt.Sprintf("fotos-b/s%03d/img.jpg", i), size, fmt.Sprint("c", i))
		w.file(fmt.Sprintf("fotos/s%03d/img.jpg", i), size, fmt.Sprint("c", i))
	}
	// fotos holds ten times more besides, so it overlaps fotos-b by less
	// than half.
	w.file("fotos/video.mkv", 200_000, "")
	s, rels := w.relate()
	wantShort(t, s, rels, "inside fotos-b<-fotos")
}

func TestSymlinksMatchByLinkText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		target, want string
	}{{"../shared", "same b<-a"}, {"../elsewhere", "overlap a<-b"}} {
		w := newWorld(t)
		w.file("a/f.bin", 100, "f")
		w.add(wf{path: "a/link", kind: domain.EntrySymlink, link: "../shared"})
		w.file("b/g.bin", 100, "f")
		w.add(wf{path: "b/link", kind: domain.EntrySymlink, link: tc.target})
		s, rels := w.relate()
		if tc.want == "same b<-a" {
			wantShort(t, s, rels, tc.want)
			continue
		}
		// Both sides match all their bytes and miss one link: equal shares,
		// so a stays the folder whose partner was searched first.
		if len(rels) != 1 || rels[0].Kind != KindOverlap || rels[0].MatchedBytes != 100 {
			t.Errorf("link to %s:\n  %v", tc.target, s.lines(rels, false))
		}
	}
}

func TestOverlapNeedsHalfOfOneSide(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		unique int64
		want   bool
	}{{100, true}, {101, false}} {
		w := newWorld(t)
		w.file("a/m.bin", 100, "m")
		w.file("a/u.bin", tc.unique, "")
		w.file("b/m.bin", 100, "m")
		w.file("b/v.bin", 100_000, "")
		s, rels := w.relate()
		got := slices.ContainsFunc(rels, func(r result) bool { return r.Kind == KindOverlap })
		if got != tc.want {
			t.Errorf("unique %d: want an overlap %v:\n  %v", tc.unique, tc.want, s.lines(rels, false))
		}
	}
}

func TestUniqueFilePreventsInside(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("a/m.bin", 100, "m")
	w.file("a/u.bin", 10, "")
	w.add(wf{path: "a/s.bin", size: 20, state: domain.ContentSampled})
	w.file("b/m.bin", 100, "m")
	w.file("b/z.bin", 5, "")
	s, rels := w.relate()
	// b has the larger matched share (100 of 105 bytes): it is side a.
	wantLines(t, s, rels, "overlap b<-a matched=100 redundant=100 a=2/105 b=3/130 a_only=1/5 b_only=2/30")
}

// The deepest partner may sit below a folder that holds nothing else; the
// line names that folder.
func TestPartnerThroughAWrapperFolder(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("fotos-b/2014/a.jpg", 100, "a")
	w.file("fotos-b/2014/b.jpg", 100, "b")
	w.file("fotos/x/a.jpg", 100, "a")
	w.file("fotos/y/b.jpg", 100, "b")
	w.file("fotos/z.jpg", 100, "")
	s, rels := w.relate()
	wantShort(t, s, rels, "inside fotos-b<-fotos")
}

// An ancestor's overlap does not hide a descendant that is entirely the
// other folder: fotos/2014 holds the same files as fotos-b, which is the
// line the owner acts on (it ranks first). fotos-b holds nothing but 2014,
// so the same pair names fotos-b/2014, year with year.
func TestOverlapDoesNotHideAStrongerDescendantResult(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("fotos-b/2014/11/a.jpg", 100, "a")
	w.file("fotos-b/2014/12/b.jpg", 100, "b")
	w.file("fotos/2014/x/a.jpg", 100, "a")
	w.file("fotos/2014/y/b.jpg", 100, "b")
	w.file("fotos/z.jpg", 100, "")
	s, rels := w.relate()
	if got := s.lines(rels, true); len(got) == 0 || got[0] != "same fotos/2014<-fotos-b/2014" {
		t.Fatalf("relations %q, want same fotos/2014<-fotos-b/2014 first", got)
	}
	wantShort(t, s, rels, "same fotos/2014<-fotos-b/2014", "overlap fotos-b<-fotos")
}

// Folders related only through a third are one group: with fotos/2013 and
// fotos-reorg2 each the same as fotos-b, their event and day subfolders are
// not reported pair by pair (one line per copy).
func TestFoldersRelatedThroughAThirdAreOneGroup(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for i, c := range []string{"a", "b", "c", "d"} {
		w.file(fmt.Sprintf("fotos/2013/event-%d/DSC_%d.JPG", i, i), 100+int64(i), c)
		w.file(fmt.Sprintf("fotos-b/2013/%d/dsc_%d.jpg", 10+i%2, i), 100+int64(i), c)
		w.file(fmt.Sprintf("fotos-reorg2/2013/%d/%d/dsc_%d.jpg", 10+i%2, i, i), 100+int64(i), c)
	}
	w.file("fotos/extras/x.jpg", 500, "")
	s, rels := w.relate()
	wantShort(t, s, rels, "same fotos-reorg2/2013<-fotos-b/2013", "same fotos/2013<-fotos-b/2013")
}

// Two copies of the same photos, both inside a larger original: each copy
// is reported inside the original, not paired with its twin, and no
// subfolder pair of the original and a copy is reported (one line per
// copy).
func TestTwinCopiesAreInsideTheLargerOriginal(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for i, c := range []string{"a", "b", "c", "d"} {
		w.file(fmt.Sprintf("fotos/2013/event-%d/DSC_%d.JPG", i, i), 100+int64(i), c)
		w.file(fmt.Sprintf("fotos-b/2013/%d/dsc_%d.jpg", 10+i%2, i), 100+int64(i), c)
		w.file(fmt.Sprintf("fotos-reorg2/2013/%d/%d/dsc_%d.jpg", 10+i%2, i, i), 100+int64(i), c)
	}
	w.file("fotos/2013/event-0/EXTRA.JPG", 500, "")
	s, rels := w.relate()
	wantShort(t, s, rels, "inside fotos-b<-fotos", "inside fotos-reorg2<-fotos")
}

// Relations cross sources, offline ones included.
func TestRelationsAcrossSources(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("docs/a.txt", 100, "a")
	w.file("docs/b.txt", 200, "b")
	w.file("usb:backup/docs/a.txt", 100, "a")
	w.file("usb:backup/docs/b.txt", 200, "b")
	w.file("music/x.mp3", 5000, "")
	w.file("usb:other/y.bin", 7000, "")
	s, rels := w.relate()
	// usb:backup holds nothing but docs: the same pair names docs on both
	// sides (the deepest equivalent folders). Side a is the path-later
	// side, docs (after backup/docs).
	wantShort(t, s, rels, "same docs<-usb:backup/docs")
	if rels[0].A != w.ref("docs") || rels[0].B != w.ref("usb:backup/docs") {
		t.Errorf("sides %v %v", rels[0].A, rels[0].B)
	}
}

// Archives (M4b-1): a backup archive next to its unpacked copy is the same
// as the folder, side a, and redundant by its packed size.
func TestArchiveNextToItsUnpackedCopy(t *testing.T) {
	t.Parallel()
	for _, arc := range []string{"bkp.tar.gz", "backups/2019/bkp.tar.gz"} {
		w := newWorld(t)
		w.file("bkp/a.txt", 100, "a")
		w.file("bkp/sub/b.txt", 200, "b")
		w.file("bkp/c.txt", 300, "c")
		w.archive(wf{path: arc, size: 400},
			mem("bkp/x.txt", 100, "a"), mem("bkp/deep/y.txt", 200, "b"), mem("bkp/z.txt", 300, "c"))
		s, rels := w.relate()
		wantLines(t, s, rels, "same "+arc+"<-bkp matched=600 redundant=400 a=3/600 b=3/600 a_only=0/0 b_only=0/0")
		if rels[0].A != w.ref(arc) {
			t.Errorf("side a %v, want the archive %v", rels[0].A, w.ref(arc))
		}
	}
}

func TestFolderInsideAnArchive(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("old-site/index.html", 100, "i")
	w.file("old-site/css/a.css", 200, "c")
	w.archive(wf{path: "site-backup.zip", size: 500},
		mem("index.html", 100, "i"), mem("css/a.css", 200, "c"), mem("js/app.js", 300, "x"))
	s, rels := w.relate()
	wantLines(t, s, rels, "inside old-site<-site-backup.zip matched=300 redundant=300 a=2/300 b=3/600 a_only=0/0 b_only=1/300")
}

// A part of an archive is a member-folder ref and frees nothing.
func TestPartOfAnArchiveFreesNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("fotos/a.jpg", 100, "a")
	w.file("fotos/b.jpg", 200, "b")
	w.file("fotos/c.jpg", 50, "")
	w.archive(wf{path: "bkp.tar.gz", size: 700},
		mem("home/fotos/a.jpg", 100, "a"), mem("home/fotos/b.jpg", 200, "b"), mem("home/docs/x.doc", 500, ""))
	s, rels := w.relate()
	wantShort(t, s, rels, "inside bkp.tar.gz!home/fotos<-fotos")
	if r := rels[0]; r.Redundant != 0 || r.A != w.ref("bkp.tar.gz!home/fotos") {
		t.Errorf("%s, side a %v", s.line(r), r.A)
	}
}

// An archive that is not complete takes part only as a plain file.
func TestIncompleteArchiveIsAPlainFile(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("loose/a.txt", 100, "a")
	w.file("loose/b.txt", 200, "b")
	members := []indextest.Member{mem("a.txt", 100, "a"), mem("b.txt", 200, "b")}
	w.archives = append(w.archives, warc{file: wf{path: "p/y.tar", size: 260, content: "y"}, format: domain.ArchiveTar,
		state: domain.ArchivePartial, members: members})
	w.file("q/y-copy.tar", 260, "y")
	s, rels := w.relate()
	wantLines(t, s, rels, "same q<-p matched=260 redundant=260 a=1/260 b=1/260 a_only=0/0 b_only=0/0")
}

// A tar hard-link member has its target's key: it counts toward inside,
// and the pair inside the archive frees nothing.
func TestMemberHardLinkCountsTowardInsideAndFreesNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("loose/f.bin", 300, "f")
	w.archives = append(w.archives, warc{file: wf{path: "t.tar", size: 450}, format: domain.ArchiveTar,
		members: []indextest.Member{mem("orig/f.bin", 300, "f"), mem("copy/g.bin", 300, "f")}})
	w.seed()
	if _, err := w.st.Writer().Exec(`UPDATE archive_members SET link_member = ? WHERE id = ?`,
		int64(w.ref("t.tar!orig/f.bin").Member), int64(w.ref("t.tar!copy/g.bin").Member)); err != nil {
		t.Fatal(err)
	}
	s, rels := w.relate()
	wantLines(t, s, rels,
		"same t.tar<-loose matched=600 redundant=450 a=2/600 b=1/300 a_only=0/0 b_only=0/0",
		"same t.tar!orig<-t.tar!copy matched=300 redundant=0 a=1/300 b=1/300 a_only=0/0 b_only=0/0")
}

// Two names of one archive file (a hard-linked backup) are one inode: the
// same, and neither frees anything.
func TestHardLinkedArchiveFreesNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	members := []indextest.Member{mem("a.txt", 100, "a"), mem("b.txt", 200, "b")}
	w.archive(wf{path: "daily.0/bkp.zip", size: 400, ino: 9}, members...)
	w.archive(wf{path: "daily.1/bkp.zip", size: 400, ino: 9}, members...)
	s, rels := w.relate()
	wantLines(t, s, rels, "same daily.1/bkp.zip<-daily.0/bkp.zip matched=300 redundant=0 a=2/300 b=2/300 a_only=0/0 b_only=0/0")
}

// A file of an ancestor is no partner (a fix to m4b, which proposed the
// ancestor itself): a copy of a file kept beside its folder relates to
// nothing, as in the corpus's ISOs/copia.
func TestAncestorIsNoPartner(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("ISOs/a.iso", 1500, "iso")
	w.file("ISOs/b.iso", 1200, "")
	w.file("ISOs/copia/a.iso", 1500, "iso")
	s, rels := w.relate()
	wantShort(t, s, rels)
}
