package rules

import (
	"bytes"
	"errors"
	"strings"
)

// pattern is a validated name pattern, matched against one raw name byte-wise
// with ASCII letters compared case-insensitively: '*' matches any run of
// bytes, '?' exactly one byte, and every other byte itself. Bytes outside
// ASCII, including invalid UTF-8, match only themselves; there is no Unicode
// folding or normalization.
type pattern struct {
	lowered []byte
	// shape, lit, and suffix let the common forms skip the general matcher:
	// lit is the pattern without its leading or trailing '*', or for
	// shapeAffix the part before its only '*', and suffix the part after.
	shape  shape
	lit    []byte
	suffix []byte
}

type shape uint8

const (
	shapeGlob   shape = iota // anything else
	shapeExact               // no wildcard
	shapeSuffix              // '*' then no wildcard, as in "*.exe"
	shapePrefix              // no wildcard, then '*', as in "~$*"
	shapeAffix               // one '*' between parts without wildcards, as in "DSC*.jpg"
)

// parsePattern validates a pattern: a single path component (non-empty, not
// "." or "..", without '/' or NUL) of at most 255 bytes, without the reserved
// bytes '[', ']', and '\'.
func parsePattern(s string) (pattern, error) {
	switch {
	case strings.ContainsAny(s, `[]\`):
		return pattern{}, errors.New(`"[", "]", and "\" are reserved`)
	case len(s) > 255:
		return pattern{}, errors.New("longer than 255 bytes")
	case s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\x00"):
		return pattern{}, errors.New("not a single path component")
	}
	p := pattern{lowered: lowerASCII([]byte(s))}
	wild := func(b []byte) bool { return bytes.ContainsAny(b, "*?") }
	switch l := p.lowered; {
	case !wild(l):
		p.shape, p.lit = shapeExact, l
	case len(l) > 1 && l[0] == '*' && !wild(l[1:]):
		p.shape, p.lit = shapeSuffix, l[1:]
	case len(l) > 1 && l[len(l)-1] == '*' && !wild(l[:len(l)-1]):
		p.shape, p.lit = shapePrefix, l[:len(l)-1]
	default:
		if before, after, ok := bytes.Cut(l, []byte("*")); ok && !wild(before) && !wild(after) {
			p.shape, p.lit, p.suffix = shapeAffix, before, after
		}
	}
	return p, nil
}

// match reports whether name matches the pattern. It allocates nothing.
func (p pattern) match(name []byte) bool {
	switch p.shape {
	case shapeExact:
		return len(name) == len(p.lit) && foldedEqual(p.lit, name)
	case shapeSuffix:
		return len(name) >= len(p.lit) && foldedEqual(p.lit, name[len(name)-len(p.lit):])
	case shapePrefix:
		return len(name) >= len(p.lit) && foldedEqual(p.lit, name[:len(p.lit)])
	case shapeAffix:
		return len(name) >= len(p.lit)+len(p.suffix) && foldedEqual(p.lit, name[:len(p.lit)]) &&
			foldedEqual(p.suffix, name[len(name)-len(p.suffix):])
	}
	return globMatchFold(p.lowered, name)
}

// foldedEqual reports whether name equals the ASCII-lowercased lit, lowering
// ASCII letters of name; the lengths must be equal.
func foldedEqual(lit, name []byte) bool {
	for i, c := range name {
		if lower(c) != lit[i] {
			return false
		}
	}
	return true
}

// matchAny reports whether name matches one of pats.
func matchAny(pats []pattern, name []byte) bool {
	for _, p := range pats {
		if p.match(name) {
			return true
		}
	}
	return false
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// lowerASCII lowers the ASCII letters of b in place and returns it; other
// bytes, including non-ASCII and invalid UTF-8, are unchanged.
func lowerASCII(b []byte) []byte {
	for i, c := range b {
		b[i] = lower(c)
	}
	return b
}

// globMatchFold matches name against an ASCII-lowercased pattern byte-wise,
// lowering ASCII letters of name as it goes: '*' matches any run of bytes,
// '?' exactly one byte. It backtracks only to the last '*', so it runs in
// O(len(pattern) * len(name)) and allocates nothing.
func globMatchFold(pattern, name []byte) bool {
	p, n := 0, 0
	star, mark := -1, 0
	for n < len(name) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, n
			p++
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == lower(name[n])):
			p++
			n++
		case star >= 0:
			mark++
			p, n = star+1, mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
