package corpus

// rescueDeclarations are the user-material indicators of the programs and
// disposable groups, each under its outermost such group: the rows of the
// rescue card (r2c design D6), taken from a scan of the corpus with the
// default policy. The corpus does not run the rules, so a rules change that
// moves any of them must update this table, as it must update the
// assertions in expect.go.
var rescueDeclarations = []rescueDecl{
	{programs + "/Microsoft Office", programs + "/Microsoft Office/OFFICE11/Meu orcamento casamento.xls"},
	{"Jogos/Need for Speed Underground 2", "Jogos/Need for Speed Underground 2/save"},
	{"Jogos/Need for Speed Underground 2", "Jogos/Need for Speed Underground 2/save/Joao/profile.sav"},
}
