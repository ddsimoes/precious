package dates

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/executor"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/media"
)

// Tests of the media job (design D3, D4, D8, Concurrency; task 2.2). The
// helpers of this file end in A (slice A).

// requestA requests src's media job, as every requester does.
func (e *env) requestA(src domain.SourceID) {
	e.t.Helper()
	e.write(func(tx *jobs.Tx) error { return EnqueueMedia(context.Background(), tx, src) })
}

// runScopeA runs the queued media job of scope as the runner would: its
// row turns running with one more attempt, the handler runs, and the row
// ends succeeded, failed, or queued again without that attempt on a
// deferral. It returns the job's ID and the handler's error.
func (e *env) runScopeA(ctx context.Context, scope string, rt jobs.Runtime) (domain.JobID, error) {
	e.t.Helper()
	var (
		id       int64
		payload  string
		src      string
		attempts int
	)
	if err := e.st.Reader().QueryRow(`SELECT id, payload, source_id, attempts FROM jobs
		WHERE kind = ? AND scope_key = ? AND state = 'queued'`, string(KindMedia), scope).Scan(&id, &payload, &src,
		&attempts); err != nil {
		e.t.Fatalf("queued media job %s: %v", scope, err)
	}
	attempt := attempts + 1
	e.exec(`UPDATE jobs SET state = 'running', attempts = ? WHERE id = ?`, attempt, id)
	job := jobs.Job{ID: domain.JobID(id), Kind: KindMedia, PayloadVersion: 1, Payload: []byte(payload),
		SourceID: domain.SourceID(src), Attempt: attempt}
	err := (&handler{s: e.svc}).Run(ctx, job, rt)
	var d *jobs.Defer
	switch {
	case errors.As(err, &d):
		e.exec(`UPDATE jobs SET state = 'queued', attempts = ? WHERE id = ?`, attempts, id)
	case err != nil:
		e.exec(`UPDATE jobs SET state = 'failed' WHERE id = ?`, id)
	default:
		e.exec(`UPDATE jobs SET state = 'succeeded' WHERE id = ?`, id)
	}
	return job.ID, err
}

// mediaA requests src's media job and runs it, failing the test on an
// error; it returns its runtime.
func (e *env) mediaA(src domain.SourceID) *fakeRuntime {
	e.t.Helper()
	e.requestA(src)
	rt := &fakeRuntime{}
	if _, err := e.runScopeA(context.Background(), mediaScope(src), rt); err != nil {
		e.t.Fatalf("media job of %s: %v", src, err)
	}
	return rt
}

// hasQueuedA reports whether a media job of scope is queued.
func (e *env) hasQueuedA(scope string) bool {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM jobs WHERE kind = ? AND scope_key = ? AND state = 'queued'`,
		string(KindMedia), scope) > 0
}

// opensA counts the files opened since the recorder's last reset, by full
// path.
func (e *env) opensA() map[string]int {
	out := map[string]int{}
	for _, c := range e.rec.Calls() {
		if c.Op == instrument.OpOpenFile {
			out[c.FullPath()]++
		}
	}
	return out
}

// stagesA records the stages each media job reached.
type stagesA struct {
	mu    sync.Mutex
	byJob map[domain.JobID][]string
}

func (s *stagesA) of(id domain.JobID) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.byJob[id]...)
}

func (s *stagesA) count(id domain.JobID, stage string) int {
	n := 0
	for _, st := range s.of(id) {
		if st == stage {
			n++
		}
	}
	return n
}

// hookA records every stage, then calls fn when set.
func (e *env) hookA(fn func(ctx context.Context, job jobs.Job, stage string) error) *stagesA {
	rec := &stagesA{byJob: map[domain.JobID][]string{}}
	e.svc.job.hook = func(ctx context.Context, job jobs.Job, stage string) error {
		rec.mu.Lock()
		rec.byJob[job.ID] = append(rec.byJob[job.ID], stage)
		rec.mu.Unlock()
		if fn != nil {
			return fn(ctx, job, stage)
		}
		return nil
	}
	return rec
}

// photoDiskA adds source src at /mnt/<src>: Fotos holding n photos
// IMG_0001.JPG… of random bytes, and scans it.
func (e *env) photoDiskA(src domain.SourceID, n int) *synthfs.Node {
	e.t.Helper()
	return e.disk(src, "/mnt/"+string(src), posix, func(root *synthfs.Node) {
		d := root.Dir("Fotos")
		for i := range n {
			d.File(fmt.Sprintf("IMG_%04d.JPG", i+1), 4096, time.Date(2010, 1, 1, 10, 0, i, 0, time.UTC)).Seed(uint64(i + 1))
		}
	})
}

// mediaStateA returns the media job's state of src: dirty and passes_job.
func (e *env) mediaStateA(src domain.SourceID) (dirty bool, passes sql.NullInt64) {
	e.t.Helper()
	if err := e.st.Reader().QueryRow(`SELECT dirty, passes_job FROM media_sources WHERE source_id = ?`,
		string(src)).Scan(&dirty, &passes); err != nil {
		e.t.Fatal(err)
	}
	return dirty, passes
}

// metaStateA returns the media_meta state of src's entry at p, "" without
// a row.
func (e *env) metaStateA(src domain.SourceID, p string) string {
	e.t.Helper()
	var s string
	err := e.st.Reader().QueryRow(`SELECT state FROM media_meta WHERE entry_id = ?`, int64(e.id(src, p))).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// exifA is the EXIF exifJPEGA writes: IFD0 Make and Model, the Exif IFD's
// DateTimeOriginal ("YYYY:MM:DD HH:MM:SS"), and a GPS IFD when gps is set.
type exifA struct {
	make, model, original string
	gps                   *time.Time
}

// exifJPEGA is a minimal JPEG (SOI, APP1 Exif, EOI) carrying x, big-endian.
func exifJPEGA(x exifA) []byte {
	type entry struct {
		tag, typ uint16
		count    uint32
		value    []byte
	}
	ascii := func(tag uint16, s string) entry {
		return entry{tag, 2, uint32(len(s) + 1), append([]byte(s), 0)}
	}
	long := func(tag uint16, v uint32) entry { return entry{tag, 4, 1, binary.BigEndian.AppendUint32(nil, v)} }
	ifd := func(es []entry, at int) []byte {
		head := make([]byte, 2+12*len(es)+4)
		binary.BigEndian.PutUint16(head, uint16(len(es)))
		var data []byte
		for i, e := range es {
			o := head[2+12*i:]
			binary.BigEndian.PutUint16(o, e.tag)
			binary.BigEndian.PutUint16(o[2:], e.typ)
			binary.BigEndian.PutUint32(o[4:], e.count)
			if len(e.value) <= 4 {
				copy(o[8:12], e.value)
				continue
			}
			binary.BigEndian.PutUint32(o[8:], uint32(at+len(head)+len(data)))
			data = append(data, e.value...)
		}
		return append(head, data...)
	}
	ifd0 := []entry{ascii(0x010F, x.make), ascii(0x0110, x.model), long(0x8769, 0)}
	if x.gps != nil {
		ifd0 = append(ifd0, long(0x8825, 0))
	}
	sub := []entry{ascii(0x9003, x.original)}
	subAt := 8 + len(ifd(ifd0, 8))
	subBytes := ifd(sub, subAt)
	ifd0[2] = long(0x8769, uint32(subAt))
	var gpsBytes []byte
	if x.gps != nil {
		gpsAt := subAt + len(subBytes)
		ifd0[3] = long(0x8825, uint32(gpsAt))
		g := x.gps.UTC()
		var hms []byte
		for _, v := range []int{g.Hour(), g.Minute(), g.Second()} {
			hms = binary.BigEndian.AppendUint32(hms, uint32(v))
			hms = binary.BigEndian.AppendUint32(hms, 1)
		}
		gpsBytes = ifd([]entry{{0x0007, 5, 3, hms}, ascii(0x001D, g.Format("2006:01:02"))}, gpsAt)
	}
	tiff := append([]byte("MM\x00\x2a\x00\x00\x00\x08"), ifd(ifd0, 8)...)
	tiff = append(append(tiff, subBytes...), gpsBytes...)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	n := len(payload) + 2
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte(n >> 8), byte(n)}
	out = append(out, payload...)
	return append(out, 0xFF, 0xD9)
}

// exifTimeA is t as EXIF writes it.
func exifTimeA(t time.Time) string { return t.Format("2006:01:02 15:04:05") }

// A malformed file is read safely: random bytes and a truncated JPEG give
// no metadata, at most 1 MiB is read of each, and the job goes on.
func TestMediaMalformedFileIsReadSafelyA(t *testing.T) {
	e := newEnv(t)
	mtime := time.Date(2012, 2, 1, 9, 0, 0, 0, time.UTC)
	good := exifJPEGA(exifA{make: "Canon", model: "IXUS", original: "2010:07:17 10:00:00"})
	e.disk("m", "/mnt/m", posix, func(root *synthfs.Node) {
		d := root.Dir("Fotos")
		d.File("a_random.jpg", 3<<20, mtime).Seed(7)
		d.File("b_truncated.jpg", 0, mtime).Content(good[:len(good)/2])
		d.File("c_good.jpg", 0, mtime).Content(good)
	})
	e.mediaA("m")
	for _, name := range []string{"a_random.jpg", "b_truncated.jpg"} {
		var capture, mk sql.NullString
		var state string
		if err := e.st.Reader().QueryRow(`SELECT state, capture_local, make FROM media_meta WHERE entry_id = ?`,
			int64(e.id("m", "Fotos/"+name))).Scan(&state, &capture, &mk); err != nil {
			t.Fatal(err)
		}
		if state != "read" || capture.Valid || mk.Valid {
			t.Errorf("%s: state %s, capture %v, make %v; want read with nothing", name, state, capture, mk)
		}
	}
	var capture string
	if err := e.st.Reader().QueryRow(`SELECT capture_local FROM media_meta WHERE entry_id = ?`,
		int64(e.id("m", "Fotos/c_good.jpg"))).Scan(&capture); err != nil {
		t.Fatal(err)
	}
	if capture != "2010-07-17T10:00:00" {
		t.Errorf("good photo's capture %q", capture)
	}
	read := map[string]int{}
	for _, c := range e.rec.Calls() {
		if c.Op == instrument.OpReadAt {
			read[c.FullPath()] += c.Bytes
		}
	}
	for p, n := range read {
		if n > media.MaxBytes {
			t.Errorf("%s: %d bytes read", p, n)
		}
	}
	if read["/mnt/m/Fotos/a_random.jpg"] == 0 {
		t.Errorf("the random file was not read: %v", read)
	}
}

// writeOps are the instrument's Writer operations.
var writeOps = []instrument.Op{instrument.OpRename, instrument.OpMkdir, instrument.OpRmdir, instrument.OpSync,
	instrument.OpCreate, instrument.OpUnlink, instrument.OpSetModTime}

// Reading leaves the disk untouched: no Writer call, and no file's times
// change.
func TestMediaReadingLeavesTheDiskUntouchedA(t *testing.T) {
	e, _, _ := newCorpusEnv(t)
	ids := e.mediaIDs(corpusSource)
	paths := map[domain.EntryID]string{}
	before := map[domain.EntryID]index.PostFacts{}
	for _, id := range ids {
		var p []byte
		if err := e.st.Reader().QueryRow(`SELECT path FROM entries WHERE id = ?`, int64(id)).Scan(&p); err != nil {
			t.Fatal(err)
		}
		paths[id] = string(p)
		before[id] = e.facts(corpusRoot, string(p))
	}
	e.rec.Reset()
	e.mediaA(corpusSource)
	if n := len(e.opensA()); n == 0 {
		t.Fatal("the job opened no file")
	}
	for _, op := range writeOps {
		if n := e.rec.Count(op); n != 0 {
			t.Errorf("%d %s calls", n, op)
		}
	}
	for _, id := range ids {
		if got := e.facts(corpusRoot, paths[id]); got != before[id] {
			t.Errorf("%s: facts %+v, were %+v", paths[id], got, before[id])
		}
	}
}

// A rescan reads nothing again, nor does a move by Precious; the moved
// photo's date follows its new path.
func TestMediaRescanReadsNothingAgainA(t *testing.T) {
	e, _, _ := newCorpusEnv(t)
	e.mediaA(corpusSource)
	e.scan(corpusSource)
	e.rec.Reset()
	e.mediaA(corpusSource)
	if o := e.opensA(); len(o) != 0 {
		t.Errorf("after a rescan the job opened %v", o)
	}
	const ana = "Viagens/2010-07 Bahia/do celular da Ana/IMG_0102.JPG"
	id := e.id(corpusSource, ana)
	if src := e.dateSourceA(id); src != string(media.SourceFolderName) {
		t.Fatalf("before the move: source %s", src)
	}
	e.move(corpusSource, corpusRoot, ana, "Viagens")
	e.rec.Reset()
	e.mediaA(corpusSource)
	if o := e.opensA(); len(o) != 0 {
		t.Errorf("after a move the job opened %v", o)
	}
	if s := e.metaStateA(corpusSource, "Viagens/IMG_0102.JPG"); s != "none" && s != "read" {
		t.Errorf("moved photo's metadata %q", s)
	}
	if src := e.dateSourceA(id); src != string(media.SourceMtime) {
		t.Errorf("after the move: source %s, want mtime", src)
	}
	if got, want := e.storedSummary(corpusSource), e.recounted(corpusSource); got != want {
		t.Errorf("summary %+v, recount %+v", got, want)
	}
}

// dateSourceA is the media_dates source of id.
func (e *env) dateSourceA(id domain.EntryID) string {
	e.t.Helper()
	var s string
	if err := e.st.Reader().QueryRow(`SELECT source FROM media_dates WHERE entry_id = ?`, int64(id)).Scan(&s); err != nil {
		e.t.Fatalf("date of %d: %v", id, err)
	}
	return s
}

// A file changed during its read: the result is dropped, and the photo is
// read again after the next scan.
func TestMediaFileChangedDuringItsReadA(t *testing.T) {
	e := newEnv(t)
	root := e.photoDiskA("s", 3)
	const target = "Fotos/IMG_0002.JPG"
	node := root.Child("Fotos").Child("IMG_0002.JPG")
	var once sync.Once
	e.rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpFileStat && c.FullPath() == "/mnt/s/"+target {
			once.Do(func() { node.Patch(0, []byte{0xAB}) })
		}
	})
	rt := e.mediaA("s")
	if s := e.metaStateA("s", target); s != "pending" {
		t.Errorf("changed photo's metadata %q, want pending", s)
	}
	if s := e.metaStateA("s", "Fotos/IMG_0001.JPG"); s != "read" {
		t.Errorf("other photo's metadata %q", s)
	}
	if rt.progress[progChanged] != 1 || rt.progress[progFiles] != 3 || rt.progress[progOfFiles] != 3 {
		t.Errorf("progress %v", rt.progress)
	}
	e.rec.SetBeforeCall(nil)
	e.scan("s")
	e.rec.Reset()
	e.mediaA("s")
	if s := e.metaStateA("s", target); s != "read" {
		t.Errorf("after the rescan: %q", s)
	}
	if o := e.opensA(); len(o) != 1 || o["/mnt/s/"+target] != 1 {
		t.Errorf("after the rescan the job opened %v", o)
	}
}

// A set_mtime outcome between a read and its commit drops the result; the
// carried row is read exactly once more.
func TestMediaSetMtimeBeforeTheCommitA(t *testing.T) {
	e := newEnv(t)
	e.photoDiskA("s", 3)
	const target = "Fotos/IMG_0003.JPG"
	newTime := time.Date(2009, 5, 1, 12, 0, 0, 0, time.UTC)
	var once sync.Once
	stages := e.hookA(func(ctx context.Context, job jobs.Job, stage string) error {
		if stage != stageReadCommit {
			return nil
		}
		once.Do(func() {
			dir := e.open("/mnt/s", "Fotos")
			w, _ := fsaccess.AsWriter(dir)
			err := w.SetModTime([]byte("IMG_0003.JPG"), newTime)
			dir.Close()
			if err != nil {
				t.Fatal(err)
			}
			facts := e.facts("/mnt/s", target)
			e.write(func(tx *jobs.Tx) error {
				if err := index.ApplyModTime(ctx, tx.SQL(), index.ModTime{Source: "s", Entry: e.id("s", target),
					Facts: facts}); err != nil {
					return err
				}
				return EnqueueMedia(ctx, tx, "s") // as ActionDone does
			})
		})
		return nil
	})
	e.requestA("s")
	id, err := e.runScopeA(context.Background(), mediaScope("s"), &fakeRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	if n := stages.count(id, stagePlanned); n != 2 {
		t.Errorf("the job ran its passes %d times, want 2", n)
	}
	opens := e.opensA()
	for i := 1; i <= 3; i++ {
		p := fmt.Sprintf("/mnt/s/Fotos/IMG_%04d.JPG", i)
		want := 1
		if strings.HasSuffix(p, target) {
			want = 2
		}
		if opens[p] != want {
			t.Errorf("%s opened %d times, want %d", p, opens[p], want)
		}
	}
	var state string
	var mtime int64
	if err := e.st.Reader().QueryRow(`SELECT state, mtime_ns FROM media_meta WHERE entry_id = ?`,
		int64(e.id("s", target))).Scan(&state, &mtime); err != nil {
		t.Fatal(err)
	}
	if state != "read" || mtime != newTime.UnixNano() {
		t.Errorf("metadata %s at %v", state, time.Unix(0, mtime).UTC())
	}
}

// On a FAT device whose index times differ from the disk's within the
// tolerance, every read is applied.
func TestMediaFATTimesWithinToleranceA(t *testing.T) {
	e := newEnv(t)
	fat := fsaccess.Capabilities{Known: true, NormalizationSensitive: true, LocalTime: true,
		TimeResolution: 2 * time.Second, NoReplaceRename: true}
	root, _ := corpus.BuildSynth(e.sfs, "/mnt/fat", corpus.FATFixture())
	e.addSource("fat", "/mnt/fat", root, fat)
	e.scan("fat")
	e.sfs.SetTimeZone(root.Info().Dev, time.FixedZone("summer", 3600))
	e.scan("fat")
	const photo = "DCIM/100CANON/IMG_1201.JPG"
	var indexed int64
	if err := e.st.Reader().QueryRow(`SELECT mtime_ns FROM entries WHERE id = ?`,
		int64(e.id("fat", photo))).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if disk := e.facts("/mnt/fat", photo).MtimeNs; disk == indexed {
		t.Fatalf("the disk's time equals the index's (%d): the fixture does not differ", disk)
	}
	rt := e.mediaA("fat")
	for i := range 4 {
		p := fmt.Sprintf("DCIM/100CANON/IMG_%04d.JPG", 1201+i)
		if s := e.metaStateA("fat", p); s != "read" {
			t.Errorf("%s: %q", p, s)
		}
	}
	if rt.progress[progChanged] != 0 {
		t.Errorf("progress %v", rt.progress)
	}
}

// A request while the job is ending: the follow-up runs the passes after
// it, and no file is read twice.
func TestMediaRequestWhileTheJobIsEndingA(t *testing.T) {
	e := newEnv(t)
	e.photoDiskA("s", 3)
	var once sync.Once
	stages := e.hookA(func(ctx context.Context, job jobs.Job, stage string) error {
		if stage == stageEnded {
			once.Do(func() { e.requestA("s") })
		}
		return nil
	})
	e.requestA("s")
	first, err := e.runScopeA(context.Background(), mediaScope("s"), &fakeRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	if !e.hasQueuedA(mediaNextScope("s")) {
		t.Fatal("no follow-up after a request while the job was running")
	}
	next, err := e.runScopeA(context.Background(), mediaNextScope("s"), &fakeRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	if stages.count(first, stagePlanned) != 1 || stages.count(next, stagePlanned) != 1 {
		t.Errorf("stages: first %v, follow-up %v", stages.of(first), stages.of(next))
	}
	for p, n := range e.opensA() {
		if n != 1 {
			t.Errorf("%s opened %d times", p, n)
		}
	}
	if dirty, passes := e.mediaStateA("s"); dirty || passes.Valid {
		t.Errorf("dirty %v, passes_job %v", dirty, passes)
	}
}

// A request during each of passes 1–4: the same job loops once more, and
// the follow-up then ends without running.
func TestMediaRequestDuringEachPassA(t *testing.T) {
	for _, at := range []string{stagePlanned, stageReadCommit, stageDerived, stageSnapshot} {
		t.Run(at, func(t *testing.T) {
			e := newEnv(t)
			e.photoDiskA("s", 3)
			var once sync.Once
			stages := e.hookA(func(ctx context.Context, job jobs.Job, stage string) error {
				if stage == at {
					once.Do(func() { e.requestA("s") })
				}
				return nil
			})
			e.requestA("s")
			first, err := e.runScopeA(context.Background(), mediaScope("s"), &fakeRuntime{})
			if err != nil {
				t.Fatal(err)
			}
			if n := stages.count(first, stageDetected); n != 2 {
				t.Errorf("the job ran its passes %d times, want 2: %v", n, stages.of(first))
			}
			next, err := e.runScopeA(context.Background(), mediaNextScope("s"), &fakeRuntime{})
			if err != nil {
				t.Fatal(err)
			}
			if s := stages.of(next); len(s) != 0 {
				t.Errorf("the follow-up reached %v", s)
			}
			for p, n := range e.opensA() {
				if n != 1 {
					t.Errorf("%s opened %d times", p, n)
				}
			}
		})
	}
}

// The follow-up starting while the first job holds passes_job defers,
// without using an attempt, and later ends at once.
func TestMediaFollowUpDefersWhileTheFirstRunsA(t *testing.T) {
	e := newEnv(t)
	e.photoDiskA("s", 2)
	var (
		once     sync.Once
		deferred error
		follow   domain.JobID
	)
	stages := e.hookA(func(ctx context.Context, job jobs.Job, stage string) error {
		if stage == stagePlanned {
			once.Do(func() {
				e.requestA("s")
				follow, deferred = e.runScopeA(ctx, mediaNextScope("s"), &fakeRuntime{})
			})
		}
		return nil
	})
	e.requestA("s")
	first, err := e.runScopeA(context.Background(), mediaScope("s"), &fakeRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	var d *jobs.Defer
	if !errors.As(deferred, &d) || d.Reason != DeferMediaRunning || !d.Until.Equal(testNow.Add(startDelay)) {
		t.Fatalf("the follow-up returned %v, want a deferral %q", deferred, DeferMediaRunning)
	}
	if s := stages.of(follow); len(s) != 0 {
		t.Errorf("the deferred follow-up reached %v", s)
	}
	if n := stages.count(first, stageDetected); n != 2 {
		t.Errorf("the first job ran its passes %d times, want 2", n)
	}
	if n := e.count(`SELECT attempts FROM jobs WHERE id = ?`, int64(follow)); n != 0 {
		t.Errorf("the deferral used an attempt: %d", n)
	}
	again, err := e.runScopeA(context.Background(), mediaNextScope("s"), &fakeRuntime{})
	if err != nil || again != follow {
		t.Fatalf("follow-up %d: %v", again, err)
	}
	if s := stages.of(follow); len(s) != 0 {
		t.Errorf("the follow-up ran after the first job: %v", s)
	}
}

// A request while the follow-up runs: a new media job defers until it
// ends, then runs only if dirty is set.
func TestMediaRequestWhileTheFollowUpRunsA(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("after_end_read=%v", late), func(t *testing.T) {
			e := newEnv(t)
			e.photoDiskA("s", 2)
			var (
				first, follow, fresh domain.JobID
				ended, planned       sync.Once
				deferred             error
			)
			stages := e.hookA(func(ctx context.Context, job jobs.Job, stage string) error {
				switch {
				case job.ID == first && stage == stageEnded:
					ended.Do(func() { e.requestA("s") })
				case job.ID == follow && stage == stagePlanned && !late:
					planned.Do(func() {
						e.requestA("s")
						fresh, deferred = e.runScopeA(ctx, mediaScope("s"), &fakeRuntime{})
					})
				case job.ID == follow && stage == stageEnded && late:
					planned.Do(func() { e.requestA("s") })
				}
				return nil
			})
			e.requestA("s")
			if first = e.queuedIDA(mediaScope("s")); first == 0 {
				t.Fatal("no media job")
			}
			if _, err := e.runScopeA(context.Background(), mediaScope("s"), &fakeRuntime{}); err != nil {
				t.Fatal(err)
			}
			follow = e.queuedIDA(mediaNextScope("s"))
			if _, err := e.runScopeA(context.Background(), mediaNextScope("s"), &fakeRuntime{}); err != nil {
				t.Fatal(err)
			}
			if !late {
				var d *jobs.Defer
				if !errors.As(deferred, &d) || d.Reason != DeferMediaRunning {
					t.Fatalf("the new job returned %v while the follow-up ran", deferred)
				}
				if n := stages.count(follow, stageDetected); n != 2 {
					t.Errorf("the follow-up ran its passes %d times, want 2", n)
				}
			} else {
				fresh = e.queuedIDA(mediaScope("s"))
			}
			if fresh == 0 || fresh == first {
				t.Fatalf("no new media job (first %d, new %d)", first, fresh)
			}
			if _, err := e.runScopeA(context.Background(), mediaScope("s"), &fakeRuntime{}); err != nil {
				t.Fatal(err)
			}
			ran := stages.count(fresh, stageDetected) > 0
			if ran != late {
				t.Errorf("the new job ran its passes: %v, want %v (%v)", ran, late, stages.of(fresh))
			}
		})
	}
}

// queuedIDA is the ID of the queued media job of scope, 0 when none.
func (e *env) queuedIDA(scope string) domain.JobID {
	e.t.Helper()
	var id int64
	err := e.st.Reader().QueryRow(`SELECT id FROM jobs WHERE kind = ? AND scope_key = ? AND state = 'queued'`,
		string(KindMedia), scope).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return domain.JobID(id)
}

// A job that fails with a request pending: dirty is set afterwards,
// passes_job is released, and the follow-up runs the passes; with no
// follow-up, the next request does.
func TestMediaJobThatFailsWithARequestPendingA(t *testing.T) {
	boom := errors.New("boom")
	for _, c := range []struct {
		name    string
		request bool
		cancel  bool
	}{{"fails", true, false}, {"cancelled", true, true}, {"fails without follow-up", false, false}} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.photoDiskA("s", 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var once sync.Once
			stages := e.hookA(func(_ context.Context, job jobs.Job, stage string) error {
				var err error
				if stage == stageRead {
					once.Do(func() {
						if c.request {
							e.requestA("s")
						}
						if c.cancel {
							cancel()
							return
						}
						err = boom
					})
				}
				return err
			})
			e.requestA("s")
			if _, err := e.runScopeA(ctx, mediaScope("s"), &fakeRuntime{}); err == nil {
				t.Fatal("the job did not fail")
			} else if !c.cancel && !errors.Is(err, boom) {
				t.Fatalf("the job failed with %v", err)
			}
			if dirty, passes := e.mediaStateA("s"); !dirty || passes.Valid {
				t.Errorf("after the failure: dirty %v, passes_job %v", dirty, passes)
			}
			var next domain.JobID
			if c.request {
				var err error
				if next, err = e.runScopeA(context.Background(), mediaNextScope("s"), &fakeRuntime{}); err != nil {
					t.Fatal(err)
				}
			} else {
				if e.hasQueuedA(mediaNextScope("s")) || e.hasQueuedA(mediaScope("s")) {
					t.Fatal("a media job is queued without a request")
				}
				e.requestA("s")
				var err error
				if next, err = e.runScopeA(context.Background(), mediaScope("s"), &fakeRuntime{}); err != nil {
					t.Fatal(err)
				}
			}
			if stages.count(next, stageDetected) != 1 {
				t.Errorf("the next job's stages %v", stages.of(next))
			}
			if dirty, passes := e.mediaStateA("s"); dirty || passes.Valid {
				t.Errorf("at the end: dirty %v, passes_job %v", dirty, passes)
			}
		})
	}
}

// A retry (attempt 2) with dirty clear runs the passes; a first attempt
// with dirty clear runs nothing.
func TestMediaRetryIgnoresIfDirtyA(t *testing.T) {
	e := newEnv(t)
	e.photoDiskA("s", 2)
	e.exec(`INSERT INTO media_sources (source_id, dirty) VALUES ('s', 0)`)
	stages := e.hookA(nil)
	h := &handler{s: e.svc}
	if err := e.runJob(context.Background(), KindMedia, "s", `{"if_dirty":true}`, 1, h, &fakeRuntime{}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.opensA()); n != 0 || len(stages.byJob) != 0 {
		t.Errorf("a first attempt with dirty clear opened %d files, stages %v", n, stages.byJob)
	}
	if err := e.runJob(context.Background(), KindMedia, "s", `{"if_dirty":true}`, 2, h, &fakeRuntime{}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.opensA()); n != 2 {
		t.Errorf("a retry opened %d files, want 2", n)
	}
	if dirty, passes := e.mediaStateA("s"); dirty || passes.Valid {
		t.Errorf("dirty %v, passes_job %v", dirty, passes)
	}
}

// The job defers while an organize job of its source is queued.
func TestMediaDefersWhileOrganizingA(t *testing.T) {
	e := newEnv(t)
	e.photoDiskA("s", 2)
	e.svc.DeferWhile(executor.OrganizeActive)
	var org int64
	if err := e.st.Writer().QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
		max_attempts, available_at, created_at, updated_at) VALUES (?, 1, '{}', 's', 'queued', 0, 3, 0, 0, 0)
		RETURNING id`, string(executor.KindOrganize)).Scan(&org); err != nil {
		t.Fatal(err)
	}
	e.requestA("s")
	_, err := e.runScopeA(context.Background(), mediaScope("s"), &fakeRuntime{})
	var d *jobs.Defer
	if !errors.As(err, &d) || d.Reason != index.DeferOrganizing || !d.Until.Equal(testNow.Add(startDelay)) {
		t.Fatalf("the job returned %v, want a deferral %q", err, index.DeferOrganizing)
	}
	if dirty, passes := e.mediaStateA("s"); !dirty || passes.Valid {
		t.Errorf("while deferred: dirty %v, passes_job %v", dirty, passes)
	}
	if n := len(e.opensA()); n != 0 {
		t.Errorf("a deferred job opened %d files", n)
	}
	e.exec(`UPDATE jobs SET state = 'succeeded' WHERE id = ?`, org)
	if _, err := e.runScopeA(context.Background(), mediaScope("s"), &fakeRuntime{}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.opensA()); n != 2 {
		t.Errorf("after organizing the job opened %d files", n)
	}
}

// Quarantined photos are never enrolled or opened.
func TestMediaSkipsTheQuarantineA(t *testing.T) {
	e := newEnv(t)
	mtime := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	e.disk("s", "/mnt/s", posix, func(root *synthfs.Node) {
		root.Dir("Fotos").File("IMG_0001.JPG", 4096, mtime).Seed(1)
		root.Dir(index.QuarantineName).Dir("1").File("IMG_0002.JPG", 4096, mtime).Seed(2)
	})
	e.mediaA("s")
	q := index.QuarantineName + "/1/IMG_0002.JPG"
	if s := e.metaStateA("s", q); s != "" {
		t.Errorf("quarantined photo's metadata %q", s)
	}
	if n := e.count(`SELECT count(*) FROM media_dates WHERE entry_id = ?`, int64(e.id("s", q))); n != 0 {
		t.Errorf("quarantined photo has a date")
	}
	if o := e.opensA(); len(o) != 1 || o["/mnt/s/Fotos/IMG_0001.JPG"] != 1 {
		t.Errorf("opened %v", o)
	}
}

// sonyIDsA are the corpus's Sony photos.
func (e *env) sonyIDsA() []domain.EntryID {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT entry_id FROM media_meta WHERE make = 'SONY' ORDER BY entry_id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []domain.EntryID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, domain.EntryID(id))
	}
	return out
}

// shiftA records a shift correction of ids, re-derives them, and requests
// the job in one transaction, as set-date-correction does.
func (e *env) shiftA(src domain.SourceID, shift int64, ids []domain.EntryID) {
	e.t.Helper()
	e.write(func(tx *jobs.Tx) error {
		for _, id := range ids {
			if _, err := tx.SQL().Exec(`INSERT INTO date_corrections (entry_id, kind, shift_s, batch_id, created_at)
				VALUES (?, 'shift', ?, 'test', 0)`, int64(id), shift); err != nil {
				return err
			}
		}
		if err := e.svc.Rederive(context.Background(), tx.SQL(), ids); err != nil {
			return err
		}
		return EnqueueMedia(context.Background(), tx, src)
	})
}

// cameraStateA is the media_cameras state of key in src, "" without a row.
func (e *env) cameraStateA(src domain.SourceID, key string) string {
	e.t.Helper()
	var s string
	err := e.st.Reader().QueryRow(`SELECT state FROM media_cameras WHERE source_id = ? AND camera_key = ?`,
		string(src), key).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

const sonyKeyA = "SONY|DSC-W55|"

// A correction committed during the cameras pass: the pass still writes;
// the loop runs again and its write leaves the corrected photos unflagged.
func TestMediaCorrectionDuringTheCamerasPassA(t *testing.T) {
	e, _, _ := newCorpusEnv(t)
	var (
		snapshot, detected sync.Once
		firstWrite         string
	)
	e.hookA(func(ctx context.Context, job jobs.Job, stage string) error {
		switch stage {
		case stageSnapshot:
			snapshot.Do(func() { e.shiftA(corpusSource, 31_546_800, e.sonyIDsA()) })
		case stageDetected:
			detected.Do(func() { firstWrite = e.cameraStateA(corpusSource, sonyKeyA) })
		}
		return nil
	})
	e.mediaA(corpusSource)
	if firstWrite != media.CameraOffset {
		t.Errorf("the pass during the correction wrote the Sony as %q, want offset", firstWrite)
	}
	if s := e.cameraStateA(corpusSource, sonyKeyA); s != media.CameraOK {
		t.Errorf("the Sony is %q after the loop ran again", s)
	}
	if n := e.count(`SELECT count(*) FROM media_dates WHERE source_id = ? AND flags & 4 <> 0`,
		string(corpusSource)); n != 0 {
		t.Errorf("%d photos still flagged camera_offset", n)
	}
	if got, want := e.storedSummary(corpusSource), e.recounted(corpusSource); got != want {
		t.Errorf("summary %+v, recount %+v", got, want)
	}
}

// On an offline source, a correction's request recomputes the cameras
// without opening anything.
func TestMediaOfflineSourceRecomputesTheCamerasA(t *testing.T) {
	e, _, _ := newCorpusEnv(t)
	e.mediaA(corpusSource)
	if s := e.cameraStateA(corpusSource, sonyKeyA); s != media.CameraOffset {
		t.Fatalf("the Sony is %q", s)
	}
	e.sfs.Vanish(corpusRoot)
	e.clk.set(testNow.Add(time.Hour))
	e.shiftA(corpusSource, 31_546_800, e.sonyIDsA())
	e.rec.Reset()
	e.mediaA(corpusSource)
	for _, op := range []instrument.Op{instrument.OpOpenFile, instrument.OpOpenDir, instrument.OpReadAt} {
		if n := e.rec.Count(op); n != 0 {
			t.Errorf("%d %s calls on an offline source", n, op)
		}
	}
	if s := e.cameraStateA(corpusSource, sonyKeyA); s != media.CameraOK {
		t.Errorf("the Sony is %q after the correction", s)
	}
	var detected int64
	if err := e.st.Reader().QueryRow(`SELECT detected_at FROM media_sources WHERE source_id = ?`,
		string(corpusSource)).Scan(&detected); err != nil {
		t.Fatal(err)
	}
	if want := testNow.Add(time.Hour).UnixMilli(); detected != want {
		t.Errorf("detected_at %d, want %d", detected, want)
	}
}
