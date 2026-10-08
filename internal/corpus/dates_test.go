package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/jpeg"
	"image/png"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/media"
)

// readMeta reads a corpus file's headers through internal/media.
func readMeta(t *testing.T, p string) media.Meta {
	t.Helper()
	b := readReal(t, p)
	m, err := media.Read(bytes.NewReader(b), int64(len(b)), media.FormatOf(strings.ToLower(strings.TrimPrefix(path.Ext(p), "."))))
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return m
}

func ptrTime(t time.Time) *time.Time { return &t }
func ptrInt(v int) *int              { return &v }

// The writers round-trip through media.Read: what each fixture was written
// with is what the parser reads.
func TestMediaWritersRoundTrip(t *testing.T) {
	for _, c := range []struct {
		path string
		want media.Meta
	}{
		{bahia + "/IMG_0101.JPG", media.Meta{CaptureLocal: "2010-07-17T10:00:00", GPS: ptrTime(at(2010, 7, 17, 10, 0)),
			Make: "Canon", Model: "Canon PowerShot SX230 HS"}},
		{bahia + "/IMG_0103.JPG", media.Meta{CaptureLocal: "2010-07-17T12:00:00", Make: "Canon", Model: "Canon PowerShot SX230 HS"}},
		{natalTrip + "/IMG_0201.JPG", media.Meta{CaptureLocal: "2010-12-24T19:00:00", GPS: ptrTime(at(2010, 12, 24, 19, 0)),
			Make: "Canon", Model: "Canon PowerShot SX230 HS"}},
		{bahia + "/DSC00306.JPG", media.Meta{CaptureLocal: "2009-07-17T10:00:00", Make: "SONY", Model: "DSC-W55"}},
		{natalTrip + "/DSC00401.JPG", media.Meta{CaptureLocal: "2009-12-24T16:00:00", Make: "SONY", Model: "DSC-W55"}},
		{ouroPreto + "/DSCN0002.JPG", media.Meta{CaptureLocal: "2008-03-22T14:10:00.340", CaptureOffsetMin: ptrInt(-180),
			Make: "NIKON", Model: "COOLPIX P5000", Serial: "3012345"}},
		{ouroPreto + "/DSCN0004.JPG", media.Meta{CaptureLocal: "2000-01-01T00:00:00", CaptureOffsetMin: ptrInt(-180),
			Make: "NIKON", Model: "COOLPIX P5000", Serial: "3012345"}},
		{celular11 + "/DCIM/Camera/VID_20110423_101500.mp4", media.Meta{Container: ptrTime(at(2011, 4, 23, 13, 15))}},
		{bahia + "/do celular da Ana/IMG_0102.JPG", media.Meta{}},
		{whatsApp11 + "/IMG-20110416-WA0003.jpg", media.Meta{}},
		{"Midia/foto.jpg", media.Meta{}},
		// The ffmpeg test clip's creation time is 0: absent.
		{"Midia/video.mp4", media.Meta{}},
	} {
		if got := readMeta(t, c.path); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: read %+v, want %+v", c.path, got, c.want)
		}
	}
	// The photos with EXIF are still JPEGs, and the screenshot a PNG.
	if _, err := png.Decode(bytes.NewReader(readReal(t, celular11+"/Pictures/Screenshots/Screenshot_2011-05-02-21-14-07.png"))); err != nil {
		t.Errorf("screenshot: %v", err)
	}
	for _, p := range []string{bahia + "/IMG_0101.JPG", ouroPreto + "/DSCN0001.JPG"} {
		if _, err := jpeg.Decode(bytes.NewReader(readReal(t, p))); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if !bytes.Equal(readReal(t, whatsApp11+"/IMG-20110416-WA0003.jpg"), readReal(t, whatsApp11+"/Sent/IMG-20110416-WA0003.jpg")) {
		t.Error("the sent WhatsApp image differs from its original")
	}
	if bytes.Equal(readReal(t, bahia+"/IMG_0102.JPG"), readReal(t, bahia+"/do celular da Ana/IMG_0102.JPG")) {
		t.Error("Ana's IMG_0102.JPG has the Canon's bytes")
	}
}

// Every image and video has a date, and nothing else does; the declared
// ones are the D19 table's.
func TestDateTruth(t *testing.T) {
	g := Corpus().GroundTruth()
	count, flagged := 0, map[string][]string{}
	for _, e := range g.Entries {
		raw, _ := e.RawPath()
		p := string(raw)
		if (e.Date != nil) != (e.Kind == domain.EntryFile && isMedia(p)) {
			t.Errorf("%s: date %+v", p, e.Date)
		}
		if e.Date == nil {
			continue
		}
		count++
		d := e.Date
		if (d.Effective == nil) != (d.Source == "none") || d.Flags == nil || len(d.Local) != map[string]int{
			precYear: 4, precMonth: 7, precDay: 10, precSecond: 19}[d.Precision] {
			t.Errorf("%s: malformed date %+v", p, d)
		}
		for _, f := range d.Flags {
			flagged[f] = append(flagged[f], p)
		}
	}
	if count != 145 {
		t.Errorf("%d media files", count)
	}
	if n := len(flagged[flagCameraOffset]); n != 12 {
		t.Errorf("%d photos with camera_offset", n)
	}
	want := []string{ouroPreto + "/DSCN0001.JPG", ouroPreto + "/DSCN0002.JPG", ouroPreto + "/DSCN0003.JPG"}
	if !slices.Equal(flagged[flagMtimeDisagrees], want) {
		t.Errorf("mtime_disagrees on %q", flagged[flagMtimeDisagrees])
	}
	if !slices.Equal(flagged[flagImplausible], []string{ouroPreto + "/DSCN0004.JPG"}) {
		t.Errorf("implausible on %q", flagged[flagImplausible])
	}
	byPath := map[string]*DateTruth{}
	for _, e := range g.Entries {
		raw, _ := e.RawPath()
		byPath[string(raw)] = e.Date
	}
	for p, w := range map[string]string{
		// path: source precision local refined
		bahia + "/IMG_0101.JPG":                                                "exif second 2010-07-17T10:00:00 false",
		bahia + "/DSC00301.JPG":                                                "exif second 2009-07-17T07:00:00 false",
		bahia + "/do celular da Ana/IMG_0102.JPG":                              "folder_name second 2010-07-17T18:00:00 true",
		ouroPreto + "/DSCN0001.JPG":                                            "exif second 2008-03-22T14:00:00 false",
		ouroPreto + "/DSCN0004.JPG":                                            "folder_name month 2008-03 false",
		celular11 + "/DCIM/Camera/VID_20110423_101500.mp4":                     "container second 2011-04-23T13:15:00 false",
		celular11 + "/Pictures/Screenshots/Screenshot_2011-05-02-21-14-07.png": "file_name second 2011-05-02T21:14:07 false",
		whatsApp11 + "/IMG-20110416-WA0003.jpg":                                "file_name day 2011-04-16 false",
		whatsApp11 + "/Sent/IMG-20110416-WA0003.jpg":                           "file_name day 2011-04-16 false",
		whatsApp09 + "/IMG-20090612-WA0001.jpg":                                "file_name second 2009-06-12T12:00:00 true",
		"Fotos/2004/Natal/DSC00101.JPG":                                        "folder_name second 2004-12-24T20:10:00 true",
		"Fotos - Copia/2006/Praia/DSC_editada.JPG":                             "folder_name year 2006 false",
		"Fotos/2007/Formatura/MVI_3005.AVI":                                    "folder_name second 2007-12-15T21:30:00 true",
		"Downloads/fotos_2005_do_pendrive/Carnaval/DSC01001.JPG":               "mtime second 2005-02-06T16:00:00 false",
		"celular_backup_2009/DCIM/100MEDIA/IMAG0001.jpg":                       "mtime second 2009-05-02T18:00:00 false",
		"Midia/video.mp4":                                                      "mtime second 2010-03-03T20:00:00 false",
		localCfg + "/Temporary Internet Files/Content.IE5/X1/a.gif":            "mtime second 2004-12-30T18:00:00 false",
	} {
		d := byPath[p]
		if d == nil {
			t.Errorf("%s: no date", p)
			continue
		}
		if got := fmt.Sprintf("%s %s %s %v", d.Source, d.Precision, d.Local, d.Refined); got != w {
			t.Errorf("%s: %s, want %s", p, got, w)
		}
	}
	if e := byPath[ouroPreto+"/DSCN0001.JPG"].Effective; !e.Equal(time.Date(2008, 3, 22, 17, 0, 0, 120e6, time.UTC)) {
		t.Errorf("DSCN0001.JPG at %v", e)
	}
	if !reflect.DeepEqual(g.Cameras, []CameraTruth{
		{Key: canonKey, Photos: 8, Folders: []string{}},
		{Key: nikonKey, Photos: 3, Folders: []string{}},
		{Key: sonyKey, ShiftS: 31_546_800, Photos: 12, Folders: []string{bahia, natalTrip}},
	}) {
		t.Errorf("cameras %+v", g.Cameras)
	}
	if fat := FATFixture().GroundTruth(); fat.Cameras != nil {
		t.Errorf("FAT fixture cameras %+v", fat.Cameras)
	}
}

// The declared and ruled dates agree with internal/media's derivation, and
// the cameras with its detection (both are re-checked through the job by
// the dates tests).
func TestDateTruthMatchesMedia(t *testing.T) {
	g := Corpus().GroundTruth()
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	var photos []media.Photo
	folderIDs := map[string]int64{}
	effective := map[string]media.Effective{}
	keys := map[string]string{}
	mtimes := map[string]time.Time{}
	for _, it := range Corpus().items {
		mtimes[it.path] = it.mtime
	}
	for i, e := range g.Entries {
		if e.Date == nil {
			continue
		}
		raw, _ := e.RawPath()
		p := string(raw)
		mtime := mtimes[p]
		f := media.FormatOf(strings.ToLower(strings.TrimPrefix(path.Ext(p), ".")))
		in := media.Inputs{Path: raw, Mtime: &mtime, MetaState: media.MetaNone, Zone: time.UTC, Now: now}
		if f != media.FormatNone {
			m := readMeta(t, p)
			in.MetaState, in.Meta = media.MetaRead, &m
			keys[p] = media.CameraKey(m.Make, m.Model, m.Serial)
		}
		eff := media.Derive(in)
		effective[p] = eff
		if c := eff.Candidates; keys[p] != "" && len(c) > 0 && c[0].Source == media.SourceEXIF && c[0].Plausible {
			dir := path.Dir(p)
			if _, ok := folderIDs[dir]; !ok {
				folderIDs[dir] = int64(len(folderIDs) + 1)
			}
			ph := media.Photo{Entry: int64(i), Folder: folderIDs[dir], Camera: keys[p], Capture: c[0].Date.Instant,
				OffsetKnown: c[0].Date.OffsetMin != nil}
			for _, g := range c {
				if g.Source == media.SourceGPS {
					ph.GPS = &g.Date.Instant
				}
			}
			if fd, ok := media.FolderPathDate([]byte(dir), time.UTC, now); ok {
				ph.FolderDate = &fd
			}
			photos = append(photos, ph)
		}
	}
	offsetFolders := map[int64]string{}
	for dir, id := range folderIDs {
		offsetFolders[id] = dir
	}
	var cameras []CameraTruth
	offset := map[string]map[int64]bool{}
	for _, r := range media.Detect(photos) {
		c := CameraTruth{Key: r.Key, Photos: r.Photos, Folders: []string{}}
		if r.State == media.CameraOffset {
			c.ShiftS = *r.ShiftS
			offset[r.Key] = map[int64]bool{}
			for _, ev := range r.Events {
				c.Folders = append(c.Folders, offsetFolders[ev.Folder])
				offset[r.Key][ev.Folder] = true
			}
		} else if r.State != media.CameraOK {
			t.Errorf("camera %s is %s", r.Key, r.State)
		}
		cameras = append(cameras, c)
	}
	if !reflect.DeepEqual(cameras, g.Cameras) {
		t.Errorf("detection %+v, truth %+v", cameras, g.Cameras)
	}
	for _, e := range g.Entries {
		if e.Date == nil {
			continue
		}
		raw, _ := e.RawPath()
		p := string(raw)
		eff := effective[p]
		if fs := offset[keys[p]]; fs != nil && fs[folderIDs[path.Dir(p)]] {
			eff.Flags |= media.FlagCameraOffset
		}
		got := DateTruth{Source: string(eff.Source), Refined: eff.Refined, Flags: flagNames(eff.Flags)}
		if eff.Date != nil {
			got.Effective, got.Local, got.Precision = &eff.Date.Instant, eff.Date.Local, string(eff.Date.Precision)
		}
		if !reflect.DeepEqual(&got, e.Date) {
			t.Errorf("%s: derived %s, truth %s", p, truthString(&got), truthString(e.Date))
		}
	}
}

func flagNames(f media.Flags) []string {
	out := []string{}
	for i, n := range []string{flagMtimeDisagrees, flagImplausible, flagCameraOffset, flagNoDateMetadata} {
		if f&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

func truthString(d *DateTruth) string {
	e := "nil"
	if d.Effective != nil {
		e = d.Effective.Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("{%s %s %s %s refined=%v %v}", e, d.Local, d.Precision, d.Source, d.Refined, d.Flags)
}

// Fotos, Fotos - Copia, Midia, and the pendrive copies keep their bytes,
// times, and order: r5 adds its fixtures in new folders only (D19).
func TestOlderFoldersUnchanged(t *testing.T) {
	h := sha256.New()
	n := 0
	for _, it := range Corpus().items {
		for _, prefix := range []string{"Fotos/", "Fotos - Copia/", "Midia/", "Downloads/fotos_2005_do_pendrive"} {
			if strings.HasPrefix(it.path+"/", prefix) || strings.HasPrefix(it.path, prefix) {
				fmt.Fprintf(h, "%s\x00%s\x00%d\x00%x\n", it.path, it.kind, it.mtime.UnixNano(), it.sum)
				n++
				break
			}
		}
	}
	const want = "623e52326f87626c93b96272658ee54466a97821d76d33b84fa6152706241858" // the r4 corpus, 132 entries
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		t.Errorf("%d entries hash to %s, want %s", n, got, want)
	}
}
