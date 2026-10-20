package media

import (
	"bytes"
	"fmt"
	"strings"
)

// DefaultTemplate is the template of an organize by date without one.
const DefaultTemplate = "{year}/{month}"

const maxComponents = 4

type token uint8

const (
	tokLiteral token = iota
	tokYear
	tokMonth
	tokDay
	tokEvent
)

var tokens = map[string]token{"{year}": tokYear, "{month}": tokMonth, "{day}": tokDay, "{event}": tokEvent}

type piece struct {
	tok token
	lit string
}

// Template is a parsed organize-by-date template (D16): 1–4 components of
// literal text and the tokens {year}, {month}, {day}, and {event}.
type Template struct {
	src   string
	comps [][]piece
	need  Precision // the finest date token's; "" without one
}

// ParseTemplate parses a template; "" is DefaultTemplate. Every refusal
// wraps ErrInvalidTemplate.
func ParseTemplate(s string) (Template, error) {
	if s == "" {
		s = DefaultTemplate
	}
	bad := func(format string, args ...any) (Template, error) {
		return Template{}, fmt.Errorf("%w: %s", ErrInvalidTemplate, fmt.Sprintf(format, args...))
	}
	parts := strings.Split(s, "/")
	if len(parts) > maxComponents {
		return bad("at most %d folders", maxComponents)
	}
	t := Template{src: s}
	for _, part := range parts {
		if part == "" {
			return bad("an empty folder")
		}
		if part == "." || part == ".." {
			return bad("a folder named %q", part)
		}
		var comp []piece
		for rest := part; rest != ""; {
			switch i := strings.IndexAny(rest, "{}\x00"); {
			case i < 0:
				comp = append(comp, piece{lit: rest})
				rest = ""
			case i > 0:
				comp = append(comp, piece{lit: rest[:i]})
				rest = rest[i:]
			case rest[0] != '{':
				return bad("a brace or NUL outside a token")
			default:
				j := strings.IndexByte(rest, '}')
				if j < 0 {
					return bad("an unclosed token")
				}
				tok, ok := tokens[rest[:j+1]]
				if !ok {
					return bad("unknown token %q", rest[:j+1])
				}
				comp = append(comp, piece{tok: tok})
				rest = rest[j+1:]
				var p Precision
				switch tok {
				case tokYear:
					p = PrecisionYear
				case tokMonth:
					p = PrecisionMonth
				case tokDay:
					p = PrecisionDay
				}
				if p.rank() > t.need.rank() {
					t.need = p
				}
			}
		}
		t.comps = append(t.comps, comp)
	}
	return t, nil
}

// String is the template's text.
func (t Template) String() string {
	if t.src == "" {
		return DefaultTemplate
	}
	return t.src
}

// Folders is the folder path a file of date d, in a folder whose event
// name is event, goes to below the destination. An empty event drops its
// token and the literal text just before it, and a component left empty
// goes. A date coarser than the template's finest token is ErrTooCoarse.
func (t Template) Folders(d Date, event []byte) ([][]byte, error) {
	if t.comps == nil {
		var err error
		if t, err = ParseTemplate(""); err != nil {
			return nil, err
		}
	}
	if t.need != "" && d.Precision.rank() < t.need.rank() {
		return nil, ErrTooCoarse
	}
	if t.need != "" {
		if _, p, err := parseWall(d.Local); err != nil || p != d.Precision {
			return nil, ErrTooCoarse
		}
	}
	var out [][]byte
	for _, comp := range t.comps {
		var parts [][]byte
		lits := []bool{}
		for _, pc := range comp {
			var v []byte
			switch pc.tok {
			case tokLiteral:
				v = []byte(pc.lit)
			case tokYear:
				v = []byte(d.Local[0:4])
			case tokMonth:
				v = []byte(d.Local[5:7])
			case tokDay:
				v = []byte(d.Local[8:10])
			case tokEvent:
				if len(event) == 0 {
					if n := len(parts); n > 0 && lits[n-1] {
						parts, lits = parts[:n-1], lits[:n-1]
					}
					continue
				}
				v = event
			}
			parts = append(parts, v)
			lits = append(lits, pc.tok == tokLiteral)
		}
		c := bytes.Join(parts, nil)
		if len(c) == 0 || string(c) == "." || string(c) == ".." {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// EventName is a folder's name without its leading folder date (D6's
// syntax) and the separators after it: "2010-07 Bahia" is "Bahia", and
// "2010" is empty.
func EventName(folder []byte) []byte {
	_, _, n, ok := leadingDate(string(folder))
	if !ok {
		return bytes.Clone(folder)
	}
	rest := folder[n:]
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '-' || rest[0] == '_' || rest[0] == '.') {
		rest = rest[1:]
	}
	return bytes.Clone(rest)
}

// RenamedName is "YYYYMMDD_HHMMSS_<name>" from the date's Local (D16); a
// name that already starts with that prefix is kept. A date coarser than a
// second is ErrTooCoarse.
func RenamedName(name []byte, d Date) ([]byte, error) {
	if d.Precision != PrecisionSecond {
		return nil, ErrTooCoarse
	}
	if _, p, err := parseWall(d.Local); err != nil || p != PrecisionSecond {
		return nil, ErrTooCoarse
	}
	l := d.Local
	prefix := l[0:4] + l[5:7] + l[8:10] + "_" + l[11:13] + l[14:16] + l[17:19] + "_"
	if bytes.HasPrefix(name, []byte(prefix)) {
		return bytes.Clone(name), nil
	}
	return append([]byte(prefix), name...), nil
}
