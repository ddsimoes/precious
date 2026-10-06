package domain

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// undisplay inverts DisplayName. It rejects any display string that contains an
// unescaped non-printable rune or an escape DisplayName never produces, so a
// successful round trip also proves every hidden byte became a visible escape.
func undisplay(s string) ([]byte, error) {
	var out []byte
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size <= 1 {
				return nil, fmt.Errorf("display form holds invalid UTF-8 at offset %d", i)
			}
			if r != ' ' && !unicode.IsPrint(r) {
				return nil, fmt.Errorf("display form holds unescaped non-printable %U at offset %d", r, i)
			}
			out = append(out, s[i:i+size]...)
			i += size
			continue
		}
		rest := s[i+1:]
		switch {
		case strings.HasPrefix(rest, `\`):
			out = append(out, '\\')
			i += 2
		case strings.HasPrefix(rest, "x") && len(rest) >= 3:
			hex := rest[1:3]
			if strings.ToUpper(hex) != hex {
				return nil, fmt.Errorf("lowercase hex escape %q at offset %d", hex, i)
			}
			v, err := strconv.ParseUint(hex, 16, 8)
			if err != nil {
				return nil, fmt.Errorf("bad byte escape at offset %d: %v", i, err)
			}
			out = append(out, byte(v))
			i += 4
		case strings.HasPrefix(rest, "u{"):
			end := strings.IndexByte(rest, '}')
			if end < 0 {
				return nil, fmt.Errorf("unterminated rune escape at offset %d", i)
			}
			v, err := strconv.ParseUint(rest[2:end], 16, 32)
			if err != nil || !utf8.ValidRune(rune(v)) {
				return nil, fmt.Errorf("bad rune escape %q at offset %d", rest[:end+1], i)
			}
			out = utf8.AppendRune(out, rune(v))
			i += 1 + end + 1
		default:
			return nil, fmt.Errorf("dangling escape at offset %d", i)
		}
	}
	return out, nil
}

func checkRoundTrip(t *testing.T, raw []byte) string {
	t.Helper()
	disp := DisplayName(raw)
	back, err := undisplay(disp)
	if err != nil {
		t.Fatalf("DisplayName(%q) = %q: %v", raw, disp, err)
	}
	if !bytes.Equal(back, raw) {
		t.Fatalf("DisplayName(%q) = %q decodes to %q", raw, disp, back)
	}
	return disp
}

// Pieces chosen to provoke collisions: escape-lookalike text next to the bytes
// it imitates, truncated and surrogate sequences, controls, bidi and
// zero-width format characters, and normalization-distinct spellings.
var trickyPieces = []string{
	"a", "Z", " ", ".", `\`, "x", "X", "E9", "e9", "u{", "}", "{", "202E",
	`\xE9`, `\\`, `\u{202E}`, "\xE9", "\xC3", "\xA9", "é", "e\u0301",
	"\u202E", "\u2066", "\u200D", "\u00A0", "\u2028", "\x00", "\x09", "\x1B", "\x7F",
	"\u0085", "\xC2\x85", "\xF0\x9F", "😀", "\xED\xA0\x80", "\uFFFD", "\xFF", "\xC0\xAF",
}

// TestDisplayNameInjective is the D4 property test: over random names built
// from tricky pieces and raw bytes, the display form always decodes back to the
// exact raw bytes, and no two distinct raw names share a display form.
func TestDisplayNameInjective(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	seen := make(map[string][]byte)
	for range 50_000 {
		var raw []byte
		for range rng.IntN(9) {
			if rng.IntN(4) == 0 {
				raw = append(raw, byte(rng.IntN(256)))
			} else {
				raw = append(raw, trickyPieces[rng.IntN(len(trickyPieces))]...)
			}
		}
		disp := checkRoundTrip(t, raw)
		if prev, ok := seen[disp]; ok && !bytes.Equal(prev, raw) {
			t.Fatalf("raw names %q and %q share display form %q", prev, raw, disp)
		}
		seen[disp] = raw
	}
}

// FuzzDisplayNameRoundTrip extends the property test under `go test -fuzz`.
func FuzzDisplayNameRoundTrip(f *testing.F) {
	for _, p := range trickyPieces {
		f.Add([]byte(p))
	}
	f.Add([]byte("f\xE9.txt"))
	f.Add([]byte(`<img src=x onerror=alert(1)>`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		checkRoundTrip(t, raw)
	})
}

// TestA17DistinctNamesDisplayDistinctly covers A17 "distinct names display
// distinctly": byte 0xE9 and the literal text `\xE9` never share a display form.
func TestA17DistinctNamesDisplayDistinctly(t *testing.T) {
	invalid := DisplayName([]byte("caf\xE9"))
	literal := DisplayName([]byte(`caf\xE9`))
	if invalid == literal {
		t.Fatalf("both names display as %q", invalid)
	}
	if invalid != `caf\xE9` {
		t.Errorf("invalid byte displays as %q, want %q", invalid, `caf\xE9`)
	}
	if literal != `caf\\xE9` {
		t.Errorf("literal escape text displays as %q, want %q", literal, `caf\\xE9`)
	}
}

func TestDisplayNameEscapesBidiOverride(t *testing.T) {
	// "invoice<RLO>fdp.exe" renders as "invoiceexe.pdf" when the override is honored.
	raw := []byte("invoice\u202Efdp.exe")
	got := DisplayName(raw)
	if want := `invoice\u{202E}fdp.exe`; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	for _, r := range []rune{'\u202A', '\u202B', '\u202C', '\u202D', '\u202E', '\u2066', '\u2067', '\u2068', '\u2069', '\u200E', '\u200F'} {
		disp := DisplayName(utf8.AppendRune(nil, r))
		if strings.ContainsRune(disp, r) {
			t.Errorf("bidi control %U kept verbatim in %q", r, disp)
		}
	}
}

func TestDisplayNameKeepsPrintableText(t *testing.T) {
	for _, s := range []string{"Report.txt", "report.txt", "Program Files", "日本語", "é", "<img src=x onerror=alert(1)>"} {
		if got := DisplayName([]byte(s)); got != s {
			t.Errorf("DisplayName(%q) = %q, want unchanged", s, got)
		}
	}
}
