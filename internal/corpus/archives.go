package corpus

import (
	"crypto/sha256"
	"path"
	"slices"
	"strings"
	"time"

	"precious/internal/domain"
)

// The archives.format and archive_members.kind vocabulary of the R2 schema
// (design Interfaces), as the plain strings the ground truth carries.
const (
	formatZip     = "zip"
	formatTarGzip = "tar_gzip"
	formatGzip    = "gzip"
	formatBzip2   = "bzip2"

	memberDirectory = "directory"
	memberFile      = "file"
)

// archive is what a complete listing of an archive file holds (R2 design
// D7): its format and every member, folders implied by deeper paths
// included, sorted by path.
type archive struct {
	format  string
	members []member
}

// member is one member of an archive.
type member struct {
	path    string    // cleaned, '/'-joined inside the archive
	kind    string    // memberDirectory or memberFile
	data    []byte    // a file's content
	sum     [32]byte  // a file's SHA-256
	stored  bool      // a zip member stored without compression
	locator *int      // a zip member's central-directory index
	mtime   time.Time // as the archive records it; zero when it records none
}

// newArchive adds the folders implied by deeper member paths, digests the
// files, and sorts the members by path.
func newArchive(format string, members []member) *archive {
	seen := make(map[string]bool, len(members))
	for _, m := range members {
		seen[m.path] = true
	}
	all := slices.Clone(members)
	for _, m := range members {
		for dir := path.Dir(m.path); dir != "."; dir = path.Dir(dir) {
			if !seen[dir] {
				seen[dir] = true
				all = append(all, member{path: dir, kind: memberDirectory})
			}
		}
	}
	for i := range all {
		if all[i].kind == memberFile {
			all[i].sum = sha256.Sum256(all[i].data)
		}
	}
	slices.SortFunc(all, func(a, b member) int { return strings.Compare(a.path, b.path) })
	return &archive{format: format, members: all}
}

// zip adds a zip archive of members.
func (d *def) zip(p string, mtime time.Time, members []zipMember) {
	ms := make([]member, len(members))
	for i, m := range members {
		ms[i] = member{path: m.name, kind: memberFile, data: m.data, stored: !m.deflate, locator: &i, mtime: m.mtime}
	}
	d.add(item{path: p, kind: domain.EntryFile, data: zipArchive(members), mtime: mtime, archive: newArchive(formatZip, ms)})
}

// tarGzipOf adds a tar.gz of the folder src as it is now: one member per
// folder and file below src, at its path relative to src, in creation order,
// each folder dated by the newest file below it.
func (d *def) tarGzipOf(p string, mtime time.Time, src string) {
	var tms []tarMember
	var ms []member
	for _, it := range d.items {
		rel, ok := strings.CutPrefix(it.path, src+"/")
		if !ok {
			continue
		}
		switch it.kind {
		case domain.EntryDirectory:
			tms = append(tms, tarMember{name: rel, mtime: d.newest(it.path), dir: true})
			ms = append(ms, member{path: rel, kind: memberDirectory, mtime: d.newest(it.path)})
		case domain.EntryFile:
			tms = append(tms, tarMember{name: rel, data: it.data, mtime: it.mtime})
			ms = append(ms, member{path: rel, kind: memberFile, data: it.data, mtime: it.mtime})
		default:
			panic("corpus: tarGzipOf holds folders and files only: " + displayPath(it.path))
		}
	}
	d.add(item{path: p, kind: domain.EntryFile, data: tarGzip(tms), mtime: mtime, archive: newArchive(formatTarGzip, ms)})
}

// newest returns the newest modification time of the files below dir.
func (d *def) newest(dir string) time.Time {
	var t time.Time
	for _, it := range d.items {
		if it.kind == domain.EntryFile && strings.HasPrefix(it.path, dir+"/") && it.mtime.After(t) {
			t = it.mtime
		}
	}
	return t
}

// gzip adds a single-file gzip of data. Its member is named after the file
// without ".gz", as the readers name it (m4b design D2).
func (d *def) gzip(p string, mtime time.Time, data []byte) {
	name := strings.TrimSuffix(path.Base(p), ".gz")
	d.add(item{path: p, kind: domain.EntryFile, data: gzipFile(name, mtime, data), mtime: mtime,
		archive: newArchive(formatGzip, []member{{path: name, kind: memberFile, data: data, mtime: mtime}})})
}

// bzip2 adds a single-file bzip2 with the compressed content b. Its member is
// named after the file without ".bz2"; bzip2 records no time.
func (d *def) bzip2(p string, mtime time.Time, b []byte) {
	name := strings.TrimSuffix(path.Base(p), ".bz2")
	d.add(item{path: p, kind: domain.EntryFile, data: b, mtime: mtime,
		archive: newArchive(formatBzip2, []member{{path: name, kind: memberFile, data: bunzip2(b)}})})
}
