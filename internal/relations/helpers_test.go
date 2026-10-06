package relations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index/indextest"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// world describes the index of one or more sources by "source:path" (a
// bare path is on "disk") and seeds it with indextest: entries, content
// rows, and complete archives.
type world struct {
	t        testing.TB
	st       *store.Store
	files    []wf
	archives []warc
	seeded   map[domain.SourceID]*indextest.Seeded
	arcs     map[string]*indextest.SeededArchive // by "source:path"
}

// wf is one entry. A regular file with a content label is hashed (digest
// sha256(label)); without one it is unique_size unless state says
// otherwise. ino > 0 makes it one name of a multiply-linked file (dev 1).
type wf struct {
	path    string
	size    int64
	content string
	state   domain.ContentState
	kind    domain.EntryKind
	link    string
	ino     uint64
	// folder flags (kind directory)
	unreadable, mount bool
}

// warc is a complete archive: its file (a wf) and its members.
type warc struct {
	file    wf
	format  domain.ArchiveFormat
	state   domain.ArchiveState
	members []indextest.Member
}

func newWorld(t testing.TB) *world {
	return &world{t: t, st: storetest.Open(t)}
}

func where(p string) (domain.SourceID, string) {
	if src, path, ok := strings.Cut(p, ":"); ok {
		return domain.SourceID(src), path
	}
	return "disk", p
}

func digest(label string) []byte {
	s := sha256.Sum256([]byte(label))
	return s[:]
}

func (w *world) file(path string, size int64, content string) {
	w.files = append(w.files, wf{path: path, size: size, content: content})
}

func (w *world) add(f wf) { w.files = append(w.files, f) }

// archive adds a complete archive file (unique_size when f has no content).
func (w *world) archive(f wf, members ...indextest.Member) {
	w.archives = append(w.archives, warc{file: f, format: domain.ArchiveZip, members: members})
}

// mem is a file member: hashed with content, else unique_size.
func mem(path string, size int64, content string) indextest.Member {
	m := indextest.Member{Path: path, Size: size, Content: indextest.Content{State: domain.ContentUniqueSize}}
	if content != "" {
		m.Content = indextest.Content{State: domain.ContentHashed, SHA256: digest(content)}
	}
	return m
}

func (f wf) node() indextest.Node {
	_, path := where(f.path)
	n := indextest.Node{Path: path, Kind: f.kind, Size: f.size, LinkText: f.link, Unreadable: f.unreadable,
		MountBoundary: f.mount}
	if f.ino > 0 {
		n.Size = 0
		n.Lstat = &fsaccess.EntryInfo{Kind: domain.EntryFile, Size: f.size, ModTime: indextest.DefaultNow,
			Dev: 1, Ino: f.ino, Nlink: 2}
	}
	return n
}

func (f wf) contentState() (indextest.Content, bool) {
	if f.kind != "" && f.kind != domain.EntryFile || f.size == 0 {
		return indextest.Content{}, false
	}
	switch {
	case f.state == domain.ContentHashed || f.state == "" && f.content != "":
		return indextest.Content{State: domain.ContentHashed, SHA256: digest(f.content)}, true
	case f.state == domain.ContentSampled:
		return indextest.Content{State: domain.ContentSampled, Sample: digest("sample:" + f.path)}, true
	case f.state == "":
		return indextest.Content{State: domain.ContentUniqueSize}, true
	case f.state == "none":
		return indextest.Content{}, false
	}
	return indextest.Content{State: f.state}, true
}

// seed writes the world once: per source, its entries, then content rows
// and archives.
func (w *world) seed() {
	w.t.Helper()
	if w.seeded != nil {
		return
	}
	nodes := map[domain.SourceID][]indextest.Node{}
	all := slices.Clone(w.files)
	for _, a := range w.archives {
		all = append(all, a.file)
	}
	for _, f := range all {
		src, _ := where(f.path)
		nodes[src] = append(nodes[src], f.node())
	}
	srcs := []domain.SourceID{"disk"}
	for src := range nodes {
		if src != "disk" {
			srcs = append(srcs, src)
		}
	}
	slices.Sort(srcs)
	w.seeded = map[domain.SourceID]*indextest.Seeded{}
	w.arcs = map[string]*indextest.SeededArchive{}
	for _, src := range srcs {
		w.seeded[src] = indextest.Seed(w.t, w.st, indextest.Tree{Source: src, CreateSource: true,
			MountPoint: "/mnt/" + string(src), Nodes: nodes[src]})
	}
	for _, f := range all {
		if c, ok := f.contentState(); ok {
			src, path := where(f.path)
			w.seeded[src].SetContent(w.st, path, c)
		}
	}
	for _, a := range w.archives {
		src, path := where(a.file.path)
		w.arcs[string(src)+":"+path] = w.seeded[src].SeedArchive(w.st, path,
			indextest.Archive{Format: a.format, State: a.state, Members: a.members})
	}
	indextest.RecomputeCoverage(w.t, w.st)
}

// id returns the entry ID of "source:path".
func (w *world) id(p string) domain.EntryID {
	w.t.Helper()
	w.seed()
	src, path := where(p)
	return w.seeded[src].ID(path)
}

// ref returns the ref of "source:path", or of "source:archive!member".
func (w *world) ref(p string) domain.Ref {
	w.t.Helper()
	w.seed()
	if arc, member, ok := strings.Cut(p, "!"); ok {
		src, path := where(arc)
		a := w.arcs[string(src)+":"+path]
		return domain.Ref{Entry: a.ID, Member: a.Member(member)}
	}
	return domain.Ref{Entry: w.id(p)}
}

func (w *world) snapshot(provisional bool) *Snapshot {
	w.t.Helper()
	w.seed()
	var s *Snapshot
	err := w.st.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		s, err = loadSnapshot(context.Background(), tx, provisional)
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

// relate relates the world's final snapshot.
func (w *world) relate() (*Snapshot, []result) {
	w.t.Helper()
	s := w.snapshot(false)
	return s, s.Relate()
}

// name renders directory d as "path", "source:path" off "disk", and
// "archive!member/path" inside an archive.
func (s *Snapshot) name(d int32) string {
	p := string(s.dirPath(d))
	if a := s.arc[d]; a >= 0 && s.arcDir[a] != d {
		z := string(s.dirPath(s.arcDir[a]))
		p = z + "!" + p[len(z)+1:]
	}
	if src := s.sources[s.src[d]]; src != "disk" {
		p = string(src) + ":" + p
	}
	return p
}

// line renders a result: kind a<-b and its counts.
func (s *Snapshot) line(r result) string {
	return fmt.Sprintf("%s %s<-%s matched=%d redundant=%d a=%d/%d b=%d/%d a_only=%d/%d b_only=%d/%d",
		r.Kind, s.name(r.aDir), s.name(r.bDir), r.MatchedBytes, r.Redundant, r.AFiles, r.ABytes, r.BFiles, r.BBytes,
		r.AOnlyFiles, r.AOnlyBytes, r.BOnlyFiles, r.BOnlyBytes)
}

// short renders a result as "kind a<-b".
func (s *Snapshot) short(r result) string {
	return fmt.Sprintf("%s %s<-%s", r.Kind, s.name(r.aDir), s.name(r.bDir))
}

func (s *Snapshot) lines(rels []result, short bool) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		if short {
			out[i] = s.short(r)
		} else {
			out[i] = s.line(r)
		}
	}
	return out
}

// wantLines fails unless rels render as want (full lines), in rank order.
func wantLines(t *testing.T, s *Snapshot, rels []result, want ...string) {
	t.Helper()
	if got := s.lines(rels, false); !slices.Equal(got, want) {
		t.Fatalf("relations:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// wantShort fails unless rels render as want (short lines), sorted.
func wantShort(t *testing.T, s *Snapshot, rels []result, want ...string) {
	t.Helper()
	got := s.lines(rels, true)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("relations:\n  %s\nwant:\n  %s", strings.Join(s.lines(rels, false), "\n  "), strings.Join(want, "\n  "))
	}
}
