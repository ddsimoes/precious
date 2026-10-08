package cleanup

import (
	"encoding/json"

	"precious/internal/domain"
	"precious/internal/rules"
)

// Classes of the files a check finds no copy of (r4 design D8):
//
//   - possibly_valuable: the family is personal, or the traits hold user
//     material, credentials, or a database, or the file kind is image,
//     video, audio, document, or source, or the extension is mail;
//   - likely_junk: otherwise, the family is disposable, or the category is
//     an installer download, an application installation, or an operating
//     system installation;
//   - uncertain: everything else.
//
// A file is ranked by its index row's classification; an archive member,
// which has none, by its name through the rules.

// mailExts are the mail extensions D8 counts as possibly valuable.
var mailExts = map[string]bool{"eml": true, "mbox": true, "msg": true, "pst": true, "dbx": true}

// rank is D8's ranking.
func rank(category domain.Category, family domain.Family, traits []domain.Trait, kind domain.FileKind,
	ext string) string {
	if family == domain.FamilyPersonal || domain.FamilyOf(category) == domain.FamilyPersonal || mailExts[ext] {
		return classValuable
	}
	for _, t := range traits {
		switch t {
		case domain.TraitContainsUserMaterial, domain.TraitContainsCredentials, domain.TraitContainsDatabase:
			return classValuable
		}
	}
	switch kind {
	case domain.FileKindImage, domain.FileKindVideo, domain.FileKindAudio, domain.FileKindDocument,
		domain.FileKindSource:
		return classValuable
	}
	if family == domain.FamilyDisposable || domain.FamilyOf(category) == domain.FamilyDisposable {
		return classJunk
	}
	switch category {
	case domain.CategoryInstallerDownload, domain.CategoryApplicationInstallation, domain.CategoryOSInstallation:
		return classJunk
	}
	return classUncertain
}

// fileClass ranks a file by its index row's columns; traits is the JSON
// array the row stores.
func fileClass(category, family, traits, kind, ext string) string {
	var ts []domain.Trait
	if traits != "" {
		// A malformed list ranks as none: the other facts still apply.
		_ = json.Unmarshal([]byte(traits), &ts)
	}
	return rank(domain.Category(category), domain.Family(family), ts, domain.FileKind(kind), ext)
}

// memberClass ranks an archive member by its name and size through the
// rules, as a scan classifies a file.
func (c *checker) memberClass(name []byte, size int64) string {
	kind := c.s.pol.FileKind(name)
	res := c.s.pol.ClassifyFile(rules.FileFacts{Name: name, Kind: kind, Size: size})
	return rank(res.Category, res.Family, res.Traits, kind, extOf(name))
}

// extOf is a name's extension as the index stores it: the bytes after the
// last dot, ASCII lower-cased, and empty for a name that starts or ends
// with its only dot.
func extOf(name []byte) string {
	i := -1
	for j := len(name) - 1; j >= 0; j-- {
		if name[j] == '.' {
			i = j
			break
		}
	}
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	b := make([]byte, 0, len(name)-i-1)
	for _, x := range name[i+1:] {
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		b = append(b, x)
	}
	return string(b)
}
