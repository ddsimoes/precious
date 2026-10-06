package viewer

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// textLimit is how much of a file /text decodes (§11.12).
const textLimit = 1 << 20

// Encoding names reported by /text.
const (
	encUTF8        = "UTF-8"
	encUTF16LE     = "UTF-16LE"
	encUTF16BE     = "UTF-16BE"
	encWindows1252 = "windows-1252"
)

var bomUTF8 = []byte{0xEF, 0xBB, 0xBF}

// decode returns the text of b, the start of a file, with the name of its
// encoding (design D12): a byte-order mark gives UTF-8 or UTF-16, otherwise
// valid UTF-8 is UTF-8, and anything else is Windows-1252. truncated says b
// stops before the end of the file, so a character cut by the cap is
// dropped rather than decoded as an error.
func decode(b []byte, truncated bool) (encoding, text string) {
	switch {
	case bytes.HasPrefix(b, bomUTF8):
		b = b[len(bomUTF8):]
		if truncated {
			b = trimPartialRune(b)
		}
		return encUTF8, strings.ToValidUTF8(string(b), string(utf8.RuneError))
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return encUTF16LE, decodeUTF16(b[2:], binary.LittleEndian, truncated)
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return encUTF16BE, decodeUTF16(b[2:], binary.BigEndian, truncated)
	}
	u := b
	if truncated {
		u = trimPartialRune(u)
	}
	if utf8.Valid(u) {
		return encUTF8, string(u)
	}
	// Windows-1252 maps every byte, so decoding cannot fail.
	s, _ := charmap.Windows1252.NewDecoder().Bytes(b)
	return encWindows1252, string(s)
}

// trimPartialRune drops an incomplete UTF-8 sequence at the end of b.
func trimPartialRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

// decodeUTF16 decodes b, after its byte-order mark. Unpaired surrogates and a
// trailing odd byte become U+FFFD, except a byte or a surrogate pair cut by
// the cap of a truncated read, which is dropped.
func decodeUTF16(b []byte, order binary.ByteOrder, truncated bool) string {
	out := make([]byte, 0, len(b)+len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		r := rune(order.Uint16(b[i:]))
		if utf16.IsSurrogate(r) {
			switch {
			case i+3 < len(b):
				if pair := utf16.DecodeRune(r, rune(order.Uint16(b[i+2:]))); pair != utf8.RuneError {
					r = pair
					i += 2
				} else {
					r = utf8.RuneError
				}
			case truncated && r < 0xDC00:
				// A high surrogate whose low half lies past the cap.
				return string(out)
			default:
				r = utf8.RuneError
			}
		}
		out = utf8.AppendRune(out, r)
	}
	if len(b)%2 == 1 && !truncated {
		out = utf8.AppendRune(out, utf8.RuneError)
	}
	return string(out)
}
