package corpus

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"time"
)

// Writers of the media-date fixtures (r5 design D19): EXIF spliced into a
// JPEG, an MP4 with a creation time, and a PNG. They write only what the
// fixtures need, in their own code: the ground truth never depends on
// internal/media, and the corpus tests read these files back through it.

// exif is the EXIF a camera writes into a photo.
type exif struct {
	make, model, serial string
	// dateTime is IFD0 DateTime and original DateTimeOriginal, both
	// "YYYY:MM:DD HH:MM:SS"; offset is OffsetTimeOriginal ("-03:00") and
	// subsec SubSecTimeOriginal ("12"), or "".
	dateTime, original, offset, subsec string
	// gps, when set, is written as GPSDateStamp and GPSTimeStamp (UTC).
	gps *time.Time
}

// exifTime is a time as EXIF writes it.
func exifTime(t time.Time) string { return t.Format("2006:01:02 15:04:05") }

// ifdEntry is one TIFF directory entry, its value in big-endian bytes.
type ifdEntry struct {
	tag, typ uint16
	count    uint32
	value    []byte
}

func asciiEntry(tag uint16, s string) ifdEntry {
	return ifdEntry{tag: tag, typ: 2, count: uint32(len(s) + 1), value: append([]byte(s), 0)}
}

func longEntry(tag uint16, v uint32) ifdEntry {
	return ifdEntry{tag: tag, typ: 4, count: 1, value: binary.BigEndian.AppendUint32(nil, v)}
}

func rationalsEntry(tag uint16, vals ...uint32) ifdEntry {
	var b []byte
	for _, v := range vals {
		b = binary.BigEndian.AppendUint32(b, v)
		b = binary.BigEndian.AppendUint32(b, 1)
	}
	return ifdEntry{tag: tag, typ: 5, count: uint32(len(vals)), value: b}
}

// ifdBytes writes a directory at offset at, its longer values right after
// it, and returns the bytes.
func ifdBytes(entries []ifdEntry, at int) []byte {
	head := make([]byte, 2+12*len(entries)+4)
	binary.BigEndian.PutUint16(head, uint16(len(entries)))
	var data []byte
	for i, e := range entries {
		o := head[2+12*i:]
		binary.BigEndian.PutUint16(o, e.tag)
		binary.BigEndian.PutUint16(o[2:], e.typ)
		binary.BigEndian.PutUint32(o[4:], e.count)
		if len(e.value) <= 4 {
			copy(o[8:12], e.value)
			continue
		}
		binary.BigEndian.PutUint32(o[8:], uint32(at+len(head)+len(data)))
		data = append(data, e.value...)
		if len(data)%2 == 1 {
			data = append(data, 0)
		}
	}
	return append(head, data...)
}

// tiff is the big-endian TIFF structure of x: IFD0 (Make, Model, DateTime,
// and the pointers), the Exif IFD, and the GPS IFD when x has GPS.
func (x exif) tiff() []byte {
	var ifd0 []ifdEntry
	if x.make != "" {
		ifd0 = append(ifd0, asciiEntry(0x010F, x.make))
	}
	if x.model != "" {
		ifd0 = append(ifd0, asciiEntry(0x0110, x.model))
	}
	if x.dateTime != "" {
		ifd0 = append(ifd0, asciiEntry(0x0132, x.dateTime))
	}
	exifAt := len(ifd0)
	ifd0 = append(ifd0, longEntry(0x8769, 0))
	gpsAt := -1
	if x.gps != nil {
		gpsAt = len(ifd0)
		ifd0 = append(ifd0, longEntry(0x8825, 0))
	}
	var sub []ifdEntry
	if x.original != "" {
		sub = append(sub, asciiEntry(0x9003, x.original))
	}
	if x.offset != "" {
		sub = append(sub, asciiEntry(0x9011, x.offset))
	}
	if x.subsec != "" {
		sub = append(sub, asciiEntry(0x9291, x.subsec))
	}
	if x.serial != "" {
		sub = append(sub, asciiEntry(0xA431, x.serial))
	}
	const first = 8
	subOff := first + len(ifdBytes(ifd0, first))
	subBytes := ifdBytes(sub, subOff)
	ifd0[exifAt] = longEntry(0x8769, uint32(subOff))
	out := []byte("MM\x00\x2a\x00\x00\x00\x08")
	var gpsBytes []byte
	if gpsAt >= 0 {
		gpsOff := subOff + len(subBytes)
		ifd0[gpsAt] = longEntry(0x8825, uint32(gpsOff))
		g := x.gps.UTC()
		gpsBytes = ifdBytes([]ifdEntry{
			rationalsEntry(0x0007, uint32(g.Hour()), uint32(g.Minute()), uint32(g.Second())),
			asciiEntry(0x001D, g.Format("2006:01:02")),
		}, gpsOff)
	}
	out = append(out, ifdBytes(ifd0, first)...)
	out = append(out, subBytes...)
	return append(out, gpsBytes...)
}

// withEXIF splices x into a JPEG as an APP1 segment right after SOI.
func withEXIF(jpeg []byte, x exif) []byte {
	payload := append([]byte("Exif\x00\x00"), x.tiff()...)
	if len(payload)+2 > 0xFFFF || !bytes.HasPrefix(jpeg, []byte{0xFF, 0xD8}) {
		panic("corpus: cannot splice EXIF")
	}
	n := len(payload) + 2
	seg := append([]byte{0xFF, 0xE1, byte(n >> 8), byte(n)}, payload...)
	out := append([]byte{0xFF, 0xD8}, seg...)
	return append(out, jpeg[2:]...)
}

// box is an ISO-BMFF box.
func box(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	return append(append(out, typ...), body...)
}

// mp4Video is an MP4 whose `moov/mvhd` records created (UTC) as its
// creation and modification times, followed by size bytes of media data.
func mp4Video(label string, created time.Time, size int) []byte {
	const mac1904 = 2082844800 // seconds from 1904-01-01 to 1970-01-01
	secs := uint32(created.Unix() + mac1904)
	var mvhd []byte
	mvhd = append(mvhd, 0, 0, 0, 0) // version 0, flags
	mvhd = binary.BigEndian.AppendUint32(mvhd, secs)
	mvhd = binary.BigEndian.AppendUint32(mvhd, secs)
	mvhd = binary.BigEndian.AppendUint32(mvhd, 1000) // timescale
	mvhd = binary.BigEndian.AppendUint32(mvhd, 5000) // duration: 5 s
	mvhd = binary.BigEndian.AppendUint32(mvhd, 0x00010000)
	mvhd = binary.BigEndian.AppendUint16(mvhd, 0x0100)
	mvhd = append(mvhd, make([]byte, 10)...)
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		mvhd = binary.BigEndian.AppendUint32(mvhd, v)
	}
	mvhd = append(mvhd, make([]byte, 24)...)
	mvhd = binary.BigEndian.AppendUint32(mvhd, 2) // next track ID
	ftyp := box("ftyp", []byte("isom"), binary.BigEndian.AppendUint32(nil, 0x200), []byte("isomiso2mp41"))
	return bytes.Join([][]byte{ftyp, box("moov", box("mvhd", mvhd)), box("mdat", random(label, size))}, nil)
}

// pngImage is a real PNG of a gradient, different for every label.
func pngImage(label string, w, h int) []byte {
	p := newPRNG(label)
	base := p.next()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(base) + uint8(x*255/w), uint8(base>>8) + uint8(y*255/h), uint8(base >> 16), 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
