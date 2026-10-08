package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
)

func mustRead(t *testing.T, b []byte, f Format) (Meta, *recorder) {
	t.Helper()
	r := newRecorder(b)
	m, err := Read(r, int64(len(b)), f)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if r.bytes > MaxBytes {
		t.Fatalf("read %d bytes, over %d", r.bytes, MaxBytes)
	}
	return m, r
}

func sameMeta(t *testing.T, what string, got, want Meta) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got  %s\n want %s", what, metaString(got), metaString(want))
	}
}

func metaString(m Meta) string {
	s := "capture=" + m.CaptureLocal
	if m.CaptureOffsetMin != nil {
		s += " offset=" + time.Duration(*m.CaptureOffsetMin*int(time.Minute)).String()
	}
	if m.GPS != nil {
		s += " gps=" + m.GPS.Format(time.RFC3339Nano)
	}
	if m.Container != nil {
		s += " container=" + m.Container.Format(time.RFC3339Nano)
	}
	return s + " make=" + m.Make + " model=" + m.Model + " serial=" + m.Serial
}

func TestFormatOf(t *testing.T) {
	for ext, want := range map[string]Format{
		"jpg": FormatJPEG, "jpeg": FormatJPEG, "tif": FormatTIFF, "tiff": FormatTIFF, "cr2": FormatTIFF,
		"nef": FormatTIFF, "arw": FormatTIFF, "dng": FormatTIFF, "pef": FormatTIFF, "srw": FormatTIFF,
		"orf": FormatTIFF, "rw2": FormatTIFF, "heic": FormatISOBMFF, "heif": FormatISOBMFF, "avif": FormatISOBMFF,
		"cr3": FormatISOBMFF, "mp4": FormatISOBMFF, "m4v": FormatISOBMFF, "mov": FormatISOBMFF,
		"3gp": FormatISOBMFF, "3g2": FormatISOBMFF,
		"mkv": FormatNone, "webm": FormatNone, "avi": FormatNone, "png": FormatNone, "gif": FormatNone,
		"raw": FormatNone, "JPG": FormatNone, ".jpg": FormatNone, "": FormatNone,
	} {
		if got := FormatOf(ext); got != want {
			t.Errorf("FormatOf(%q) = %s, want %s", ext, got, want)
		}
	}
	for kind, want := range map[string]bool{"image": true, "video": true, "audio": false, "document": false, "": false} {
		if IsMediaKind(kind) != want {
			t.Errorf("IsMediaKind(%q) = %v", kind, !want)
		}
	}
}

func TestReadJPEG(t *testing.T) {
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		xmp := jpegSegment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x/>"))
		app0 := jpegSegment(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"))
		b := buildJPEG(app0, xmp, exifAPP1(sampleTIFF(bo, 42)))
		m, _ := mustRead(t, b, FormatJPEG)
		sameMeta(t, bo.String(), m, sampleMeta())
	}
	// Fill bytes before a marker are skipped.
	b := buildJPEG(append([]byte{0xFF}, exifAPP1(sampleTIFF(binary.LittleEndian, 42))...))
	m, _ := mustRead(t, b, FormatJPEG)
	sameMeta(t, "fill byte", m, sampleMeta())
}

func TestReadJPEGFallbacks(t *testing.T) {
	bo := binary.LittleEndian
	cases := []struct {
		name string
		ifd0 []tiffTag
		exif []tiffTag
		want string
	}{
		{"digitized", nil, []tiffTag{asciiTag(tagDateTimeDigitized, "2005:06:07 08:09:10")}, "2005-06-07T08:09:10"},
		{"ifd0", []tiffTag{asciiTag(tagDateTime, "2004:01:02 03:04:05")}, nil, "2004-01-02T03:04:05"},
		{"blank original", []tiffTag{asciiTag(tagDateTime, "2004:01:02 03:04:05")},
			[]tiffTag{asciiTag(tagDateTimeOriginal, "    :  :     :  :  ")}, "2004-01-02T03:04:05"},
		{"impossible", nil, []tiffTag{asciiTag(tagDateTimeOriginal, "2004:02:30 03:04:05")}, ""},
		{"default kept", nil, []tiffTag{asciiTag(tagDateTimeOriginal, "2000:01:01 00:00:00")}, "2000-01-01T00:00:00"},
	}
	for _, c := range cases {
		m, _ := mustRead(t, buildJPEG(exifAPP1(buildTIFF(bo, 42, c.ifd0, c.exif, nil))), FormatJPEG)
		if m.CaptureLocal != c.want || m.CaptureOffsetMin != nil {
			t.Errorf("%s: capture %q offset %v, want %q", c.name, m.CaptureLocal, m.CaptureOffsetMin, c.want)
		}
	}
	// The offset and subseconds belong to DateTimeOriginal only.
	m, _ := mustRead(t, buildJPEG(exifAPP1(buildTIFF(bo, 42, nil, []tiffTag{
		asciiTag(tagDateTimeDigitized, "2005:06:07 08:09:10"), asciiTag(tagOffsetTimeOriginal, "+02:00"),
		asciiTag(tagSubSecTimeOriginal, "5")}, nil))), FormatJPEG)
	if m.CaptureLocal != "2005-06-07T08:09:10" || m.CaptureOffsetMin != nil {
		t.Errorf("digitized with original's offset: %s", metaString(m))
	}
	// A bad offset is left out; the capture stays.
	m, _ = mustRead(t, buildJPEG(exifAPP1(buildTIFF(bo, 42, nil, []tiffTag{
		asciiTag(tagDateTimeOriginal, "2005:06:07 08:09:10"), asciiTag(tagOffsetTimeOriginal, "+15:00")}, nil))), FormatJPEG)
	if m.CaptureLocal != "2005-06-07T08:09:10" || m.CaptureOffsetMin != nil {
		t.Errorf("bad offset: %s", metaString(m))
	}
	// CameraSerialNumber stands in for BodySerialNumber.
	m, _ = mustRead(t, buildJPEG(exifAPP1(buildTIFF(bo, 42,
		[]tiffTag{asciiTag(tagCameraSerialNumber, "SN-1 ")}, nil, nil))), FormatJPEG)
	if m.Serial != "SN-1" {
		t.Errorf("camera serial %q", m.Serial)
	}
}

func TestReadTIFF(t *testing.T) {
	for _, c := range []struct {
		name  string
		bo    binary.ByteOrder
		magic uint16
		head  string
	}{
		{"little-endian TIFF", binary.LittleEndian, 42, "II*\x00"},
		{"big-endian TIFF", binary.BigEndian, 42, "MM\x00*"},
		{"ORF", binary.LittleEndian, 0x4F52, "IIRO"},
		{"ORF big-endian", binary.BigEndian, 0x4F52, "MMOR"},
		{"RW2", binary.LittleEndian, 0x0055, "IIU\x00"},
	} {
		b := sampleTIFF(c.bo, c.magic)
		if string(b[:4]) != c.head {
			t.Fatalf("%s: header %q", c.name, b[:4])
		}
		// A RAW file: image data follows the IFDs.
		b = append(b, bytes.Repeat([]byte{0x11}, 4096)...)
		m, _ := mustRead(t, b, FormatTIFF)
		sameMeta(t, c.name, m, sampleMeta())
	}
	// An unknown magic is not TIFF.
	b := sampleTIFF(binary.LittleEndian, 43)
	if m, _ := mustRead(t, b, FormatTIFF); !reflect.DeepEqual(m, Meta{}) {
		t.Errorf("BigTIFF magic read as %s", metaString(m))
	}
}

func TestReadTIFFFarIFD(t *testing.T) {
	// IFD0 sits 3 MiB into the file, past the first window: it is reached
	// by a positioned read.
	const at = 3 << 20
	b := make([]byte, at)
	copy(b, "MM\x00*")
	binary.BigEndian.PutUint32(b[4:], at)
	ifd := layoutIFD(binary.BigEndian, []tiffTag{asciiTag(tagMake, "SONY"), asciiTag(tagModel, "ILCE-7")}, at)
	b = append(b, ifd...)
	m, r := mustRead(t, b, FormatTIFF)
	if m.Make != "SONY" || m.Model != "ILCE-7" {
		t.Errorf("far IFD0: %s", metaString(m))
	}
	if r.bytes > FirstWindow+int64(len(ifd))+2 {
		t.Errorf("read %d bytes for a far IFD0", r.bytes)
	}
}

func TestReadHEIF(t *testing.T) {
	for _, c := range []struct {
		name  string
		brand string
		ver   byte
		idat  bool
	}{
		{"HEIC iloc v0", "heic", 0, false},
		{"HEIC iloc v1", "heic", 1, false},
		{"AVIF iloc v1 idat", "avif", 1, true},
		{"AVIF iloc v0", "avif", 0, false},
	} {
		for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			b := buildHEIF(c.brand, sampleTIFF(bo, 42), c.ver, c.idat)
			m, _ := mustRead(t, b, FormatISOBMFF)
			sameMeta(t, c.name+" "+bo.String(), m, sampleMeta())
		}
	}
}

func TestReadCR3(t *testing.T) {
	created := time.Date(2019, 5, 4, 8, 8, 9, 0, time.UTC)
	m, _ := mustRead(t, buildCR3(binary.LittleEndian, created), FormatISOBMFF)
	off := 60
	gps := time.Date(2019, 5, 4, 8, 8, 7, 0, time.UTC)
	sameMeta(t, "CR3", m, Meta{CaptureLocal: "2019-05-04T09:08:07", CaptureOffsetMin: &off, GPS: &gps,
		Container: &created, Make: "Canon", Model: "Canon EOS R", Serial: "123456789"})
}

func TestReadMP4(t *testing.T) {
	created := time.Date(2011, 4, 23, 13, 15, 0, 0, time.UTC)
	for _, c := range []struct {
		name    string
		b       []byte
		maxRead int64
	}{
		{"moov first", buildMP4("isom", created, false, 2<<20, 0), FirstWindow},
		{"moov last", buildMP4("isom", created, true, 3<<20, 0), FirstWindow + 64<<10},
		{"MOV, mvhd v1", append(mkBox("wide"), buildMP4("qt  ", created, true, 5<<20, 1)...), FirstWindow + 64<<10},
		{"3GP", buildMP4("3gp4", created, false, 1000, 0), FirstWindow},
	} {
		m, r := mustRead(t, c.b, FormatISOBMFF)
		if m.Container == nil || !m.Container.Equal(created) || m.CaptureLocal != "" || m.GPS != nil {
			t.Errorf("%s: %s", c.name, metaString(m))
		}
		if r.bytes > c.maxRead {
			t.Errorf("%s: read %d bytes, want at most %d (no media payload)", c.name, r.bytes, c.maxRead)
		}
	}
	// A creation time of 0 is absent.
	m, _ := mustRead(t, buildMP4("isom", time.Time{}, false, 100, 0), FormatISOBMFF)
	if m.Container != nil {
		t.Errorf("zero creation time read as %v", m.Container)
	}
	// A 64-bit box size over mdat.
	ftyp := mkBox("ftyp", []byte("isom"), u32(0))
	big := append(append(u32(1), []byte("mdat")...), u64(16+1000)...)
	big = append(big, make([]byte, 1000)...)
	b := bytes.Join([][]byte{ftyp, big, mkBox("moov", mvhd(0, created))}, nil)
	if m, _ := mustRead(t, b, FormatISOBMFF); m.Container == nil || !m.Container.Equal(created) {
		t.Errorf("largesize mdat: %s", metaString(m))
	}
}

// TestReadBounds: at most MaxBytes is read, and loops, offsets outside the
// file, and huge counts give no value.
func TestReadBounds(t *testing.T) {
	bo := binary.LittleEndian
	empty := func(name string, b []byte, f Format) {
		t.Helper()
		m, _ := mustRead(t, b, f)
		if !reflect.DeepEqual(m, Meta{}) {
			t.Errorf("%s: %s, want no value", name, metaString(m))
		}
	}
	good := sampleTIFF(bo, 42)

	// An Exif IFD pointer back to IFD0: a loop.
	loop := buildTIFF(bo, 42, []tiffTag{asciiTag(tagMake, "X"), longTag(bo, tagExifIFD, 8)}, nil, nil)
	empty("Exif IFD loop", loop, FormatTIFF)
	empty("Exif IFD loop in JPEG", buildJPEG(exifAPP1(loop)), FormatJPEG)
	// A GPS pointer to the Exif IFD.
	// IFD0 holds Make, the GPS pointer, then the Exif pointer: point the
	// first at the second's IFD.
	gl := buildTIFF(bo, 42, []tiffTag{asciiTag(tagMake, "X"), longTag(bo, tagGPSIFD, 0)},
		[]tiffTag{asciiTag(tagDateTimeOriginal, "2005:06:07 08:09:10")}, nil)
	copy(gl[8+2+12+8:8+2+12+12], gl[8+2+24+8:8+2+24+12])
	empty("GPS IFD onto the Exif IFD", gl, FormatTIFF)

	// IFD0 outside the file.
	out := append([]byte(nil), good...)
	bo.PutUint32(out[4:], uint32(len(out)+100))
	empty("IFD0 outside the file", out, FormatTIFF)
	// The Exif IFD outside the file.
	out = buildTIFF(bo, 42, []tiffTag{asciiTag(tagMake, "X"), longTag(bo, tagExifIFD, 1<<30)}, nil, nil)
	empty("Exif IFD outside the file", out, FormatTIFF)
	// A huge entry count.
	huge := append([]byte(nil), good...)
	bo.PutUint16(huge[8:], 0xFFFF)
	empty("huge IFD count", huge, FormatTIFF)
	// A value outside the file leaves only that value out.
	vo := buildTIFF(bo, 42, []tiffTag{asciiTag(tagMake, "NIKON"), asciiTag(tagModel, "COOLPIX P5000")}, nil, nil)
	bo.PutUint32(vo[8+2+12+8:], 1<<30) // Model's value offset
	if m, _ := mustRead(t, vo, FormatTIFF); m.Make != "NIKON" || m.Model != "" {
		t.Errorf("value outside the file: %s", metaString(m))
	}

	// ISO-BMFF: a box larger than the file, a box smaller than its header,
	// a huge iloc count, and too many boxes.
	created := time.Date(2011, 4, 23, 13, 15, 0, 0, time.UTC)
	mp4 := buildMP4("isom", created, false, 100, 0)
	over := append([]byte(nil), mp4...)
	ftypLen := binary.BigEndian.Uint32(over)
	binary.BigEndian.PutUint32(over[ftypLen:], 1<<30) // moov's size
	empty("moov past the end", over, FormatISOBMFF)
	small := append([]byte(nil), mp4...)
	binary.BigEndian.PutUint32(small[ftypLen:], 4)
	empty("box smaller than its header", small, FormatISOBMFF)
	large := append([]byte(nil), mp4...)
	copy(large[ftypLen:], append(u32(1), []byte("moov")...))
	empty("64-bit size past the end", append(large[:ftypLen+8], u64(1<<40)...), FormatISOBMFF)
	var many []byte
	for range 1100 {
		many = append(many, mkBox("free")...)
	}
	empty("too many boxes", append(many, mkBox("moov", mvhd(0, created))...), FormatISOBMFF)
	heif := buildHEIF("heic", good, 0, false)
	i := bytes.Index(heif, []byte("iloc"))
	hugeIloc := append([]byte(nil), heif...)
	binary.BigEndian.PutUint16(hugeIloc[i+4+4+2:], 0xFFFF) // item_count
	empty("huge iloc count", hugeIloc, FormatISOBMFF)
	extentOut := append([]byte(nil), heif...)
	j := bytes.LastIndex(extentOut, u32(uint32(len(good)+10))) // the Exif extent's length
	binary.BigEndian.PutUint32(extentOut[j-4:], 1<<30)         // its offset
	empty("Exif extent outside the file", extentOut, FormatISOBMFF)

	// JPEG: a segment past the end, random bytes, and a truncated file.
	j0 := buildJPEG(exifAPP1(good))
	empty("JPEG truncated in APP1", j0[:40], FormatJPEG)
	empty("not a JPEG", []byte("GIF89a......"), FormatJPEG)
	rnd := rand.New(rand.NewPCG(1, 2))
	noise := make([]byte, 64<<10)
	for i := range noise {
		noise[i] = byte(rnd.Uint32())
	}
	for _, f := range []Format{FormatJPEG, FormatTIFF, FormatISOBMFF} {
		mustRead(t, noise, f)
	}

	// A 2 GiB video whose `moov` follows `mdat`: only box headers and
	// `mvhd` are read past the first window.
	ftyp := mkBox("ftyp", []byte("isom"), u32(0))
	moov := mkBox("moov", mvhd(0, created))
	const size = 2 << 30
	mdat := append(u32(uint32(size-len(ftyp)-len(moov))), []byte("mdat")...)
	z := sparse{prefix: append(ftyp, mdat...), suffix: moov, size: size}
	r := &recorder{r: z}
	m, err := Read(r, z.size, FormatISOBMFF)
	if err != nil {
		t.Fatal(err)
	}
	if m.Container == nil || !m.Container.Equal(created) || r.bytes > FirstWindow+int64(len(moov)) {
		t.Errorf("a 2 GiB video: %s, %d bytes read", metaString(m), r.bytes)
	}
	// Many tiny boxes spread out past the window: each header is a
	// positioned read, and the box limit stops them well under MaxBytes.
	spread := mkBox("ftyp", []byte("isom"))
	for range 2000 {
		spread = append(spread, mkBox("free", make([]byte, 1000))...)
	}
	m, rec := mustRead(t, spread, FormatISOBMFF)
	if !reflect.DeepEqual(m, Meta{}) || rec.bytes > MaxBytes {
		t.Errorf("spread boxes: %s, %d bytes", metaString(m), rec.bytes)
	}
}

func TestReadErrors(t *testing.T) {
	b := buildJPEG(exifAPP1(sampleTIFF(binary.LittleEndian, 42)))
	r := newRecorder(b)
	r.fail = errDisk
	if _, err := Read(r, int64(len(b)), FormatJPEG); !errors.Is(err, errDisk) {
		t.Errorf("a failing reader: %v, want %v", err, errDisk)
	}
	// A failure past the first window is the reader's too.
	created := time.Date(2011, 4, 23, 13, 15, 0, 0, time.UTC)
	mp4 := buildMP4("isom", created, true, 1<<20, 0)
	fr := &failAfter{r: newRecorder(mp4), n: 1}
	if _, err := Read(fr, int64(len(mp4)), FormatISOBMFF); !errors.Is(err, errDisk) {
		t.Errorf("a failure after the window: %v", err)
	}
	// A panic is no metadata, not a crash.
	r = newRecorder(b)
	r.panic = true
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(prev)
	if m, err := Read(r, int64(len(b)), FormatJPEG); err != nil || !reflect.DeepEqual(m, Meta{}) {
		t.Errorf("a panic: %s, %v", metaString(m), err)
	}
	// A file shorter than its size gives no value.
	if m, err := Read(newRecorder(b[:100]), int64(len(b)), FormatJPEG); err != nil || !reflect.DeepEqual(m, Meta{}) {
		t.Errorf("a short file: %s, %v", metaString(m), err)
	}
	// FormatNone and empty files read nothing.
	r = newRecorder(b)
	if m, err := Read(r, int64(len(b)), FormatNone); err != nil || !reflect.DeepEqual(m, Meta{}) || r.calls != 0 {
		t.Errorf("FormatNone read %d times", r.calls)
	}
	if m, err := Read(newRecorder(nil), 0, FormatJPEG); err != nil || !reflect.DeepEqual(m, Meta{}) {
		t.Errorf("an empty file: %s, %v", metaString(m), err)
	}
}

// failAfter fails every read after its first n.
type failAfter struct {
	r *recorder
	n int
}

func (f *failAfter) ReadAt(p []byte, off int64) (int, error) {
	if f.n <= 0 {
		return 0, errDisk
	}
	f.n--
	return f.r.ReadAt(p, off)
}
