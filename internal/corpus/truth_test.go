package corpus

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
)

func sumHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func rawOf(t *testing.T, p Path) string {
	t.Helper()
	b, err := Entry{PathB64: p.PathB64}.RawPath()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The generator's digests are the SHA-256 of the files it writes.
func TestDigestsMatchWrittenFiles(t *testing.T) {
	g := Corpus().GroundTruth()
	files := 0
	for _, e := range g.Entries {
		if e.Kind != domain.EntryFile {
			if e.SHA256 != "" {
				t.Errorf("%s: a %s with a digest", e.Path, e.Kind)
			}
			continue
		}
		raw, err := e.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		if got := sumHex(readReal(t, string(raw))); got != e.SHA256 {
			t.Errorf("%s: written digest %s, ground truth %s", e.Path, got, e.SHA256)
		}
		files++
	}
	if files < 300 {
		t.Errorf("%d files", files)
	}
}

// unpacked is one member as the standard library reads it back.
type unpacked struct {
	kind    string
	data    []byte
	stored  bool
	locator int // a zip member's central-directory index, else -1
	mtime   time.Time
}

// unpack reads an archive written to disk with the standard library: every
// member, folders implied by deeper paths included.
func unpack(t *testing.T, format, name string, b []byte) map[string]unpacked {
	t.Helper()
	out := map[string]unpacked{}
	switch format {
	case formatZip:
		z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		for i, f := range z.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			out[f.Name] = unpacked{kind: memberFile, data: data, stored: f.Method == zip.Store, locator: i, mtime: f.Modified}
		}
	case formatTarGzip:
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(zr)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			switch h.Typeflag {
			case tar.TypeDir:
				out[strings.TrimSuffix(h.Name, "/")] = unpacked{kind: memberDirectory, locator: -1, mtime: h.ModTime}
			case tar.TypeReg:
				data, err := io.ReadAll(tr)
				if err != nil {
					t.Fatal(err)
				}
				out[h.Name] = unpacked{kind: memberFile, data: data, locator: -1, mtime: h.ModTime}
			default:
				t.Fatalf("%s: member %s of type %c", name, h.Name, h.Typeflag)
			}
		}
	case formatGzip:
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		out[strings.TrimSuffix(path.Base(name), ".gz")] = unpacked{kind: memberFile, data: data, locator: -1, mtime: zr.ModTime}
	case formatBzip2:
		data, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(b)))
		if err != nil {
			t.Fatal(err)
		}
		out[strings.TrimSuffix(path.Base(name), ".bz2")] = unpacked{kind: memberFile, data: data, locator: -1}
	default:
		t.Fatalf("%s: format %q", name, format)
	}
	for p := range out {
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			if _, ok := out[dir]; !ok {
				out[dir] = unpacked{kind: memberDirectory, locator: -1}
			}
		}
	}
	return out
}

// Each archive's members are exactly what the standard library unpacks from
// the written file, with the digests of the unpacked bytes.
func TestMembersMatchUnpackedBytes(t *testing.T) {
	g := Corpus().GroundTruth()
	formats := map[string]string{}
	for _, a := range g.Members {
		name := rawOf(t, a.Path)
		formats[name] = a.Format
		got := unpack(t, a.Format, name, readReal(t, name))
		listed := map[string]bool{}
		for _, m := range a.Members {
			p := rawOf(t, m.Path)
			listed[p] = true
			u, ok := got[p]
			switch {
			case !ok:
				t.Errorf("%s: member %s not in the archive", a.Path.Path, m.Path.Path)
			case u.kind != m.Kind:
				t.Errorf("%s!%s: kind %s, unpacked %s", a.Path.Path, m.Path.Path, m.Kind, u.kind)
			case m.Kind == memberFile && (m.Size == nil || *m.Size != int64(len(u.data)) || m.SHA256 != sumHex(u.data)):
				t.Errorf("%s!%s: size or digest differs from the unpacked bytes", a.Path.Path, m.Path.Path)
			case m.Stored != u.stored:
				t.Errorf("%s!%s: stored %v, unpacked %v", a.Path.Path, m.Path.Path, m.Stored, u.stored)
			case m.Kind == memberDirectory && (m.Size != nil || m.SHA256 != ""):
				t.Errorf("%s!%s: a folder with a size or digest", a.Path.Path, m.Path.Path)
			case (m.Locator == nil) != (u.locator < 0) || m.Locator != nil && *m.Locator != u.locator:
				t.Errorf("%s!%s: locator %v, unpacked %d", a.Path.Path, m.Path.Path, m.Locator, u.locator)
			case (m.MTime == nil) != u.mtime.IsZero() || m.MTime != nil && !m.MTime.Equal(u.mtime):
				t.Errorf("%s!%s: mtime %v, unpacked %v", a.Path.Path, m.Path.Path, m.MTime, u.mtime)
			}
		}
		for p := range got {
			if !listed[p] {
				t.Errorf("%s: unpacked member %s is not listed", a.Path.Path, p)
			}
		}
		if !slices.IsSortedFunc(a.Members, func(x, y Member) int { return strings.Compare(rawOf(t, x.Path), rawOf(t, y.Path)) }) {
			t.Errorf("%s: members not sorted", a.Path.Path)
		}
	}
	want := map[string]string{
		"Downloads/fotos_2005_do_pendrive.zip": formatZip,
		"Downloads/eMule0.47c-Installer.zip":   formatZip,
		"Midia/videos.zip":                     formatZip,
		"Projetos/site_antigo_2006.tar.gz":     formatTarGzip,
		"Documentos/notas_2007.txt.gz":         formatGzip,
		"Documentos/notas_antigas.txt.bz2":     formatBzip2,
	}
	if !equalMaps(formats, want) {
		t.Errorf("archives %v, want %v", formats, want)
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// The tar.gz holds Projetos/site_antigo at the same relative paths, byte for
// byte; the stored video and the single-file archives hold what D19 says.
func TestNewArchiveFixtures(t *testing.T) {
	site := map[string][]byte{}
	for _, p := range truthPaths(Corpus().GroundTruth(), "Projetos/site_antigo/", domain.EntryFile) {
		site[p] = readReal(t, "Projetos/site_antigo/"+p)
	}
	got := unpack(t, formatTarGzip, "site_antigo_2006.tar.gz", readReal(t, "Projetos/site_antigo_2006.tar.gz"))
	files := 0
	for p, u := range got {
		if u.kind != memberFile {
			continue
		}
		files++
		if want, ok := site[p]; !ok || !bytes.Equal(want, u.data) {
			t.Errorf("tar.gz member %s differs from Projetos/site_antigo/%s", p, p)
		}
	}
	if files != len(site) || files < 10 {
		t.Errorf("tar.gz has %d files, site_antigo %d", files, len(site))
	}

	videos := unpack(t, formatZip, "videos.zip", readReal(t, "Midia/videos.zip"))
	if v := videos["ferias/video.mp4"]; !v.stored || !bytes.Equal(v.data, readReal(t, "Midia/video.mp4")) {
		t.Error("Midia/videos.zip does not store the video")
	}
	if v := videos["ferias/leia-me.txt"]; v.stored || len(v.data) == 0 {
		t.Error("Midia/videos.zip does not deflate its text")
	}
	if notas := unpack(t, formatGzip, "notas_2007.txt.gz", readReal(t, "Documentos/notas_2007.txt.gz")); string(notas["notas_2007.txt"].data) != notas2007 {
		t.Error("notas_2007.txt.gz does not hold its text")
	}
	if b := readReal(t, "Documentos/notas_antigas.txt.bz2"); !bytes.HasPrefix(b, []byte("BZh")) {
		t.Error("notas_antigas.txt.bz2 is not a bzip2")
	}
}

// Downloads holds two files of one size with different content.
func TestEqualSizePair(t *testing.T) {
	a, b := readReal(t, "Downloads/driver_impressora.exe"), readReal(t, "Downloads/driver_scanner.exe")
	if len(a) != len(b) || bytes.Equal(a, b) {
		t.Errorf("driver pair: %d and %d bytes, equal %v", len(a), len(b), bytes.Equal(a, b))
	}
}

// copyDigests maps every copy of the ground truth (non-empty files and file
// members) to its size and digest, read from the entries and the members.
func copyDigests(t *testing.T, g GroundTruth) map[string][2]string {
	t.Helper()
	out := map[string][2]string{}
	for _, e := range g.Entries {
		if e.Kind == domain.EntryFile && *e.Size > 0 {
			raw, err := e.RawPath()
			if err != nil {
				t.Fatal(err)
			}
			out[string(raw)] = [2]string{e.SHA256, strconv.FormatInt(*e.Size, 10)}
		}
	}
	for _, a := range g.Members {
		for _, m := range a.Members {
			if m.Kind == memberFile && *m.Size > 0 {
				out[rawOf(t, a.Path)+"!"+rawOf(t, m.Path)] = [2]string{m.SHA256, strconv.FormatInt(*m.Size, 10)}
			}
		}
	}
	return out
}

// The duplicate groups are consistent and complete: each has at least two
// copies of one size and digest, no copy is in two groups, and every digest
// held by two copies is a group.
func TestDuplicateGroupsConsistent(t *testing.T) {
	g := Corpus().GroundTruth()
	copies := copyDigests(t, g)
	byDigest := map[string]int{}
	for _, c := range copies {
		byDigest[c[0]]++
	}
	seen := map[string]bool{}
	groups := map[string]bool{}
	for _, d := range g.Duplicates {
		if len(d.Copies) < 2 {
			t.Errorf("group %s has %d copies", d.SHA256, len(d.Copies))
		}
		for _, c := range d.Copies {
			p := rawOf(t, c)
			x, ok := copies[p]
			switch {
			case !ok:
				t.Errorf("group %s: %s is no copy", d.SHA256, c.Path)
			case x[0] != d.SHA256 || x[1] != strconv.FormatInt(d.Size, 10):
				t.Errorf("group %s: %s has another size or digest", d.SHA256, c.Path)
			case seen[p]:
				t.Errorf("%s is in two groups", c.Path)
			}
			seen[p] = true
		}
		if byDigest[d.SHA256] != len(d.Copies) {
			t.Errorf("group %s lists %d copies of %d", d.SHA256, len(d.Copies), byDigest[d.SHA256])
		}
		groups[d.SHA256] = true
	}
	for digest, n := range byDigest {
		if n > 1 && !groups[digest] {
			t.Errorf("digest %s has %d copies and no group", digest, n)
		}
	}

	// The groups R2.1 names.
	groupOf := map[string][]string{}
	for _, d := range g.Duplicates {
		var paths []string
		for _, c := range d.Copies {
			paths = append(paths, rawOf(t, c))
		}
		for _, p := range paths {
			groupOf[p] = paths
		}
	}
	for p, n := range map[string]int{
		"Downloads/Setup.exe":      2,
		"Documentos/curriculo.doc": 4,
		"Downloads/fotos_2005_do_pendrive.zip!Carnaval/DSC01005.JPG":        3,
		"Downloads/fotos_2005_do_pendrive.zip!Carnaval/DSC01001.JPG":        4,
		"Projetos/site_antigo_2006.tar.gz!contato.php":                      2,
		"Midia/videos.zip!ferias/video.mp4":                                 2,
		"Downloads/eMule0.47c-Installer.zip!emule-0.47c/emule.exe":          2,
		"HD antigo/backup pc velho/Arquivos de programas/Winamp/winamp.exe": 2,
	} {
		if len(groupOf[p]) != n {
			t.Errorf("%s: group of %d, want %d (%v)", p, len(groupOf[p]), n, groupOf[p])
		}
	}
	for _, p := range []string{"Downloads/driver_impressora.exe", "Documentos/Nova pasta/Nova pasta (2)/teste.txt"} {
		if groupOf[p] != nil {
			t.Errorf("%s is in a group", p)
		}
	}
}

// Every side of a declared relation is a folder or archive of the tree, and
// what is only on one side lies on that side; the relations of the
// duplicates delta spec are declared.
func TestRelationsExist(t *testing.T) {
	g := Corpus().GroundTruth()
	sides := map[string]bool{}
	for _, e := range g.Entries {
		if e.Kind == domain.EntryDirectory {
			raw, _ := e.RawPath()
			sides[string(raw)] = true
		}
	}
	for _, a := range g.Members {
		sides[rawOf(t, a.Path)] = true
	}
	copies := copyDigests(t, g)
	declared := map[[3]string]bool{}
	for _, r := range g.Relations {
		a, b := rawOf(t, r.A), rawOf(t, r.B)
		if !sides[a] || !sides[b] {
			t.Errorf("relation %s/%s: a side is no folder or archive", r.A.Path, r.B.Path)
		}
		if !slices.Contains([]string{relSame, relInside, relOverlap}, r.Kind) {
			t.Errorf("relation %s/%s: kind %q", r.A.Path, r.B.Path, r.Kind)
		}
		if (r.Kind == relSame) != (len(r.AOnly) == 0 && len(r.BOnly) == 0) {
			t.Errorf("relation %s/%s: %s with %d and %d only on one side", r.A.Path, r.B.Path, r.Kind, len(r.AOnly), len(r.BOnly))
		}
		for side, only := range map[string][]Path{a: r.AOnly, b: r.BOnly} {
			for _, p := range only {
				raw := rawOf(t, p)
				if _, ok := copies[raw]; !ok || !strings.HasPrefix(raw, side+"/") && !strings.HasPrefix(raw, side+"!") {
					t.Errorf("relation %s/%s: %s is not a copy on side %s", r.A.Path, r.B.Path, p.Path, side)
				}
			}
		}
		declared[[3]string{r.Kind, a, b}] = true
		declared[[3]string{r.Kind, b, a}] = true
	}
	for _, want := range [][3]string{
		{relSame, "Downloads/fotos_2005_do_pendrive.zip", "Downloads/fotos_2005_do_pendrive"},
		{relSame, "Downloads/eMule0.47c-Installer.zip", "Downloads/emule-0.47c"},
		{relSame, "Projetos/site_antigo_2006.tar.gz", "Projetos/site_antigo"},
		{relSame, oldCopy + "/Arquivos de programas/Winamp", programs + "/Winamp"},
		{relOverlap, "Fotos - Copia", "Fotos"},
		{relSame, "Fotos - Copia/2004", "Fotos/2004"},
	} {
		if !declared[want] {
			t.Errorf("relation %v not declared", want)
		}
	}
	// Sides follow D9: the archive is side A of a same relation.
	for _, r := range g.Relations {
		if strings.HasSuffix(r.B.Path, ".zip") || strings.HasSuffix(r.B.Path, ".tar.gz") {
			t.Errorf("relation %s/%s: the archive is side B", r.A.Path, r.B.Path)
		}
	}
}

// A declaration that the content contradicts is refused.
func TestRelationDeclarationChecked(t *testing.T) {
	d := newDef()
	d.file("a/x", at(2005, 1, 1, 0, 0), []byte("x"))
	d.file("b/x", at(2005, 1, 1, 0, 0), []byte("x"))
	d.file("b/y", at(2005, 1, 1, 0, 0), []byte("y"))
	defer func() {
		if recover() == nil {
			t.Error("a wrong kind was accepted")
		}
	}()
	d.finish(nil, []relationDecl{{relSame, "a", "b"}}, nil)
}

// Rescue: R2.6's spreadsheet is listed inside Microsoft Office, every row
// lies inside its group, and the rows are in the card's order: largest
// first, then by path.
func TestRescue(t *testing.T) {
	g := Corpus().GroundTruth()
	bytes := map[string]int64{}
	for _, r := range g.Rescue {
		for _, e := range g.Entries {
			if e.Size != nil && (e.Path == r.Path || strings.HasPrefix(e.Path, r.Path+"/")) {
				bytes[r.Path] += *e.Size
			}
		}
	}
	want := Rescue{Path: programs + "/Microsoft Office/OFFICE11/Meu orcamento casamento.xls", Group: programs + "/Microsoft Office"}
	if !slices.Contains(g.Rescue, want) {
		t.Errorf("rescue %v lacks %v", g.Rescue, want)
	}
	for i, r := range g.Rescue {
		if !strings.HasPrefix(r.Path, r.Group+"/") {
			t.Errorf("rescue row %s outside its group %s", r.Path, r.Group)
		}
		if i == 0 {
			continue
		}
		prev := g.Rescue[i-1]
		if bytes[r.Path] > bytes[prev.Path] || bytes[r.Path] == bytes[prev.Path] && r.Path < prev.Path {
			t.Errorf("rescue row %s (%d bytes) after %s (%d bytes)", r.Path, bytes[r.Path], prev.Path, bytes[prev.Path])
		}
	}
	if len(g.Rescue) != len(rescueDeclarations) {
		t.Errorf("%d rescue rows, %d declared", len(g.Rescue), len(rescueDeclarations))
	}
}

// A rescue declaration outside its group is refused.
func TestRescueDeclarationChecked(t *testing.T) {
	d := newDef()
	d.file("a/x", at(2005, 1, 1, 0, 0), []byte("x"))
	d.file("b/y", at(2005, 1, 1, 0, 0), []byte("y"))
	defer func() {
		if recover() == nil {
			t.Error("a row outside its group was accepted")
		}
	}()
	d.finish(nil, nil, []rescueDecl{{"a", "b/y"}})
}

// The large-file fixtures are at least 16 MiB, in pairs of one size whose
// samples and content compare as their case says, with correct digests.
func TestLargeFiles(t *testing.T) {
	f := synthfs.New()
	files := AddLargeFiles(f.Root("/src").Dir("grandes"), at(2012, 1, 1, 0, 0))
	dir, err := f.OpenRoot("/src")
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	info, err := dir.Lstat([]byte("grandes"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := dir.OpenDir([]byte("grandes"), info)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	const sample = 64 << 10
	samples := func(lf LargeFile) []byte {
		info, err := sub.Lstat([]byte(lf.Name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size != lf.Size {
			t.Fatalf("%s: %d bytes, want %d", lf.Name, info.Size, lf.Size)
		}
		file, err := sub.OpenFile([]byte(lf.Name), info)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		var out []byte
		for _, off := range []int64{0, lf.Size/2 - sample/2, lf.Size - sample} {
			buf := make([]byte, sample)
			if _, err := file.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			out = append(out, buf...)
		}
		return out
	}

	byCase := map[string][]LargeFile{}
	sizes := map[int64]int{}
	for _, lf := range files {
		if lf.Size < 16<<20 {
			t.Errorf("%s: %d bytes", lf.Name, lf.Size)
		}
		byCase[lf.Case] = append(byCase[lf.Case], lf)
		sizes[lf.Size]++
		buf := make([]byte, 1<<20)
		if got, err := digest(sub, lf.Name, buf); err != nil || got != lf.SHA256 {
			t.Errorf("%s: digest %s (%v), want %s", lf.Name, got, err, lf.SHA256)
		}
	}
	for c, want := range map[string][2]bool{ // equal samples, equal content
		LargeEqualSamples:     {true, false},
		LargeDifferentSamples: {false, false},
		LargeIdentical:        {true, true},
	} {
		pair := byCase[c]
		if len(pair) != 2 || pair[0].Size != pair[1].Size || sizes[pair[0].Size] != 2 {
			t.Errorf("%s: not a pair of one size of its own: %+v", c, pair)
			continue
		}
		if got := bytes.Equal(samples(pair[0]), samples(pair[1])); got != want[0] {
			t.Errorf("%s: samples equal %v", c, got)
		}
		if got := pair[0].SHA256 == pair[1].SHA256; got != want[1] {
			t.Errorf("%s: content equal %v", c, got)
		}
	}
}
