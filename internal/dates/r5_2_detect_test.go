package dates

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/media"
)

// R5.2's detection (task 2.5): the cameras pass of the media job.

// cameraRowA is one media_cameras row.
type cameraRowA struct {
	state  string
	photos int
	shift  sql.NullInt64
	basis  cameraBasis
}

// camerasA returns src's media_cameras rows by key.
func (e *env) camerasA(src domain.SourceID) map[string]cameraRowA {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT camera_key, state, photos, suggested_shift_s, basis FROM media_cameras
		WHERE source_id = ?`, string(src))
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]cameraRowA{}
	for rows.Next() {
		var (
			key, raw string
			c        cameraRowA
		)
		if err := rows.Scan(&key, &c.state, &c.photos, &c.shift, &raw); err != nil {
			e.t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &c.basis); err != nil {
			e.t.Fatalf("basis of %s: %v", key, err)
		}
		out[key] = c
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// flaggedA returns the paths of src's photos flagged camera_offset.
func (e *env) flaggedA(src domain.SourceID) []string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT e.path FROM media_dates d JOIN entries e ON e.id = d.entry_id
		WHERE d.source_id = ? AND d.flags & 4 <> 0 ORDER BY e.path`, string(src))
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p []byte
		if err := rows.Scan(&p); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, string(p))
	}
	return out
}

// R5.2 The camera with a constant clock offset is detected.
func TestR5_2TheCameraWithAClockOffsetIsDetectedA(t *testing.T) {
	e, _, truth := newCorpusEnv(t)
	e.mediaA(corpusSource)
	cams := e.camerasA(corpusSource)
	sony := cams[sonyKeyA]
	if sony.state != media.CameraOffset || !sony.shift.Valid || sony.shift.Int64 != 31_546_800 || sony.photos != 12 {
		t.Errorf("the Sony: %+v", sony)
	}
	var folders []string
	for _, ev := range sony.basis.Events {
		folders = append(folders, ev.Path)
		if ev.Reference == nil || *ev.Reference != media.ReferenceGPS {
			t.Errorf("event %s: reference %v", ev.Path, ev.Reference)
		}
		if string(ev.PathB64) != ev.Path {
			t.Errorf("event %s: path_b64 %q", ev.Path, ev.PathB64)
		}
		if want := e.id(corpusSource, ev.Path).String(); ev.FolderID != want {
			t.Errorf("event %s: folder_id %s, want %s", ev.Path, ev.FolderID, want)
		}
	}
	slices.Sort(folders)
	if want := []string{"Viagens/2010-07 Bahia", "Viagens/2010-12 Natal"}; !reflect.DeepEqual(folders, want) {
		t.Errorf("the Sony's events %v, want %v", folders, want)
	}
	for _, key := range []string{"Canon|Canon PowerShot SX230 HS|", "NIKON|COOLPIX P5000|3012345"} {
		if c := cams[key]; c.state != media.CameraOK || c.shift.Valid {
			t.Errorf("%s: %+v", key, c)
		}
	}
	if len(cams) != len(truth.Cameras) {
		t.Errorf("%d cameras, the truth lists %d", len(cams), len(truth.Cameras))
	}
	for _, ct := range truth.Cameras {
		if c, ok := cams[ct.Key]; !ok || c.photos != ct.Photos || c.shift.Int64 != ct.ShiftS {
			t.Errorf("camera %s: %+v, truth %+v", ct.Key, c, ct)
		}
	}
	var sonyPhotos []string
	for _, id := range e.sonyIDsA() {
		var p []byte
		if err := e.st.Reader().QueryRow(`SELECT path FROM entries WHERE id = ?`, int64(id)).Scan(&p); err != nil {
			t.Fatal(err)
		}
		sonyPhotos = append(sonyPhotos, string(p))
	}
	slices.Sort(sonyPhotos)
	if got := e.flaggedA(corpusSource); len(sonyPhotos) != 12 || !reflect.DeepEqual(got, sonyPhotos) {
		t.Errorf("flagged %v, want the Sony's 12 photos %v", got, sonyPhotos)
	}
	s := e.storedSummary(corpusSource)
	if s.Cameras.Offset != 1 || s.Cameras.Disagrees != 0 || s.Flags.CameraOffset != 12 {
		t.Errorf("summary %+v", s)
	}
	if got, want := s, e.recounted(corpusSource); got != want {
		t.Errorf("summary %+v, recount %+v", got, want)
	}
}

// shotA is one photo of a synthetic event.
type shotA struct {
	folder, name, make, model string
	at                        time.Time
}

// eventsDiskA adds source "s" holding shots, each a JPEG with its EXIF
// capture as a wall time (no offset, no GPS) and modified at it, and runs
// its media job.
func (e *env) eventsDiskA(shots []shotA) {
	e.t.Helper()
	e.disk("s", "/mnt/s", posix, func(root *synthfs.Node) {
		dirs := map[string]*synthfs.Node{}
		for _, s := range shots {
			d := dirs[s.folder]
			if d == nil {
				d = root.Dir(s.folder)
				dirs[s.folder] = d
			}
			d.File(s.name, 0, s.at).Content(exifJPEGA(exifA{make: s.make, model: s.model,
				original: exifTimeA(s.at)})).ModTime(s.at)
		}
	})
	e.mediaA("s")
}

// burstA is n shots of one camera an hour apart from at, named prefix_k.
func burstA(folder, prefix, make, model string, at time.Time, n int) []shotA {
	var out []shotA
	for i := range n {
		out = append(out, shotA{folder: folder, name: fmt.Sprintf("%s_%02d.JPG", prefix, i+1), make: make,
			model: model, at: at.Add(time.Duration(i) * time.Hour)})
	}
	return out
}

const (
	alfaKeyA = "Alfa|A1|"
	betaKeyA = "Beta|B1|"
)

// Two cameras and no reference: one event holds photos of two cameras a
// day apart, with no GPS, no folder date, and no other event.
func TestR5_2TwoCamerasAndNoReferenceA(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2015, 5, 10, 10, 0, 0, 0, time.UTC)
	shots := append(burstA("Evento", "A", "Alfa", "A1", day, 3), burstA("Evento", "B", "Beta", "B1", day.AddDate(0, 0, 1), 3)...)
	e.eventsDiskA(shots)
	cams := e.camerasA("s")
	for _, key := range []string{alfaKeyA, betaKeyA} {
		if c := cams[key]; c.state != media.CameraDisagrees || c.shift.Valid {
			t.Errorf("%s: %+v", key, c)
		}
	}
	if f := e.flaggedA("s"); len(f) != 0 {
		t.Errorf("flagged %v", f)
	}
}

// Two events and no reference: two events each hold photos of the same two
// cameras, a constant year apart, with no GPS and no folder date.
func TestR5_2TwoEventsAndNoReferenceA(t *testing.T) {
	e := newEnv(t)
	may := time.Date(2015, 5, 10, 10, 0, 0, 0, time.UTC)
	aug := time.Date(2015, 8, 1, 15, 0, 0, 0, time.UTC)
	var shots []shotA
	shots = append(shots, burstA("Festa", "A", "Alfa", "A1", may, 3)...)
	shots = append(shots, burstA("Festa", "B", "Beta", "B1", may.AddDate(-1, 0, 0), 3)...)
	shots = append(shots, burstA("Viagem", "A", "Alfa", "A1", aug, 3)...)
	shots = append(shots, burstA("Viagem", "B", "Beta", "B1", aug.AddDate(-1, 0, 0), 3)...)
	e.eventsDiskA(shots)
	cams := e.camerasA("s")
	for _, key := range []string{alfaKeyA, betaKeyA} {
		c := cams[key]
		if c.state != media.CameraDisagrees || c.shift.Valid || len(c.basis.Events) != 2 {
			t.Errorf("%s: %+v", key, c)
		}
	}
	if f := e.flaggedA("s"); len(f) != 0 {
		t.Errorf("flagged %v", f)
	}
	if s := e.storedSummary("s"); s.Cameras.Disagrees != 2 || s.Cameras.Offset != 0 || s.Flags.CameraOffset != 0 {
		t.Errorf("summary %+v", s)
	}
}

// A camera used on another day of a trip: a folder holds a week of one
// camera's photos and one day of another camera's, within that week.
func TestR5_2ACameraUsedOnAnotherDayOfATripA(t *testing.T) {
	e := newEnv(t)
	start := time.Date(2015, 1, 1, 12, 0, 0, 0, time.UTC)
	var shots []shotA
	for i := range 7 {
		shots = append(shots, shotA{folder: "Ferias", name: fmt.Sprintf("A_%02d.JPG", i+1), make: "Alfa",
			model: "A1", at: start.AddDate(0, 0, i)})
	}
	shots = append(shots, burstA("Ferias", "B", "Beta", "B1", start.AddDate(0, 0, 3).Add(-2*time.Hour), 3)...)
	e.eventsDiskA(shots)
	cams := e.camerasA("s")
	for _, key := range []string{alfaKeyA, betaKeyA} {
		if c := cams[key]; c.state != media.CameraOK || c.shift.Valid {
			t.Errorf("%s: %+v", key, c)
		}
	}
	if f := e.flaggedA("s"); len(f) != 0 {
		t.Errorf("flagged %v", f)
	}
}
