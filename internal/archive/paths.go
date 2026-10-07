package archive

import (
	"bytes"
	"crypto/sha256"
	"hash"
)

// cleanPath splits a raw member path on '/' only and drops empty and "."
// components (m4b D4). Components keep their raw bytes, so a backslash
// is an ordinary byte; they alias raw, each capped at its own end. abs
// reports a raw path starting with '/'; leaves reports a path that leaves the
// archive: abs, or one with a ".." component, which is kept in comps.
func cleanPath(raw []byte) (comps [][]byte, abs, leaves bool) {
	abs = len(raw) > 0 && raw[0] == '/'
	leaves = abs
	for len(raw) > 0 {
		var c []byte
		if i := bytes.IndexByte(raw, '/'); i >= 0 {
			c, raw = raw[:i:i], raw[i+1:]
		} else {
			c, raw = raw, nil
		}
		switch {
		case len(c) == 0, len(c) == 1 && c[0] == '.':
			continue
		case len(c) == 2 && c[0] == '.' && c[1] == '.':
			leaves = true
		}
		comps = append(comps, c)
	}
	return comps, abs, leaves
}

// joinPath joins cleaned components with '/', after a leading '/' when abs.
// The result is never nil: an empty path is the archive's top level.
func joinPath(comps [][]byte, abs bool) []byte {
	n := len(comps)
	if abs {
		n++
	}
	for _, c := range comps {
		n += len(c)
	}
	b := make([]byte, 0, n)
	if abs {
		b = append(b, '/')
	}
	for i, c := range comps {
		if i > 0 {
			b = append(b, '/')
		}
		b = append(b, c...)
	}
	return b
}

// pathKey is a cleaned path's SHA-256, truncated to 128 bits (m4b D4): a
// false collision would only reject an archive, with negligible probability.
type pathKey [16]byte

// The flags a pathSet keeps per cleaned path.
const (
	// pathFolder: a directory member, or a folder implied by a deeper member.
	pathFolder uint8 = 1 << iota
	// pathMember: a member other than a directory.
	pathMember
)

// pathSet detects colliding member paths (m4b D4) and resolves tar hard
// links. It keeps one key and its flags per cleaned path, implied folders
// included, and the size of every path a hard link may name.
type pathSet struct {
	h    hash.Hash
	sum  [sha256.Size]byte
	seen map[pathKey]uint8
	// content holds the unpacked size of each file member, and of each hard
	// link to one: the paths a hard link may name.
	content map[pathKey]int64
}

func newPathSet() *pathSet {
	return &pathSet{h: sha256.New(), seen: make(map[pathKey]uint8), content: make(map[pathKey]int64)}
}

var slash = []byte{'/'}

// add records a member at the cleaned path comps, which must not leave the
// archive, and every folder above it. It returns the path's key, the number
// of folders above it seen for the first time (they count against the entry
// budget), and the detail of a rejection, or "". Two directory members may
// share a path; any other repeat is rejected, as is a path that is both a
// folder and another kind of member. The empty path is the top level, a
// folder.
func (s *pathSet) add(comps [][]byte, dir bool) (key pathKey, implied int, detail string) {
	if len(comps) == 0 {
		if dir {
			return pathKey{}, 0, ""
		}
		return pathKey{}, 0, detailFileFolder
	}
	s.h.Reset()
	for i, c := range comps {
		key = s.next(i, c)
		v := s.seen[key]
		last := i == len(comps)-1
		if dir || !last {
			if v&pathMember != 0 {
				return key, implied, detailFileFolder
			}
			if v == 0 && !last {
				implied++
			}
			if v&pathFolder == 0 {
				s.seen[key] = v | pathFolder
			}
			continue
		}
		switch {
		case v&pathMember != 0:
			return key, implied, detailSharedPath
		case v&pathFolder != 0:
			return key, implied, detailFileFolder
		}
		s.seen[key] = pathMember
	}
	return key, implied, ""
}

// target returns the size of the content at the cleaned path comps, and
// whether a hard link may name it: a file member, or a hard link to one.
func (s *pathSet) target(comps [][]byte) (size int64, ok bool) {
	if len(comps) == 0 {
		return 0, false
	}
	s.h.Reset()
	var key pathKey
	for i, c := range comps {
		key = s.next(i, c)
	}
	size, ok = s.content[key]
	return size, ok
}

// markContent makes the path of key, whose content has size bytes, a
// hard-link target.
func (s *pathSet) markContent(key pathKey, size int64) { s.content[key] = size }

// next extends the running hash by component i and returns the key of the
// path so far. Hashing each prefix incrementally keeps a deep path linear.
func (s *pathSet) next(i int, c []byte) pathKey {
	if i > 0 {
		s.h.Write(slash)
	}
	s.h.Write(c)
	var key pathKey
	copy(key[:], s.h.Sum(s.sum[:0]))
	return key
}
