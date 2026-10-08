package media

import (
	"errors"
	"fmt"
	"io"
)

// Limits on a file's structures (D1). Exceeding one is malformed input.
const (
	maxBoxes   = 1024 // ISO-BMFF boxes visited
	maxEntries = 1024 // TIFF IFD entries visited
	maxDepth   = 8    // ISO-BMFF nesting
	maxString  = 256  // an EXIF text value
	maxBlob    = 256 << 10
)

// errMalformed stops a parser: the structure read so far yields no value.
// It never leaves Read.
var errMalformed = errors.New("malformed")

// ioError is an error of the caller's ReaderAt, the only error Read returns.
type ioError struct{ err error }

func (e *ioError) Error() string { return fmt.Sprintf("media: read: %v", e.err) }
func (e *ioError) Unwrap() error { return e.err }

// source serves a file's bytes from its first window, then by positioned
// reads within the MaxBytes budget, and checks every range against the
// file's size.
type source struct {
	r     io.ReaderAt
	size  int64
	win   []byte
	used  int64 // bytes asked of r
	boxes int
	ents  int
}

func newSource(r io.ReaderAt, size int64) (*source, error) {
	s := &source{r: r, size: size}
	if size <= 0 {
		return s, nil
	}
	n := int64(FirstWindow)
	if size < n {
		n = size
	}
	buf := make([]byte, n)
	s.used = n
	got, err := r.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, &ioError{err}
	}
	s.win = buf[:got]
	return s, nil
}

// at returns n bytes at off. A range outside the file, past the end of what
// the file holds, or over the budget is errMalformed; a failure of the
// ReaderAt is an *ioError.
func (s *source) at(off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off > s.size || int64(n) > s.size-off {
		return nil, errMalformed
	}
	if off+int64(n) <= int64(len(s.win)) {
		return s.win[off : off+int64(n)], nil
	}
	if s.used+int64(n) > MaxBytes {
		return nil, errMalformed
	}
	s.used += int64(n)
	buf := make([]byte, n)
	got, err := s.r.ReadAt(buf, off)
	if got == n {
		return buf, nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		return nil, errMalformed // the file is shorter than its size
	}
	return nil, &ioError{err}
}

// box counts one ISO-BMFF box against maxBoxes.
func (s *source) box() error {
	s.boxes++
	if s.boxes > maxBoxes {
		return errMalformed
	}
	return nil
}

// view is a TIFF structure's bytes: a range of the file, or bytes in
// memory (an APP1 segment, an Exif item, a CMT box).
type view interface {
	at(off int64, n int) ([]byte, error)
	len() int64
}

// fileView is the whole file seen through its source.
type fileView struct{ s *source }

func (v fileView) at(off int64, n int) ([]byte, error) { return v.s.at(off, n) }
func (v fileView) len() int64                          { return v.s.size }

// memView is bytes already read.
type memView []byte

func (v memView) at(off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off > int64(len(v)) || int64(n) > int64(len(v))-off {
		return nil, errMalformed
	}
	return v[off : off+int64(n)], nil
}
func (v memView) len() int64 { return int64(len(v)) }
