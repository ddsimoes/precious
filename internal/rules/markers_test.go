package rules

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"precious/internal/domain"
	"precious/policies"
)

// noRules is a valid rules file without rules.
var noRules = []byte("version = \"rules-test\"\n")

// TestEmbeddedPolicy: the embedded files load, and Default is them.
func TestEmbeddedPolicy(t *testing.T) {
	p, err := Load(policies.Markers, policies.Rules)
	if err != nil {
		t.Fatal(err)
	}
	if p.Version() != "rules-v2+markers-v3" || Default().Version() != p.Version() {
		t.Errorf("Version = %q, Default().Version = %q; want rules-v2+markers-v3", p.Version(), Default().Version())
	}
}

// Task 3.1: the embedded markers-v3 file is versioned and reaches
// every file kind but other through its tables.
func TestEmbeddedMarkers(t *testing.T) {
	p := Default()
	if p.markersVersion != "markers-v3" {
		t.Errorf("version = %q, want markers-v3", p.markersVersion)
	}
	reached := map[domain.FileKind]bool{}
	for _, k := range p.kindExt {
		reached[k] = true
	}
	for _, kn := range p.kindNames {
		reached[kn.kind] = true
	}
	for _, k := range domain.FileKinds {
		if k != domain.FileKindOther && !reached[k] {
			t.Errorf("no extension or name maps to %s", k)
		}
	}
}

// Task 3.1: unknown keys and malformed entries are rejected, each named.
func TestMarkersRejectMalformed(t *testing.T) {
	cases := map[string]struct{ markers, want string }{
		"unknown key":          {"version = \"m-v1\"\nmarkers = [\"a\"]\n", `unknown key "markers"`},
		"unknown nested key":   {"version = \"m-v1\"\n[[signals]]\nid = \"a\"\nnames = [\"x\"]\nkind = [\"file\"]\n", `unknown key "signals.kind"`},
		"unknown pair key":     {"version = \"m-v1\"\n[[file_kind_pairs]]\next = \"bin\"\nwith = \"cue\"\nkind = \"archive\"\nsize = 1\n", `unknown key "file_kind_pairs.size"`},
		"missing version":      {"[[signals]]\nid = \"a\"\nnames = [\"x\"]\n", "version"},
		"bad signal id":        {"version = \"m-v1\"\n[[signals]]\nid = \"Bad-ID\"\nnames = [\"x\"]\n", "signals[0].id"},
		"duplicate signal":     {"version = \"m-v1\"\n[[signals]]\nid = \"a\"\nnames = [\"x\"]\n[[signals]]\nid = \"a\"\nnames = [\"y\"]\n", "used by another signal"},
		"unknown entry kind":   {"version = \"m-v1\"\n[[signals]]\nid = \"a\"\nkinds = [\"folder\"]\nnames = [\"x\"]\n", "not an entry kind"},
		"no names":             {"version = \"m-v1\"\n[[signals]]\nid = \"a\"\n", "at least one pattern"},
		"bad except":           {"version = \"m-v1\"\n[[signals]]\nid = \"a\"\nnames = [\"x\"]\nexcept = [\"a/b\"]\n", "signals[0].except[0]"},
		"class pattern":        {"version = \"m-v1\"\n[[signals]]\nid = \"a\"\nnames = [\"*.[ch]\"]\n", "reserved"},
		"slash pattern":        {"version = \"m-v1\"\n[[signals]]\nid = \"a\"\nnames = [\"*/x\"]\n", "single path component"},
		"unknown file kind":    {"version = \"m-v1\"\n[file_kinds]\npicture = [\"jpg\"]\n", `"picture" must be a file kind`},
		"other listed":         {"version = \"m-v1\"\n[file_kinds]\nother = [\"dat\"]\n", "other than other"},
		"upper-case extension": {"version = \"m-v1\"\n[file_kinds]\nimage = [\"JPG\"]\n", "lower-case extension"},
		"dotted extension":     {"version = \"m-v1\"\n[file_kinds]\nimage = [\".jpg\"]\n", "lower-case extension"},
		"duplicate extension":  {"version = \"m-v1\"\n[file_kinds]\nimage = [\"img\"]\narchive = [\"img\"]\n", "already listed"},
		"name kind other":      {"version = \"m-v1\"\n[[file_kind_names]]\nkind = \"other\"\nnames = [\"x\"]\n", "other than other"},
		"name kind no names":   {"version = \"m-v1\"\n[[file_kind_names]]\nkind = \"image\"\n", "at least one pattern"},
		"pair same extension":  {"version = \"m-v1\"\n[[file_kind_pairs]]\next = \"bin\"\nwith = \"bin\"\nkind = \"archive\"\n", "two different"},
		"pair kind other":      {"version = \"m-v1\"\n[[file_kind_pairs]]\next = \"bin\"\nwith = \"cue\"\nkind = \"other\"\n", "other than other"},
		"pair listed ext":      {"version = \"m-v1\"\n[file_kinds]\nimage = [\"bin\"]\n[[file_kind_pairs]]\next = \"bin\"\nwith = \"cue\"\nkind = \"archive\"\n", "never pairs"},
		"65 signals":           {"version = \"m-v1\"\n" + strings.Repeat("[[signals]]\nid = \"s\"\nnames = [\"x\"]\n", 65), "at most 64"},
		"pair twice":           {"version = \"m-v1\"\n[[file_kind_pairs]]\next = \"bin\"\nwith = \"cue\"\nkind = \"archive\"\n[[file_kind_pairs]]\next = \"bin\"\nwith = \"ccd\"\nkind = \"archive\"\n", "paired twice"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load([]byte(c.markers), noRules)
			if err == nil || !strings.Contains(err.Error(), "markers: ") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

// Patterns match ASCII case-insensitively on raw bytes; non-ASCII bytes,
// including invalid UTF-8, match only themselves.
func TestPatternMatching(t *testing.T) {
	r := signalRule{id: "x", kinds: kindBit(domain.EntryFile), names: []pattern{
		mustPattern(t, "*.EXE"), mustPattern(t, "unins???.dat"), mustPattern(t, "caf\xe9*"),
	}, except: []pattern{mustPattern(t, "~$*")}}
	cases := []struct {
		name string
		kind domain.EntryKind
		want bool
	}{
		{"Setup.Exe", domain.EntryFile, true},
		{".exe", domain.EntryFile, true},
		{"setup.exe", domain.EntryDirectory, false}, // kind filter
		{"setup.exe.bak", domain.EntryFile, false},
		{"~$setup.exe", domain.EntryFile, false}, // except
		{"UNINS000.DAT", domain.EntryFile, true},
		{"unins00.dat", domain.EntryFile, false}, // ? is exactly one byte
		{"unins0000.dat", domain.EntryFile, false},
		{"caf\xe9-notes", domain.EntryFile, true},
		{"CAF\xe9", domain.EntryFile, true},
		{"caf\xc9", domain.EntryFile, false}, // no folding outside ASCII
	}
	for _, c := range cases {
		if got := r.matches([]byte(c.name), c.kind); got != c.want {
			t.Errorf("matches(%q, %s) = %v, want %v", c.name, c.kind, got, c.want)
		}
	}
	// A '*' in the name is an ordinary byte; the pattern's '*' still spans it.
	if !mustPattern(t, "a*").match([]byte("a*b")) || !mustPattern(t, "*b").match([]byte("a*b")) {
		t.Error("a literal '*' in a name broke wildcard matching")
	}
	if !mustPattern(t, "*a*b*c").match([]byte("xxaxxbxxbxc")) || mustPattern(t, "*a*b*c").match([]byte("xxaxxbxxbx")) {
		t.Error("backtracking to the last '*' is wrong")
	}
	if got := mustPattern(t, "Caf\xc9*").lowered; string(got) != "caf\xc9*" {
		t.Errorf("pattern lowered to %q; non-ASCII bytes must stay as written", got)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "a\x00b", "a[b", "x\\y", strings.Repeat("a", 256)} {
		if _, err := parsePattern(bad); err == nil {
			t.Errorf("parsePattern(%q) accepted", bad)
		}
	}
	// The fast paths for exact, suffix, and prefix patterns agree with the
	// general matcher.
	pats := []string{"*.exe", "~$*", "Thumbs.db", "*", "**", "a*", "*a", "a*a", "aa*aa", "DSC*.jpg", "*.CHK", "found.???", "unins???.exe", "caf\xe9*", "*\xe9"}
	names := []string{"", "a", "A", "x.exe", "X.EXE", ".exe", "exe", "~$doc", "~$", "~", "thumbs.DB", "Thumbs.db.bak", "found.000", "FILE0000.chk", "caf\xe9", "CAF\xc9", "aa", "aaa", "aaaa", "aaaaa", "unins000.exe", "DSC.jpg", "dsc00101.JPG", "DSC.jpgx"}
	for _, ps := range pats {
		pat := mustPattern(t, ps)
		for _, n := range names {
			if got, want := pat.match([]byte(n)), globMatchFold(pat.lowered, []byte(n)); got != want {
				t.Errorf("pattern %q on %q: fast path %v, general matcher %v", ps, n, got, want)
			}
		}
	}
}

// The signals one name raises, as the scanner sees them, and which of them
// are indicators of user material.
func TestAppendNameSignals(t *testing.T) {
	p := Default()
	cases := []struct {
		name string
		kind domain.EntryKind
		want []SignalID
	}{
		{"saves", domain.EntryDirectory, []SignalID{"save_data_present"}},
		{"Profile.SQLite", domain.EntryFile, []SignalID{"database_present"}},
		{"Thumbs.db", domain.EntryFile, []SignalID{"system_junk_present"}}, // *.db is deliberately not a database pattern
		{"RECYCLER", domain.EntryDirectory, []SignalID{"system_junk_present"}},
		{".Trash-1000", domain.EntryDirectory, []SignalID{"system_junk_present"}},
		{"Carta para Ana.doc", domain.EntryFile, []SignalID{"editable_document_present"}},
		{"~$curriculo.doc", domain.EntryFile, []SignalID{"temporary_name_present"}}, // a lock file is no document
		{"build", domain.EntryDirectory, []SignalID{"generated_output_name_present"}},
		{"build", domain.EntryFile, nil},
		{"Main.class", domain.EntryFile, []SignalID{"compiled_output_present"}},
		{"unins000.exe", domain.EntryFile, []SignalID{"executable_present", "uninstaller_name_present"}},
		{"uninstall.exe", domain.EntryFile, []SignalID{"executable_present", "uninstaller_name_present"}},
		{"Setup.exe", domain.EntryFile, []SignalID{"executable_present", "installer_name_present"}},
		{"Outlook.pst", domain.EntryFile, []SignalID{"mail_store_present"}},
		{"id_rsa", domain.EntryFile, []SignalID{"credential_file_present"}},
		{"AutoRecovery save of Relatorio.asd", domain.EntryFile, []SignalID{"recovery_material_present"}},
		{"FILE0000.CHK", domain.EntryFile, []SignalID{"recovered_fragment_present"}},
		{"found.000", domain.EntryDirectory, []SignalID{"recovered_fragment_present"}},
		{"filme.avi.part", domain.EntryFile, []SignalID{"partial_download_present"}},
		{"Office 2003.iso", domain.EntryFile, []SignalID{"disk_image_present"}},
		{"DSC00101.JPG", domain.EntryFile, []SignalID{"camera_photo_present"}},
		{"IMG_1234.jpeg", domain.EntryFile, []SignalID{"camera_photo_present"}},
		{"P1010001.JPG", domain.EntryFile, []SignalID{"camera_photo_present"}},
		{"DCIM", domain.EntryDirectory, []SignalID{"camera_folder_present"}},
		{"system32", domain.EntryDirectory, []SignalID{"os_image_layout_present"}},
		{"WINDOWS", domain.EntryDirectory, []SignalID{"windows_folder_present"}},
		{"Arquivos de programas", domain.EntryDirectory, []SignalID{"program_files_present"}},
		{"Documents and Settings", domain.EntryDirectory, []SignalID{"user_profiles_present"}},
		// Images programs ship raise nothing, so they are no indicator.
		{"winamp.ico", domain.EntryFile, nil},
		{"splash.bmp", domain.EntryFile, nil},
		{"logo.gif", domain.EntryFile, nil},
	}
	for _, c := range cases {
		got := p.AppendNameSignals(nil, []byte(c.name), c.kind)
		if !slices.Equal(got, c.want) {
			t.Errorf("AppendNameSignals(%q, %s) = %v, want %v", c.name, c.kind, got, c.want)
		}
	}

	indicators := map[SignalID]bool{
		"recovered_fragment_present": true, "save_data_present": true, "profile_data_present": true,
		"database_present": true, "mail_store_present": true, "credential_file_present": true,
		"recovery_material_present": true, "editable_document_present": true,
		"camera_photo_present": true, "camera_folder_present": true,
	}
	for _, s := range p.signals {
		if got := p.IsIndicator(s.id); got != indicators[s.id] {
			t.Errorf("IsIndicator(%s) = %v, want %v", s.id, got, indicators[s.id])
		}
	}
	if p.IsIndicator("no_such_signal") {
		t.Error("an undefined signal is an indicator")
	}
}

// The name indexes of AppendNameSignals give what matching every pattern in
// turn gives, for names made from every pattern of the embedded file.
func TestAppendNameSignalsIndexes(t *testing.T) {
	p := Default()
	var names []string
	for _, s := range p.signals {
		for _, pat := range append(slices.Clone(s.names), s.except...) {
			n := strings.NewReplacer("*", "Ab", "?", "x").Replace(string(pat.lowered))
			names = append(names, n, strings.ToUpper(n), "x"+n, n+"x", strings.Repeat("y", 300)+n)
		}
	}
	for _, name := range names {
		for _, kind := range []domain.EntryKind{domain.EntryFile, domain.EntryDirectory} {
			var want []SignalID
			for _, s := range p.signals {
				if s.matches([]byte(name), kind) {
					want = append(want, s.id)
				}
			}
			if got := p.AppendNameSignals(nil, []byte(name), kind); !slices.Equal(got, want) {
				t.Errorf("AppendNameSignals(%q, %s) = %v; matching each pattern gives %v", name, kind, got, want)
			}
		}
	}
}

// Task 3.1: AppendNameSignals allocates nothing once dst has room, so the
// scanner can call it for every entry.
func TestAppendNameSignalsAllocs(t *testing.T) {
	p := Default()
	dst := make([]SignalID, 0, 8)
	for _, name := range []string{"Profile.SQLite", "unins000.exe", "DSC00101.JPG", "readme.txt"} {
		n := []byte(name)
		if allocs := testing.AllocsPerRun(100, func() { dst = p.AppendNameSignals(dst[:0], n, domain.EntryFile) }); allocs != 0 {
			t.Errorf("AppendNameSignals(%q) allocates %v times per name", name, allocs)
		}
	}
}

func BenchmarkAppendNameSignals(b *testing.B) {
	p := Default()
	names := [][]byte{
		[]byte("DSC00101.JPG"), []byte("kernel32.dll"), []byte("Meu orcamento casamento.xls"),
		[]byte("node_modules"), []byte("f\xe9.txt"), []byte("a-fairly-long-file-name-without-any-signal.dat"),
	}
	dst := make([]SignalID, 0, 8)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		dst = p.AppendNameSignals(dst[:0], names[i%len(names)], domain.EntryFile)
	}
}

// Task 3.1: the file kind of each extension class, the system names, the
// disk-image pair, and the table's edge cases.
func TestFileKind(t *testing.T) {
	p := Default()
	cases := map[string]domain.FileKind{
		"GetRight-setup.exe":            domain.FileKindInstaller,
		"setup.exe":                     domain.FileKindInstaller,
		"GetRight.exe":                  domain.FileKindExecutable,
		"uninstall.exe":                 domain.FileKindExecutable,
		"kernel32.DLL":                  domain.FileKindExecutable,
		"IMG_0001.JPG":                  domain.FileKindImage,
		"clip.Mov":                      domain.FileKindVideo,
		"song.mp3":                      domain.FileKindAudio,
		"Relatorio.docx":                domain.FileKindDocument,
		"main.c":                        domain.FileKindSource,
		"backup.tar.gz":                 domain.FileKindArchive,
		"Office.msi":                    domain.FileKindInstaller,
		"x.iso":                         domain.FileKindArchive,
		"disc.IMG":                      domain.FileKindArchive,
		"disc.nrg":                      domain.FileKindArchive,
		"disc.mdf":                      domain.FileKindArchive,
		"jogo.cue":                      domain.FileKindArchive,
		"jogo.bin":                      domain.FileKindOther, // a lone .bin is no disk image
		"desktop.ini":                   domain.FileKindSystem,
		"Thumbs.db":                     domain.FileKindSystem,
		".DS_Store":                     domain.FileKindSystem,
		"FILE0000.CHK":                  domain.FileKindSystem,
		"filme.avi.part":                domain.FileKindOther,
		".bashrc":                       domain.FileKindOther, // only dot first: no extension
		"README":                        domain.FileKindOther,
		"trailing.":                     domain.FileKindOther,
		"x." + strings.Repeat("j", 40):  domain.FileKindOther,
		"f\xe9.JPG":                     domain.FileKindImage, // invalid UTF-8 before the extension
		"12345678909-IRPF-2006-ORI.DEC": domain.FileKindOther,
	}
	for name, want := range cases {
		if got := p.FileKind([]byte(name)); got != want {
			t.Errorf("FileKind(%q) = %s, want %s", name, got, want)
		}
	}
	name := []byte("IMG_0001.JPG")
	if allocs := testing.AllocsPerRun(100, func() { p.FileKind(name) }); allocs != 0 {
		t.Errorf("FileKind allocates %v times per name", allocs)
	}
}

// A .bin is a disk image only beside a .cue of the same stem, ignoring ASCII
// case; the stems come from PairStem over the folder's files.
func TestFileKindNearPair(t *testing.T) {
	p := Default()
	stems := map[string]bool{}
	for _, name := range []string{"Jogo.CUE", "dados.bin", "notes.txt", "other.cue"} {
		if s, ok := p.PairStem([]byte(name)); ok {
			stems[s] = true
		}
	}
	if want := map[string]bool{"jogo": true, "other": true}; !maps.Equal(stems, want) {
		t.Fatalf("stems = %v, want %v", stems, want)
	}
	cases := map[string]domain.FileKind{
		"jogo.bin":  domain.FileKindArchive,
		"JOGO.Bin":  domain.FileKindArchive,
		"dados.bin": domain.FileKindOther,
		"jogo.dat":  domain.FileKindOther,
		"jogo.cue":  domain.FileKindArchive,
		"jogo.jpg":  domain.FileKindImage,
	}
	for name, want := range cases {
		if got := p.FileKindNear([]byte(name), stems); got != want {
			t.Errorf("FileKindNear(%q) = %s, want %s", name, got, want)
		}
	}
	name := []byte("jogo.bin")
	if allocs := testing.AllocsPerRun(100, func() { p.FileKindNear(name, stems) }); allocs != 0 {
		t.Errorf("FileKindNear allocates %v times per name", allocs)
	}
}

// Paired picks out the names whose kind FileKindNear may change: the paired
// extension in any case, and nothing else.
func TestPaired(t *testing.T) {
	p := Default()
	for name, want := range map[string]bool{
		"jogo.bin": true, "JOGO.BIN": true, "jogo.cue": false, "dados.dat": false, "bin": false, ".bin": false,
	} {
		if got := p.Paired([]byte(name)); got != want {
			t.Errorf("Paired(%q) = %v, want %v", name, got, want)
		}
	}
	name := []byte("jogo.bin")
	if allocs := testing.AllocsPerRun(100, func() { p.Paired(name) }); allocs != 0 {
		t.Errorf("Paired allocates %v times per name", allocs)
	}
}

func mustPattern(t *testing.T, s string) pattern {
	t.Helper()
	p, err := parsePattern(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
