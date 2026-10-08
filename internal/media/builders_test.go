package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"
)

// Hand-built header bytes for the parser tests and fuzz seeds.

// tiffTag is one IFD entry; data is the value's bytes in the TIFF's byte
// order.
type tiffTag struct {
	id    uint16
	typ   uint16
	count uint32
	data  []byte
}

func asciiTag(id uint16, s string) tiffTag {
	return tiffTag{id: id, typ: 2, count: uint32(len(s) + 1), data: append([]byte(s), 0)}
}

func longTag(bo binary.ByteOrder, id uint16, v uint32) tiffTag {
	b := make([]byte, 4)
	bo.PutUint32(b, v)
	return tiffTag{id: id, typ: 4, count: 1, data: b}
}

func rationalTag(bo binary.ByteOrder, id uint16, vals ...[2]uint32) tiffTag {
	b := make([]byte, 8*len(vals))
	for i, v := range vals {
		bo.PutUint32(b[8*i:], v[0])
		bo.PutUint32(b[8*i+4:], v[1])
	}
	return tiffTag{id: id, typ: 5, count: uint32(len(vals)), data: b}
}

// layoutIFD lays out an IFD at offset at, with its out-of-line values
// right after it.
func layoutIFD(bo binary.ByteOrder, tags []tiffTag, at int) []byte {
	head := make([]byte, 2+12*len(tags)+4)
	bo.PutUint16(head, uint16(len(tags)))
	var data []byte
	dataAt := at + len(head)
	for i, t := range tags {
		e := head[2+12*i:]
		bo.PutUint16(e, t.id)
		bo.PutUint16(e[2:], t.typ)
		bo.PutUint32(e[4:], t.count)
		if len(t.data) <= 4 {
			copy(e[8:12], t.data)
			continue
		}
		bo.PutUint32(e[8:], uint32(dataAt+len(data)))
		data = append(data, t.data...)
		if len(data)%2 == 1 {
			data = append(data, 0)
		}
	}
	return append(head, data...)
}

// buildTIFF builds a TIFF structure: IFD0, and Exif and GPS IFDs when given
// (pointed to from IFD0).
func buildTIFF(bo binary.ByteOrder, magic uint16, ifd0, exif, gps []tiffTag) []byte {
	head := make([]byte, 8)
	if bo == binary.ByteOrder(binary.LittleEndian) {
		copy(head, "II")
	} else {
		copy(head, "MM")
	}
	bo.PutUint16(head[2:], magic)
	bo.PutUint32(head[4:], 8)
	tags := append([]tiffTag(nil), ifd0...)
	exifPtr, gpsPtr := -1, -1
	if exif != nil {
		exifPtr = len(tags)
		tags = append(tags, longTag(bo, tagExifIFD, 0))
	}
	if gps != nil {
		gpsPtr = len(tags)
		tags = append(tags, longTag(bo, tagGPSIFD, 0))
	}
	n0 := len(layoutIFD(bo, tags, 8))
	exifAt := 8 + n0
	exifBytes := layoutIFD(bo, exif, exifAt)
	gpsAt := exifAt
	if exif != nil {
		gpsAt += len(exifBytes)
	}
	if exifPtr >= 0 {
		tags[exifPtr] = longTag(bo, tagExifIFD, uint32(exifAt))
	}
	if gpsPtr >= 0 {
		tags[gpsPtr] = longTag(bo, tagGPSIFD, uint32(gpsAt))
	}
	out := append(head, layoutIFD(bo, tags, 8)...)
	if exif != nil {
		out = append(out, exifBytes...)
	}
	if gps != nil {
		out = append(out, layoutIFD(bo, gps, gpsAt)...)
	}
	return out
}

// sampleTIFF is a camera's full EXIF: make, model, IFD0 time, capture with
// offset and subseconds, a body serial, and GPS.
func sampleTIFF(bo binary.ByteOrder, magic uint16) []byte {
	return buildTIFF(bo, magic,
		[]tiffTag{asciiTag(tagMake, "NIKON"), asciiTag(tagModel, "COOLPIX P5000"), asciiTag(tagDateTime, "2011:01:15 10:00:00")},
		[]tiffTag{asciiTag(tagDateTimeOriginal, "2008:03:22 14:00:00"), asciiTag(tagDateTimeDigitized, "2008:03:22 14:00:01"),
			asciiTag(tagOffsetTimeOriginal, "-03:00"), asciiTag(tagSubSecTimeOriginal, "25"), asciiTag(tagBodySerialNumber, "3012345")},
		[]tiffTag{asciiTag(tagGPSDateStamp, "2008:03:22"), rationalTag(bo, tagGPSTimeStamp, [2]uint32{17, 1}, [2]uint32{0, 1}, [2]uint32{3050, 100})})
}

// sampleMeta is what sampleTIFF holds.
func sampleMeta() Meta {
	off := -180
	gps := time.Date(2008, 3, 22, 17, 0, 30, 500_000_000, time.UTC)
	return Meta{CaptureLocal: "2008-03-22T14:00:00.250", CaptureOffsetMin: &off, GPS: &gps,
		Make: "NIKON", Model: "COOLPIX P5000", Serial: "3012345"}
}

// jpegSegment is a JPEG marker segment.
func jpegSegment(marker byte, payload []byte) []byte {
	n := len(payload) + 2
	return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, payload...)
}

// buildJPEG is SOI, the segments, a scan of junk, and EOI.
func buildJPEG(segs ...[]byte) []byte {
	out := []byte{0xFF, 0xD8}
	for _, s := range segs {
		out = append(out, s...)
	}
	out = append(out, jpegSegment(0xDA, []byte{1, 2, 3})...)
	out = append(out, bytes.Repeat([]byte{0x55}, 64)...)
	return append(out, 0xFF, 0xD9)
}

func exifAPP1(tiff []byte) []byte { return jpegSegment(0xE1, append([]byte("Exif\x00\x00"), tiff...)) }

// mkBox is an ISO-BMFF box.
func mkBox(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:], typ)
	return append(out, body...)
}

// fullBox is a box with version and flags.
func fullBox(typ string, version byte, payload ...[]byte) []byte {
	return mkBox(typ, append([][]byte{{version, 0, 0, 0}}, payload...)...)
}

func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// mvhd is a version 0 or 1 `mvhd` with a creation time.
func mvhd(version byte, created time.Time) []byte {
	secs := uint64(0)
	if !created.IsZero() {
		secs = uint64(created.Unix() - mac1904)
	}
	if version == 1 {
		return fullBox("mvhd", 1, u64(secs), u64(secs), u32(1000), u64(0), make([]byte, 80))
	}
	return fullBox("mvhd", 0, u32(uint32(secs)), u32(uint32(secs)), u32(1000), u32(0), make([]byte, 80))
}

// buildMP4 is ftyp, moov (mvhd), and mdat of mdatSize bytes, with moov
// first or last.
func buildMP4(brand string, created time.Time, moovLast bool, mdatSize int, version byte) []byte {
	ftyp := mkBox("ftyp", []byte(brand), u32(0), []byte(brand))
	moov := mkBox("moov", mvhd(version, created), mkBox("trak", mkBox("tkhd", make([]byte, 84))))
	mdat := mkBox("mdat", bytes.Repeat([]byte{0xAB}, mdatSize))
	if moovLast {
		return bytes.Join([][]byte{ftyp, mdat, moov}, nil)
	}
	return bytes.Join([][]byte{ftyp, moov, mdat}, nil)
}

// buildHEIF is an image file whose `meta` holds an Exif item located by
// `iloc` (version ilocVersion), in the file (method 0) or in `idat`
// (method 1, iloc version 1).
func buildHEIF(brand string, tiff []byte, ilocVersion byte, inIdat bool) []byte {
	item := append(u32(6), append([]byte("Exif\x00\x00"), tiff...)...)
	ftyp := mkBox("ftyp", []byte(brand), u32(0), []byte("mif1"), []byte(brand))
	hdlr := fullBox("hdlr", 0, u32(0), []byte("pict"), make([]byte, 13))
	infe1 := fullBox("infe", 2, u16(1), u16(0), []byte("hvc1"), []byte{0})
	infe2 := fullBox("infe", 2, u16(2), u16(0), []byte("Exif"), []byte{0})
	iinf := fullBox("iinf", 0, u16(2), infe1, infe2)
	iloc := func(exifOff uint32) []byte {
		var body [][]byte
		body = append(body, []byte{0x44, 0x40}) // offset 4, length 4, base 4, index 0
		body = append(body, u16(2))
		// item 1: the image, somewhere in mdat
		body = append(body, u16(1))
		if ilocVersion == 1 {
			body = append(body, u16(0))
		}
		body = append(body, u16(0), u32(0), u16(1), u32(0), u32(4))
		// item 2: Exif, with a base offset of 0 and one extent
		body = append(body, u16(2))
		if ilocVersion == 1 {
			m := uint16(0)
			if inIdat {
				m = 1
			}
			body = append(body, u16(m))
		}
		body = append(body, u16(0), u32(0), u16(1), u32(exifOff), u32(uint32(len(item))))
		return fullBox("iloc", ilocVersion, body...)
	}
	if inIdat {
		meta := fullBox("meta", 0, hdlr, iinf, iloc(0), mkBox("idat", item))
		return bytes.Join([][]byte{ftyp, meta, mkBox("mdat", []byte{1, 2, 3, 4})}, nil)
	}
	// Lay the meta out once to learn where mdat's payload starts.
	meta := fullBox("meta", 0, hdlr, iinf, iloc(0))
	at := len(ftyp) + len(meta) + 8 + 4
	meta = fullBox("meta", 0, hdlr, iinf, iloc(uint32(at)))
	return bytes.Join([][]byte{ftyp, meta, mkBox("mdat", []byte{1, 2, 3, 4}, item)}, nil)
}

// buildCR3 is a CR3: ftyp "crx ", moov with Canon's uuid box holding CMT1
// (IFD0), CMT2 (Exif IFD), and CMT4 (GPS IFD), and mvhd.
func buildCR3(bo binary.ByteOrder, created time.Time) []byte {
	cmt1 := buildTIFF(bo, 42, []tiffTag{asciiTag(tagMake, "Canon"), asciiTag(tagModel, "Canon EOS R")}, nil, nil)
	cmt2 := buildTIFF(bo, 42, []tiffTag{asciiTag(tagDateTimeOriginal, "2019:05:04 09:08:07"),
		asciiTag(tagOffsetTimeOriginal, "+01:00"), asciiTag(tagBodySerialNumber, "123456789")}, nil, nil)
	cmt4 := buildTIFF(bo, 42, []tiffTag{asciiTag(tagGPSDateStamp, "2019:05:04"),
		rationalTag(bo, tagGPSTimeStamp, [2]uint32{8, 1}, [2]uint32{8, 1}, [2]uint32{7, 1})}, nil, nil)
	uuid := mkBox("uuid", canonUUID, mkBox("CNCV", []byte("CanonCR3_001/00.10.00/00.00.00")),
		mkBox("CMT1", cmt1), mkBox("CMT2", cmt2), mkBox("CMT3", []byte("maker notes")), mkBox("CMT4", cmt4))
	ftyp := mkBox("ftyp", []byte("crx "), u32(1), []byte("crx isom"))
	moov := mkBox("moov", uuid, mvhd(0, created))
	return bytes.Join([][]byte{ftyp, moov, mkBox("mdat", make([]byte, 100))}, nil)
}

// recorder is a ReaderAt that counts the bytes asked of it, and can fail
// or panic.
type recorder struct {
	mu    sync.Mutex
	r     io.ReaderAt
	bytes int64
	calls int
	fail  error
	panic bool
}

func newRecorder(b []byte) *recorder { return &recorder{r: bytes.NewReader(b)} }

func (r *recorder) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	r.bytes += int64(len(p))
	r.calls++
	r.mu.Unlock()
	if r.panic {
		panic("boom")
	}
	if r.fail != nil {
		return 0, r.fail
	}
	return r.r.ReadAt(p, off)
}

var errDisk = errors.New("disk error")

// sparse is a large file of zero bytes between a prefix and a suffix,
// without holding it in memory.
type sparse struct {
	prefix, suffix []byte
	size           int64
}

func (z sparse) ReadAt(p []byte, off int64) (int, error) {
	if off >= z.size {
		return 0, io.EOF
	}
	tail := z.size - int64(len(z.suffix))
	n := 0
	for n < len(p) && off+int64(n) < z.size {
		i := off + int64(n)
		switch {
		case i < int64(len(z.prefix)):
			p[n] = z.prefix[i]
		case i >= tail:
			p[n] = z.suffix[i-tail]
		default:
			p[n] = 0
		}
		n++
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
