package rules

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"sort"

	"github.com/BurntSushi/toml"

	"precious/internal/domain"
)

// SignalID names a signal of the markers file: an observation that one name
// was seen, never a category.
type SignalID string

// maxExtension bounds the length of a file-kind extension.
const maxExtension = 32

// maxStem bounds the stem FileKindNear looks up in a file's sibling stems.
// Longer names never pair.
const maxStem = 255

// maxName is the longest name a pattern can equal.
const maxName = 255

// maxSignals is the most signals a markers file may define: AppendNameSignals
// keeps the signals its name indexes hit in one 64-bit set.
const maxSignals = 64

type signalRule struct {
	id        SignalID
	kinds     kindSet // empty: any kind
	names     []pattern
	except    []pattern
	indicator bool
	// residual are the names patterns that the name indexes of the Policy
	// do not cover.
	residual []pattern
}

// signalSet is a set of signal indexes.
type signalSet uint64

type kindPattern struct {
	kind  domain.FileKind
	names []pattern
}

// kindPair gives a file whose extension is ext the kind kind when its folder
// holds a file with the same stem and the extension with.
type kindPair struct {
	ext, with string
	kind      domain.FileKind
}

// markersFile is the TOML shape of a markers file.
type markersFile struct {
	Version       string              `toml:"version"`
	Signals       []signalFile        `toml:"signals"`
	FileKinds     map[string][]string `toml:"file_kinds"`
	FileKindNames []kindNameFile      `toml:"file_kind_names"`
	FileKindPairs []kindPairFile      `toml:"file_kind_pairs"`
}

type signalFile struct {
	ID        string   `toml:"id"`
	Kinds     []string `toml:"kinds"`
	Names     []string `toml:"names"`
	Except    []string `toml:"except"`
	Indicator bool     `toml:"indicator"`
}

type kindNameFile struct {
	Kind  string   `toml:"kind"`
	Names []string `toml:"names"`
}

type kindPairFile struct {
	Ext  string `toml:"ext"`
	With string `toml:"with"`
	Kind string `toml:"kind"`
}

var (
	versionPattern   = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	idPattern        = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	extensionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_+-]*$`)
)

// loadMarkers decodes and validates a markers file into p. Unknown keys, a
// missing version, duplicate signal IDs, unknown entry or file kinds,
// malformed patterns, and malformed or duplicate extensions are reported
// through add.
func (p *Policy) loadMarkers(data []byte, add func(string, ...any)) {
	var f markersFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		add("%v", err)
		return
	}
	for _, k := range md.Undecoded() {
		add("unknown key %q", k.String())
	}

	p.markersVersion = f.Version
	if !versionPattern.MatchString(f.Version) {
		add("version %q must be a non-empty lowercase identifier such as \"markers-v3\"", f.Version)
	}

	p.signalIdx = make(map[SignalID]int, len(f.Signals))
	for i, s := range f.Signals {
		key := fmt.Sprintf("signals[%d]", i)
		r := signalRule{id: SignalID(s.ID), indicator: s.Indicator}
		_, dup := p.signalIdx[r.id]
		switch {
		case !idPattern.MatchString(s.ID):
			add("%s.id %q must match %s", key, s.ID, idPattern)
		case dup:
			add("%s.id %q is used by another signal", key, s.ID)
		default:
			p.signalIdx[r.id] = len(p.signals)
		}
		for j, k := range s.Kinds {
			bit := kindBit(domain.EntryKind(k))
			if bit == 0 {
				add("%s.kinds[%d] %q is not an entry kind", key, j, k)
				continue
			}
			r.kinds |= bit
		}
		r.names = requiredPatterns(key+".names", s.Names, add)
		r.except = patterns(key+".except", s.Except, add)
		p.signals = append(p.signals, r)
	}
	if len(p.signals) > maxSignals {
		add("signals defines %d signals; at most %d are allowed", len(p.signals), maxSignals)
	} else {
		p.indexSignals()
	}

	kinds := make([]string, 0, len(f.FileKinds))
	for k := range f.FileKinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	p.kindExt = make(map[string]domain.FileKind)
	extOwner := map[string]string{}
	for _, k := range kinds {
		key := "file_kinds." + k
		kind, ok := listedFileKind(k)
		if !ok {
			add("%s: %q must be a file kind other than other, the kind of every unlisted extension", key, k)
			continue
		}
		for j, ext := range f.FileKinds[k] {
			switch {
			case !validExtension(ext):
				add("%s[%d] %q must be a lower-case extension without a dot, at most %d bytes", key, j, ext, maxExtension)
			case extOwner[ext] != "":
				add("%s[%d] %q is already listed under %s", key, j, ext, extOwner[ext])
			default:
				extOwner[ext] = key
				p.kindExt[ext] = kind
			}
		}
	}

	for i, kn := range f.FileKindNames {
		key := fmt.Sprintf("file_kind_names[%d]", i)
		kind, ok := listedFileKind(kn.Kind)
		if !ok {
			add("%s.kind %q must be a file kind other than other", key, kn.Kind)
		}
		p.kindNames = append(p.kindNames, kindPattern{kind: kind, names: requiredPatterns(key+".names", kn.Names, add)})
	}

	pairExt := map[string]bool{}
	for i, kp := range f.FileKindPairs {
		key := fmt.Sprintf("file_kind_pairs[%d]", i)
		kind, ok := listedFileKind(kp.Kind)
		switch {
		case !validExtension(kp.Ext) || !validExtension(kp.With) || kp.Ext == kp.With:
			add("%s: ext %q and with %q must be two different lower-case extensions without a dot", key, kp.Ext, kp.With)
		case !ok:
			add("%s.kind %q must be a file kind other than other", key, kp.Kind)
		case pairExt[kp.Ext]:
			add("%s.ext %q is paired twice", key, kp.Ext)
		case extOwner[kp.Ext] != "":
			add("%s.ext %q is already listed under %s, so it never pairs", key, kp.Ext, extOwner[kp.Ext])
		default:
			pairExt[kp.Ext] = true
			p.pairs = append(p.pairs, kindPair{ext: kp.Ext, with: kp.With, kind: kind})
		}
	}
}

// indexSignals splits every signal's names patterns into the two name
// indexes, for exact names and for "*.ext" patterns, and its residual
// patterns, which AppendNameSignals matches one by one.
func (p *Policy) indexSignals() {
	p.exactIdx = make(map[string]signalSet)
	p.extIdx = make(map[string]signalSet)
	for i := range p.signals {
		r := &p.signals[i]
		bit := signalSet(1) << i
		for _, pat := range r.names {
			switch {
			case pat.shape == shapeExact:
				p.exactIdx[string(pat.lit)] |= bit
			case pat.shape == shapeSuffix && len(pat.lit) > 1 && len(pat.lit)-1 <= maxExtension &&
				pat.lit[0] == '.' && !bytes.Contains(pat.lit[1:], []byte(".")):
				p.extIdx[string(pat.lit[1:])] |= bit
			default:
				r.residual = append(r.residual, pat)
			}
		}
	}
}

// requiredPatterns validates the name patterns listed under key, of which
// there must be at least one.
func requiredPatterns(key string, names []string, add func(string, ...any)) []pattern {
	if len(names) == 0 {
		add("%s must list at least one pattern", key)
	}
	return patterns(key, names, add)
}

// patterns validates the name patterns listed under key.
func patterns(key string, names []string, add func(string, ...any)) []pattern {
	var out []pattern
	for j, n := range names {
		pat, err := parsePattern(n)
		if err != nil {
			add("%s[%d] %q: %v", key, j, n, err)
			continue
		}
		out = append(out, pat)
	}
	return out
}

// kindSet is a set of entry kinds, one bit per kind.
type kindSet uint8

// kindBit returns the bit of an entry kind a markers file may name, or 0.
func kindBit(k domain.EntryKind) kindSet {
	switch k {
	case domain.EntryDirectory:
		return 1 << 0
	case domain.EntryFile:
		return 1 << 1
	case domain.EntrySymlink:
		return 1 << 2
	case domain.EntryFIFO:
		return 1 << 3
	case domain.EntrySocket:
		return 1 << 4
	case domain.EntryCharDevice:
		return 1 << 5
	case domain.EntryBlockDevice:
		return 1 << 6
	}
	return 0
}

// has reports whether the set, where empty means every kind, holds k.
func (s kindSet) has(k domain.EntryKind) bool { return s == 0 || s&kindBit(k) != 0 }

// listedFileKind parses a file kind a markers file may list: any but other.
func listedFileKind(s string) (domain.FileKind, bool) {
	kind, err := domain.ParseFileKind(s)
	return kind, err == nil && kind != domain.FileKindOther
}

func validExtension(ext string) bool {
	return len(ext) <= maxExtension && extensionPattern.MatchString(ext)
}

// AppendNameSignals appends to dst the IDs of the signals that one name of
// the given kind raises, in markers-file order, and returns the extended
// slice. It allocates nothing beyond growing dst, so the scanner may call it
// for every entry it lists.
func (p *Policy) AppendNameSignals(dst []SignalID, name []byte, kind domain.EntryKind) []SignalID {
	if len(name) > maxName {
		for i := range p.signals {
			if p.signals[i].matches(name, kind) {
				dst = append(dst, p.signals[i].id)
			}
		}
		return dst
	}
	var buf [maxName]byte
	lowered := buf[:len(name)]
	for i, c := range name {
		lowered[i] = lower(c)
	}
	hit := p.exactIdx[string(lowered)]
	if dot := bytes.LastIndexByte(lowered, '.'); dot >= 0 && len(lowered)-dot-1 <= maxExtension {
		hit |= p.extIdx[string(lowered[dot+1:])]
	}
	for i := range p.signals {
		r := &p.signals[i]
		if !r.kinds.has(kind) {
			continue
		}
		if (hit&(signalSet(1)<<i) != 0 || matchAny(r.residual, name)) && !matchAny(r.except, name) {
			dst = append(dst, r.id)
		}
	}
	return dst
}

// IsIndicator reports whether the signal marks user material (design D9): a
// folder that holds one below it is never suggested for discard.
func (p *Policy) IsIndicator(id SignalID) bool {
	i, ok := p.signalIdx[id]
	return ok && p.signals[i].indicator
}

// FileKind returns the name-derived file kind of a regular file (§6.2): the
// kind of the first file_kind_names pattern the name matches, else the kind
// its extension (the bytes after the last '.', compared ignoring ASCII case)
// is listed under, else other. A name whose only dot is its first byte has no
// extension. It is a hint, never verified format evidence. A paired extension
// (a disk image's `.bin`) only gets its pair kind through FileKindNear.
func (p *Policy) FileKind(name []byte) domain.FileKind { return p.FileKindNear(name, nil) }

// FileKindNear is FileKind for a file whose folder holds files with the
// sibling stems built by PairStem: a file whose extension is paired in the
// markers file (`bin` with `cue`) gets the pair's kind when its own stem,
// compared ignoring ASCII case, is among them. It allocates nothing.
func (p *Policy) FileKindNear(name []byte, siblingStems map[string]bool) domain.FileKind {
	for i := range p.kindNames {
		if matchAny(p.kindNames[i].names, name) {
			return p.kindNames[i].kind
		}
	}
	_, ext, ok := splitExt(name)
	if !ok {
		return domain.FileKindOther
	}
	var buf [maxExtension]byte
	lowered := buf[:len(ext)]
	for i, c := range ext {
		lowered[i] = lower(c)
	}
	if k, ok := p.kindExt[string(lowered)]; ok {
		return k
	}
	if k, ok := p.pairKind(name, siblingStems); ok {
		return k
	}
	return domain.FileKindOther
}

// pairKind returns the pair kind of a file whose extension is paired and
// whose stem, ASCII-lowercased, is among siblingStems.
func (p *Policy) pairKind(name []byte, siblingStems map[string]bool) (domain.FileKind, bool) {
	stem, ext, ok := splitExt(name)
	if !ok || len(siblingStems) == 0 || len(stem) > maxStem {
		return "", false
	}
	for i := range p.pairs {
		if !equalFoldASCII(ext, p.pairs[i].ext) {
			continue
		}
		var buf [maxStem]byte
		s := buf[:len(stem)]
		for j, c := range stem {
			s[j] = lower(c)
		}
		if siblingStems[string(s)] {
			return p.pairs[i].kind, true
		}
	}
	return "", false
}

// PairStem returns the ASCII-lowercased stem of name when its extension is
// the companion of a file-kind pair (a `.cue` sheet beside its `.bin`). The
// scanner collects these stems over a folder's files into
// FileFacts.SiblingStems; names of no pair give ok = false.
func (p *Policy) PairStem(name []byte) (stem string, ok bool) {
	s, ext, ok := splitExt(name)
	if !ok {
		return "", false
	}
	for i := range p.pairs {
		if equalFoldASCII(ext, p.pairs[i].with) {
			return string(lowerASCII(slices.Clone(s))), true
		}
	}
	return "", false
}

// Paired reports whether the kind of a file called name may depend on its
// folder's other files: its extension is the paired side of a file-kind pair
// (a `.bin` that is a disk image only beside its `.cue`). The scanner holds
// back only such files until their folder is listed. It allocates nothing.
func (p *Policy) Paired(name []byte) bool {
	_, ext, ok := splitExt(name)
	if !ok {
		return false
	}
	for i := range p.pairs {
		if equalFoldASCII(ext, p.pairs[i].ext) {
			return true
		}
	}
	return false
}

// splitExt splits a name at its last dot into a stem and a non-empty
// extension of at most maxExtension bytes. A name whose only dot is its first
// byte has no extension.
func splitExt(name []byte) (stem, ext []byte, ok bool) {
	for i := len(name) - 1; i > 0; i-- {
		if name[i] == '.' {
			ext = name[i+1:]
			return name[:i], ext, len(ext) > 0 && len(ext) <= maxExtension
		}
	}
	return nil, nil, false
}

// equalFoldASCII reports whether b equals the lower-case s ignoring ASCII
// case.
func equalFoldASCII(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i, c := range b {
		if lower(c) != s[i] {
			return false
		}
	}
	return true
}

// matches reports whether name (of kind) raises the signal, matching every
// names pattern in turn: the reference AppendNameSignals's indexes follow.
func (r *signalRule) matches(name []byte, kind domain.EntryKind) bool {
	if !r.kinds.has(kind) {
		return false
	}
	return matchAny(r.names, name) && !matchAny(r.except, name)
}
