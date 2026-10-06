package domain

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// DisplayName renders raw filename bytes for humans (design D4). The mapping is
// injective, so two different raw names never share a display form:
//
//   - printable UTF-8 and ASCII space are kept as is;
//   - the escape character `\` becomes `\\`;
//   - each byte that is not part of valid UTF-8 becomes `\xNN`;
//   - every other non-printable rune (C0/C1 controls, DEL, format characters such
//     as bidirectional overrides and zero-width characters, non-ASCII spaces)
//     becomes `\u{XXXX}`.
//
// The result is plain text; callers still escape it for their output context
// (HTML, JSON, logs).
func DisplayName(raw []byte) string {
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRune(raw[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			fmt.Fprintf(&b, `\x%02X`, raw[i])
		case r == '\\':
			b.WriteString(`\\`)
		case r == ' ' || unicode.IsPrint(r):
			b.Write(raw[i : i+size])
		default:
			fmt.Fprintf(&b, `\u{%04X}`, r)
		}
		i += size
	}
	return b.String()
}

// MemberDisplayName renders the raw name or path of an archive member for
// humans. In a zip member's name, each '/'-separated component that is not
// valid UTF-8 is decoded from code page 850, the encoding Windows zip tools
// write for Portuguese names (APPNOTE's CP437 differs in letters such as õ
// and ã), then rendered as DisplayName renders it; every other component,
// and every name of another format, renders as DisplayName. Unlike
// DisplayName, this is not injective: the raw bytes stay the identity.
func MemberDisplayName(raw []byte, zip bool) string {
	if !zip || utf8.Valid(raw) {
		return DisplayName(raw)
	}
	var b strings.Builder
	b.Grow(len(raw) + len(raw)/2)
	for i, c := range bytes.Split(raw, []byte{'/'}) {
		if i > 0 {
			b.WriteByte('/')
		}
		if !utf8.Valid(c) {
			dec := make([]byte, 0, 2*len(c))
			for _, x := range c {
				dec = utf8.AppendRune(dec, charmap.CodePage850.DecodeByte(x))
			}
			c = dec
		}
		b.WriteString(DisplayName(c))
	}
	return b.String()
}
