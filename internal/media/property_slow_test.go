//go:build slow

package media

import (
	"encoding/binary"
	"math/rand/v2"
	"testing"
	"time"
)

// TestParsersSurviveMutations is the long randomized property run (task
// 1.5): seeds of every format, mutated by flips, overwrites with boundary
// integers, truncations, and splices, never panic the parsers (no recover
// net here), never error on an in-memory reader, and never read more than
// MaxBytes.
func TestParsersSurviveMutations(t *testing.T) {
	created := time.Date(2011, 4, 23, 13, 15, 0, 0, time.UTC)
	seeds := []struct {
		b []byte
		f Format
	}{
		{buildJPEG(exifAPP1(sampleTIFF(binary.LittleEndian, 42))), FormatJPEG},
		{buildJPEG(exifAPP1(sampleTIFF(binary.BigEndian, 42))), FormatJPEG},
		{sampleTIFF(binary.LittleEndian, 0x4F52), FormatTIFF},
		{sampleTIFF(binary.BigEndian, 42), FormatTIFF},
		{sampleTIFF(binary.LittleEndian, 0x0055), FormatTIFF},
		{buildMP4("isom", created, false, 256, 0), FormatISOBMFF},
		{buildMP4("qt  ", created, true, 256, 1), FormatISOBMFF},
		{buildHEIF("heic", sampleTIFF(binary.BigEndian, 42), 0, false), FormatISOBMFF},
		{buildHEIF("avif", sampleTIFF(binary.LittleEndian, 42), 1, true), FormatISOBMFF},
		{buildCR3(binary.LittleEndian, created), FormatISOBMFF},
	}
	boundary := []uint64{0, 1, 2, 7, 8, 15, 16, 0xFF, 0xFFFF, 0x7FFFFFFF, 0xFFFFFFFF, 1 << 40, 1<<63 - 1, 1<<64 - 1}
	rnd := rand.New(rand.NewPCG(5, 1990))
	const rounds = 200_000
	for i := range rounds {
		s := seeds[i%len(seeds)]
		b := append([]byte(nil), s.b...)
		for range 1 + rnd.IntN(4) {
			if len(b) == 0 {
				break
			}
			at := rnd.IntN(len(b))
			switch rnd.IntN(5) {
			case 0: // flip a byte
				b[at] ^= byte(1 + rnd.IntN(255))
			case 1: // a boundary integer, either byte order
				var w [8]byte
				v := boundary[rnd.IntN(len(boundary))]
				if rnd.IntN(2) == 0 {
					binary.BigEndian.PutUint64(w[:], v)
				} else {
					binary.LittleEndian.PutUint64(w[:], v)
				}
				n := []int{2, 4, 8}[rnd.IntN(3)]
				copy(b[at:], w[8-n:])
			case 2: // truncate
				b = b[:at]
			case 3: // splice a piece of the file elsewhere
				from := rnd.IntN(len(b))
				n := rnd.IntN(64)
				if from+n > len(b) {
					n = len(b) - from
				}
				b = append(b[:at], append(append([]byte(nil), b[from:from+n]...), b[at:]...)...)
			case 4: // a pointer at another offset of the file
				var w [4]byte
				binary.BigEndian.PutUint32(w[:], uint32(rnd.IntN(len(b)+16)))
				copy(b[at:], w[:])
			}
		}
		for _, f := range []Format{s.f, FormatJPEG, FormatTIFF, FormatISOBMFF}[:1+i%4] {
			r := newRecorder(b)
			if _, err := read(r, int64(len(b)), f); err != nil {
				t.Fatalf("round %d, %s: %v", i, f, err)
			}
			if r.bytes > MaxBytes {
				t.Fatalf("round %d, %s: read %d bytes", i, f, r.bytes)
			}
		}
	}
}
