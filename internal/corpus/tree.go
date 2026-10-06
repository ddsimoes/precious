package corpus

import (
	"bytes"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"precious/internal/domain"
)

// Corpus returns the §15 regression corpus. It is built once per process.
func Corpus() *Tree { return corpusOnce() }

var corpusOnce = sync.OnceValue(buildCorpus)

// Folders the definition and the assertions share.
const (
	backupC  = "Backup_PC_2004/C"
	programs = backupC + "/Arquivos de programas"
	joao     = backupC + "/Documents and Settings/Joao"
	meusDocs = joao + "/Meus documentos"
	natal    = meusDocs + "/Minhas imagens/2004/Natal"
	localCfg = joao + "/Configurações locais"
	oldCopy  = "HD antigo/backup pc velho"
)

func buildCorpus() *Tree {
	d := newDef()
	d.backupC()
	d.oldDiskCopy()
	d.photos()
	d.downloads()
	d.documents()
	d.projects()
	d.games()
	d.diskImages()
	d.junk()
	d.phone()
	d.media()
	d.private()
	return d.finish(expectations)
}

// sized is a file name and its size.
type sized struct {
	name string
	size int
}

// programFile returns plausible content for a program file by its extension.
func programFile(p string, size int) []byte {
	switch strings.ToLower(path.Ext(p)) {
	case ".exe", ".dll":
		return pe(p, size)
	case ".hlp":
		return winhelp(p, size)
	case ".ico":
		return icon(p, size)
	case ".bmp":
		return bitmap(p, size)
	}
	return random(p, size)
}

// binaries adds program files under dir, one second apart from mtime.
func (d *def) binaries(dir string, mtime time.Time, files ...sized) {
	for i, f := range files {
		p := dir + "/" + f.name
		d.file(p, mtime.Add(time.Duration(i)*time.Second), programFile(p, f.size))
	}
}

// camera adds count camera-named photos DSC<first...>.JPG under dir, a minute
// apart from mtime.
func (d *def) camera(dir string, first, count int, mtime time.Time) {
	for i := range count {
		p := fmt.Sprintf("%s/DSC%05d.JPG", dir, first+i)
		d.file(p, mtime.Add(time.Duration(i)*time.Minute), photo(p, 160, 120))
	}
}

// backupC is the copied C: drive of 2004.
func (d *def) backupC() {
	d.binaries(programs+"/Winamp", at(2003, 3, 10, 14, 20),
		sized{"winamp.exe", 160_000}, sized{"winamp.hlp", 24_000}, sized{"winamp.ico", 2_238},
		sized{"Plugins/in_mp3.dll", 48_000}, sized{"Plugins/in_wave.dll", 20_000},
		sized{"Plugins/out_ds.dll", 19_000}, sized{"Plugins/gen_ml.dll", 60_000},
		sized{"Skins/base.bmp", 11_000})
	d.binaries(programs+"/Mozilla Firefox", at(2004, 11, 9, 21, 5),
		sized{"firefox.exe", 140_000}, sized{"js3250.dll", 95_000}, sized{"xpcom.dll", 32_000},
		sized{"nspr4.dll", 36_000}, sized{"firefox.ico", 4_286},
		sized{"components/gklayout.dll", 125_000}, sized{"components/necko.dll", 55_000})
	d.binaries(programs+"/ICQ", at(2003, 6, 2, 10, 0),
		sized{"icq.exe", 130_000}, sized{"icq.hlp", 26_000}, sized{"icq.ico", 1_150},
		sized{"bin/icqcore.dll", 70_000}, sized{"bin/icqnet.dll", 35_000}, sized{"bin/icqateimg.dll", 22_000})
	d.binaries(programs+"/Ahead/Nero", at(2003, 8, 15, 16, 40),
		sized{"nero.exe", 200_000}, sized{"nero.hlp", 45_000}, sized{"nero.bmp", 15_000},
		sized{"bin/neroapi.dll", 90_000}, sized{"bin/nerocom.dll", 30_000})
	d.binaries(programs+"/Microsoft Office/OFFICE11", at(2004, 2, 20, 9, 30),
		sized{"WINWORD.EXE", 210_000}, sized{"EXCEL.EXE", 190_000}, sized{"WWLIB.DLL", 150_000},
		sized{"XLINTL32.DLL", 45_000}, sized{"OUTLLIB.DLL", 75_000}, sized{"WINWORD.HLP", 40_000},
		sized{"1046/WWINTL.DLL", 30_000})
	d.file(programs+"/Microsoft Office/OFFICE11/Meu orcamento casamento.xls", at(2004, 5, 14, 22, 12),
		ole("orcamento casamento", 38_400))
	d.binaries(programs+"/Kazaa Lite", at(2003, 9, 1, 19, 45),
		sized{"kazaalite.exe", 115_000}, sized{"kl.ico", 2_238}, sized{"splash.bmp", 20_000},
		sized{"bin/kpp.dll", 40_000}, sized{"bin/kazaacore.dll", 60_000})
	d.binaries(programs+"/WinRAR", at(2003, 5, 5, 11, 15),
		sized{"WinRAR.exe", 120_000}, sized{"Rar.exe", 75_000}, sized{"UnRAR.exe", 45_000},
		sized{"RarExt.dll", 30_000}, sized{"WinRAR.hlp", 35_000})

	windows := backupC + "/WINDOWS"
	installed := at(2003, 1, 20, 8, 0)
	d.binaries(windows, installed, sized{"explorer.exe", 100_000}, sized{"Fonts/arial.ttf", 60_000})
	d.file(windows+"/win.ini", installed, []byte(winINI))
	d.file(windows+"/system.ini", installed, []byte(systemINI))
	d.binaries(windows+"/system32", installed,
		sized{"kernel32.dll", 80_000}, sized{"user32.dll", 50_000}, sized{"gdi32.dll", 25_000},
		sized{"shell32.dll", 90_000}, sized{"advapi32.dll", 55_000}, sized{"comctl32.dll", 50_000},
		sized{"msvcrt.dll", 30_000}, sized{"ole32.dll", 40_000}, sized{"wininet.dll", 60_000},
		sized{"ntdll.dll", 70_000})
	d.file(windows+"/system32/drivers/etc/hosts", installed, []byte(hostsFile))

	d.file(meusDocs+"/curriculo.doc", at(2004, 3, 2, 23, 41), ole("curriculo 2004", 24_576))
	d.file(meusDocs+"/TCC_rascunho.doc", at(2004, 10, 21, 1, 7), ole("TCC rascunho", 61_440))
	d.file(meusDocs+"/desktop.ini", at(2003, 1, 20, 8, 5), []byte(desktopINI))
	d.camera(natal, 101, 4, at(2004, 12, 24, 20, 10))
	d.file(natal+"/Thumbs.db", at(2004, 12, 26, 11, 0), ole("Natal thumbs", 9_216))

	profile := joao + "/Dados de aplicativos/Mozilla/Firefox/Profiles/x.default"
	d.file(profile+"/prefs.js", at(2004, 12, 30, 18, 2), []byte(prefsJS))
	d.file(profile+"/bookmarks.html", at(2004, 12, 28, 15, 33), []byte(bookmarksHTML))
	d.file(joao+"/Dados de aplicativos/ICQ/Joao/history.dat", at(2004, 12, 31, 23, 50), random("ICQ history", 30_000))
	d.file(localCfg+"/Temp/~WRL0003.tmp", at(2004, 10, 21, 1, 7), random("WRL0003", 12_000))
	d.file(localCfg+"/Temporary Internet Files/Content.IE5/X1/a.gif", at(2004, 12, 30, 18, 0), gifImage("IE cache a.gif"))
}

// oldDiskCopy is a second, partial copy of the 2004 backup, with one document
// edited after the first copy.
func (d *def) oldDiskCopy() {
	d.copyTree(meusDocs, oldCopy+"/Documents and Settings/Joao/Meus documentos",
		"TCC_rascunho.doc", "Minhas imagens/2004/Natal/DSC00104.JPG")
	edited := bytes.Clone(d.data(meusDocs + "/TCC_rascunho.doc"))
	edited = append(edited, random("TCC rascunho edit", 4_096)...)
	d.file(oldCopy+"/Documents and Settings/Joao/Meus documentos/TCC_rascunho.doc", at(2005, 1, 15, 22, 30), edited)
	d.copyTree(programs+"/Winamp", oldCopy+"/Arquivos de programas/Winamp")
}

// photos are Fotos and Fotos - Copia, which differ in a few files.
func (d *def) photos() {
	for i, n := range []int{101, 102, 103, 104} {
		p := fmt.Sprintf("%s/DSC%05d.JPG", natal, n)
		d.file(fmt.Sprintf("Fotos/2004/Natal/DSC%05d.JPG", n), at(2004, 12, 24, 20, 10+i), d.data(p))
	}
	d.camera("Fotos/2004/Natal", 105, 1, at(2004, 12, 25, 9, 0))
	d.camera("Fotos/2004/Aniversario Ana", 150, 4, at(2004, 8, 7, 19, 30))
	d.camera("Fotos/2005/Carnaval", 1001, 5, at(2005, 2, 6, 16, 0))
	d.camera("Fotos/2005/Ferias Floripa", 1101, 4, at(2005, 7, 18, 10, 20))
	d.camera("Fotos/2006/Praia", 2001, 5, at(2006, 1, 3, 11, 45))
	d.file("Fotos/2006/Praia/Thumbs.db", at(2006, 1, 5, 20, 0), ole("Praia thumbs", 8_192))
	d.camera("Fotos/2006/Casamento", 2101, 6, at(2006, 5, 20, 17, 0))
	d.file("Fotos/2006/Casamento/Thumbs.db", at(2006, 5, 22, 21, 0), ole("Casamento thumbs", 11_264))
	d.camera("Fotos/2007/Formatura", 3001, 4, at(2007, 12, 15, 21, 0))
	d.file("Fotos/2007/Formatura/MVI_3005.AVI", at(2007, 12, 15, 21, 30), avi("Formatura video", 350_000))
	d.camera("Fotos/2009/Viagem Chile", 4001, 5, at(2009, 9, 12, 8, 15))

	d.copyTree("Fotos", "Fotos - Copia",
		"2005/Carnaval/DSC01005.JPG", "2007/Formatura/DSC03004.JPG", "2009/Viagem Chile/DSC04005.JPG")
	d.file("Fotos - Copia/2006/Praia/DSC_editada.JPG", at(2008, 3, 1, 14, 0), photo("DSC02003 editada", 160, 120))
}

// downloads is the Downloads folder, plus the MP3s also found in Musicas.
func (d *def) downloads() {
	var pendrive []zipMember
	for _, it := range d.items {
		if rel, ok := strings.CutPrefix(it.path, "Fotos/2005/"); ok && it.kind == domain.EntryFile {
			pendrive = append(pendrive, zipMember{name: rel, data: it.data, mtime: it.mtime})
		}
	}
	d.file("Downloads/fotos_2005_do_pendrive.zip", at(2006, 2, 10, 13, 0), zipArchive(pendrive))
	d.copyTree("Fotos/2005", "Downloads/fotos_2005_do_pendrive")

	d.binaries("Downloads", at(2004, 4, 2, 20, 0),
		sized{"winamp5_full.exe", 450_000}, sized{"icq2003b.exe", 380_000},
		sized{"nero-7.5.exe", 900_000}, sized{"msn_messenger_7.exe", 420_000},
		sized{"WinXP_SP2_ptBR.exe", 1_400_000})
	setup := d.file("Downloads/Setup.exe", at(2005, 6, 1, 15, 0), pe("Setup.exe", 210_000))
	d.file("Downloads/Setup(1).exe", at(2005, 6, 1, 15, 3), setup)
	d.file("Downloads/pacote.msi", at(2006, 9, 9, 9, 9), ole("pacote.msi", 260_000))

	emule := "Downloads/emule-0.47c"
	unpacked := at(2006, 11, 4, 22, 0)
	d.binaries(emule, unpacked, sized{"emule.exe", 600_000}, sized{"unrar.dll", 80_000}, sized{"lang/pt_BR.dll", 50_000})
	d.file(emule+"/license.txt", unpacked, []byte(emuleLicense))
	d.file(emule+"/Changelog.txt", unpacked, []byte(emuleChangelog))
	d.file(emule+"/webserver/eMule.tmpl", unpacked, filler("eMule.tmpl", 9_000))
	var members []zipMember
	for _, it := range d.items {
		if strings.HasPrefix(it.path, emule+"/") && it.kind == domain.EntryFile {
			members = append(members, zipMember{name: strings.TrimPrefix(it.path, "Downloads/"), data: it.data, mtime: it.mtime})
		}
	}
	d.file("Downloads/eMule0.47c-Installer.zip", at(2006, 11, 4, 21, 55), zipArchive(members))

	d.file("Downloads/filme.avi.part", at(2007, 8, 30, 3, 12), avi("filme.avi", 800_000))

	tracks := []struct {
		artist, title string
		size          int
	}{
		{"Legiao Urbana", "Tempo Perdido", 320_000},
		{"Legiao Urbana", "Pais e Filhos", 360_000},
		{"Charlie Brown Jr", "Zoio de Lula", 300_000},
		{"Skank", "Garota Nacional", 280_000},
		{"Los Hermanos", "Anna Julia", 260_000},
	}
	for i, tr := range tracks {
		name := tr.artist + " - " + tr.title + ".mp3"
		mtime := at(2005, 3, 12, 22, 10+i)
		data := d.file("Downloads/mp3/"+name, mtime, mp3(name, tr.size))
		d.file("Musicas/"+tr.artist+"/"+name, mtime, data)
	}
	d.file("Musicas/Skank/Skank - Vou Deixar.mp3", at(2005, 4, 2, 18, 0), mp3("Vou Deixar", 290_000))
}

// documents has version families, tax returns by year, and credentials.
func (d *def) documents() {
	curriculo := meusDocs + "/curriculo.doc"
	d.file("Documentos/curriculo.doc", at(2004, 3, 2, 23, 41), d.data(curriculo))
	d.file("Documentos/curriculo_final.doc", at(2006, 3, 10, 20, 0), ole("curriculo final", 26_112))
	d.file("Documentos/curriculo_final2.doc", at(2006, 3, 12, 9, 15), ole("curriculo final2", 26_624))
	d.file("Documentos/curriculo (1).doc", at(2007, 4, 2, 14, 22), d.data(curriculo))
	for i, v := range []string{"", "2", "_revisada", "_AGORA_VAI"} {
		name := "TCC_versao_final" + v + ".doc"
		d.file("Documentos/TCC/"+name, at(2005, 11, 20+3*i, 23, 0), ole(name, 70_656+512*i))
	}
	for year := 2006; year <= 2012; year++ {
		dir := fmt.Sprintf("Documentos/Imposto de Renda/IRPF%d", year)
		d.file(fmt.Sprintf("%s/12345678909-IRPF-A-%d-%d-ORIGI.DEC", dir, year, year-1), at(year, 4, 25, 21, 0), random(dir+" DEC", 6_000+100*(year-2006)))
		d.file(dir+"/recibo.REC", at(year, 4, 25, 21, 5), random(dir+" REC", 1_500))
	}
	d.file("Documentos/senhas.txt", at(2008, 7, 19, 0, 30), []byte(senhasTXT))
	d.file("Documentos/Nova pasta/Nova pasta (2)/teste.txt", at(2008, 6, 1, 10, 0), nil)
	d.emptyDir("Documentos/Nova pasta (3)", at(2010, 8, 14, 16, 2))
	d.file("Documentos/LEIAME.TXT", at(2003, 2, 1, 12, 0), []byte(leiameUpper))
	d.file("Documentos/leiame.txt", at(2009, 2, 1, 12, 0), []byte(leiameLower))
	d.file("Documentos/f\xe9.txt", at(2005, 5, 5, 5, 5), []byte(latin1Name))
}

// projects are source projects with .git, a copy with one changed file, Java
// build output, and node_modules.
func (d *def) projects() {
	site := "Projetos/site_antigo"
	written := at(2008, 8, 3, 23, 0)
	d.file(site+"/index.php", written, []byte(indexPHP))
	d.file(site+"/contato.php", written.Add(time.Minute), []byte(contatoPHP))
	d.file(site+"/config.php", written, []byte(configPHP))
	d.file(site+"/includes/db.php", written, []byte(dbPHP))
	d.file(site+"/css/estilo.css", written, []byte(estiloCSS))
	d.file(site+"/img/logo.gif", written, gifImage("logo.gif"))
	d.file(site+"/backup_banco.sql", at(2008, 9, 1, 2, 0), filler("backup_banco.sql", 40_000))
	d.file(site+"/.git/HEAD", written, []byte(gitHEAD))
	d.file(site+"/.git/config", written, []byte(gitConfig))
	d.file(site+"/.git/description", written, []byte(gitDescription))
	d.file(site+"/.git/refs/heads/master", written, []byte("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391\n"))
	d.file(site+"/.git/index", written, headed("DIRC\x00\x00\x00\x02", "site index", 1_024))
	d.file(site+"/.git/objects/e6/9de29bb2d1d6434b8b29ae775ad8c2e48c5391", written, random("git object e6", 120))
	d.file(site+"/.git/objects/4b/825dc642cb6eb9a060e54bf8d69288fbee4904", written, random("git object 4b", 96))

	d.copyTree(site, "Projetos/site_antigo_copia", "contato.php")
	d.file("Projetos/site_antigo_copia/contato.php", at(2009, 1, 10, 20, 0), []byte(contatoPHPChanged))

	java := "Projetos/tcc_java"
	coded := at(2005, 10, 2, 22, 0)
	d.file(java+"/build.xml", coded, []byte(buildXML))
	for i, class := range []string{"Main", "Grafo", "Vertice", "Aresta", "util/Leitor"} {
		pkg, name := "br.ufmg.tcc", class
		if dir, base, ok := strings.Cut(class, "/"); ok {
			pkg, name = pkg+"."+dir, base
		}
		src := fmt.Sprintf("package %s;\n\npublic class %s {\n", pkg, name)
		d.file(java+"/src/br/ufmg/tcc/"+class+".java", coded.Add(time.Duration(i)*time.Hour),
			append([]byte(src), append(filler(class+".java", 1_500), "}\n"...)...))
	}
	built := at(2005, 12, 1, 3, 0)
	for i, class := range []string{"Main", "Main$1", "Grafo", "Vertice", "Aresta", "util/Leitor"} {
		d.file(java+"/bin/br/ufmg/tcc/"+class+".class", built.Add(time.Duration(i)*time.Second), classFile(class, 2_000+300*i))
	}

	react := "Projetos/app_react"
	started := at(2012, 6, 9, 15, 0)
	d.file(react+"/package.json", started, []byte(packageJSON))
	d.file(react+"/src/App.js", started.Add(2*time.Hour), []byte(appJS))
	d.file(react+"/src/index.js", started, []byte(indexJS))
	d.file(react+"/public/index.html", started, []byte(reactIndexHTML))
	installed := started.Add(10 * time.Minute)
	for _, mod := range []struct {
		name  string
		files []sized
	}{
		{"react", []sized{{"package.json", 900}, {"index.js", 200}, {"cjs/react.development.js", 60_000}}},
		{"react-dom", []sized{{"package.json", 1_100}, {"index.js", 250}, {"cjs/react-dom.development.js", 180_000}}},
		{"react-scripts", []sized{{"package.json", 1_600}, {"bin/react-scripts.js", 1_800}}},
		{"lodash", []sized{{"package.json", 700}, {"lodash.js", 140_000}, {"fp/placeholder.js", 120}}},
	} {
		for _, f := range mod.files {
			p := react + "/node_modules/" + mod.name + "/" + f.name
			d.file(p, installed, filler(p, f.size))
		}
	}
	d.symlink(react+"/node_modules/.bin/react-scripts", "../react-scripts/bin/react-scripts.js", installed)
}

// games holds a game with saves and disk images, one .bin with its .cue and
// one without.
func (d *def) games() {
	cs := "Jogos/Counter-Strike 1.6"
	d.binaries(cs, at(2004, 6, 12, 14, 0), sized{"hl.exe", 180_000},
		sized{"cstrike/maps/de_dust2.bsp", 400_000}, sized{"cstrike/maps/cs_office.bsp", 350_000},
		sized{"cstrike/models/player/gign/gign.mdl", 90_000})
	d.file(cs+"/cstrike/liblist.gam", at(2004, 6, 12, 14, 0), []byte(liblistGam))
	d.file(cs+"/cstrike/config.cfg", at(2006, 10, 1, 1, 30), []byte(csConfig))

	nfs := "Jogos/Need for Speed Underground 2"
	d.binaries(nfs, at(2005, 1, 8, 13, 0), sized{"speed2.exe", 300_000}, sized{"GLOBAL/globalb.lzc", 100_000})
	d.file(nfs+"/save/Joao/profile.sav", at(2005, 3, 20, 2, 14), random("NFS profile", 48_000))

	d.file("Jogos/retro/jogo.bin", at(2009, 4, 4, 16, 0), random("jogo.bin", 2_352*300))
	d.file("Jogos/retro/jogo.cue", at(2009, 4, 4, 16, 0), []byte(cueSheet))
	d.file("Jogos/retro/dados/dados.bin", at(2009, 4, 5, 10, 0), random("dados.bin", 120_000))
}

// diskImages are installation discs and an identical copy of one.
func (d *def) diskImages() {
	xp := d.file("ISOs/Windows XP Professional SP2.iso", at(2005, 8, 20, 12, 0), iso("WinXP SP2", 1_500_000))
	d.file("ISOs/Office 2003.iso", at(2005, 8, 21, 12, 0), iso("Office 2003", 1_200_000))
	d.file("ISOs/copia/Windows XP Professional SP2.iso", at(2005, 8, 20, 12, 0), xp)
}

// junk is recovery output, the recycle bin, and system folders.
func (d *def) junk() {
	d.file("temp/~$curriculo.doc", at(2006, 3, 12, 9, 15), headed("\x04Joao", "owner file", 162))
	for i := range 5 {
		d.file(fmt.Sprintf("temp/recuperados/FILE%04d.CHK", i), at(2007, 2, 11, 10, 0), random(fmt.Sprintf("recuperado %d", i), 4_096*(i+1)))
	}
	d.file("found.000/FILE0000.CHK", at(2007, 2, 11, 10, 2), random("found.000", 8_192))
	d.file("RECYCLER/S-1-5-21-123/INFO2", at(2008, 11, 30, 18, 0), random("INFO2", 820))
	d.file("RECYCLER/S-1-5-21-123/desktop.ini", at(2003, 1, 20, 8, 1), []byte(desktopINI))
	d.file("System Volume Information/tracking.log", at(2010, 1, 2, 3, 4), random("tracking.log", 20_480))
}

// phone is a phone backup with unique photos and contacts.
func (d *def) phone() {
	backup := "celular_backup_2009"
	for i := range 6 {
		p := fmt.Sprintf("%s/DCIM/100MEDIA/IMAG%04d.jpg", backup, i+1)
		d.file(p, at(2009, 5, 2+i, 18, 0), photo(p, 160, 120))
	}
	for i := range 2 {
		p := fmt.Sprintf("%s/WhatsApp/Media/WhatsApp Images/IMG-20090612-WA%04d.jpg", backup, i+1)
		d.file(p, at(2009, 6, 12, 12, i), photo(p, 120, 160))
	}
	d.file(backup+"/contatos.vcf", at(2009, 7, 1, 9, 0), []byte(vcard))
}

// media are the viewer fixtures (R1.13).
func (d *def) media() {
	saved := at(2010, 3, 3, 20, 0)
	d.file("Midia/foto.jpg", saved, photo("Midia foto", 320, 240))
	d.file("Midia/video.mp4", saved, videoMP4)
	d.file("Midia/musica.mp3", saved, audioMP3)
	d.file("Midia/documento.pdf", saved, documentPDF)
	d.file("Midia/notas.md", saved, []byte(notasMD))
	d.file("Midia/script.py", saved, []byte(scriptPY))
	d.file("Midia/carta_1252.txt", saved, []byte(carta1252))
	d.file("Midia/pagina.html", saved, []byte(paginaHTML))
	d.file("Midia/desenho.svg", saved, []byte(desenhoSVG))
}

// private is the unreadable folder.
func (d *def) private() {
	d.file("privado/diario.txt", at(2011, 3, 12, 23, 0), []byte(diarioTXT))
	d.unreadable("privado")
}
