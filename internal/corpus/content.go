package corpus

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	_ "embed"
	"encoding/binary"
	"hash/fnv"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"time"
)

// Viewer fixtures (R1.13): tiny valid files generated once with ffmpeg and,
// for the PDF, written by hand.
var (
	//go:embed testdata/video.mp4
	videoMP4 []byte
	//go:embed testdata/audio.mp3
	audioMP3 []byte
	//go:embed testdata/documento.pdf
	documentPDF []byte
)

// notesBZ2 is a single-file bzip2 taken from m4b's archive tests: Go has no
// bzip2 writer.
//
//go:embed testdata/notes.txt.bz2
var notesBZ2 []byte

// prng is splitmix64: a fixed, seedable generator, so the corpus is the same
// on every run and Go version.
type prng struct{ s uint64 }

func newPRNG(label string) *prng {
	h := fnv.New64a()
	h.Write([]byte(label))
	return &prng{s: h.Sum64()}
}

func (p *prng) next() uint64 {
	p.s += 0x9e3779b97f4a7c15
	z := p.s
	z = (z ^ z>>30) * 0xbf58476d1ce4e5b9
	z = (z ^ z>>27) * 0x94d049bb133111eb
	return z ^ z>>31
}

// random returns size pseudo-random bytes seeded by label.
func random(label string, size int) []byte {
	b := make([]byte, size)
	p := newPRNG(label)
	i := 0
	for ; i+8 <= size; i += 8 {
		binary.LittleEndian.PutUint64(b[i:], p.next())
	}
	var tail [8]byte
	binary.LittleEndian.PutUint64(tail[:], p.next())
	copy(b[i:], tail[:])
	return b
}

// headed returns size pseudo-random bytes that start with header.
func headed(header, label string, size int) []byte {
	b := random(label, size)
	copy(b, header)
	return b
}

// Binary formats, recognizable by their first bytes.

func pe(label string, size int) []byte {
	b := headed("MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xff\x00\x00", label, size)
	copy(b[0x4e:], "This program cannot be run in DOS mode.\r\r\n$")
	return b
}

// ole is a compound file: .doc, .xls, .msi, Thumbs.db.
func ole(label string, size int) []byte {
	return headed("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1", label, size)
}

func winhelp(label string, size int) []byte { return headed("?_\x03\x00", label, size) }

func icon(label string, size int) []byte {
	return headed("\x00\x00\x01\x00\x01\x00\x20\x20", label, size)
}

func classFile(label string, size int) []byte {
	return headed("\xca\xfe\xba\xbe\x00\x00\x00\x31", label, size)
}

func bitmap(label string, size int) []byte {
	b := headed("BM", label, size)
	binary.LittleEndian.PutUint32(b[2:], uint32(size))
	return b
}

func mp3(label string, size int) []byte {
	return headed("ID3\x03\x00\x00\x00\x00\x00\x00\xff\xfb\x90\x64", label, size)
}

func avi(label string, size int) []byte {
	b := headed("RIFF\x00\x00\x00\x00AVI LIST", label, size)
	binary.LittleEndian.PutUint32(b[4:], uint32(size-8))
	return b
}

// iso is an ISO 9660 image: the primary volume descriptor sits at 32 KiB.
func iso(label string, size int) []byte {
	b := random(label, size)
	copy(b[32768:], "\x01CD001\x01\x00")
	return b
}

// photo is a real JPEG of a noisy gradient, different for every label.
func photo(label string, w, h int) []byte {
	p := newPRNG(label)
	base := p.next()
	r0, g0, b0 := uint8(base), uint8(base>>8), uint8(base>>16)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			n := p.next()
			i := img.PixOffset(x, y)
			img.Pix[i] = r0 + uint8(x*128/w) + uint8(n&0x1f)
			img.Pix[i+1] = g0 + uint8(y*128/h) + uint8(n>>8&0x1f)
			img.Pix[i+2] = b0 + uint8((x+y)*64/(w+h)) + uint8(n>>16&0x1f)
			img.Pix[i+3] = 0xff
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// gifImage is a real tiny GIF.
func gifImage(label string) []byte {
	p := newPRNG(label)
	c := p.next()
	pal := color.Palette{color.White, color.RGBA{uint8(c), uint8(c >> 8), uint8(c >> 16), 0xff}}
	img := image.NewPaletted(image.Rect(0, 0, 16, 16), pal)
	for i := range img.Pix {
		img.Pix[i] = uint8(p.next() & 1)
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// zipMember is one file stored in a zip archive.
type zipMember struct {
	name    string
	data    []byte
	mtime   time.Time
	deflate bool // method deflate; otherwise store
}

// zipArchive is a real zip of members, stored without compression unless a
// member asks for deflate.
func zipArchive(members []zipMember) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, m := range members {
		method := zip.Store
		if m.deflate {
			method = zip.Deflate
		}
		f, err := w.CreateHeader(&zip.FileHeader{Name: m.name, Method: method, Modified: m.mtime})
		if err == nil {
			_, err = f.Write(m.data)
		}
		if err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// tarMember is one member of a tar archive: a folder when dir is set (its
// name without a trailing '/'), else a file.
type tarMember struct {
	name  string
	data  []byte
	mtime time.Time
	dir   bool
}

// tarGzip is a real tar.gz of members, with fixed owners and modes and an
// empty gzip header.
func tarGzip(members []tarMember) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, m := range members {
		h := &tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.data)), ModTime: m.mtime,
			Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if m.dir {
			h.Name, h.Mode, h.Typeflag = m.name+"/", 0o755, tar.TypeDir
		}
		err := tw.WriteHeader(h)
		if err == nil && !m.dir {
			_, err = tw.Write(m.data)
		}
		if err != nil {
			panic(err)
		}
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// gzipFile is a real single-file gzip of data, whose header names the file
// and its modification time.
func gzipFile(name string, mtime time.Time, data []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Name, zw.ModTime = name, mtime
	_, err := zw.Write(data)
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// bunzip2 returns the content of a single-file bzip2.
func bunzip2(b []byte) []byte {
	data, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(b)))
	if err != nil {
		panic(err)
	}
	return data
}

// at is a UTC modification time.
func at(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}
