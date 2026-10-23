package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index/indextest"
	"precious/internal/jobs"
	"precious/internal/relations"
	"precious/internal/rules"
)

// pworld describes an index of several sources by "source:path" (a bare
// path is on "disk"), seeds it with indextest (entries, content rows, and
// complete archives), and runs one relate pass with the policy's member
// classifier, as serve wires it (r5c design D13). Sources named in offline
// are created offline; the entries in missing ("source:path") are marked
// missing before the pass.
type pworld struct {
	*env
	t        *testing.T
	files    []pf
	archives []parc
	missing  []string
	offline  map[domain.SourceID]bool
	seeded   map[domain.SourceID]*indextest.Seeded
	arcs     map[string]*indextest.SeededArchive
}

// pf is one entry. A regular file with a content label is hashed (digest
// sha256(label)); without one it is unique_size unless state says
// otherwise ("none": no content row). ino > 0 makes it one name of a
// multiply-linked file (dev 1).
type pf struct {
	path     string
	size     int64
	content  string
	state    domain.ContentState
	kind     domain.EntryKind
	link     string
	ino      uint64
	category domain.Category
	fileKind domain.FileKind
	triage   domain.Triage
	// folder flags (kind directory)
	unreadable, mount bool
}

// parc is a complete archive: its file and its members.
type parc struct {
	file    pf
	members []indextest.Member
}

func newPWorld(t *testing.T, offline ...domain.SourceID) *pworld {
	w := &pworld{env: newEnv(t), t: t, offline: map[domain.SourceID]bool{}}
	for _, s := range offline {
		w.offline[s] = true
	}
	return w
}

func pwhere(p string) (domain.SourceID, string) {
	if src, path, ok := strings.Cut(p, ":"); ok {
		return domain.SourceID(src), path
	}
	return "disk", p
}

func pdigest(label string) []byte {
	s := sha256.Sum256([]byte(label))
	return s[:]
}

// file adds a regular file: hashed with content, else unique_size.
func (w *pworld) file(path string, size int64, content string) {
	w.files = append(w.files, pf{path: path, size: size, content: content})
}

func (w *pworld) add(f pf) { w.files = append(w.files, f) }

// archive adds a complete zip (unique_size when f has no content).
func (w *pworld) archive(f pf, members ...indextest.Member) {
	w.archives = append(w.archives, parc{file: f, members: members})
}

// pmem is a file member: hashed with content, else unique_size.
func pmem(path string, size int64, content string) indextest.Member {
	m := indextest.Member{Path: path, Size: size, Content: indextest.Content{State: domain.ContentUniqueSize}}
	if content != "" {
		m.Content = indextest.Content{State: domain.ContentHashed, SHA256: pdigest(content)}
	}
	return m
}

func (f pf) node() indextest.Node {
	_, path := pwhere(f.path)
	n := indextest.Node{Path: path, Kind: f.kind, Size: f.size, LinkText: f.link, Unreadable: f.unreadable,
		MountBoundary: f.mount, Category: f.category, FileKind: f.fileKind, Triage: f.triage}
	if f.ino > 0 {
		n.Size = 0
		n.Lstat = &fsaccess.EntryInfo{Kind: domain.EntryFile, Size: f.size, ModTime: indextest.DefaultNow,
			Dev: 1, Ino: f.ino, Nlink: 2}
	}
	return n
}

func (f pf) contentState() (indextest.Content, bool) {
	if f.kind != "" && f.kind != domain.EntryFile || f.size == 0 {
		return indextest.Content{}, false
	}
	switch {
	case f.state == domain.ContentHashed || f.state == "" && f.content != "":
		return indextest.Content{State: domain.ContentHashed, SHA256: pdigest(f.content)}, true
	case f.state == domain.ContentSampled:
		return indextest.Content{State: domain.ContentSampled, Sample: pdigest("sample:" + f.path)}, true
	case f.state == "":
		return indextest.Content{State: domain.ContentUniqueSize}, true
	case f.state == "none":
		return indextest.Content{}, false
	}
	return indextest.Content{State: f.state}, true
}

// seed writes the world: per source its entries, then content rows and
// archives, then coverage; and runs one relate pass.
func (w *pworld) seed() {
	w.t.Helper()
	nodes := map[domain.SourceID][]indextest.Node{}
	all := slices.Clone(w.files)
	for _, a := range w.archives {
		all = append(all, a.file)
	}
	for _, f := range all {
		src, _ := pwhere(f.path)
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
		mount := "/mnt/" + string(src)
		if w.offline[src] {
			mount = ""
		}
		w.seeded[src] = indextest.Seed(w.t, w.st, indextest.Tree{Source: src, CreateSource: true,
			MountPoint: mount, Nodes: nodes[src]})
	}
	// Rows without identity (unique_size, pending) are written in one
	// transaction; the read states through SetContent, which copies the
	// entry's identity.
	type plainRow struct {
		id    domain.EntryID
		state domain.ContentState
	}
	var plain []plainRow
	for _, f := range all {
		c, ok := f.contentState()
		if !ok {
			continue
		}
		src, path := pwhere(f.path)
		if c.State == domain.ContentUniqueSize || c.State == domain.ContentPending {
			plain = append(plain, plainRow{w.seeded[src].ID(path), c.State})
			continue
		}
		w.seeded[src].SetContent(w.st, path, c)
	}
	err := w.st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, r := range plain {
			if _, err := tx.Exec(`INSERT INTO file_content (entry_id, source_id, state, size)
				SELECT id, source_id, ?, size FROM entries WHERE id = ?`, string(r.state), int64(r.id)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, a := range w.archives {
		src, path := pwhere(a.file.path)
		w.arcs[string(src)+":"+path] = w.seeded[src].SeedArchive(w.st, path,
			indextest.Archive{Format: domain.ArchiveZip, Members: a.members})
	}
	indextest.RecomputeCoverage(w.t, w.st)
	// A scan always records the link count; content.Copies reads it.
	w.exec(w.t, `UPDATE entries SET nlink = 1 WHERE nlink IS NULL AND kind = 'file'`)
	for _, p := range w.missing {
		src, path := pwhere(p)
		w.exec(w.t, `UPDATE entries SET state = 'missing' WHERE id = ?`, int64(w.seeded[src].ID(path)))
	}
	w.relate()
}

// relate runs one relate pass, members classified by the default policy.
func (w *pworld) relate() {
	w.t.Helper()
	pol := rules.Default()
	h := relations.NewHandler(w.st, clock.Real{}, config.Defaults().Duplicates, nil)
	h.SetClassifier(func(name []byte, size int64) (domain.Category, domain.FileKind, domain.Triage) {
		kind := pol.FileKind(name)
		res := pol.ClassifyFile(rules.FileFacts{Name: name, Kind: kind, Size: size})
		return res.Category, kind, res.Triage
	})
	if err := h.Run(context.Background(), jobs.Job{Kind: relations.KindRelate}, scanRuntime{}); err != nil {
		w.t.Fatal(err)
	}
}

// id is the API ref of "source:path" or "source:archive!member".
func (w *pworld) id(p string) string {
	w.t.Helper()
	if arc, member, ok := strings.Cut(p, "!"); ok {
		src, path := pwhere(arc)
		a := w.arcs[string(src)+":"+path]
		if a == nil {
			w.t.Fatalf("no archive %q", arc)
		}
		return domain.Ref{Member: a.Member(member)}.String()
	}
	src, path := pwhere(p)
	return w.seeded[src].ID(path).String()
}

// The decoded r5c shapes.

type pamount struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

type psums struct {
	OnlyCopy    pamount `json:"only_copy"`
	ThisSource  pamount `json:"this_source"`
	OtherSource pamount `json:"other_source"`
	Unchecked   pamount `json:"unchecked"`
	Unreadable  pamount `json:"unreadable"`
	Disposable  pamount `json:"disposable"`
}

// at returns the amount of a place.
func (s *psums) at(place string) *pamount {
	switch place {
	case "only_copy":
		return &s.OnlyCopy
	case "this_source":
		return &s.ThisSource
	case "other_source":
		return &s.OtherSource
	case "unchecked":
		return &s.Unchecked
	case "unreadable":
		return &s.Unreadable
	}
	return nil
}

func (s *psums) add(o psums) {
	for _, p := range []string{"only_copy", "this_source", "other_source", "unchecked", "unreadable"} {
		a, b := s.at(p), o.at(p)
		a.Files += b.Files
		a.Bytes += b.Bytes
	}
	s.Disposable.Files += o.Disposable.Files
	s.Disposable.Bytes += o.Disposable.Bytes
}

type pcopy struct {
	Ref        string  `json:"ref"`
	SourceID   string  `json:"source_id"`
	Path       string  `json:"path"`
	ArchiveID  *string `json:"archive_id"`
	Offline    bool    `json:"offline"`
	ViaArchive bool    `json:"via_archive"`
	More       int     `json:"more"`
}

type pcovered struct {
	Kind  string `json:"kind"`
	Other struct {
		ID       string `json:"id"`
		SourceID string `json:"source_id"`
		Path     string `json:"path"`
	} `json:"other"`
}

// prow is an EntryRow with the r5c fields.
type prow struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Kind         string    `json:"kind"`
	State        string    `json:"state"`
	Size         int64     `json:"size"`
	TotalBytes   int64     `json:"total_bytes"`
	ArchiveState *string   `json:"archive_state"`
	Place        *string   `json:"place"`
	PlaceOffline bool      `json:"place_offline"`
	Disposable   bool      `json:"disposable"`
	Places       *psums    `json:"places"`
	MemberPlaces *psums    `json:"member_places"`
	OtherCopy    *pcopy    `json:"other_copy"`
	CoveredBy    *pcovered `json:"covered_by"`
}

func (r prow) place() string {
	if r.Place == nil {
		return "<null>"
	}
	return *r.Place
}

type ppage struct {
	Items      []prow  `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// children reads every children page of ref with query (places=1 is
// added), limit rows per page, and the number of pages.
func (w *pworld) children(ref, query string, limit int) ([]prow, int) {
	w.t.Helper()
	var (
		out   []prow
		pages int
	)
	base := "/api/entries/" + ref + "/children?places=1&limit=" + strconv.Itoa(limit)
	if query != "" {
		base += "&" + query
	}
	target := base
	for {
		var p ppage
		w.get(w.t, target, 200, &p)
		pages++
		out = append(out, p.Items...)
		if p.NextCursor == nil {
			return out, pages
		}
		target = base + "&cursor=" + url.QueryEscape(*p.NextCursor)
	}
}

// byName indexes rows by name.
func byName(rows []prow) map[string]prow {
	out := make(map[string]prow, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

type ptreemap struct {
	Entry prow   `json:"entry"`
	Items []prow `json:"items"`
}

type pdetail struct {
	Entry          prow  `json:"entry"`
	PlacesUpdating *bool `json:"places_updating"`
	Coverage       struct {
		Candidate pamount `json:"candidate"`
		Checked   pamount `json:"checked"`
	} `json:"coverage"`
}

// detail reads GET /api/entries/{ref}?places=1.
func (w *pworld) detail(ref string) pdetail {
	w.t.Helper()
	var d pdetail
	w.get(w.t, "/api/entries/"+ref+"?places=1", 200, &d)
	return d
}

// treemap reads GET /api/entries/{ref}/treemap?places=1.
func (w *pworld) treemap(ref string) ptreemap {
	w.t.Helper()
	var m ptreemap
	w.get(w.t, "/api/entries/"+ref+"/treemap?places=1", 200, &m)
	return m
}

// bodyOf requests target and returns its raw body, failing on any status
// but 200.
func (e *env) bodyOf(t *testing.T, target string) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d; body %s", target, rec.Code, rec.Body)
	}
	return bytes.Clone(rec.Body.Bytes())
}
