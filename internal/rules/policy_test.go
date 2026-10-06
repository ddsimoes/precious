package rules

import (
	"fmt"
	"slices"
	"testing"

	"precious/internal/corpus"
	"precious/internal/domain"
)

// want is an expected classification of one entry of a tree.
type want struct {
	path     string
	category domain.Category
	triage   domain.Triage
	group    bool
	veto     bool
}

func checkTree(t *testing.T, got map[string]classified, wants ...want) {
	t.Helper()
	for _, w := range wants {
		g, ok := got[w.path]
		if !ok {
			t.Errorf("%s: not in the tree", w.path)
			continue
		}
		if g.Category != w.category || g.Triage != w.triage || g.Group != w.group || g.Veto != w.veto {
			t.Errorf("%s = %s/%s group %v veto %v (rules %v, %s); want %s/%s group %v veto %v",
				w.path, g.Category, g.Triage, g.Group, g.Veto, g.Rules, g.Reason, w.category, w.triage, w.group, w.veto)
		}
	}
}

// Task 3.3: each §8 example gets its §8 category from facts the scanner
// gives.
func TestRulesSection8(t *testing.T) {
	p := Default()
	tree := files(
		"RECYCLER/S-1-5-21-123/INFO2:820",
		"RECYCLER/S-1-5-21-123/desktop.ini:62",
		"$RECYCLE.BIN/S-1-5-21-1/$R0Q2.doc:9000",
		".Trash-1000/files/x.txt",
		"System Volume Information/tracking.log:20480",
		"Fotos/Thumbs.db:9216",
		"Fotos/a/.DS_Store:6148",
		"Downloads/setup.exe:210000",
		"Downloads/GetRight-install.exe:210000",
		"Downloads/winamp5_full.exe:450000",
		"Downloads/pacote.msi:260000",
		"Downloads/x.iso:700000",
		"Downloads/y.part:80000",
		"Downloads/z.crdownload:80000",
		"Downloads/w.partial:80000",
		"Downloads/disc.nrg:700000",
		"Downloads/disc.img:700000",
		"Downloads/retro/jogo.bin:705600",
		"Downloads/retro/jogo.cue:70",
		"Downloads/retro/dados.bin:120000",
		"Temp/~WRL0003.tmp:12000",
		"tmp/x.tmp:10",
		"docs/~$curriculo.doc:162",
		"Cache/data_1:4000",
		"Temporary Internet Files/Content.IE5/X1/a.gif:83",
		"proj/package.json:900",
		"proj/node_modules/lodash/package.json:700",
		"proj/node_modules/lodash/lodash.js:140000",
		"proj/__pycache__/m.cpython-311.pyc:4000",
		"C/Arquivos de programas/Winamp/winamp.exe:160000",
		"C/Arquivos de programas/Winamp/winamp.hlp:24000",
		"C/Arquivos de programas/Winamp/winamp.ico:2238",
		"C/Arquivos de programas/Winamp/Plugins/in_mp3.dll:48000",
		"C/Arquivos de programas/Winamp/Skins/base.bmp:11000",
		"C/Arquivos de programas/App/unins000.exe:70000",
		"C/Arquivos de programas/App/readme.txt:90000",
		"C/WINDOWS/explorer.exe:100000",
		"C/WINDOWS/win.ini:300",
		"C/WINDOWS/system32/kernel32.dll:80000",
		"C/WINDOWS/system32/drivers/etc/hosts:700",
		"C/Documents and Settings/Joao/Meus documentos/carta.doc:30000",
		"cd/I386/setupldr.bin:300000",
		"keys/id_rsa:1700",
		"keys/cofre.kdbx:30000",
		"keys/cert.pfx:3000",
		"Jogos/NFS/save/Joao/profile.sav:48000",
		"Mozilla/Profiles/x.default/prefs.js:2000",
		"Mozilla/Profiles/x.default/places.sqlite:20000",
		"Mail/Outlook.pst:200000",
		"DCIM/100MEDIA/IMAG0001.jpg:50000",
		"found.000/FILE0000.CHK:8192",
		"recuperados/FILE0001.CHK:4096",
	)
	got := classifyTree(p, tree)
	checkTree(t, got,
		// System junk.
		want{"RECYCLER", domain.CategorySystemJunk, domain.TriageDiscard, false, false},
		want{"$RECYCLE.BIN", domain.CategorySystemJunk, domain.TriageReview, false, true}, // a deleted document vetoes
		want{".Trash-1000", domain.CategorySystemJunk, domain.TriageDiscard, false, false},
		want{"System Volume Information", domain.CategorySystemJunk, domain.TriageDiscard, false, false},
		want{"Fotos/Thumbs.db", domain.CategorySystemJunk, domain.TriageDiscard, false, false},
		want{"Fotos/a/.DS_Store", domain.CategorySystemJunk, domain.TriageDiscard, false, false},
		want{"RECYCLER/S-1-5-21-123/desktop.ini", domain.CategorySystemJunk, domain.TriageDiscard, false, false},
		want{"found.000", domain.CategorySystemJunk, domain.TriageReview, false, false},
		want{"found.000/FILE0000.CHK", domain.CategorySystemJunk, domain.TriageReview, false, false},
		want{"recuperados/FILE0001.CHK", domain.CategorySystemJunk, domain.TriageReview, false, false},
		// Temporary data, caches, partial downloads.
		want{"Temp", domain.CategoryTemporaryData, domain.TriageDiscard, false, false},
		want{"tmp/x.tmp", domain.CategoryTemporaryData, domain.TriageDiscard, false, false},
		want{"docs/~$curriculo.doc", domain.CategoryTemporaryData, domain.TriageDiscard, false, false},
		want{"Cache", domain.CategoryCache, domain.TriageDiscard, true, false},
		want{"Temporary Internet Files", domain.CategoryCache, domain.TriageDiscard, true, false},
		want{"Downloads/y.part", domain.CategoryTemporaryData, domain.TriageDiscard, false, false},
		want{"Downloads/z.crdownload", domain.CategoryTemporaryData, domain.TriageDiscard, false, false},
		want{"Downloads/w.partial", domain.CategoryTemporaryData, domain.TriageDiscard, false, false},
		// Installers and disk images.
		want{"Downloads/setup.exe", domain.CategoryInstallerDownload, domain.TriageDiscard, false, false},
		want{"Downloads/GetRight-install.exe", domain.CategoryInstallerDownload, domain.TriageDiscard, false, false},
		want{"Downloads/winamp5_full.exe", domain.CategoryInstallerDownload, domain.TriageDiscard, false, false},
		want{"Downloads/pacote.msi", domain.CategoryInstallerDownload, domain.TriageDiscard, false, false},
		want{"Downloads/x.iso", domain.CategoryInstallerDownload, domain.TriageDiscard, false, false},
		want{"Downloads/disc.nrg", domain.CategoryInstallerDownload, domain.TriageDiscard, false, false},
		want{"Downloads/disc.img", domain.CategoryInstallerDownload, domain.TriageDiscard, false, false},
		want{"Downloads/retro/jogo.bin", domain.CategoryInstallerDownload, domain.TriageDiscard, false, false},
		want{"Downloads/retro/dados.bin", domain.CategoryUnknown, domain.TriageReview, false, false},
		want{"Downloads", domain.CategoryDownloadCollection, domain.TriageReview, false, false},
		// Applications and operating systems.
		want{"C/Arquivos de programas/Winamp", domain.CategoryApplicationInstallation, domain.TriageDiscard, true, false},
		want{"C/Arquivos de programas/App", domain.CategoryApplicationInstallation, domain.TriageDiscard, true, false},
		want{"C/Arquivos de programas", domain.CategoryMixed, domain.TriageReview, false, false},
		want{"C/WINDOWS", domain.CategoryOSInstallation, domain.TriageReview, true, false},
		want{"C/WINDOWS/system32", domain.CategoryOSInstallation, domain.TriageReview, true, false},
		want{"cd/I386", domain.CategoryOSInstallation, domain.TriageReview, true, false},
		want{"C", domain.CategoryBackup, domain.TriageReview, true, false},
		// Generated artifacts.
		want{"proj/node_modules", domain.CategoryGeneratedArtifacts, domain.TriageDiscard, true, false},
		want{"proj/__pycache__", domain.CategoryGeneratedArtifacts, domain.TriageDiscard, true, false},
		want{"proj", domain.CategorySourceProject, domain.TriageKeep, true, false},
		// Personal material.
		want{"keys/id_rsa", domain.CategoryApplicationUserData, domain.TriageKeep, false, false},
		want{"keys/cofre.kdbx", domain.CategoryApplicationUserData, domain.TriageKeep, false, false},
		want{"keys/cert.pfx", domain.CategoryApplicationUserData, domain.TriageKeep, false, false},
		want{"Jogos/NFS/save", domain.CategoryApplicationUserData, domain.TriageKeep, true, false},
		want{"Mozilla/Profiles/x.default", domain.CategoryApplicationUserData, domain.TriageKeep, true, false},
		want{"Mail/Outlook.pst", domain.CategoryApplicationUserData, domain.TriageKeep, false, false},
		want{"DCIM", domain.CategoryPersonalMedia, domain.TriageKeep, false, false},
	)
	if k := got["Downloads/retro/jogo.bin"].FileKind; k != domain.FileKindArchive {
		t.Errorf("jogo.bin beside jogo.cue has kind %s, want archive", k)
	}
	if k := got["Downloads/retro/dados.bin"].FileKind; k != domain.FileKindOther {
		t.Errorf("dados.bin has kind %s, want other", k)
	}
	if tr := got["keys"].Traits; !slices.Contains(tr, domain.TraitContainsCredentials) {
		t.Errorf("keys traits = %v, want contains_credentials", tr)
	}
	if tr := got["Mozilla"].Traits; !slices.Contains(tr, domain.TraitContainsDatabase) || !slices.Contains(tr, domain.TraitContainsUserMaterial) {
		t.Errorf("Mozilla traits = %v, want contains_database and contains_user_material", tr)
	}
	if tr := got["proj"].Traits; !slices.Contains(tr, domain.TraitPossibleGeneratedContent) {
		t.Errorf("proj traits = %v, want possible_generated_content", tr)
	}
}

// Task 3.3: a folder of year folders full of JPEGs is personal media, the
// year folders too, whatever the camera named the files.
func TestRulesYearFoldersOfPhotos(t *testing.T) {
	var specs []string
	for year := 2003; year <= 2009; year++ {
		for i := range 12 {
			specs = append(specs, fmt.Sprintf("Fotos/%d/foto %02d.jpg:%d", year, i, 50_000+i*1_000))
		}
	}
	specs = append(specs, "Fotos/2005/Thumbs.db:20000", "Fotos/2007/video.avi:300000")
	got := classifyTree(Default(), files(specs...))
	checkTree(t, got,
		want{"Fotos", domain.CategoryPersonalMedia, domain.TriageKeep, false, false},
		want{"Fotos/2003", domain.CategoryPersonalMedia, domain.TriageKeep, false, false},
		want{"Fotos/2007", domain.CategoryPersonalMedia, domain.TriageKeep, false, false},
	)
}

// Task 3.3: the icons, skins, and splash screens programs ship are not
// indicators, so an application without user material is suggested for
// discard; a document inside one vetoes that (R1.5).
func TestRulesProgramIconsAreNotIndicators(t *testing.T) {
	p := Default()
	for _, name := range []string{"winamp.ico", "kl.ico", "splash.bmp", "base.bmp", "logo.gif", "toolbar.png", "icon.svg"} {
		for _, s := range p.AppendNameSignals(nil, []byte(name), domain.EntryFile) {
			if p.IsIndicator(s) {
				t.Errorf("%s raises the indicator %s", name, s)
			}
		}
	}
	got := classifyTree(p, files(
		"Arquivos de programas/Kazaa Lite/kazaalite.exe:115000",
		"Arquivos de programas/Kazaa Lite/kl.ico:2238",
		"Arquivos de programas/Kazaa Lite/splash.bmp:20000",
		"Arquivos de programas/Kazaa Lite/bin/kpp.dll:40000",
		"Arquivos de programas/Microsoft Office/OFFICE11/EXCEL.EXE:190000",
		"Arquivos de programas/Microsoft Office/OFFICE11/XLINTL32.DLL:45000",
		"Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls:38400",
	))
	checkTree(t, got,
		want{"Arquivos de programas/Kazaa Lite", domain.CategoryApplicationInstallation, domain.TriageDiscard, true, false},
		want{"Arquivos de programas/Microsoft Office", domain.CategoryApplicationInstallation, domain.TriageReview, true, true},
	)
	if n := got["Arquivos de programas/Kazaa Lite"].Indicators; n != 0 {
		t.Errorf("Kazaa Lite has %d indicators, want 0", n)
	}
}

// Task 3.2: a bin, obj, target, or build folder is generated artifacts only
// when compiled output is below it; a program's own bin folder of DLLs stays
// part of the program.
func TestRulesBuildOutputNeedsCompiledOutput(t *testing.T) {
	got := classifyTree(Default(), files(
		"tcc_java/build.xml:400",
		"tcc_java/src/Main.java:1500",
		"tcc_java/bin/br/Main.class:2000",
		"tcc_java/bin/br/Grafo.class:2300",
		"tcc_java/bin/br/Aresta.class:2600",
		"c_proj/Makefile:300",
		"c_proj/build/main.o:9000",
		"c_proj/build/util.o:9000",
		"c_proj/build/io.o:9000",
		"c_proj/obj/a.obj:9000",
		"c_proj/target/x.class:100",
		"ICQ/icq.exe:130000",
		"ICQ/bin/icqcore.dll:70000",
		"ICQ/bin/icqnet.dll:35000",
		"ICQ/bin/icqateimg.dll:22000",
	))
	checkTree(t, got,
		want{"tcc_java/bin", domain.CategoryGeneratedArtifacts, domain.TriageDiscard, true, false},
		want{"c_proj/build", domain.CategoryGeneratedArtifacts, domain.TriageDiscard, true, false},
		want{"tcc_java", domain.CategorySourceProject, domain.TriageKeep, true, false},
		want{"ICQ", domain.CategoryApplicationInstallation, domain.TriageDiscard, true, false},
		want{"ICQ/bin", domain.CategoryApplicationInstallation, domain.TriageDiscard, true, false},
	)
	for _, p := range []string{"c_proj/obj", "c_proj/target"} { // below the minimum count
		if c := got[p].Category; c == domain.CategoryGeneratedArtifacts {
			t.Errorf("%s with one compiled file is %s", p, c)
		}
	}
}

// A Downloads folder stays a downloads folder when an exported bookmarks file
// or a mail archive lands at its top; only a real profile folder is a profile
// (owner smoke test: a laptop backup's Downloads/Downloads read as a profile).
func TestRulesDownloadsWinOverProfileFiles(t *testing.T) {
	got := classifyTree(Default(), files(
		"Downloads/bookmarks.html:40000",
		"Downloads/backup-email.pst:900000",
		"Downloads/setup.exe:210000",
		"Downloads/foto.jpg:300000",
		"x.default/prefs.js:9000",
		"x.default/bookmarks.html:40000",
		"x.default/places.sqlite:500000",
	))
	checkTree(t, got,
		want{"Downloads", domain.CategoryDownloadCollection, domain.TriageReview, false, false},
		want{"x.default", domain.CategoryApplicationUserData, domain.TriageKeep, true, false},
	)
}

// Every rule's explanation is a plain sentence, and Explain finds it.
func TestRulesExplanations(t *testing.T) {
	p := Default()
	for _, rules := range [][]rule{p.fileRules, p.folderRules} {
		for _, r := range rules {
			e := p.Explain([]string{r.id})[0]
			if e == "" || e[len(e)-1] != '.' || e[0] < 'A' || e[0] > 'Z' {
				t.Errorf("rule %s explains %q; want a sentence", r.id, e)
			}
		}
	}
}

// R1.4 and R1.5 ahead of the scanner: the regression corpus, classified
// bottom-up from its ground truth the way the scanner will, gets every
// category, triage, group, veto, and file kind its expectation table asserts.
func TestRulesCorpusGroundTruth(t *testing.T) {
	gt := corpus.Corpus().GroundTruth()
	nodes := make([]node, 0, len(gt.Entries))
	for _, e := range gt.Entries {
		raw, err := e.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		n := node{path: string(raw), kind: e.Kind}
		if e.Size != nil {
			n.size = *e.Size
		}
		nodes = append(nodes, n)
	}
	got := classifyTree(Default(), nodes)
	asserted := 0
	for _, e := range gt.Entries {
		raw, _ := e.RawPath()
		g := got[string(raw)]
		if e.Category != "" {
			asserted++
			if string(g.Category) != e.Category || string(g.Triage) != e.Triage || g.Group != *e.Group || g.Veto != *e.Veto {
				t.Errorf("%s = %s/%s group %v veto %v (rules %v, %s); ground truth %s/%s group %v veto %v",
					e.Path, g.Category, g.Triage, g.Group, g.Veto, g.Rules, g.Reason, e.Category, e.Triage, *e.Group, *e.Veto)
			}
		}
		if e.FileKind != "" {
			asserted++
			if string(g.FileKind) != e.FileKind {
				t.Errorf("%s has file kind %s; ground truth %s", e.Path, g.FileKind, e.FileKind)
			}
		}
	}
	if asserted < 50 {
		t.Errorf("only %d assertions checked; the corpus expectation table has more", asserted)
	}
}
