// Package media reads the dates and cameras recorded in photo and video
// headers, and derives each media file's effective date (precious-spec
// §10.7; r5 design D1, D2, D5–D8, D16). It uses the standard library only
// and never touches a database or a filesystem: callers hand it an
// io.ReaderAt and the facts of an entry.
//
//   - Read parses EXIF (JPEG APP1, TIFF and TIFF-based RAW, the Exif item of
//     HEIC/HEIF/AVIF, CR3's CMT boxes) and the creation time of MP4/MOV
//     (`moov/mvhd`), reading header structures only and at most MaxBytes of
//     a file (ADR 0012).
//   - Derive picks the effective date from the owner's correction and the
//     candidates (EXIF, GPS, container, file name, folder name, modification
//     time), with its precision, confidence, and flags (D5, D6, D8).
//   - Detect finds cameras whose clock is off against the other cameras of
//     an event (D8).
//   - Template, EventName, and RenamedName build the folders and names of
//     an organize by date (D16).
package media

import (
	"errors"
	"time"
)

// Format says which parser reads a file (D1).
type Format uint8

const (
	// FormatNone is a media file without a parser here: it is dated by
	// name, folder, or modification time (D2).
	FormatNone Format = iota
	// FormatJPEG reads the APP1 Exif segment.
	FormatJPEG
	// FormatTIFF reads IFD0 and its Exif and GPS IFDs, for TIFF and the
	// TIFF-based RAW formats (ORF and RW2 with their own header magics).
	FormatTIFF
	// FormatISOBMFF reads `meta` (the Exif item of HEIC/HEIF/AVIF) and
	// `moov` (`mvhd`, and CR3's CMT boxes).
	FormatISOBMFF
)

// String names the format, for logs.
func (f Format) String() string {
	switch f {
	case FormatJPEG:
		return "jpeg"
	case FormatTIFF:
		return "tiff"
	case FormatISOBMFF:
		return "isobmff"
	default:
		return "none"
	}
}

var formats = map[string]Format{
	"jpg": FormatJPEG, "jpeg": FormatJPEG, "jpe": FormatJPEG, "jfif": FormatJPEG,
	"tif": FormatTIFF, "tiff": FormatTIFF, "cr2": FormatTIFF, "nef": FormatTIFF, "arw": FormatTIFF,
	"dng": FormatTIFF, "pef": FormatTIFF, "srw": FormatTIFF, "orf": FormatTIFF, "rw2": FormatTIFF,
	"heic": FormatISOBMFF, "heif": FormatISOBMFF, "avif": FormatISOBMFF, "cr3": FormatISOBMFF,
	"mp4": FormatISOBMFF, "m4v": FormatISOBMFF, "mov": FormatISOBMFF, "3gp": FormatISOBMFF, "3g2": FormatISOBMFF,
}

// FormatOf returns the parser for an extension (lower-case, without the
// dot), or FormatNone (D1, D2).
func FormatOf(ext string) Format { return formats[ext] }

// IsMediaKind reports whether a file kind of the policy's tables is media
// (D2): "image" or "video". It is the Go twin of dates.MediaCond.
func IsMediaKind(fileKind string) bool { return fileKind == "image" || fileKind == "video" }

const (
	// FirstWindow is the size of the first read of a file (D1).
	FirstWindow = 256 << 10
	// MaxBytes bounds the bytes read from one file (D1).
	MaxBytes = 1 << 20
)

// Meta is what Read finds in a file's headers. Every field is optional.
type Meta struct {
	// CaptureLocal is the EXIF capture wall time, "2006-01-02T15:04:05",
	// or with ".000" milliseconds from SubSecTimeOriginal; "" when none.
	CaptureLocal string
	// CaptureOffsetMin is OffsetTimeOriginal, in minutes east of UTC.
	CaptureOffsetMin *int
	// GPS is GPSDateStamp + GPSTimeStamp, and Container the `mvhd`
	// creation time; both UTC instants.
	GPS, Container      *time.Time
	Make, Model, Serial string
}

// Precision is how fine a date is.
type Precision string

const (
	PrecisionSecond Precision = "second"
	PrecisionDay    Precision = "day"
	PrecisionMonth  Precision = "month"
	PrecisionYear   Precision = "year"
)

// rank orders precisions from the coarsest (1) to the finest (4); 0 is
// unknown.
func (p Precision) rank() int {
	switch p {
	case PrecisionYear:
		return 1
	case PrecisionMonth:
		return 2
	case PrecisionDay:
		return 3
	case PrecisionSecond:
		return 4
	}
	return 0
}

// Source is where an effective date comes from (D5).
type Source string

const (
	SourceOwner      Source = "owner"
	SourceEXIF       Source = "exif"
	SourceGPS        Source = "gps"
	SourceContainer  Source = "container"
	SourceFileName   Source = "file_name"
	SourceFolderName Source = "folder_name"
	SourceMtime      Source = "mtime"
	SourceNone       Source = "none"
)

// Confidence is how far an effective date can be trusted (D5).
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
	ConfidenceLowest Confidence = "lowest"
	ConfidenceNone   Confidence = "none"
)

// MetaState is the state of a file's `media_meta` row (D3).
type MetaState string

const (
	MetaPending    MetaState = "pending"
	MetaRead       MetaState = "read"
	MetaNone       MetaState = "none"
	MetaUnreadable MetaState = "unreadable"
)

// Flags is the `media_dates.flags` bitmask (D8).
type Flags uint32

const (
	FlagMtimeDisagrees Flags = 1
	FlagImplausible    Flags = 2
	FlagCameraOffset   Flags = 4
	FlagNoDateMetadata Flags = 8
)

// Correction kinds (D11).
const (
	CorrectionSet       = "set"
	CorrectionShift     = "shift"
	CorrectionUseName   = "use_name"
	CorrectionUseFolder = "use_folder"
)

// ErrTooCoarse refuses a folder or name that needs a finer date than the
// file has (D6, D16).
var ErrTooCoarse = errors.New("media: date too coarse")

// ErrInvalidTemplate wraps every refusal of ParseTemplate. The organize
// commands answer it with invalid_request.
var ErrInvalidTemplate = errors.New("media: invalid template")
