package corpus

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
)

// realDirs holds each tree written once to a real directory by TestMain.
var realDirs = map[string]string{}

var trees = map[string]func() *Tree{"corpus": Corpus, "fat": FATFixture}

func TestMain(m *testing.M) {
	base, err := os.MkdirTemp("", "corpus-test-")
	if err != nil {
		panic(err)
	}
	for name, tree := range trees {
		dir := filepath.Join(base, name)
		if _, err := WriteDir(dir, tree()); err != nil {
			panic(err)
		}
		realDirs[name] = dir
	}
	code := m.Run()
	if err := os.Chmod(filepath.Join(realDirs["corpus"], "privado"), 0o755); err != nil {
		panic(err)
	}
	if err := os.RemoveAll(base); err != nil {
		panic(err)
	}
	os.Exit(code)
}

// seen is what a walk observes of one entry.
type seen struct {
	kind       domain.EntryKind
	size       int64 // files only
	mtime      time.Time
	unreadable bool
}

// walkSynth lists the synthfs tree at root through fsaccess, as a scan does.
func walkSynth(t *testing.T, f *synthfs.FS, root string) map[string]seen {
	t.Helper()
	out := map[string]seen{}
	d, err := f.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	var walk func(d fsaccess.Dir, prefix string)
	walk = func(d fsaccess.Dir, prefix string) {
		defer d.Close()
		for {
			batch, err := d.ReadBatch(64)
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range batch {
				info, err := d.Lstat(e.Name)
				if err != nil {
					t.Fatal(err)
				}
				p := prefix + string(e.Name)
				s := seen{kind: info.Kind}
				switch info.Kind {
				case domain.EntryFile:
					s.size, s.mtime = info.Size, info.ModTime
				case domain.EntryDirectory:
					s.mtime = info.ModTime
					sub, err := d.OpenDir(e.Name, info)
					if o, _ := fsaccess.OutcomeOf(err); o == domain.OutcomeUnreadable {
						s.unreadable = true
					} else if err != nil {
						t.Fatal(err)
					} else {
						walk(sub, p+"/")
					}
				}
				out[p] = s
			}
		}
	}
	walk(d, "")
	return out
}

// walkReal lists the real tree at dir with filepath.WalkDir. Folders the
// ground truth calls unreadable are recorded and not entered, so the result
// is the same when running as root, who can list them.
func walkReal(t *testing.T, dir string, unreadable map[string]bool) map[string]seen {
	t.Helper()
	out := map[string]seen{}
	err := filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		p := filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		s := seen{}
		switch {
		case d.Type().IsRegular():
			s.kind, s.size, s.mtime = domain.EntryFile, info.Size(), info.ModTime().UTC()
		case d.IsDir():
			s.kind, s.mtime = domain.EntryDirectory, info.ModTime().UTC()
		case d.Type()&fs.ModeSymlink != 0:
			s.kind = domain.EntrySymlink
		default:
			return fmt.Errorf("unexpected entry %s of type %v", name, d.Type())
		}
		if unreadable[p] {
			s.unreadable = true
			out[p] = s
			return filepath.SkipDir
		}
		out[p] = s
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// truth maps a ground truth to what walks must see (without times).
func truth(t *testing.T, g GroundTruth) map[string]seen {
	t.Helper()
	out := map[string]seen{}
	for _, e := range g.Entries {
		raw, err := e.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		s := seen{kind: e.Kind, unreadable: e.Unreadable}
		if e.Kind == domain.EntryFile {
			if e.Size == nil {
				t.Fatalf("%s: file without size", e.Path)
			}
			s.size = *e.Size
		} else if e.Size != nil {
			t.Fatalf("%s: %s with a size", e.Path, e.Kind)
		}
		if _, dup := out[string(raw)]; dup {
			t.Fatalf("%s: listed twice", e.Path)
		}
		out[string(raw)] = s
	}
	return out
}

func unreadableSet(g GroundTruth) map[string]bool {
	out := map[string]bool{}
	for _, e := range g.Entries {
		if e.Unreadable {
			raw, _ := e.RawPath()
			out[string(raw)] = true
		}
	}
	return out
}

func withoutTimes(m map[string]seen) map[string]seen {
	out := make(map[string]seen, len(m))
	for p, s := range m {
		s.mtime = time.Time{}
		out[p] = s
	}
	return out
}

func diff(t *testing.T, what string, got, want map[string]seen) {
	t.Helper()
	var problems []string
	for p, w := range want {
		if g, ok := got[p]; !ok {
			problems = append(problems, fmt.Sprintf("missing %q", p))
		} else if g != w {
			problems = append(problems, fmt.Sprintf("%q: got %+v, want %+v", p, g, w))
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			problems = append(problems, fmt.Sprintf("unexpected %q", p))
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		t.Errorf("%s: %d differences:\n%s", what, len(problems), strings.Join(problems[:min(len(problems), 20)], "\n"))
	}
}

// Both builders produce the same paths, kinds, sizes, and modification
// times, and exactly the ground truth's entries.
func TestBuildersAgree(t *testing.T) {
	for name, tree := range trees {
		t.Run(name, func(t *testing.T) {
			f := synthfs.New()
			_, g := BuildSynth(f, "/src", tree())
			synth := walkSynth(t, f, "/src")
			onDisk := walkReal(t, realDirs[name], unreadableSet(g))
			diff(t, "real directory vs synthfs", onDisk, synth)
			diff(t, "synthfs vs ground truth", withoutTimes(synth), truth(t, g))
			if len(g.Entries) == 0 {
				t.Fatal("empty ground truth")
			}
		})
	}
}

// Every ground-truth path exists on disk with its kind, and the synthfs
// builder's ground truth is the real one's.
func TestGroundTruthPathsExist(t *testing.T) {
	g := Corpus().GroundTruth()
	_, sg := BuildSynth(synthfs.New(), "/src", Corpus())
	if !reflect.DeepEqual(g, sg) {
		t.Fatal("synthfs ground truth differs from the tree's")
	}
	for _, e := range g.Entries {
		raw, _ := e.RawPath()
		info, err := os.Lstat(filepath.Join(realDirs["corpus"], filepath.FromSlash(string(raw))))
		if err != nil {
			t.Errorf("%s: %v", e.Path, err)
			continue
		}
		var kind domain.EntryKind
		switch {
		case info.Mode().IsRegular():
			kind = domain.EntryFile
		case info.IsDir():
			kind = domain.EntryDirectory
		case info.Mode()&fs.ModeSymlink != 0:
			kind = domain.EntrySymlink
		}
		if kind != e.Kind {
			t.Errorf("%s: kind %s on disk, %s in the ground truth", e.Path, kind, e.Kind)
		}
	}
}

func truthPaths(g GroundTruth, prefix string, kind domain.EntryKind) []string {
	var out []string
	for _, e := range g.Entries {
		raw, _ := e.RawPath()
		if rel, ok := strings.CutPrefix(string(raw), prefix); ok && e.Kind == kind {
			out = append(out, rel)
		}
	}
	return out
}

// The version families and the photo copy are present as §15 describes.
func TestFamiliesAndCopies(t *testing.T) {
	g := Corpus().GroundTruth()
	docs := truthPaths(g, "Documentos/", domain.EntryFile)
	for _, want := range []string{
		"curriculo.doc", "curriculo_final.doc", "curriculo_final2.doc", "curriculo (1).doc",
		"TCC/TCC_versao_final.doc", "TCC/TCC_versao_final2.doc",
		"senhas.txt", "Nova pasta/Nova pasta (2)/teste.txt",
	} {
		if !slices.Contains(docs, want) {
			t.Errorf("Documentos/%s missing", want)
		}
	}
	for year := 2006; year <= 2012; year++ {
		if !slices.ContainsFunc(docs, func(p string) bool {
			return strings.HasPrefix(p, fmt.Sprintf("Imposto de Renda/IRPF%d/", year))
		}) {
			t.Errorf("no IRPF%d", year)
		}
	}

	fotos := truthPaths(g, "Fotos/", domain.EntryFile)
	copia := truthPaths(g, "Fotos - Copia/", domain.EntryFile)
	var missing, extra []string
	for _, p := range fotos {
		if !slices.Contains(copia, p) {
			missing = append(missing, p)
		}
	}
	for _, p := range copia {
		if !slices.Contains(fotos, p) {
			extra = append(extra, p)
		}
	}
	if len(missing) != 3 || !slices.Equal(extra, []string{"2006/Praia/DSC_editada.JPG"}) || len(copia) < 30 {
		t.Errorf("Fotos - Copia: %d files, missing %q, extra %q", len(copia), missing, extra)
	}
}

func readReal(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(realDirs["corpus"], filepath.FromSlash(p)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Copies are byte-identical on disk; the edited files differ.
func TestDuplicatesByteIdentical(t *testing.T) {
	same := [][2]string{
		{"Downloads/Setup.exe", "Downloads/Setup(1).exe"},
		{"ISOs/Windows XP Professional SP2.iso", "ISOs/copia/Windows XP Professional SP2.iso"},
		{meusDocs + "/curriculo.doc", "Documentos/curriculo.doc"},
		{meusDocs + "/curriculo.doc", "Documentos/curriculo (1).doc"},
		{natal + "/DSC00101.JPG", "Fotos/2004/Natal/DSC00101.JPG"},
		{programs + "/Winamp/winamp.exe", oldCopy + "/Arquivos de programas/Winamp/winamp.exe"},
		{"Downloads/mp3/Skank - Garota Nacional.mp3", "Musicas/Skank/Skank - Garota Nacional.mp3"},
		{"Projetos/site_antigo/index.php", "Projetos/site_antigo_copia/index.php"},
		{"Fotos/2005/Carnaval/DSC01001.JPG", "Downloads/fotos_2005_do_pendrive/Carnaval/DSC01001.JPG"},
	}
	g := Corpus().GroundTruth()
	for _, p := range truthPaths(g, "Fotos - Copia/", domain.EntryFile) {
		if p != "2006/Praia/DSC_editada.JPG" {
			same = append(same, [2]string{"Fotos/" + p, "Fotos - Copia/" + p})
		}
	}
	for _, p := range truthPaths(g, "Downloads/mp3/", domain.EntryFile) {
		artist, _, _ := strings.Cut(p, " - ")
		same = append(same, [2]string{"Downloads/mp3/" + p, "Musicas/" + artist + "/" + p})
	}
	for _, pair := range same {
		if !bytes.Equal(readReal(t, pair[0]), readReal(t, pair[1])) {
			t.Errorf("%s and %s differ", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]string{
		{"Projetos/site_antigo/contato.php", "Projetos/site_antigo_copia/contato.php"},
		{meusDocs + "/TCC_rascunho.doc", oldCopy + "/Documents and Settings/Joao/Meus documentos/TCC_rascunho.doc"},
		{"Fotos/2006/Praia/DSC02003.JPG", "Fotos - Copia/2006/Praia/DSC_editada.JPG"},
	} {
		if bytes.Equal(readReal(t, pair[0]), readReal(t, pair[1])) {
			t.Errorf("%s and %s are identical", pair[0], pair[1])
		}
	}
}

// The zips hold exactly their unpacked folders' files, byte for byte.
func TestZipsMatchUnpacked(t *testing.T) {
	for _, c := range []struct{ zip, dir string }{
		{"Downloads/fotos_2005_do_pendrive.zip", "Downloads/fotos_2005_do_pendrive/"},
		{"Downloads/eMule0.47c-Installer.zip", "Downloads/"},
	} {
		b := readReal(t, c.zip)
		z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		if len(z.File) < 3 {
			t.Fatalf("%s: %d members", c.zip, len(z.File))
		}
		for _, m := range z.File {
			rc, err := m.Open()
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, readReal(t, c.dir+m.Name)) {
				t.Errorf("%s: member %s differs from its unpacked copy", c.zip, m.Name)
			}
		}
	}
	if n := len(truthPaths(Corpus().GroundTruth(), "Downloads/emule-0.47c/", domain.EntryFile)); n < 3 {
		t.Errorf("emule-0.47c has %d files", n)
	}
}

// The assertion table uses the D9 vocabulary and agrees with D9's triage and
// group rules.
func TestExpectationsFollowD9(t *testing.T) {
	categories := []string{personalMedia, documents, sourceProject, applicationUserData,
		applicationInstallation, applicationConfiguration, osInstallation, installerDownload,
		systemJunk, cache, temporaryData, generatedArtifacts,
		downloadCollection, backup, mixed, unknown}
	fileKinds := []string{kindImage, kindVideo, kindAudio, kindDocument, kindSource,
		kindArchive, kindInstaller, kindExecutable, kindSystem, kindOther}
	triageOf := func(category string) string {
		switch category {
		case personalMedia, documents, sourceProject, applicationUserData:
			return keep
		case systemJunk, cache, temporaryData, generatedArtifacts, installerDownload, applicationInstallation:
			return discard
		}
		return review
	}
	groups := []string{applicationInstallation, osInstallation, sourceProject, applicationUserData, cache, generatedArtifacts, backup}
	if len(categories) != 16 || len(fileKinds) != 10 {
		t.Fatal("vocabulary size")
	}
	kinds := map[string]domain.EntryKind{}
	for _, e := range Corpus().GroundTruth().Entries {
		raw, _ := e.RawPath()
		kinds[string(raw)] = e.Kind
	}
	seenPath := map[string]bool{}
	for _, x := range expectations {
		kind, ok := kinds[x.path]
		if !ok || seenPath[x.path] {
			t.Errorf("%s: not in the ground truth, or asserted twice", x.path)
			continue
		}
		seenPath[x.path] = true
		if x.fileKind != "" && (!slices.Contains(fileKinds, x.fileKind) || kind != domain.EntryFile) {
			t.Errorf("%s: file kind %q on a %s", x.path, x.fileKind, kind)
		}
		if x.category == "" {
			if x.triage != "" || x.group || x.veto || x.fileKind == "" {
				t.Errorf("%s: asserts nothing, or triage without a category", x.path)
			}
			continue
		}
		if !slices.Contains(categories, x.category) {
			t.Errorf("%s: category %q", x.path, x.category)
		}
		want := triageOf(x.category)
		recovery := strings.HasSuffix(x.path, ".CHK") || x.path == "found.000"
		if x.veto || (recovery && x.category == systemJunk) {
			want = review
		}
		if x.triage != want {
			t.Errorf("%s: triage %s, D9 gives %s", x.path, x.triage, want)
		}
		if x.veto && triageOf(x.category) != discard {
			t.Errorf("%s: veto on a category that is not discard", x.path)
		}
		if g := kind == domain.EntryDirectory && slices.Contains(groups, x.category); x.group != g {
			t.Errorf("%s: group %v, D9 gives %v", x.path, x.group, g)
		}
	}

	gt := Corpus().GroundTruth()
	asserted := 0
	for _, e := range gt.Entries {
		if e.Category != "" {
			asserted++
			if e.Triage == "" || e.Group == nil || e.Veto == nil {
				t.Errorf("%s: partial assertion", e.Path)
			}
		}
	}
	if asserted < 40 {
		t.Errorf("only %d entries asserted", asserted)
	}
}

// The unreadable folder cannot be listed through synthfs, nor on disk unless
// running as root.
func TestUnreadableFolder(t *testing.T) {
	f := synthfs.New()
	_, g := BuildSynth(f, "/src", Corpus())
	if !reflect.DeepEqual(unreadableSet(g), map[string]bool{"privado": true}) {
		t.Fatalf("unreadable folders %v", unreadableSet(g))
	}
	if walkSynth(t, f, "/src")["privado"] != (seen{kind: domain.EntryDirectory, mtime: at(2011, 3, 12, 23, 0), unreadable: true}) {
		t.Error("synthfs listed privado")
	}
	if len(truthPaths(g, "privado/", domain.EntryFile)) != 0 {
		t.Error("ground truth lists the contents of privado")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can list a mode-000 folder")
	}
	_, err := os.ReadDir(filepath.Join(realDirs["corpus"], "privado"))
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("listing privado on disk: %v", err)
	}
}

// The definition is the same on every build.
func TestDeterministic(t *testing.T) {
	a, b := buildCorpus(), buildCorpus()
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(a, Corpus()) {
		t.Fatal("two builds of the corpus differ")
	}
}

// The viewer fixtures are what R1.13 opens.
func TestViewerFixtures(t *testing.T) {
	if _, err := jpeg.Decode(bytes.NewReader(readReal(t, "Midia/foto.jpg"))); err != nil {
		t.Errorf("foto.jpg: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(readReal(t, "Fotos/2006/Praia/DSC02001.JPG"))); err != nil {
		t.Errorf("DSC02001.JPG: %v", err)
	}
	for name, b := range map[string][]byte{"video.mp4": videoMP4, "audio.mp3": audioMP3, "documento.pdf": documentPDF} {
		if len(b) == 0 || len(b) > 50_000 {
			t.Errorf("embedded %s has %d bytes", name, len(b))
		}
	}
	if !bytes.Equal(readReal(t, "Midia/video.mp4")[4:8], []byte("ftyp")) {
		t.Error("video.mp4 is not an MP4")
	}
	if b := readReal(t, "Midia/musica.mp3"); !bytes.HasPrefix(b, []byte("ID3")) && b[0] != 0xff {
		t.Error("musica.mp3 is not an MP3")
	}
	if b := readReal(t, "Midia/documento.pdf"); !bytes.HasPrefix(b, []byte("%PDF-")) || !bytes.HasSuffix(b, []byte("%%EOF\n")) {
		t.Error("documento.pdf is not a PDF")
	}
	notas := string(readReal(t, "Midia/notas.md"))
	if !strings.Contains(notas, "<script>") || !strings.Contains(notas, "onerror=") {
		t.Error("notas.md lacks its script and onerror")
	}
	for _, p := range []string{"Midia/pagina.html", "Midia/desenho.svg"} {
		if !strings.Contains(string(readReal(t, p)), "<script") {
			t.Errorf("%s has no script", p)
		}
	}
	carta := readReal(t, "Midia/carta_1252.txt")
	if utf8.Valid(carta) || !bytes.Contains(carta, []byte("mar\xe7o")) || !bytes.Contains(carta, []byte{0x80}) {
		t.Error("carta_1252.txt is not Windows-1252 text")
	}
	for _, e := range Corpus().GroundTruth().Entries {
		if e.PathB64 == "RG9jdW1lbnRvcy9m6S50eHQ=" {
			if e.Path != `Documentos/f\xE9.txt` {
				t.Errorf("display path %q", e.Path)
			}
			return
		}
	}
	t.Error("no Documentos/f\\xe9.txt")
}

// The FAT fixture has a dozen entries, FAT-safe names, and even-second times.
func TestFATFixture(t *testing.T) {
	tree := FATFixture()
	if n := len(tree.items); n < 12 || n > 16 {
		t.Errorf("%d entries", n)
	}
	for _, it := range tree.items {
		if it.mtime.Second()%2 != 0 || it.mtime.Nanosecond() != 0 {
			t.Errorf("%s: mtime %v is not on an even second", it.path, it.mtime)
		}
		if !utf8.ValidString(it.path) || strings.ContainsAny(it.path, `\:*?"<>|`) || it.kind != domain.EntryDirectory && it.kind != domain.EntryFile {
			t.Errorf("%s: not FAT-safe", it.path)
		}
	}
	if tree.rootMTime.Second()%2 != 0 {
		t.Error("root mtime not on an even second")
	}
}

// The corpus stays small enough for CI, with every mtime in 2003-2012.
func TestSizeAndDates(t *testing.T) {
	tree := Corpus()
	if size := tree.Size(); size > 40<<20 || size < 10<<20 {
		t.Errorf("corpus is %d bytes", size)
	}
	for _, it := range tree.items {
		if it.kind != domain.EntrySymlink && (it.mtime.Year() < 2003 || it.mtime.Year() > 2012) {
			t.Errorf("%s: mtime %v", it.path, it.mtime)
		}
	}
}
