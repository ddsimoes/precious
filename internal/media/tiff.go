package media

import (
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

// TIFF tags read (D1). No maker notes, no orientation.
const (
	tagMake               = 0x010F
	tagModel              = 0x0110
	tagDateTime           = 0x0132
	tagExifIFD            = 0x8769
	tagGPSIFD             = 0x8825
	tagCameraSerialNumber = 0xC62F // DNG, IFD0
	tagDateTimeOriginal   = 0x9003
	tagDateTimeDigitized  = 0x9004
	tagOffsetTimeOriginal = 0x9011
	tagSubSecTimeOriginal = 0x9291
	tagBodySerialNumber   = 0xA431
	tagGPSTimeStamp       = 0x0007
	tagGPSDateStamp       = 0x001D
)

// ifdKind says which IFD a directory is.
type ifdKind uint8

const (
	ifd0 ifdKind = iota
	ifdExif
	ifdGPS
)

// tiffMode says how much of a TIFF structure to read: IFD0 with its Exif
// and GPS IFDs, or only its first directory taken as one kind (CR3's CMT1,
// CMT2, CMT4).
type tiffMode uint8

const (
	tiffFull tiffMode = iota
	tiffOnly0
	tiffOnlyExif
	tiffOnlyGPS
)

// exifFields are the raw values found, before they are checked.
type exifFields struct {
	dateOriginal, dateDigitized, dateTime string
	offsetOriginal, subsecOriginal        string
	gpsDate                               string
	gpsTime                               []byte // 24 bytes: three RATIONALs, in order
	gpsOrder                              binary.ByteOrder
	make, model                           string
	bodySerial, cameraSerial              string
}

// merge fills the fields f lacks from o.
func (f *exifFields) merge(o exifFields) {
	set := func(dst *string, v string) {
		if *dst == "" {
			*dst = v
		}
	}
	set(&f.dateOriginal, o.dateOriginal)
	set(&f.dateDigitized, o.dateDigitized)
	set(&f.dateTime, o.dateTime)
	set(&f.offsetOriginal, o.offsetOriginal)
	set(&f.subsecOriginal, o.subsecOriginal)
	set(&f.gpsDate, o.gpsDate)
	if f.gpsTime == nil {
		f.gpsTime, f.gpsOrder = o.gpsTime, o.gpsOrder
	}
	set(&f.make, o.make)
	set(&f.model, o.model)
	set(&f.bodySerial, o.bodySerial)
	set(&f.cameraSerial, o.cameraSerial)
}

// parseTIFF reads a TIFF structure in v. A structural fault (header,
// directory out of range, a loop, too many entries) returns errMalformed,
// and the caller drops what this structure gave; a value out of range only
// leaves that value out. An *ioError is returned as is.
func parseTIFF(v view, s *source, mode tiffMode) (exifFields, error) {
	var f exifFields
	h, err := v.at(0, 8)
	if err != nil {
		return f, err
	}
	var bo binary.ByteOrder
	switch {
	case h[0] == 'I' && h[1] == 'I':
		bo = binary.LittleEndian
	case h[0] == 'M' && h[1] == 'M':
		bo = binary.BigEndian
	default:
		return f, errMalformed
	}
	switch bo.Uint16(h[2:]) {
	case 42, 0x4F52, 0x5352, 0x0055: // TIFF; ORF "IIRO"/"MMOR"/"IIRS"; RW2 "IIU\0"
	default:
		return f, errMalformed
	}
	t := &tiffReader{v: v, s: s, bo: bo, f: &f}
	first := int64(bo.Uint32(h[4:]))
	switch mode {
	case tiffOnly0:
		_, err = t.ifd(first, ifd0)
		return f, err
	case tiffOnlyExif:
		_, err = t.ifd(first, ifdExif)
		return f, err
	case tiffOnlyGPS:
		_, err = t.ifd(first, ifdGPS)
		return f, err
	}
	ptrs, err := t.ifd(first, ifd0)
	if err != nil {
		return f, err
	}
	seen := []int64{first}
	for _, p := range []struct {
		off  int64
		kind ifdKind
	}{{ptrs.exif, ifdExif}, {ptrs.gps, ifdGPS}} {
		if p.off == 0 {
			continue
		}
		for _, o := range seen {
			if o == p.off {
				return f, errMalformed // a loop
			}
		}
		seen = append(seen, p.off)
		if _, err := t.ifd(p.off, p.kind); err != nil {
			return f, err
		}
	}
	return f, nil
}

type tiffReader struct {
	v  view
	s  *source
	bo binary.ByteOrder
	f  *exifFields
}

type ifdPointers struct{ exif, gps int64 }

// typeSize is the byte size of one value of a TIFF field type, 0 for a
// type not read.
func typeSize(typ uint16) int64 {
	switch typ {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 13: // LONG, SLONG, IFD
		return 4
	case 5, 10: // RATIONAL, SRATIONAL
		return 8
	}
	return 0
}

// ifd reads one directory and records the tags of its kind.
func (t *tiffReader) ifd(off int64, kind ifdKind) (ifdPointers, error) {
	var ptrs ifdPointers
	if off < 8 {
		return ptrs, errMalformed
	}
	c, err := t.v.at(off, 2)
	if err != nil {
		return ptrs, err
	}
	n := int(t.bo.Uint16(c))
	t.s.ents += n
	if t.s.ents > maxEntries {
		return ptrs, errMalformed
	}
	body, err := t.v.at(off+2, 12*n)
	if err != nil {
		return ptrs, err
	}
	for i := 0; i < n; i++ {
		e := body[12*i : 12*i+12]
		tag, typ, count := t.bo.Uint16(e), t.bo.Uint16(e[2:]), int64(t.bo.Uint32(e[4:]))
		if !wanted(kind, tag) {
			continue
		}
		size := typeSize(typ) * count
		if size == 0 || size > maxString {
			continue
		}
		var val []byte
		if size <= 4 {
			val = e[8 : 8+size]
		} else {
			val, err = t.v.at(int64(t.bo.Uint32(e[8:])), int(size))
			if errors.Is(err, errMalformed) {
				continue // that value is out of range; the rest still counts
			}
			if err != nil {
				return ptrs, err
			}
		}
		t.record(kind, tag, typ, count, val, &ptrs)
	}
	return ptrs, nil
}

func wanted(kind ifdKind, tag uint16) bool {
	switch kind {
	case ifd0:
		switch tag {
		case tagMake, tagModel, tagDateTime, tagExifIFD, tagGPSIFD, tagCameraSerialNumber:
			return true
		}
	case ifdExif:
		switch tag {
		case tagDateTimeOriginal, tagDateTimeDigitized, tagOffsetTimeOriginal, tagSubSecTimeOriginal, tagBodySerialNumber:
			return true
		}
	case ifdGPS:
		return tag == tagGPSTimeStamp || tag == tagGPSDateStamp
	}
	return false
}

func (t *tiffReader) record(kind ifdKind, tag, typ uint16, count int64, val []byte, ptrs *ifdPointers) {
	f := t.f
	text := func() string {
		if typ != 2 && typ != 7 {
			return ""
		}
		return cleanText(val)
	}
	pointer := func() int64 {
		if (typ != 4 && typ != 13) || count != 1 {
			return 0
		}
		return int64(t.bo.Uint32(val))
	}
	switch kind {
	case ifd0:
		switch tag {
		case tagMake:
			f.make = text()
		case tagModel:
			f.model = text()
		case tagDateTime:
			f.dateTime = text()
		case tagCameraSerialNumber:
			f.cameraSerial = text()
		case tagExifIFD:
			ptrs.exif = pointer()
		case tagGPSIFD:
			ptrs.gps = pointer()
		}
	case ifdExif:
		switch tag {
		case tagDateTimeOriginal:
			f.dateOriginal = text()
		case tagDateTimeDigitized:
			f.dateDigitized = text()
		case tagOffsetTimeOriginal:
			f.offsetOriginal = text()
		case tagSubSecTimeOriginal:
			f.subsecOriginal = text()
		case tagBodySerialNumber:
			f.bodySerial = text()
		}
	case ifdGPS:
		switch tag {
		case tagGPSDateStamp:
			f.gpsDate = text()
		case tagGPSTimeStamp:
			if typ == 5 && count == 3 {
				f.gpsTime, f.gpsOrder = append([]byte(nil), val...), t.bo
			}
		}
	}
}

// cleanText is an EXIF text up to its first NUL, without surrounding
// spaces.
func cleanText(b []byte) string {
	if i := indexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// meta turns the raw fields into a Meta, leaving out every value that does
// not parse. The offset and subseconds belong to DateTimeOriginal only.
func (f exifFields) meta() Meta {
	var m Meta
	if wall, ok := exifWall(f.dateOriginal); ok {
		m.CaptureLocal = wall.Format(localSecond) + subsec(f.subsecOriginal)
		if off, ok := exifOffset(f.offsetOriginal); ok {
			m.CaptureOffsetMin = &off
		}
	} else if wall, ok := exifWall(f.dateDigitized); ok {
		m.CaptureLocal = wall.Format(localSecond)
	} else if wall, ok := exifWall(f.dateTime); ok {
		m.CaptureLocal = wall.Format(localSecond)
	}
	if g, ok := gpsInstant(f.gpsDate, f.gpsTime, f.gpsOrder); ok {
		m.GPS = &g
	}
	m.Make, m.Model = f.make, f.model
	m.Serial = f.bodySerial
	if m.Serial == "" {
		m.Serial = f.cameraSerial
	}
	return m
}

const localSecond = "2006-01-02T15:04:05"

// exifWall parses "YYYY:MM:DD HH:MM:SS" (also with '-' in the date or 'T'
// before the time) as a wall time; an impossible or blank date is no date.
func exifWall(s string) (time.Time, bool) {
	if len(s) < 19 {
		return time.Time{}, false
	}
	b := s[:19]
	if !(b[4] == ':' || b[4] == '-') || b[7] != b[4] || !(b[10] == ' ' || b[10] == 'T') || b[13] != ':' || b[16] != ':' {
		return time.Time{}, false
	}
	return wallFromDigits(b[0:4], b[5:7], b[8:10], b[11:13], b[14:16], b[17:19])
}

// Every date read or set lies in these years, so its instant, and its
// wall time in any zone, fit int64 nanoseconds (`effective_ns`).
const (
	minYear = 1700
	maxYear = 2200
)

// Stored dates are read back over every year an int64-nanosecond instant
// can hold, in any zone.
const (
	minStoredYear = 1600
	maxStoredYear = 2300
)

// wallFromDigits builds a valid wall time, in UTC as a carrier, from its
// decimal fields, with a year from minYear to maxYear.
func wallFromDigits(y, mo, d, h, mi, s string) (time.Time, bool) {
	return wallIn(minYear, maxYear, y, mo, d, h, mi, s)
}

// wallIn is wallFromDigits with a year from lo to hi.
func wallIn(lo, hi int, y, mo, d, h, mi, s string) (time.Time, bool) {
	var v [6]int
	for i, p := range []string{y, mo, d, h, mi, s} {
		n, ok := digits(p)
		if !ok {
			return time.Time{}, false
		}
		v[i] = n
	}
	if v[0] < lo || v[0] > hi || v[1] < 1 || v[1] > 12 || v[2] < 1 || v[3] > 23 || v[4] > 59 || v[5] > 59 {
		return time.Time{}, false
	}
	t := time.Date(v[0], time.Month(v[1]), v[2], v[3], v[4], v[5], 0, time.UTC)
	if t.Day() != v[2] {
		return time.Time{}, false
	}
	return t, true
}

func digits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// subsec is ".000" from SubSecTimeOriginal's digits, or "".
func subsec(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return ""
		}
	}
	s += "000"
	return "." + s[:3]
}

// exifOffset parses "+HH:MM" or "-HH:MM" into minutes east of UTC, at most
// 14 hours.
func exifOffset(s string) (int, bool) {
	if len(s) != 6 || (s[0] != '+' && s[0] != '-') || s[3] != ':' {
		return 0, false
	}
	h, ok1 := digits(s[1:3])
	m, ok2 := digits(s[4:6])
	if !ok1 || !ok2 || m > 59 {
		return 0, false
	}
	off := h*60 + m
	if off > 840 {
		return 0, false
	}
	if s[0] == '-' {
		off = -off
	}
	return off, true
}

// gpsInstant is GPSDateStamp ("YYYY:MM:DD") plus GPSTimeStamp (hours,
// minutes, seconds as RATIONALs), in UTC.
func gpsInstant(date string, tm []byte, bo binary.ByteOrder) (time.Time, bool) {
	if len(tm) != 24 || len(date) < 10 {
		return time.Time{}, false
	}
	b := date[:10]
	if !(b[4] == ':' || b[4] == '-') || b[7] != b[4] {
		return time.Time{}, false
	}
	day, ok := wallFromDigits(b[0:4], b[5:7], b[8:10], "00", "00", "00")
	if !ok {
		return time.Time{}, false
	}
	var ns [3]int64
	for i := range ns {
		num, den := int64(bo.Uint32(tm[8*i:])), int64(bo.Uint32(tm[8*i+4:]))
		if den == 0 {
			return time.Time{}, false
		}
		ns[i] = num * int64(time.Second) / den
	}
	if ns[0] >= 24*int64(time.Second) || ns[1] >= 60*int64(time.Second) || ns[2] >= 61*int64(time.Second) {
		return time.Time{}, false
	}
	d := time.Duration(ns[0]*3600 + ns[1]*60 + ns[2])
	return day.Add(d), true
}
