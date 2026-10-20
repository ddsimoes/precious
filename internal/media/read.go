package media

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
)

// Read parses the headers of a file of the given format (D1). It returns an
// error only when r fails; malformed input gives a partial or empty Meta. A
// panic of a parser is logged and gives an empty Meta.
func Read(r io.ReaderAt, size int64, f Format) (m Meta, err error) {
	defer func() {
		if p := recover(); p != nil {
			slog.Default().Error("media: parser panic", "format", f.String(), "size", size,
				"panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			m, err = Meta{}, nil
		}
	}()
	return read(r, size, f)
}

// read is Read without the recover net; fuzz targets call it.
func read(r io.ReaderAt, size int64, f Format) (Meta, error) {
	if f == FormatNone || size <= 0 {
		return Meta{}, nil
	}
	s, err := newSource(r, size)
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	switch f {
	case FormatJPEG:
		m, err = readJPEG(s)
	case FormatTIFF:
		var fields exifFields
		fields, err = parseTIFF(fileView{s}, s, tiffFull)
		m = fields.meta()
	case FormatISOBMFF:
		m, err = readBMFF(s)
	}
	var ioe *ioError
	if errors.As(err, &ioe) {
		return Meta{}, ioe
	}
	if err != nil {
		return Meta{}, nil
	}
	return m, nil
}

// exifHeader starts a JPEG APP1 segment that holds EXIF.
var exifHeader = []byte("Exif\x00\x00")

// readJPEG walks the JPEG segments up to the scan and parses the first APP1
// Exif segment.
func readJPEG(s *source) (Meta, error) {
	soi, err := s.at(0, 2)
	if err != nil {
		return Meta{}, err
	}
	if soi[0] != 0xFF || soi[1] != 0xD8 {
		return Meta{}, errMalformed
	}
	pos := int64(2)
	for {
		if err := s.box(); err != nil {
			return Meta{}, err
		}
		mk, err := s.at(pos, 2)
		if err != nil {
			return Meta{}, err
		}
		if mk[0] != 0xFF {
			return Meta{}, errMalformed
		}
		switch m := mk[1]; {
		case m == 0xFF: // fill byte
			pos++
			continue
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7): // no length
			pos += 2
			continue
		case m == 0xD9 || m == 0xDA: // end of image, start of scan
			return Meta{}, nil
		}
		ln, err := s.at(pos+2, 2)
		if err != nil {
			return Meta{}, err
		}
		n := int64(ln[0])<<8 | int64(ln[1])
		if n < 2 {
			return Meta{}, errMalformed
		}
		if mk[1] == 0xE1 && n-2 >= int64(len(exifHeader)) {
			body, err := s.at(pos+4, int(n-2))
			if err != nil {
				return Meta{}, err
			}
			if bytes.HasPrefix(body, exifHeader) {
				f, err := parseTIFF(memView(body[len(exifHeader):]), s, tiffFull)
				if err != nil {
					return Meta{}, err
				}
				return f.meta(), nil
			}
		}
		pos += 2 + n
	}
}
