package sources

import (
	"path/filepath"
	"strings"

	"precious/internal/fsaccess"
)

// mountFor returns the mount holding the absolute path p: the one with the
// deepest mount point containing p, and the last listed among mounts at that
// point, which covers the earlier ones.
func mountFor(mounts []fsaccess.Mount, p string) (fsaccess.Mount, bool) {
	best := -1
	for i, m := range mounts {
		if containsPath(m.Point, p) && (best < 0 || len(m.Point) >= len(mounts[best].Point)) {
			best = i
		}
	}
	if best < 0 {
		return fsaccess.Mount{}, false
	}
	return mounts[best], true
}

// mountAt returns the last mount listed at point.
func mountAt(mounts []fsaccess.Mount, point string) (fsaccess.Mount, bool) {
	for i := len(mounts) - 1; i >= 0; i-- {
		if mounts[i].Point == point {
			return mounts[i], true
		}
	}
	return fsaccess.Mount{}, false
}

// relRoot is the folder p, which lies on m, relative to m's volume: below the
// filesystem root for a volume with an identity (a bind mount contributes its
// mount root), and below the mount point for a path volume, which is known by
// its mount point alone. The result is '/'-joined, empty for the root.
func relRoot(m fsaccess.Mount, p string) []byte {
	sub := ""
	if p != m.Point {
		sub = filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(p, m.Point), string(filepath.Separator)))
	}
	if m.Volume.Kind == fsaccess.VolumePath {
		return []byte(sub)
	}
	return []byte(joinRel(strings.Trim(string(m.Root), "/"), sub))
}

// absRoot is where the folder rel of m's volume is reached through m, and
// false when m mounts a part of the volume that does not hold it.
func absRoot(m fsaccess.Mount, kind fsaccess.VolumeKind, rel []byte) (string, bool) {
	r := string(rel)
	if kind != fsaccess.VolumePath {
		root := strings.Trim(string(m.Root), "/")
		switch {
		case root == "":
		case r == root:
			r = ""
		case strings.HasPrefix(r, root+"/"):
			r = r[len(root)+1:]
		default:
			return "", false
		}
	}
	if r == "" {
		return m.Point, true
	}
	return filepath.Join(m.Point, filepath.FromSlash(r)), true
}

// locate finds the mount through which src's root folder is reached: a mount
// of the same volume holding the folder, preferring the mount point src was
// last seen at, then the mount of the largest part of the volume.
func locate(mounts []fsaccess.Mount, src Source) (fsaccess.Mount, string, bool) {
	var (
		best    fsaccess.Mount
		bestAbs string
		found   bool
	)
	for _, m := range mounts {
		if m.Volume.Kind != src.Volume.Kind || m.Volume.ID != src.Volume.ID {
			continue
		}
		abs, ok := absRoot(m, src.Volume.Kind, src.RelRoot)
		if !ok {
			continue
		}
		if !found || better(m, best, src.MountPoint) {
			best, bestAbs, found = m, abs, true
		}
	}
	return best, bestAbs, found
}

func better(m, than fsaccess.Mount, last string) bool {
	if (m.Point == last) != (than.Point == last) {
		return m.Point == last
	}
	return len(strings.Trim(string(m.Root), "/")) < len(strings.Trim(string(than.Root), "/"))
}

// pathRoot is the only location of a path-volume source's root folder.
func pathRoot(src Source) string {
	if len(src.RelRoot) == 0 {
		return src.Volume.ID
	}
	return filepath.Join(src.Volume.ID, filepath.FromSlash(string(src.RelRoot)))
}

// containsPath reports whether the absolute path p is dir or lies below it.
func containsPath(dir, p string) bool {
	if dir == p {
		return true
	}
	sep := string(filepath.Separator)
	if strings.HasSuffix(dir, sep) {
		return strings.HasPrefix(p, dir)
	}
	return strings.HasPrefix(p, dir+sep)
}

// relNested reports whether two '/'-joined relative folders are equal or one
// lies inside the other; "" is the root and holds every folder.
func relNested(a, b string) bool {
	return a == "" || b == "" || a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func joinRel(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "/" + b
}
