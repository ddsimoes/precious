package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// linkedDocs are the operator-facing documents whose links must resolve. The
// configuration reference in docs/operator.md is checked against
// config.Defaults by internal/config's docs tests.
var linkedDocs = []string{"../../README.md", "../../docs/operator.md"}

var (
	fence      = regexp.MustCompile("(?m)^```")
	inlineCode = regexp.MustCompile("`[^`\n]*`")
	inlineLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	heading    = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*$`)
	uriScheme  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
)

// TestDocsLinks checks every inline Markdown link in the operator documents:
// a relative link names an existing file, and a fragment names a heading of
// the linked Markdown file (or of the same file).
func TestDocsLinks(t *testing.T) {
	for _, doc := range linkedDocs {
		raw, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		links := markdownLinks(string(raw))
		if len(links) == 0 {
			t.Errorf("%s: no links found", doc)
		}
		for _, link := range links {
			if uriScheme.MatchString(link) {
				continue
			}
			path, fragment, _ := strings.Cut(link, "#")
			target := doc
			if path != "" {
				target = filepath.Join(filepath.Dir(doc), filepath.FromSlash(path))
				if _, err := os.Stat(target); err != nil {
					t.Errorf("%s: link %q: %v", doc, link, err)
					continue
				}
			}
			if fragment == "" {
				continue
			}
			if filepath.Ext(target) != ".md" {
				t.Errorf("%s: link %q has a fragment but does not name a Markdown file", doc, link)
				continue
			}
			text, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !headingAnchors(string(text))[fragment] {
				t.Errorf("%s: link %q: no heading with anchor #%s in %s", doc, link, fragment, target)
			}
		}
	}
}

// proseLines returns the lines of md outside fenced code blocks.
func proseLines(md string) []string {
	var lines []string
	inFence := false
	for line := range strings.SplitSeq(md, "\n") {
		if fence.MatchString(line) {
			inFence = !inFence
			continue
		}
		if !inFence {
			lines = append(lines, line)
		}
	}
	return lines
}

// markdownLinks returns the targets of the inline links in md, ignoring code.
func markdownLinks(md string) []string {
	var links []string
	for _, line := range proseLines(md) {
		for _, m := range inlineLink.FindAllStringSubmatch(inlineCode.ReplaceAllString(line, ""), -1) {
			links = append(links, m[1])
		}
	}
	return links
}

// headingAnchors returns the anchors GitHub generates for the headings of md:
// the text lowercased, with characters other than letters, digits, spaces,
// hyphens, and underscores dropped and spaces turned into hyphens; a repeated
// anchor gets the suffix -1, -2, and so on.
func headingAnchors(md string) map[string]bool {
	anchors := map[string]bool{}
	seen := map[string]int{}
	for _, line := range proseLines(md) {
		m := heading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var b strings.Builder
		for _, r := range strings.ToLower(m[1]) {
			switch {
			case r == ' ':
				b.WriteByte('-')
			case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
				b.WriteRune(r)
			}
		}
		anchor := b.String()
		if n := seen[anchor]; n > 0 {
			seen[anchor] = n + 1
			anchor += "-" + strconv.Itoa(n)
		} else {
			seen[anchor] = 1
		}
		anchors[anchor] = true
	}
	return anchors
}
