package corpus

// The classification vocabulary of design D9 and precious-spec §6.6, as the
// plain strings the ground truth carries.
const (
	personalMedia            = "personal_media"
	documents                = "documents"
	sourceProject            = "source_project"
	applicationUserData      = "application_user_data"
	applicationInstallation  = "application_installation"
	applicationConfiguration = "application_configuration"
	osInstallation           = "os_installation"
	installerDownload        = "installer_download"
	systemJunk               = "system_junk"
	cache                    = "cache"
	temporaryData            = "temporary_data"
	generatedArtifacts       = "generated_artifacts"
	downloadCollection       = "download_collection"
	backup                   = "backup"
	mixed                    = "mixed"
	unknown                  = "unknown"

	keep    = "keep"
	discard = "discard"
	review  = "review"

	kindImage      = "image"
	kindVideo      = "video"
	kindAudio      = "audio"
	kindDocument   = "document"
	kindSource     = "source"
	kindArchive    = "archive"
	kindInstaller  = "installer"
	kindExecutable = "executable"
	kindSystem     = "system"
	kindOther      = "other"
)

// expect is one asserted entry. An empty category asserts no category,
// triage, group, or veto; an empty fileKind asserts no file kind.
type expect struct {
	path     string
	category string
	triage   string
	group    bool
	veto     bool
	fileKind string
}

// expectations is what the rules must say about the corpus (R1.4, R1.5).
var expectations = []expect{
	// path                                                              category                 triage   group  veto   file kind
	{"RECYCLER", systemJunk, discard, false, false, ""},
	{"System Volume Information", systemJunk, discard, false, false, ""},
	{"Fotos/2006/Praia/Thumbs.db", systemJunk, discard, false, false, kindSystem},
	{"Fotos/2006/Casamento/Thumbs.db", systemJunk, discard, false, false, kindSystem},
	{natal + "/Thumbs.db", systemJunk, discard, false, false, kindSystem},
	{meusDocs + "/desktop.ini", systemJunk, discard, false, false, kindSystem},
	{"temp/recuperados/FILE0000.CHK", systemJunk, review, false, false, kindSystem},
	{"temp/recuperados/FILE0001.CHK", systemJunk, review, false, false, kindSystem},
	{"temp/recuperados/FILE0002.CHK", systemJunk, review, false, false, kindSystem},
	{"temp/recuperados/FILE0003.CHK", systemJunk, review, false, false, kindSystem},
	{"temp/recuperados/FILE0004.CHK", systemJunk, review, false, false, kindSystem},
	{"found.000", systemJunk, review, false, false, ""},
	{"found.000/FILE0000.CHK", systemJunk, review, false, false, kindSystem},

	{"Downloads/winamp5_full.exe", installerDownload, discard, false, false, ""},
	{"Downloads/icq2003b.exe", installerDownload, discard, false, false, ""},
	{"Downloads/nero-7.5.exe", installerDownload, discard, false, false, ""},
	{"Downloads/msn_messenger_7.exe", installerDownload, discard, false, false, ""},
	{"Downloads/Setup.exe", installerDownload, discard, false, false, ""},
	{"Downloads/Setup(1).exe", installerDownload, discard, false, false, ""},
	{"Downloads/WinXP_SP2_ptBR.exe", installerDownload, discard, false, false, ""},
	{"Downloads/pacote.msi", installerDownload, discard, false, false, ""},
	{"ISOs/Windows XP Professional SP2.iso", installerDownload, discard, false, false, kindArchive},
	{"ISOs/Office 2003.iso", installerDownload, discard, false, false, kindArchive},
	{"ISOs/copia/Windows XP Professional SP2.iso", installerDownload, discard, false, false, kindArchive},
	{"Jogos/retro/jogo.bin", installerDownload, discard, false, false, kindArchive},
	{"Jogos/retro/jogo.cue", "", "", false, false, kindArchive},
	{"Jogos/retro/dados/dados.bin", "", "", false, false, kindOther},
	{"Downloads/filme.avi.part", temporaryData, discard, false, false, ""},

	{programs + "/Winamp", applicationInstallation, discard, true, false, ""},
	{programs + "/Mozilla Firefox", applicationInstallation, discard, true, false, ""},
	{programs + "/ICQ", applicationInstallation, discard, true, false, ""},
	{programs + "/Ahead/Nero", applicationInstallation, discard, true, false, ""},
	{programs + "/Microsoft Office", applicationInstallation, review, true, true, ""},
	{programs + "/Kazaa Lite", applicationInstallation, discard, true, false, ""},
	{programs + "/WinRAR", applicationInstallation, discard, true, false, ""},
	{backupC + "/WINDOWS", osInstallation, review, true, false, ""},
	{localCfg + "/Temporary Internet Files", cache, discard, true, false, ""},

	{"Fotos", personalMedia, keep, false, false, ""},
	{"Documentos", documents, keep, false, false, ""},
	{"Projetos/site_antigo", sourceProject, keep, true, false, ""},
	{"Projetos/site_antigo_copia", sourceProject, keep, true, false, ""},
	{"Projetos/tcc_java/bin", generatedArtifacts, discard, true, false, ""},
	{"Projetos/app_react/node_modules", generatedArtifacts, discard, true, false, ""},
	{"Jogos/Need for Speed Underground 2/save", applicationUserData, keep, true, false, ""},

	{"Documentos/curriculo.doc", "", "", false, false, kindDocument},
	{"Downloads/fotos_2005_do_pendrive.zip", "", "", false, false, kindArchive},
	{"Fotos/2007/Formatura/MVI_3005.AVI", "", "", false, false, kindVideo},
	{"Midia/foto.jpg", "", "", false, false, kindImage},
	{"Midia/video.mp4", "", "", false, false, kindVideo},
	{"Midia/musica.mp3", "", "", false, false, kindAudio},
	{"Midia/documento.pdf", "", "", false, false, kindDocument},
	{"Midia/script.py", "", "", false, false, kindSource},
}
