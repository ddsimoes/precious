package corpus

// gemDeclarations are the facts of the R1 rules' classification of the
// corpus that Gems needs (design D14), taken from a scan of the corpus with
// the default policy:
//   - the file kind of every extension of kind image, video, audio, or
//     document found in the corpus;
//   - the outermost groups of family programs or disposable;
//   - the one file of those kinds whose own category is not personal (an
//     Office owner file, temporary_data);
//   - the indicators of those groups, each under its outermost group.
//
// A rules change that moves any of these must update this table, as it must
// update the assertions in expect.go.
var gemDeclarations = gemDecls{
	kinds: map[string]string{
		".jpg": kindImage, ".gif": kindImage, ".bmp": kindImage, ".ico": kindImage, ".svg": kindImage,
		".avi": kindVideo, ".mp4": kindVideo,
		".mp3": kindAudio,
		".doc": kindDocument, ".xls": kindDocument, ".pdf": kindDocument, ".txt": kindDocument,
		".md": kindDocument, ".html": kindDocument,
	},
	outside: []string{
		programs + "/Ahead",
		programs + "/ICQ",
		programs + "/Kazaa Lite",
		programs + "/Microsoft Office",
		programs + "/Mozilla Firefox",
		programs + "/WinRAR",
		programs + "/Winamp",
		backupC + "/WINDOWS",
		localCfg + "/Temporary Internet Files",
		oldCopy + "/Arquivos de programas/Winamp",
		"Downloads/emule-0.47c",
		"Jogos/Need for Speed Underground 2",
		"Projetos/app_react/node_modules",
		"Projetos/tcc_java/bin",
	},
	notPersonal: []string{"temp/~$curriculo.doc"},
	rescue: []rescueDecl{
		{programs + "/Microsoft Office", programs + "/Microsoft Office/OFFICE11/Meu orcamento casamento.xls"},
		{"Jogos/Need for Speed Underground 2", "Jogos/Need for Speed Underground 2/save"},
		{"Jogos/Need for Speed Underground 2", "Jogos/Need for Speed Underground 2/save/Joao/profile.sav"},
	},
}
