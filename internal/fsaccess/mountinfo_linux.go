package fsaccess

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// parseMountinfo parses /proc/self/mountinfo (proc(5)). Each line is
//
//	mountID parentID major:minor root mountPoint options [optional...] - fstype source superOptions
//
// with zero or more optional fields before the "-" separator. Paths and the
// source are unescaped from the kernel's octal form (`\040` for a space). A
// malformed line fails the whole parse: a mount table that cannot be trusted
// cannot prove that a directory is not a mount boundary.
func parseMountinfo(data []byte) ([]MountInfo, error) {
	var rows []MountInfo
	for n, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		row, err := parseMountinfoLine(string(line))
		if err != nil {
			return nil, fmt.Errorf("mountinfo line %d: %w", n+1, err)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("mountinfo: no mounts listed")
	}
	return rows, nil
}

func parseMountinfoLine(line string) (MountInfo, error) {
	// Fields are separated by single spaces; spaces inside values are escaped.
	// strings.Split keeps an empty mount source as an empty field.
	f := strings.Split(line, " ")
	if len(f) < 9 {
		return MountInfo{}, fmt.Errorf("too few fields (%d)", len(f))
	}
	sep := -1
	for i := 6; i < len(f); i++ {
		if f[i] == "-" {
			sep = i
			break
		}
	}
	if sep < 0 || len(f) < sep+3 {
		return MountInfo{}, fmt.Errorf("missing optional-field separator or filesystem fields")
	}
	id, err := strconv.Atoi(f[0])
	if err != nil {
		return MountInfo{}, fmt.Errorf("mount ID %q: %w", f[0], err)
	}
	if _, _, ok := strings.Cut(f[2], ":"); !ok {
		return MountInfo{}, fmt.Errorf("major:minor %q", f[2])
	}
	return MountInfo{
		MountID:    id,
		MajorMinor: f[2],
		Root:       unescapeOctal(f[3]),
		MountPoint: unescapeOctal(f[4]),
		FSType:     unescapeOctal(f[sep+1]),
		Source:     unescapeOctal(f[sep+2]),
		ReadOnly:   hasOption(f[5], "ro") || (len(f) > sep+3 && hasOption(f[sep+3], "ro")),
	}, nil
}

// hasOption reports whether the comma-separated option list contains opt.
func hasOption(list, opt string) bool {
	for o := range strings.SplitSeq(list, ",") {
		if o == opt {
			return true
		}
	}
	return false
}

// unescapeOctal decodes the kernel's `\ooo` escapes; any other backslash is
// kept literally.
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] >= '0' && s[i+1] <= '3' && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// mountTable is the mount snapshot taken by OpenRoot and shared by every Dir
// opened beneath that root.
type mountTable struct {
	rows   []MountInfo
	points map[string]struct{}
}

func newMountTable(rows []MountInfo) *mountTable {
	t := &mountTable{rows: rows, points: make(map[string]struct{}, len(rows))}
	for _, r := range rows {
		t.points[r.MountPoint] = struct{}{}
	}
	return t
}

// isMountPoint reports whether the absolute path p is listed as a mount point.
// This is what catches same-device bind mounts.
func (t *mountTable) isMountPoint(p string) bool {
	_, ok := t.points[p]
	return ok
}

// isChildMountPoint reports whether the child name of the absolute directory
// dir is listed as a mount point, without allocating for common path lengths.
func (t *mountTable) isChildMountPoint(dir string, name []byte) bool {
	var buf [512]byte
	p := append(buf[:0], dir...)
	if dir != "/" {
		p = append(p, '/')
	}
	p = append(p, name...)
	_, ok := t.points[string(p)]
	return ok
}

// lookup returns a copy of the row for the mount holding the absolute path p:
// the deepest mount point containing p whose major:minor equals dev, else the
// deepest containing mount point regardless of device (btrfs subvolumes report
// a st_dev that differs from the mount's).
func (t *mountTable) lookup(p string, dev uint64) *MountInfo {
	mm := fmt.Sprintf("%d:%d", unix.Major(dev), unix.Minor(dev))
	best := deepestMount(t.rows, p, func(r *MountInfo) bool { return r.MajorMinor == mm })
	if best == nil {
		best = deepestMount(t.rows, p, nil)
	}
	if best == nil {
		return nil
	}
	m := *best
	return &m
}

// deepestMount returns the row with the deepest mount point containing the
// absolute path p among those match accepts (all rows when match is nil), or
// nil. Among equally deep rows the last one wins, since a later mount stacks
// over an earlier one at the same point. The result aliases rows.
func deepestMount(rows []MountInfo, p string, match func(*MountInfo) bool) *MountInfo {
	var best *MountInfo
	for i := range rows {
		r := &rows[i]
		if !containsPath(r.MountPoint, p) || (match != nil && !match(r)) {
			continue
		}
		if best == nil || len(r.MountPoint) >= len(best.MountPoint) {
			best = r
		}
	}
	return best
}

// containsPath reports whether the absolute path p is mountPoint or lies below it.
func containsPath(mountPoint, p string) bool {
	if mountPoint == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == mountPoint || strings.HasPrefix(p, mountPoint+"/")
}
