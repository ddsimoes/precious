package corpus

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"precious/internal/domain"
)

// The media dates of the corpus (r5 design D19). Every image and video gets
// a DateTruth, derived without corrections for the zone UTC, after the
// `media` job and its cameras pass: the fixtures of the new folders, and
// the 2009 WhatsApp images, are declared by hand in dateDecls; every other
// media file follows the corpus's own small rule (ruleDate), which never
// imports internal/media, so the tests compare two implementations.

// DateTruth is a media file's effective date. Effective is the instant, UTC
// (the start of the period when coarser than a second), nil for source
// "none"; Local is the wall time in the capture's offset or in UTC, in the
// form of its precision ("YYYY", "YYYY-MM", "YYYY-MM-DD", or
// "YYYY-MM-DDTHH:MM:SS"). Flags are the flag names, in bit order.
type DateTruth struct {
	Effective *time.Time `json:"effective"`
	Local     string     `json:"local"`
	Precision string     `json:"precision"`
	Source    string     `json:"source"`
	Refined   bool       `json:"refined"`
	Flags     []string   `json:"flags"`
}

// CameraTruth is a camera the cameras pass lists: its key (make|model|
// serial), the photos it was given (a plausible capture and a key), and,
// for the camera with a clock offset, the suggested shift and its event
// folders; ShiftS is 0 and Folders empty for a camera that is ok.
type CameraTruth struct {
	Key     string   `json:"key"`
	ShiftS  int64    `json:"shift_s"`
	Photos  int      `json:"photos"`
	Folders []string `json:"folders"`
}

// Flag names, in bit order (design D8).
const (
	flagMtimeDisagrees = "mtime_disagrees"
	flagImplausible    = "implausible"
	flagCameraOffset   = "camera_offset"
	flagNoDateMetadata = "no_date_metadata"
)

// Date sources and precisions the truth uses.
const (
	srcEXIF      = "exif"
	srcContainer = "container"
	srcFileName  = "file_name"
	srcFolder    = "folder_name"
	srcMtime     = "mtime"

	precSecond = "second"
	precDay    = "day"
	precMonth  = "month"
	precYear   = "year"
)

// mediaExts are the image and video extensions of the policy's file kinds
// (policies/markers/v3.toml), kept here so the truth depends on no rules
// code.
var mediaExts = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`jpg jpeg jpe jfif png gif bmp tif tiff webp heic heif avif
		raw cr2 cr3 nef arw orf rw2 dng psd xcf pcx tga ico svg
		mov mp4 m4v avi mkv wmv mpg mpeg mpe 3gp 3g2 mts m2ts vob flv webm ogv asf divx`) {
		mediaExts[e] = true
	}
}

// isMedia reports whether a file is an image or a video by its name: the
// extension after the last dot of a name that does not start with its only
// dot, ignoring ASCII case.
func isMedia(p string) bool {
	name := path.Base(p)
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return false
	}
	return mediaExts[strings.ToLower(name[i+1:])]
}

// secondTruth is a second-precision date.
func secondTruth(t time.Time, local, source string, refined bool, flags ...string) *DateTruth {
	u := t.UTC()
	return &DateTruth{Effective: &u, Local: local, Precision: precSecond, Source: source, Refined: refined, Flags: nonNil(flags)}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// wall is t's wall time, to the second, in UTC.
func wall(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05") }

// leadingFolderDate is the corpus's own reading of a dated folder name:
// "YYYY", "YYYY-MM", or "YYYY-MM-DD" at its start, then the end, a space,
// '-', '_', or '.', from 1990 on. It returns the period, in UTC.
func leadingFolderDate(name string) (start, end time.Time, local, prec string, ok bool) {
	num := func(s string) (int, bool) {
		n, err := strconv.Atoi(s)
		return n, err == nil && len(s) > 0 && s[0] != '+' && s[0] != '-'
	}
	sepAt := func(i int) bool { return i == len(name) || strings.ContainsRune(" -_.", rune(name[i])) }
	if len(name) < 4 {
		return
	}
	y, okY := num(name[:4])
	if !okY || y < 1990 {
		return
	}
	if len(name) >= 10 && name[4] == '-' && name[7] == '-' && sepAt(10) {
		m, ok1 := num(name[5:7])
		d, ok2 := num(name[8:10])
		s := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
		if ok1 && ok2 && m >= 1 && m <= 12 && s.Day() == d {
			return s, s.AddDate(0, 0, 1), name[:10], precDay, true
		}
	}
	if len(name) >= 7 && name[4] == '-' && sepAt(7) {
		if m, ok1 := num(name[5:7]); ok1 && m >= 1 && m <= 12 {
			s := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
			return s, s.AddDate(0, 1, 0), name[:7], precMonth, true
		}
	}
	if sepAt(4) {
		s := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		return s, s.AddDate(1, 0, 0), name[:4], precYear, true
	}
	return
}

// ruleDate is the date of a media file without date metadata, by the
// corpus's rule: the nearest ancestor folder with a leading date; its
// period when the modification time is outside it, else the modification
// time itself (refined); with no dated folder, the modification time.
func ruleDate(p string, mtime time.Time) *DateTruth {
	parts := strings.Split(p, "/")
	for i := len(parts) - 2; i >= 0; i-- {
		start, end, local, prec, ok := leadingFolderDate(parts[i])
		if !ok {
			continue
		}
		if !mtime.Before(start) && mtime.Before(end) {
			return secondTruth(mtime, wall(mtime), srcFolder, true, flagNoDateMetadata)
		}
		return &DateTruth{Effective: &start, Local: local, Precision: prec, Source: srcFolder, Flags: []string{flagNoDateMetadata}}
	}
	return secondTruth(mtime, wall(mtime), srcMtime, false, flagNoDateMetadata)
}

// Folders of the media-date fixtures.
const (
	bahia      = "Viagens/2010-07 Bahia"
	natalTrip  = "Viagens/2010-12 Natal"
	ouroPreto  = "Viagens/2008-03 Ouro Preto"
	celular11  = "celular_2011"
	whatsApp11 = celular11 + "/WhatsApp/Media/WhatsApp Images"
	whatsApp09 = "celular_backup_2009/WhatsApp/Media/WhatsApp Images"

	canonKey = "Canon|Canon PowerShot SX230 HS|"
	sonyKey  = "SONY|DSC-W55|"
	nikonKey = "NIKON|COOLPIX P5000|3012345"

	// sonyLag is how far the Sony's clock is behind: 365 days 3 hours.
	sonyLag = 365*24*time.Hour + 3*time.Hour
)

// dateDecls are the hand-declared dates, by path.
type dateDecls map[string]*DateTruth

// cameraTruth is what the cameras pass lists for the corpus.
var corpusCameras = []CameraTruth{
	{Key: canonKey, Photos: 8, Folders: []string{}},
	{Key: nikonKey, Photos: 3, Folders: []string{}},
	{Key: sonyKey, ShiftS: int64(sonyLag / time.Second), Photos: 12, Folders: []string{bahia, natalTrip}},
}

// viagens are the trips (D19): a Canon with GPS and a Sony whose clock is
// 1 year 3 hours behind, at two events; a Nikon whose photos were copied
// years later, one with a camera's default date; and a phone photo of the
// same trip without EXIF.
func (d *def) viagens() {
	decl := d.dates
	event := func(dir string, canonFirst int, day time.Time, canonGPS int, sonyFirst int) {
		for i := range 4 {
			p := fmt.Sprintf("%s/IMG_%04d.JPG", dir, canonFirst+i)
			shot := day.Add(time.Duration(i) * time.Hour)
			x := exif{make: "Canon", model: "Canon PowerShot SX230 HS", dateTime: exifTime(shot), original: exifTime(shot)}
			if i < canonGPS {
				x.gps = &shot
			}
			d.file(p, shot, withEXIF(photo(p, 160, 120), x))
			decl[p] = secondTruth(shot, wall(shot), srcEXIF, false)
		}
		for i, m := range []int{0, 30, 60, 120, 150, 180} {
			p := fmt.Sprintf("%s/DSC%05d.JPG", dir, sonyFirst+i)
			truth := day.Add(time.Duration(m) * time.Minute)
			shot := truth.Add(-sonyLag)
			x := exif{make: "SONY", model: "DSC-W55", dateTime: exifTime(shot), original: exifTime(shot)}
			d.file(p, shot, withEXIF(photo(p, 160, 120), x))
			decl[p] = secondTruth(shot, wall(shot), srcEXIF, false, flagCameraOffset)
		}
	}
	event(bahia, 101, at(2010, 7, 17, 10, 0), 2, 301)
	ana := bahia + "/do celular da Ana/IMG_0102.JPG"
	anaTime := at(2010, 7, 17, 18, 0)
	d.file(ana, anaTime, photo(ana, 120, 160))
	decl[ana] = secondTruth(anaTime, wall(anaTime), srcFolder, true, flagNoDateMetadata)
	event(natalTrip, 201, at(2010, 12, 24, 19, 0), 1, 401)

	copied := at(2011, 1, 15, 10, 0)
	for i := range 3 {
		p := fmt.Sprintf("%s/DSCN%04d.JPG", ouroPreto, i+1)
		wallShot := at(2008, 3, 22, 14, 10*i)
		x := exif{make: "NIKON", model: "COOLPIX P5000", serial: "3012345", dateTime: exifTime(wallShot),
			original: exifTime(wallShot), offset: "-03:00", subsec: fmt.Sprintf("%d", 12+22*i)}
		d.file(p, copied, withEXIF(photo(p, 160, 120), x))
		ms := time.Duration(12+22*i) * 10 * time.Millisecond // "12" is .120 s
		decl[p] = secondTruth(wallShot.Add(3*time.Hour+ms), wall(wallShot), srcEXIF, false, flagMtimeDisagrees)
	}
	p := ouroPreto + "/DSCN0004.JPG"
	reset := "2000:01:01 00:00:00"
	d.file(p, copied, withEXIF(photo(p, 160, 120), exif{make: "NIKON", model: "COOLPIX P5000", serial: "3012345",
		dateTime: reset, original: reset, offset: "-03:00"}))
	march := at(2008, 3, 1, 0, 0)
	decl[p] = &DateTruth{Effective: &march, Local: "2008-03", Precision: precMonth, Source: srcFolder, Flags: []string{flagImplausible}}
}

// celular2011 is a phone of 2011 copied in 2012 (D19): a video with its
// creation time, a screenshot, and WhatsApp images without EXIF, one of
// them sent again.
func (d *def) celular2011() {
	decl := d.dates
	vid := celular11 + "/DCIM/Camera/VID_20110423_101500.mp4"
	created := at(2011, 4, 23, 13, 15)
	d.file(vid, created.Add(30*time.Second), mp4Video(vid, created, 24_000))
	decl[vid] = secondTruth(created, wall(created), srcContainer, false)

	shot := celular11 + "/Pictures/Screenshots/Screenshot_2011-05-02-21-14-07.png"
	copied := at(2012, 2, 1, 9, 0)
	d.file(shot, copied, pngImage(shot, 96, 160))
	taken := time.Date(2011, 5, 2, 21, 14, 7, 0, time.UTC)
	decl[shot] = secondTruth(taken, wall(taken), srcFileName, false, flagNoDateMetadata)

	day := func(t time.Time) *DateTruth {
		return &DateTruth{Effective: &t, Local: t.Format("2006-01-02"), Precision: precDay, Source: srcFileName,
			Flags: []string{flagNoDateMetadata}}
	}
	wa3 := whatsApp11 + "/IMG-20110416-WA0003.jpg"
	data := d.file(wa3, copied, photo(wa3, 120, 160))
	decl[wa3] = day(at(2011, 4, 16, 0, 0))
	wa4 := whatsApp11 + "/IMG-20110417-WA0004.jpg"
	d.file(wa4, copied, photo(wa4, 120, 160))
	decl[wa4] = day(at(2011, 4, 17, 0, 0))
	sent := whatsApp11 + "/Sent/IMG-20110416-WA0003.jpg"
	d.file(sent, copied.Add(time.Minute), data)
	decl[sent] = day(at(2011, 4, 16, 0, 0))
}

// whatsApp2009 declares the dates of the 2009 phone backup's WhatsApp
// images: their names' day contains their modification times, so they
// take those times, still from the file name (R5.3).
func (d *def) whatsApp2009() {
	decl := d.dates
	for i := range 2 {
		p := fmt.Sprintf("%s/IMG-20090612-WA%04d.jpg", whatsApp09, i+1)
		m := d.items[d.index[p]].mtime
		decl[p] = secondTruth(m, wall(m), srcFileName, true, flagNoDateMetadata)
	}
}

// checkDates fails unless every declared date names a media file.
func (d *def) checkDates() {
	for p := range d.dates {
		i, ok := d.index[p]
		if !ok || d.items[i].kind != domain.EntryFile || !isMedia(p) {
			panic("corpus: a date declared for what is not a media file: " + displayPath(p))
		}
	}
}

// dateOf is a file's DateTruth: declared, or by the rule; nil when the
// file is not media.
func (t *Tree) dateOf(it item) *DateTruth {
	if it.kind != domain.EntryFile || !isMedia(it.path) {
		return nil
	}
	if d, ok := t.dates[it.path]; ok {
		c := *d
		return &c
	}
	return ruleDate(it.path, it.mtime)
}

// cameraTruth is a copy of the tree's cameras.
func (t *Tree) cameraTruth() []CameraTruth {
	if t.cameras == nil {
		return nil
	}
	out := make([]CameraTruth, len(t.cameras))
	for i, c := range t.cameras {
		c.Folders = append([]string{}, c.Folders...)
		out[i] = c
	}
	return out
}
