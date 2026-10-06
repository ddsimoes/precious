package index

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/rules"
)

// opt is a nullable integer column.
type opt struct {
	v  int64
	ok bool
}

func some(v int64) opt { return opt{v: v, ok: true} }

// arg is the column's bind value.
func (o opt) arg() any {
	if !o.ok {
		return nil
	}
	return o.v
}

// Scan implements sql.Scanner.
func (o *opt) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*o = opt{}
	case int64:
		*o = some(v)
	default:
		return fmt.Errorf("index: scan %T into an integer column", src)
	}
	return nil
}

// text is a nullable text column: "" is NULL.
type text string

func (t text) arg() any {
	if t == "" {
		return nil
	}
	return string(t)
}

// Scan implements sql.Scanner.
func (t *text) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*t = ""
	case string:
		*t = text(v)
	case []byte:
		*t = text(v)
	default:
		return fmt.Errorf("index: scan %T into a text column", src)
	}
	return nil
}

// row is the columns of one entries row a scan writes, other than its
// identity (source, parent, name, path), its link text, its times seen, and
// its decision. It is comparable: a stored row equal to the one a scan builds
// is not written.
type row struct {
	kind    string
	special text
	size    int64
	// Own facts from Lstat (design D6); mode is Go's fs.FileMode bits and
	// alloc is st_blocks*512.
	alloc, mtime, ctime, dev, ino, nlink, mode opt
	totalBytes, totalFiles                     int64
	newest, oldest                             opt
	ext, fileKind, mainKind                    text
	category, family, traits, triage, ruleIDs  text
	group, veto                                bool
	state                                      string
	partial, boundary                          bool
}

// rowColumns are the entries columns of a row, in args order (link_text
// after mode).
const rowColumns = `kind, special_kind, size, alloc, total_bytes, total_files, mtime_ns, ctime_ns,
	newest_ns, oldest_ns, dev, ino, nlink, mode, link_text, ext, file_kind, main_kind, category, family,
	traits, triage, is_group, veto, rule_ids, state, partial, mount_boundary`

// rowParams is one placeholder per rowColumns column.
const rowParams = `?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`

// rowSet assigns every rowColumns column.
const rowSet = `kind = ?, special_kind = ?, size = ?, alloc = ?, total_bytes = ?, total_files = ?,
	mtime_ns = ?, ctime_ns = ?, newest_ns = ?, oldest_ns = ?, dev = ?, ino = ?, nlink = ?, mode = ?,
	link_text = ?, ext = ?, file_kind = ?, main_kind = ?, category = ?, family = ?, traits = ?, triage = ?,
	is_group = ?, veto = ?, rule_ids = ?, state = ?, partial = ?, mount_boundary = ?`

// args appends the row's values in rowColumns order.
func (r *row) args(dst []any, link []byte) []any {
	var linkArg any
	if link != nil {
		linkArg = link
	}
	return append(dst, r.kind, r.special.arg(), r.size, r.alloc.arg(), r.totalBytes, r.totalFiles,
		r.mtime.arg(), r.ctime.arg(), r.newest.arg(), r.oldest.arg(), r.dev.arg(), r.ino.arg(),
		r.nlink.arg(), r.mode.arg(), linkArg, r.ext.arg(), r.fileKind.arg(), r.mainKind.arg(),
		r.category.arg(), r.family.arg(), r.traits.arg(), r.triage.arg(), boolInt(r.group),
		boolInt(r.veto), r.ruleIDs.arg(), r.state, boolInt(r.partial), boolInt(r.boundary))
}

// setFacts sets the row's own facts from an Lstat, encoded as the viewer
// compares them: times in Unix nanoseconds (ctime NULL when the platform has
// none), identity as the bit pattern of the unsigned values.
func (r *row) setFacts(info *fsaccess.EntryInfo) {
	r.size = info.Size
	r.alloc = some(info.Blocks * 512)
	r.mtime = some(info.ModTime.UnixNano())
	r.ctime = opt{}
	if !info.Ctime.IsZero() {
		r.ctime = some(info.Ctime.UnixNano())
	}
	r.dev = some(int64(info.Dev))
	r.ino = some(int64(info.Ino))
	r.nlink = some(int64(info.Nlink))
	r.mode = some(int64(uint32(info.Mode)))
	r.boundary = info.MountBoundary
}

// copyFacts takes the own facts of a stored row whose entry is unchanged
// (design D8), so that a time within the filesystem's tolerance is kept as
// stored.
func (r *row) copyFacts(old *row) {
	r.size, r.alloc, r.mtime, r.ctime = old.size, old.alloc, old.mtime, old.ctime
	r.dev, r.ino, r.nlink, r.mode = old.dev, old.ino, old.nlink, old.mode
}

// classify sets the row's classification columns.
func (r *row) classify(res *rules.Result, c *codec) {
	r.category = text(res.Category)
	r.family = text(res.Family)
	r.triage = text(res.Triage)
	r.traits = c.traits(res.Traits)
	r.ruleIDs = c.ruleIDs(res.Rules)
	r.group, r.veto = res.Group, res.Veto
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// kindColumn is the entries.kind and special_kind of an entry kind.
func kindColumn(k domain.EntryKind) (string, text) {
	switch k {
	case domain.EntryDirectory, domain.EntryFile, domain.EntrySymlink:
		return string(k), ""
	default:
		return "special", text(k)
	}
}

// statsCols are the dir_stats columns of a folder, JSON as stored.
type statsCols struct {
	dirs, files, symlinks, specials, unreadable, mounts   int64
	byKind, byYear, byFamily, signals, indicators, inside string
}

// counts is one breakdown cell of dir_stats.
type counts struct{ files, bytes int64 }

func (c *counts) add(o counts) {
	c.files += o.files
	c.bytes += o.bytes
}

// familyIndex orders the four families as their JSON keys sort.
var familyKeys = func() []domain.Family {
	f := slices.Clone(domain.Families)
	slices.Sort(f)
	return f
}()

func familyIndex(f domain.Family) int { return slices.Index(familyKeys, f) }

// codec builds the JSON columns of a scan. Its caches hold the few distinct
// values of extensions, traits, and rule lists, so that a file row costs no
// allocation for them.
type codec struct {
	buf    []byte
	intern map[string]string
	keys   []string
	years  []int
}

func newCodec() *codec { return &codec{intern: map[string]string{}} }

// interned returns buf as a string, allocating only the first time.
func (c *codec) interned(b []byte) text {
	if s, ok := c.intern[string(b)]; ok {
		return text(s)
	}
	s := string(b)
	c.intern[s] = s
	return text(s)
}

// ext is the ASCII-lowercased text after the last '.' of a file name, or ""
// when there is none or the only '.' is the first byte.
func (c *codec) ext(name []byte) text {
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
	c.buf = c.buf[:0]
	for _, b := range name[i+1:] {
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		c.buf = append(c.buf, b)
	}
	return c.interned(c.buf)
}

func (c *codec) traits(ts []domain.Trait) text {
	if len(ts) == 0 {
		return ""
	}
	c.buf = append(c.buf[:0], '[')
	for i, t := range ts {
		if i > 0 {
			c.buf = append(c.buf, ',')
		}
		c.buf = strconv.AppendQuote(c.buf, string(t))
	}
	c.buf = append(c.buf, ']')
	return c.interned(c.buf)
}

func (c *codec) ruleIDs(ids []string) text {
	if len(ids) == 0 {
		return ""
	}
	c.buf = append(c.buf[:0], '[')
	for i, id := range ids {
		if i > 0 {
			c.buf = append(c.buf, ',')
		}
		c.buf = strconv.AppendQuote(c.buf, id)
	}
	c.buf = append(c.buf, ']')
	return c.interned(c.buf)
}

func (c *codec) cell(key string, n counts) {
	if len(c.buf) > 1 {
		c.buf = append(c.buf, ',')
	}
	c.buf = strconv.AppendQuote(c.buf, key)
	c.buf = append(c.buf, `:{"files":`...)
	c.buf = strconv.AppendInt(c.buf, n.files, 10)
	c.buf = append(c.buf, `,"bytes":`...)
	c.buf = strconv.AppendInt(c.buf, n.bytes, 10)
	c.buf = append(c.buf, '}')
}

// byKind is {"<kind>":{"files":n,"bytes":n}} over the kinds with files,
// keys sorted.
func (c *codec) byKind(m map[domain.FileKind]rules.KindTotals) string {
	c.keys = c.keys[:0]
	for k, t := range m {
		if t.Files > 0 {
			c.keys = append(c.keys, string(k))
		}
	}
	slices.Sort(c.keys)
	c.buf = append(c.buf[:0], '{')
	for _, k := range c.keys {
		t := m[domain.FileKind(k)]
		c.cell(k, counts{files: t.Files, bytes: t.Bytes})
	}
	return string(append(c.buf, '}'))
}

// byYear is {"<UTC year>":{"files":n,"bytes":n}}, keys sorted as strings.
func (c *codec) byYear(m map[int]counts) string {
	c.years = c.years[:0]
	for y := range m {
		c.years = append(c.years, y)
	}
	slices.SortFunc(c.years, func(a, b int) int {
		var x, y [24]byte
		return slices.Compare(strconv.AppendInt(x[:0], int64(a), 10), strconv.AppendInt(y[:0], int64(b), 10))
	})
	c.buf = append(c.buf[:0], '{')
	var key [24]byte
	for _, y := range c.years {
		c.cell(string(strconv.AppendInt(key[:0], int64(y), 10)), m[y])
	}
	return string(append(c.buf, '}'))
}

// byFamily is all four families, keys sorted.
func (c *codec) byFamily(f *[4]counts) string {
	c.buf = append(c.buf[:0], '{')
	for i, k := range familyKeys {
		c.cell(string(k), f[i])
	}
	return string(append(c.buf, '}'))
}

// signals is {"<signal>":n}, keys sorted.
func (c *codec) signals(m map[rules.SignalID]int) string {
	c.keys = c.keys[:0]
	for k := range m {
		c.keys = append(c.keys, string(k))
	}
	slices.Sort(c.keys)
	c.buf = append(c.buf[:0], '{')
	for i, k := range c.keys {
		if i > 0 {
			c.buf = append(c.buf, ',')
		}
		c.buf = strconv.AppendQuote(c.buf, k)
		c.buf = append(c.buf, ':')
		c.buf = strconv.AppendInt(c.buf, int64(m[rules.SignalID(k)]), 10)
	}
	return string(append(c.buf, '}'))
}

// sameTime reports whether a stored modification time and an observed one
// are the same under the filesystem's capabilities (design D8): within its
// resolution, or, on a local-time filesystem, within its resolution of a
// one-hour daylight-saving shift either way.
func sameTime(caps *fsaccess.Capabilities, stored opt, observed time.Time) bool {
	if !stored.ok {
		return false
	}
	res := int64(caps.TimeResolution)
	d := observed.UnixNano() - stored.v
	if abs(d) <= res {
		return true
	}
	if caps.LocalTime {
		const hour = int64(time.Hour)
		return abs(d-hour) <= res || abs(d+hour) <= res
	}
	return false
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
