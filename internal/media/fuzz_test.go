package media

import (
	"encoding/binary"
	"testing"
	"time"
)

// The fuzz targets call the parsers without Read's recover net, so a panic
// fails them. `go test` runs their seeds: the f.Add ones below, and the
// checked-in ones in testdata/fuzz.

// fuzzRead reads b as format f and fails on an error or over MaxBytes.
func fuzzRead(t *testing.T, b []byte, f Format) {
	r := newRecorder(b)
	if _, err := read(r, int64(len(b)), f); err != nil {
		t.Fatalf("read: %v", err)
	}
	if r.bytes > MaxBytes {
		t.Fatalf("read %d bytes", r.bytes)
	}
}

func FuzzReadJPEG(f *testing.F) {
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		f.Add(buildJPEG(exifAPP1(sampleTIFF(bo, 42))))
	}
	f.Add(buildJPEG(jpegSegment(0xE0, []byte("JFIF\x00")), exifAPP1(buildTIFF(binary.LittleEndian, 42,
		[]tiffTag{asciiTag(tagMake, "X"), longTag(binary.LittleEndian, tagExifIFD, 8)}, nil, nil))))
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xFF, 0xFF, 0xE1, 0x00, 0x02})
	f.Fuzz(func(t *testing.T, b []byte) { fuzzRead(t, b, FormatJPEG) })
}

func FuzzReadTIFF(f *testing.F) {
	for _, m := range []uint16{42, 0x4F52, 0x0055} {
		f.Add(sampleTIFF(binary.LittleEndian, m))
		f.Add(sampleTIFF(binary.BigEndian, m))
	}
	f.Add([]byte("II*\x00\x08\x00\x00\x00\xff\xff"))
	f.Fuzz(func(t *testing.T, b []byte) { fuzzRead(t, b, FormatTIFF) })
}

func FuzzReadISOBMFF(f *testing.F) {
	created := time.Date(2011, 4, 23, 13, 15, 0, 0, time.UTC)
	f.Add(buildMP4("isom", created, false, 64, 0))
	f.Add(buildMP4("qt  ", created, true, 64, 1))
	f.Add(buildHEIF("heic", sampleTIFF(binary.BigEndian, 42), 0, false))
	f.Add(buildHEIF("avif", sampleTIFF(binary.LittleEndian, 42), 1, true))
	f.Add(buildCR3(binary.LittleEndian, created))
	f.Fuzz(func(t *testing.T, b []byte) { fuzzRead(t, b, FormatISOBMFF) })
}

// FuzzNameDate checks the name patterns never panic, and that a date
// found is valid and consistent.
func FuzzNameDate(f *testing.F) {
	for _, s := range []string{"IMG_20150312_143000.jpg", "PXL_20210704_183015123.jpg", "Screenshot_2011-05-02-21-14-07.png",
		"IMG-20110416-WA0003.jpg", "2015-03-12 14.30.00.jpg", "2010-07 Bahia"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if d, ok := NameDate(b, recif); ok {
			if _, err := DateFromRow(d.Instant.UnixNano(), d.Local, d.OffsetMin, string(d.Precision)); err != nil {
				t.Fatalf("NameDate(%q) = %+v: %v", b, d, err)
			}
		}
		if d, ok := FolderDate(b, recif, now); ok {
			if _, err := DateFromRow(d.Instant.UnixNano(), d.Local, d.OffsetMin, string(d.Precision)); err != nil {
				t.Fatalf("FolderDate(%q) = %+v: %v", b, d, err)
			}
		}
		_ = EventName(b)
	})
}
