package media

import (
	"bytes"
	"encoding/binary"
	"time"
)

// canonUUID is the user type of CR3's `moov/uuid` box holding the CMT
// boxes (D1).
var canonUUID = []byte{0x85, 0xc0, 0xb6, 0x87, 0x82, 0x0f, 0x11, 0xe0, 0x81, 0x11, 0xf4, 0xce, 0x46, 0x2b, 0x6a, 0x48}

// mac1904 is the ISO-BMFF epoch, 1904-01-01 UTC, in Unix seconds.
const mac1904 = -2082844800

// maxContainerSecs bounds an `mvhd` time to before the year maxYear+1.
const maxContainerSecs = 7289654400 - mac1904 // 2201-01-01

// box is one ISO-BMFF box header: its type, where its payload starts (after
// the header and a `uuid` box's user type), and where it ends.
type box struct {
	typ     string
	user    []byte // a `uuid` box's user type
	payload int64
	end     int64
}

// boxes walks the boxes of v between pos and end, at nesting depth depth,
// reading only their headers, and calls fn for each.
func boxes(s *source, v view, pos, end int64, depth int, fn func(b box) error) error {
	if depth > maxDepth {
		return errMalformed
	}
	for pos < end {
		if err := s.box(); err != nil {
			return err
		}
		if end-pos < 8 {
			return errMalformed
		}
		h, err := v.at(pos, 8)
		if err != nil {
			return err
		}
		size, hdr := int64(binary.BigEndian.Uint32(h)), int64(8)
		b := box{typ: string(h[4:8])}
		switch size {
		case 0: // to the end of its parent
			size = end - pos
		case 1:
			l, err := v.at(pos+8, 8)
			if err != nil {
				return err
			}
			u := binary.BigEndian.Uint64(l)
			if u > uint64(end-pos) {
				return errMalformed
			}
			size, hdr = int64(u), 16
		}
		if b.typ == "uuid" {
			u, err := v.at(pos+hdr, 16)
			if err != nil {
				return err
			}
			b.user, hdr = u, hdr+16
		}
		if size < hdr || size > end-pos {
			return errMalformed
		}
		b.payload, b.end = pos+hdr, pos+size
		if err := fn(b); err != nil {
			return err
		}
		pos = b.end
	}
	return nil
}

// readBMFF reads `meta` (an image's Exif item) and `moov` (`mvhd` and
// CR3's CMT boxes) wherever they sit, skipping everything else by size.
func readBMFF(s *source) (Meta, error) {
	var fields exifFields
	var container *time.Time
	v := fileView{s}
	err := boxes(s, v, 0, s.size, 1, func(b box) error {
		switch b.typ {
		case "meta":
			f, err := readMetaBox(s, v, b)
			if err != nil {
				return err
			}
			fields.merge(f)
		case "moov":
			f, c, err := readMoov(s, v, b)
			if err != nil {
				return err
			}
			fields.merge(f)
			if container == nil {
				container = c
			}
		}
		return nil
	})
	if err != nil {
		return Meta{}, err
	}
	m := fields.meta()
	m.Container = container
	return m, nil
}

// readMoov reads `mvhd`'s creation time and, in Canon's `uuid` box, the
// CMT1 (IFD0), CMT2 (Exif IFD), and CMT4 (GPS IFD) TIFF structures.
func readMoov(s *source, v view, moov box) (exifFields, *time.Time, error) {
	var fields exifFields
	var created *time.Time
	err := boxes(s, v, moov.payload, moov.end, 2, func(b box) error {
		switch {
		case b.typ == "mvhd":
			t, err := mvhdCreation(v, b)
			if err != nil {
				return err
			}
			if created == nil {
				created = t
			}
		case b.typ == "uuid" && bytes.Equal(b.user, canonUUID):
			return boxes(s, v, b.payload, b.end, 3, func(c box) error {
				var mode tiffMode
				switch c.typ {
				case "CMT1":
					mode = tiffOnly0
				case "CMT2":
					mode = tiffOnlyExif
				case "CMT4":
					mode = tiffOnlyGPS
				default:
					return nil
				}
				body, err := blob(v, c.payload, c.end-c.payload)
				if err != nil {
					return err
				}
				f, err := parseTIFF(memView(body), s, mode)
				if err != nil {
					return err
				}
				fields.merge(f)
				return nil
			})
		}
		return nil
	})
	return fields, created, err
}

// blob reads up to maxBlob bytes of a payload; a longer one is cut there.
func blob(v view, off, n int64) ([]byte, error) {
	if n > maxBlob {
		n = maxBlob
	}
	return v.at(off, int(n))
}

// field reads n bytes at off within b's payload; a field past the payload's
// end is malformed, so a short box never borrows its sibling's bytes
// (Addendum G8).
func field(v view, b box, off, n int64) ([]byte, error) {
	if off < 0 || n > b.end-b.payload-off {
		return nil, errMalformed
	}
	return v.at(b.payload+off, int(n))
}

// mvhdCreation is `mvhd`'s creation_time, read as UTC; 0 is absent.
func mvhdCreation(v view, b box) (*time.Time, error) {
	h, err := field(v, b, 0, 4)
	if err != nil {
		return nil, err
	}
	var secs uint64
	switch h[0] {
	case 0:
		c, err := field(v, b, 4, 4)
		if err != nil {
			return nil, err
		}
		secs = uint64(binary.BigEndian.Uint32(c))
	case 1:
		c, err := field(v, b, 4, 8)
		if err != nil {
			return nil, err
		}
		secs = binary.BigEndian.Uint64(c)
	default:
		return nil, errMalformed
	}
	if secs == 0 || secs > maxContainerSecs {
		return nil, nil
	}
	t := time.Unix(int64(secs)+mac1904, 0).UTC()
	return &t, nil
}

// readMetaBox finds the `Exif` item of an image's `meta` box through
// `iinf` and `iloc`, and parses it as TIFF.
func readMetaBox(s *source, v view, meta box) (exifFields, error) {
	var f exifFields
	var iinf, iloc, idat *box
	// `meta` is a full box: version and flags precede its children.
	if meta.end-meta.payload < 4 {
		return f, errMalformed
	}
	err := boxes(s, v, meta.payload+4, meta.end, 2, func(b box) error {
		switch b.typ {
		case "iinf":
			iinf = &b
		case "iloc":
			iloc = &b
		case "idat":
			idat = &b
		}
		return nil
	})
	if err != nil || iinf == nil || iloc == nil {
		return f, err
	}
	id, ok, err := exifItem(s, v, *iinf)
	if err != nil || !ok {
		return f, err
	}
	locBody, err := v.at(iloc.payload, int(min(iloc.end-iloc.payload, maxBlob)))
	if err != nil {
		return f, err
	}
	exts, method, ok, err := itemExtents(locBody, id)
	if err != nil || !ok {
		return f, err
	}
	var data []byte
	for _, e := range exts {
		off := e.off
		if method == 1 {
			if idat == nil || e.off > idat.end-idat.payload {
				return f, errMalformed
			}
			off += idat.payload
			if e.n > idat.end-off {
				return f, errMalformed
			}
		}
		n := min(e.n, maxBlob-int64(len(data)))
		if n <= 0 {
			break
		}
		part, err := v.at(off, int(n))
		if err != nil {
			return f, err
		}
		data = append(data, part...)
	}
	// The item starts with the offset of the TIFF header after itself
	// (usually 6, for "Exif\0\0").
	if len(data) < 4 {
		return f, errMalformed
	}
	skip := int64(binary.BigEndian.Uint32(data))
	if skip > int64(len(data))-4 {
		return f, errMalformed
	}
	return parseTIFF(memView(data[4+skip:]), s, tiffFull)
}

// exifItem returns the ID of the first `infe` of type "Exif" in `iinf`.
func exifItem(s *source, v view, iinf box) (uint32, bool, error) {
	h, err := field(v, iinf, 0, 4)
	if err != nil {
		return 0, false, err
	}
	first := int64(6) // version and flags, entry_count (16 bits)
	if h[0] != 0 {
		first = 8 // entry_count (32 bits)
	}
	if first > iinf.end-iinf.payload {
		return 0, false, errMalformed
	}
	var id uint32
	found := false
	err = boxes(s, v, iinf.payload+first, iinf.end, 3, func(b box) error {
		if found || b.typ != "infe" {
			return nil
		}
		ver, err := field(v, b, 0, 4)
		if err != nil {
			return err
		}
		var idLen int64
		switch ver[0] {
		case 2:
			idLen = 2
		case 3:
			idLen = 4
		default:
			return nil // versions 0 and 1 carry no item type
		}
		e, err := field(v, b, 4, idLen+2+4)
		if err != nil {
			return err
		}
		if string(e[idLen+2:]) != "Exif" {
			return nil
		}
		if idLen == 2 {
			id = uint32(binary.BigEndian.Uint16(e))
		} else {
			id = binary.BigEndian.Uint32(e)
		}
		found = true
		return nil
	})
	return id, found, err
}

type extent struct{ off, n int64 }

// itemExtents finds an item's extents in an `iloc` payload, and its
// construction method (0: file offsets; 1: offsets in `idat`).
func itemExtents(c []byte, want uint32) ([]extent, int, bool, error) {
	r := &bytesReader{b: c}
	ver := r.u(1)
	r.u(3)
	sizes := r.u(2)
	offSize, lenSize := int(sizes>>12&0xF), int(sizes>>8&0xF)
	baseSize, idxSize := int(sizes>>4&0xF), int(sizes&0xF)
	if ver == 0 {
		idxSize = 0
	}
	for _, sz := range []int{offSize, lenSize, baseSize, idxSize} {
		if sz != 0 && sz != 4 && sz != 8 {
			return nil, 0, false, errMalformed
		}
	}
	var count uint64
	switch ver {
	case 0, 1:
		count = r.u(2)
	case 2:
		count = r.u(4)
	default:
		return nil, 0, false, errMalformed
	}
	if count > maxEntries {
		return nil, 0, false, errMalformed
	}
	for i := uint64(0); i < count && !r.bad; i++ {
		var id uint64
		if ver < 2 {
			id = r.u(2)
		} else {
			id = r.u(4)
		}
		method := 0
		if ver >= 1 {
			method = int(r.u(2) & 0xF)
		}
		r.u(2) // data_reference_index
		base := r.u(baseSize)
		n := r.u(2)
		if n > maxEntries {
			return nil, 0, false, errMalformed
		}
		var exts []extent
		for j := uint64(0); j < n && !r.bad; j++ {
			r.u(idxSize)
			off, ln := r.u(offSize), r.u(lenSize)
			if base > 1<<62 || off > 1<<62 || ln > 1<<62 {
				return nil, 0, false, errMalformed
			}
			exts = append(exts, extent{off: int64(base + off), n: int64(ln)})
		}
		if r.bad {
			break
		}
		if uint32(id) == want {
			if method > 1 || len(exts) == 0 {
				return nil, 0, false, errMalformed
			}
			return exts, method, true, nil
		}
	}
	if r.bad {
		return nil, 0, false, errMalformed
	}
	return nil, 0, false, nil
}

// bytesReader reads big-endian integers of 0–8 bytes, and marks itself bad
// past the end.
type bytesReader struct {
	b   []byte
	bad bool
}

func (r *bytesReader) u(n int) uint64 {
	if r.bad || n > len(r.b) {
		r.bad = true
		return 0
	}
	var v uint64
	for _, c := range r.b[:n] {
		v = v<<8 | uint64(c)
	}
	r.b = r.b[n:]
	return v
}
