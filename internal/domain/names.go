package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
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
