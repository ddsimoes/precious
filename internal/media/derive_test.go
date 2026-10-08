package media

import (
	"testing"
	"time"
)

func tp(y int, mo time.Month, d, h, mi, s int) *time.Time {
	t := time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	return &t
}

// in is a read photo at path p, modified at mtime, in zone UTC.
func in(p string, mtime *time.Time, m *Meta) Inputs {
	i := Inputs{Path: []byte(p), Mtime: mtime, MetaState: MetaRead, Meta: m, Zone: time.UTC, Now: now}
	if m == nil {
		i.MetaState = MetaNone
	}
	return i
}

type wantEff struct {
	source  Source
	conf    Confidence
	local   string
	instant time.Time
	prec    Precision
	refined bool
	corr    string
	flags   Flags
}

func checkEff(t *testing.T, name string, got Effective, w wantEff) {
	t.Helper()
	if got.Source != w.source || got.Confidence != w.conf || got.Refined != w.refined || got.Corrected != w.corr || got.Flags != w.flags {
		t.Errorf("%s: source %s conf %s refined %v corrected %q flags %d; want %s %s %v %q %d", name,
			got.Source, got.Confidence, got.Refined, got.Corrected, got.Flags, w.source, w.conf, w.refined, w.corr, w.flags)
	}
	if w.source == SourceNone {
		if got.Date != nil {
			t.Errorf("%s: date %+v, want none", name, *got.Date)
		}
		return
	}
	if got.Date == nil {
		t.Errorf("%s: no date", name)
		return
	}
	if got.Date.Local != w.local || got.Date.Precision != w.prec || (!w.instant.IsZero() && !got.Date.Instant.Equal(w.instant)) {
		t.Errorf("%s: date %s %s %v; want %s %s %v", name, got.Date.Local, got.Date.Precision, got.Date.Instant, w.local, w.prec, w.instant)
	}
}

// TestDeriveSources covers every row of D5's table, in precedence order:
// each source wins once the ones above it are taken away.
func TestDeriveSources(t *testing.T) {
	gps := tp(2010, 7, 17, 13, 0, 5)
	cont := tp(2010, 7, 17, 13, 0, 9)
	mtime := tp(2010, 7, 20, 8, 0, 0)
	full := func() *Meta {
		return &Meta{CaptureLocal: "2010-07-17T10:00:00.500", CaptureOffsetMin: intp(-180), GPS: gps, Container: cont, Make: "Canon"}
	}
	const p = "Viagens/2010-07 Bahia/IMG_20100717_095000.jpg"

	checkEff(t, "exif with offset", Derive(in(p, mtime, full())), wantEff{source: SourceEXIF, conf: ConfidenceHigh,
		local: "2010-07-17T10:00:00", instant: time.Date(2010, 7, 17, 13, 0, 0, 5e8, time.UTC), prec: PrecisionSecond, flags: FlagMtimeDisagrees})
	m := full()
	m.CaptureOffsetMin = nil
	i := in(p, mtime, m)
	i.Zone = recif
	e := Derive(i)
	checkEff(t, "exif without offset", e, wantEff{source: SourceEXIF, conf: ConfidenceMedium,
		local: "2010-07-17T10:00:00", instant: time.Date(2010, 7, 17, 13, 0, 0, 5e8, time.UTC), prec: PrecisionSecond, flags: FlagMtimeDisagrees})
	if e.Date.OffsetMin != nil {
		t.Errorf("a floating capture has offset %d", *e.Date.OffsetMin)
	}
	m.CaptureLocal = ""
	checkEff(t, "gps", Derive(in(p, mtime, m)), wantEff{source: SourceGPS, conf: ConfidenceHigh,
		local: "2010-07-17T13:00:05", instant: *gps, prec: PrecisionSecond, flags: FlagMtimeDisagrees})
	m.GPS = nil
	checkEff(t, "container", Derive(in(p, mtime, m)), wantEff{source: SourceContainer, conf: ConfidenceMedium,
		local: "2010-07-17T13:00:09", instant: *cont, prec: PrecisionSecond, flags: FlagMtimeDisagrees})
	m.Container = nil
	checkEff(t, "file name", Derive(in(p, mtime, m)), wantEff{source: SourceFileName, conf: ConfidenceLow,
		local: "2010-07-17T09:50:00", prec: PrecisionSecond, flags: FlagNoDateMetadata})
	const q = "Viagens/2010-07 Bahia/sub/foto.jpg"
	checkEff(t, "folder name, refined", Derive(in(q, mtime, m)), wantEff{source: SourceFolderName, conf: ConfidenceMedium,
		local: "2010-07-20T08:00:00", instant: *mtime, prec: PrecisionSecond, refined: true, flags: FlagNoDateMetadata})
	checkEff(t, "folder name", Derive(in(q, tp(2011, 1, 1, 0, 0, 0), m)), wantEff{source: SourceFolderName, conf: ConfidenceLow,
		local: "2010-07", instant: time.Date(2010, 7, 1, 0, 0, 0, 0, time.UTC), prec: PrecisionMonth, flags: FlagNoDateMetadata})
	checkEff(t, "mtime", Derive(in("Fotos/foto.jpg", mtime, m)), wantEff{source: SourceMtime, conf: ConfidenceLowest,
		local: "2010-07-20T08:00:00", instant: *mtime, prec: PrecisionSecond, flags: FlagNoDateMetadata})
	checkEff(t, "none", Derive(in("Fotos/foto.jpg", nil, m)), wantEff{source: SourceNone, conf: ConfidenceNone,
		flags: FlagNoDateMetadata})

	// The owner's date wins over everything.
	i = in(p, mtime, full())
	i.Correction = &Correction{Kind: CorrectionSet, SetLocal: "2010-07-18"}
	checkEff(t, "owner set", Derive(i), wantEff{source: SourceOwner, conf: ConfidenceHigh, local: "2010-07-18",
		instant: time.Date(2010, 7, 18, 0, 0, 0, 0, time.UTC), prec: PrecisionDay, corr: CorrectionSet, flags: FlagMtimeDisagrees})
	i.Correction = &Correction{Kind: CorrectionShift, ShiftS: 3600}
	checkEff(t, "owner shift", Derive(i), wantEff{source: SourceOwner, conf: ConfidenceMedium, local: "2010-07-17T11:00:00",
		instant: time.Date(2010, 7, 17, 14, 0, 0, 5e8, time.UTC), prec: PrecisionSecond, corr: CorrectionShift, flags: FlagMtimeDisagrees})

	// The candidates list every source found, the owner's first.
	got := Derive(i).Candidates
	want := []Source{SourceOwner, SourceEXIF, SourceGPS, SourceContainer, SourceFileName, SourceFolderName, SourceMtime}
	if len(got) != len(want) {
		t.Fatalf("candidates %+v", got)
	}
	for k, c := range got {
		if c.Source != want[k] || !c.Plausible {
			t.Errorf("candidate %d: %s plausible=%v, want %s", k, c.Source, c.Plausible, want[k])
		}
	}
}

func TestDeriveImplausible(t *testing.T) {
	mtime := tp(2011, 1, 15, 10, 0, 0)
	gps := tp(2008, 3, 22, 17, 0, 0)
	const p = "Viagens/2008-03 Ouro Preto/DSCN0004.JPG"
	for _, c := range []struct {
		name string
		m    Meta
		want wantEff
	}{
		{"EXIF default 2000", Meta{CaptureLocal: "2000-01-01T00:00:00"},
			wantEff{source: SourceFolderName, conf: ConfidenceLow, local: "2008-03", prec: PrecisionMonth, flags: FlagImplausible}},
		{"EXIF default 2001 with offset", Meta{CaptureLocal: "2001-01-01T00:00:00", CaptureOffsetMin: intp(-180)},
			wantEff{source: SourceFolderName, conf: ConfidenceLow, local: "2008-03", prec: PrecisionMonth, flags: FlagImplausible}},
		{"EXIF default, GPS next", Meta{CaptureLocal: "1980-01-01T00:00:00", GPS: gps},
			wantEff{source: SourceGPS, conf: ConfidenceHigh, local: "2008-03-22T17:00:00", prec: PrecisionSecond, flags: FlagImplausible | FlagMtimeDisagrees}},
		{"EXIF before 1990", Meta{CaptureLocal: "1989-12-31T23:59:59"},
			wantEff{source: SourceFolderName, conf: ConfidenceLow, local: "2008-03", prec: PrecisionMonth, flags: FlagImplausible}},
		{"EXIF in the future", Meta{CaptureLocal: "2026-10-09T12:00:01"},
			wantEff{source: SourceFolderName, conf: ConfidenceLow, local: "2008-03", prec: PrecisionMonth, flags: FlagImplausible}},
		{"EXIF tomorrow is plausible", Meta{CaptureLocal: "2026-10-09T12:00:00"},
			wantEff{source: SourceEXIF, conf: ConfidenceMedium, local: "2026-10-09T12:00:00", prec: PrecisionSecond, flags: FlagMtimeDisagrees}},
		{"EXIF one second after a default", Meta{CaptureLocal: "2000-01-01T00:00:01"},
			wantEff{source: SourceEXIF, conf: ConfidenceMedium, local: "2000-01-01T00:00:01", prec: PrecisionSecond, flags: FlagMtimeDisagrees}},
		{"GPS default", Meta{GPS: tp(1980, 1, 1, 0, 0, 0)},
			wantEff{source: SourceFolderName, conf: ConfidenceLow, local: "2008-03", prec: PrecisionMonth, flags: FlagImplausible}},
		{"container default", Meta{Container: tp(2001, 1, 1, 0, 0, 0)},
			wantEff{source: SourceFolderName, conf: ConfidenceLow, local: "2008-03", prec: PrecisionMonth, flags: FlagImplausible}},
	} {
		m := c.m
		checkEff(t, c.name, Derive(in(p, mtime, &m)), c.want)
	}
	// A 1980 modification time is never implausible-flagged, and is the
	// last resort.
	e := Derive(in("Fotos/foto.jpg", tp(1980, 1, 1, 0, 0, 0), &Meta{Make: "X"}))
	checkEff(t, "1980 mtime", e, wantEff{source: SourceMtime, conf: ConfidenceLowest, local: "1980-01-01T00:00:00",
		prec: PrecisionSecond, flags: FlagNoDateMetadata})
	if len(e.Candidates) != 1 || e.Candidates[0].Plausible {
		t.Errorf("1980 mtime candidates %+v", e.Candidates)
	}
	// An implausible name date is skipped without a flag.
	checkEff(t, "1985 name", Derive(in("IMG_19850101_120000.jpg", mtime, nil)), wantEff{source: SourceMtime,
		conf: ConfidenceLowest, local: "2011-01-15T10:00:00", prec: PrecisionSecond, flags: FlagNoDateMetadata})
}

func TestDeriveRefinement(t *testing.T) {
	const p = "celular/WhatsApp Images/IMG-20110416-WA0003.jpg"
	day := wantEff{source: SourceFileName, conf: ConfidenceLow, local: "2011-04-16", prec: PrecisionDay, flags: FlagNoDateMetadata}
	refined := func(m *time.Time) wantEff {
		return wantEff{source: SourceFileName, conf: ConfidenceMedium, local: m.Format(localSecond), instant: *m,
			prec: PrecisionSecond, refined: true, flags: FlagNoDateMetadata}
	}
	for _, c := range []struct {
		name  string
		mtime *time.Time
		local bool
		want  wantEff
	}{
		{"copied a year later", tp(2012, 2, 1, 9, 0, 0), false, day},
		{"on its day", tp(2011, 4, 16, 18, 30, 0), false, refined(tp(2011, 4, 16, 18, 30, 0))},
		{"at its first second", tp(2011, 4, 16, 0, 0, 0), false, refined(tp(2011, 4, 16, 0, 0, 0))},
		{"30 minutes after", tp(2011, 4, 17, 0, 30, 0), false, day},
		{"30 minutes after, local time", tp(2011, 4, 17, 0, 30, 0), true, refined(tp(2011, 4, 17, 0, 30, 0))},
		{"30 minutes before, local time", tp(2011, 4, 15, 23, 30, 0), true, refined(tp(2011, 4, 15, 23, 30, 0))},
		{"2 hours after, local time", tp(2011, 4, 17, 2, 0, 0), true, day},
		{"unknown time", nil, false, wantEff{source: SourceFileName, conf: ConfidenceLow, local: "2011-04-16", prec: PrecisionDay, flags: FlagNoDateMetadata}},
	} {
		i := in(p, c.mtime, nil)
		i.Caps.LocalTime = c.local
		checkEff(t, c.name, Derive(i), c.want)
	}
	// A second-precision name is never refined.
	checkEff(t, "second-precision name", Derive(in("IMG_20110416_101010.jpg", tp(2011, 4, 16, 18, 0, 0), nil)),
		wantEff{source: SourceFileName, conf: ConfidenceLow, local: "2011-04-16T10:10:10", prec: PrecisionSecond, flags: FlagNoDateMetadata})
	// "A folder year that disagrees with the modification time".
	checkEff(t, "folder year", Derive(in("Fotos/2006/Praia/foto.jpg", tp(2008, 5, 5, 5, 5, 5), nil)),
		wantEff{source: SourceFolderName, conf: ConfidenceLow, local: "2006", prec: PrecisionYear, flags: FlagNoDateMetadata})
	// The nearest dated folder wins, and the top folder's own name counts
	// only below the source's top.
	checkEff(t, "nearest folder", Derive(in("2006/2007-05/x/foto.jpg", tp(2008, 5, 5, 5, 5, 5), nil)),
		wantEff{source: SourceFolderName, conf: ConfidenceLow, local: "2007-05", prec: PrecisionMonth, flags: FlagNoDateMetadata})
}

func TestDeriveCorrections(t *testing.T) {
	mtime := tp(2012, 2, 1, 9, 0, 0)
	exif := &Meta{CaptureLocal: "2009-07-17T07:00:00", Make: "SONY", Model: "DSC-W55"}
	const p = "Viagens/2010-07 Bahia/IMG-20100717-WA0001.jpg"
	with := func(m *Meta, c Correction) Inputs {
		i := in(p, mtime, m)
		i.Correction = &c
		return i
	}
	checkEff(t, "use_name", Derive(with(exif, Correction{Kind: CorrectionUseName})), wantEff{source: SourceFileName,
		conf: ConfidenceLow, local: "2010-07-17", prec: PrecisionDay, corr: CorrectionUseName})
	checkEff(t, "use_folder", Derive(with(exif, Correction{Kind: CorrectionUseFolder})), wantEff{source: SourceFolderName,
		conf: ConfidenceLow, local: "2010-07", prec: PrecisionMonth, corr: CorrectionUseFolder})
	i := with(exif, Correction{Kind: CorrectionUseFolder})
	i.Path = []byte("Fotos/foto.jpg")
	checkEff(t, "use_folder without a folder date", Derive(i), wantEff{source: SourceEXIF, conf: ConfidenceMedium,
		local: "2009-07-17T07:00:00", prec: PrecisionSecond, flags: FlagMtimeDisagrees})
	i = with(nil, Correction{Kind: CorrectionUseName})
	i.Mtime = tp(2010, 7, 17, 15, 0, 0)
	checkEff(t, "use_name refined", Derive(i), wantEff{source: SourceFileName, conf: ConfidenceMedium,
		local: "2010-07-17T15:00:00", prec: PrecisionSecond, refined: true, corr: CorrectionUseName, flags: FlagNoDateMetadata})

	// A shift moves the EXIF capture: the Sony's +1 year 3 hours.
	checkEff(t, "shift", Derive(with(exif, Correction{Kind: CorrectionShift, ShiftS: 31546800})), wantEff{source: SourceOwner,
		conf: ConfidenceMedium, local: "2010-07-17T10:00:00", instant: time.Date(2010, 7, 17, 10, 0, 0, 0, time.UTC),
		prec: PrecisionSecond, corr: CorrectionShift, flags: FlagMtimeDisagrees})
	// It moves an implausible capture too, across 1990 either way.
	old := &Meta{CaptureLocal: "1989-12-31T12:00:00"}
	checkEff(t, "shift into 1990", Derive(with(old, Correction{Kind: CorrectionShift, ShiftS: 86400})), wantEff{source: SourceOwner,
		conf: ConfidenceMedium, local: "1990-01-01T12:00:00", prec: PrecisionSecond, corr: CorrectionShift,
		flags: FlagImplausible | FlagMtimeDisagrees})
	early := &Meta{CaptureLocal: "1990-01-01T12:00:00"}
	checkEff(t, "shift out of 1990", Derive(with(early, Correction{Kind: CorrectionShift, ShiftS: -86400})), wantEff{source: SourceOwner,
		conf: ConfidenceMedium, local: "1989-12-31T12:00:00", prec: PrecisionSecond, corr: CorrectionShift, flags: FlagMtimeDisagrees})
	// Without a capture, the effective date is shifted, at its precision.
	i = with(nil, Correction{Kind: CorrectionShift, ShiftS: 365 * 86400})
	i.Path = []byte("Fotos/2006/Praia/foto.jpg")
	checkEff(t, "shift of a folder year", Derive(i), wantEff{source: SourceOwner, conf: ConfidenceMedium, local: "2007",
		instant: time.Date(2007, 1, 1, 0, 0, 0, 0, time.UTC), prec: PrecisionYear, corr: CorrectionShift,
		flags: FlagNoDateMetadata | FlagMtimeDisagrees})
	i = with(nil, Correction{Kind: CorrectionShift, ShiftS: 60})
	i.Path, i.Mtime = []byte("Fotos/foto.jpg"), nil
	checkEff(t, "shift of nothing", Derive(i), wantEff{source: SourceNone, conf: ConfidenceNone, flags: FlagNoDateMetadata})

	// Set: any precision, any age, never skipped.
	for _, c := range []struct {
		local string
		off   *int
		prec  Precision
		inst  time.Time
	}{
		{"1978", nil, PrecisionYear, time.Date(1978, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"1978-06", nil, PrecisionMonth, time.Date(1978, 6, 1, 0, 0, 0, 0, time.UTC)},
		{"1978-06-15", nil, PrecisionDay, time.Date(1978, 6, 15, 0, 0, 0, 0, time.UTC)},
		{"2000-01-01T00:00:00", nil, PrecisionSecond, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"1978-06-15T10:00:00", intp(-180), PrecisionSecond, time.Date(1978, 6, 15, 13, 0, 0, 0, time.UTC)},
		{"2030-01-01", nil, PrecisionDay, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		e := Derive(with(exif, Correction{Kind: CorrectionSet, SetLocal: c.local, SetOffsetMin: c.off}))
		checkEff(t, "set "+c.local, e, wantEff{source: SourceOwner, conf: ConfidenceHigh, local: c.local, instant: c.inst,
			prec: c.prec, corr: CorrectionSet, flags: FlagMtimeDisagrees})
		if (c.off == nil) != (e.Date.OffsetMin == nil) {
			t.Errorf("set %s: offset %v", c.local, e.Date.OffsetMin)
		}
	}
	// A set date containing the modification time does not disagree.
	i = with(exif, Correction{Kind: CorrectionSet, SetLocal: "2012-02"})
	if e := Derive(i); e.Flags&FlagMtimeDisagrees != 0 {
		t.Errorf("a month containing the mtime disagrees: %d", e.Flags)
	}
}

func TestDeriveFlags(t *testing.T) {
	mtime := tp(2008, 3, 22, 14, 0, 0)
	exif := func(local string) *Meta { return &Meta{CaptureLocal: local, Make: "NIKON"} }
	for _, c := range []struct {
		name  string
		state MetaState
		m     *Meta
		local bool
		want  Flags
	}{
		{"read, no date", MetaRead, &Meta{Make: "NIKON"}, false, FlagNoDateMetadata},
		{"read, nothing", MetaRead, &Meta{}, false, FlagNoDateMetadata},
		{"none", MetaNone, nil, false, FlagNoDateMetadata},
		{"pending", MetaPending, nil, false, 0},
		{"unreadable", MetaUnreadable, nil, false, 0},
		{"pending with stray meta", MetaPending, exif("2008-03-22T14:00:00"), false, 0},
		{"read, implausible date found", MetaRead, exif("2000-01-01T00:00:00"), false, FlagImplausible},
		{"23 hours apart", MetaRead, exif("2008-03-21T15:00:00"), false, 0},
		{"24.5 hours apart", MetaRead, exif("2008-03-21T13:30:00"), false, FlagMtimeDisagrees},
		{"24.5 hours apart, local time", MetaRead, exif("2008-03-21T13:30:00"), true, 0},
		{"25.5 hours apart, local time", MetaRead, exif("2008-03-21T12:30:00"), true, FlagMtimeDisagrees},
	} {
		i := in("Fotos/foto.jpg", mtime, c.m)
		i.MetaState = c.state
		i.Caps.LocalTime = c.local
		if got := Derive(i).Flags; got != c.want {
			t.Errorf("%s: flags %d, want %d", c.name, got, c.want)
		}
	}
	// A name or folder date far from the modification time never disagrees.
	if f := Derive(in("2001/IMG-20010101-WA0001.jpg", tp(2012, 1, 1, 0, 0, 0), nil)).Flags; f != FlagNoDateMetadata {
		t.Errorf("name date flags %d", f)
	}
	// Derive never sets the camera bit.
	for _, m := range []*Meta{nil, exif("2008-03-22T14:00:00")} {
		if Derive(in("a.jpg", mtime, m)).Flags&FlagCameraOffset != 0 {
			t.Error("Derive set camera_offset")
		}
	}
}

func TestInputsKey(t *testing.T) {
	base := func() Inputs {
		i := in("Viagens/a.jpg", tp(2010, 1, 1, 0, 0, 0), &Meta{CaptureLocal: "2010-01-01T00:00:00", CaptureOffsetMin: intp(60),
			GPS: tp(2010, 1, 1, 0, 0, 1), Container: tp(2010, 1, 1, 0, 0, 2), Make: "a", Model: "b", Serial: "c"})
		i.Correction = &Correction{Kind: CorrectionSet, SetLocal: "2010-01-01T00:00:00", SetOffsetMin: intp(0), ShiftS: 0}
		i.Caps.Resolution = time.Nanosecond
		return i
	}
	k := InputsKey(base())
	same := base()
	same.Now = now.Add(1000 * time.Hour)
	if InputsKey(same) != k {
		t.Error("Now changed the key")
	}
	for name, mut := range map[string]func(*Inputs){
		"path":            func(i *Inputs) { i.Path = []byte("Viagens/b.jpg") },
		"mtime":           func(i *Inputs) { i.Mtime = tp(2010, 1, 1, 0, 0, 1) },
		"mtime unknown":   func(i *Inputs) { i.Mtime = nil },
		"local time":      func(i *Inputs) { i.Caps.LocalTime = true },
		"resolution":      func(i *Inputs) { i.Caps.Resolution = 2 * time.Second },
		"state":           func(i *Inputs) { i.MetaState = MetaUnreadable },
		"capture":         func(i *Inputs) { i.Meta.CaptureLocal = "2010-01-01T00:00:01" },
		"capture offset":  func(i *Inputs) { i.Meta.CaptureOffsetMin = nil },
		"gps":             func(i *Inputs) { i.Meta.GPS = nil },
		"container":       func(i *Inputs) { i.Meta.Container = tp(2011, 1, 1, 0, 0, 0) },
		"make":            func(i *Inputs) { i.Meta.Make = "" },
		"model":           func(i *Inputs) { i.Meta.Model = "B" },
		"serial":          func(i *Inputs) { i.Meta.Serial = "C" },
		"no meta":         func(i *Inputs) { i.Meta = nil },
		"correction":      func(i *Inputs) { i.Correction = nil },
		"correction kind": func(i *Inputs) { i.Correction.Kind = CorrectionShift },
		"set local":       func(i *Inputs) { i.Correction.SetLocal = "2010" },
		"set offset":      func(i *Inputs) { i.Correction.SetOffsetMin = intp(60) },
		"shift":           func(i *Inputs) { i.Correction.ShiftS = 1 },
		"zone":            func(i *Inputs) { i.Zone = recif },
		"field boundary":  func(i *Inputs) { i.Meta.Make, i.Meta.Model = "ab", "" },
	} {
		i := base()
		mut(&i)
		if InputsKey(i) == k {
			t.Errorf("changing %s kept the key", name)
		}
	}
}

func TestZoneKey(t *testing.T) {
	a, b := time.FixedZone("Local", 0), time.FixedZone("Local", -3*3600)
	if a.String() != b.String() || ZoneKey(a) == ZoneKey(b) {
		t.Errorf("two zones named Local share a key")
	}
	if ZoneKey(time.FixedZone("Local", 0)) != ZoneKey(a) {
		t.Error("one zone, two keys")
	}
	if ZoneKey(nil) != ZoneKey(time.UTC) {
		t.Error("nil is not UTC")
	}
	if ny, err := time.LoadLocation("America/New_York"); err == nil {
		// Same name and January offset, another July offset (no DST).
		if ZoneKey(ny) == ZoneKey(time.FixedZone("America/New_York", -5*3600)) {
			t.Error("daylight saving does not change the key")
		}
	}
}

func TestCameraKey(t *testing.T) {
	for _, c := range []struct{ make, model, serial, want string }{
		{"SONY", "DSC-W55", "", "SONY|DSC-W55|"},
		{"Canon\x00", " Canon PowerShot SX230 HS ", "", "Canon|Canon PowerShot SX230 HS|"},
		{"NIKON", "COOLPIX P5000", "3012345", "NIKON|COOLPIX P5000|3012345"},
		{"A|B", "C", "D|", "A/B|C|D/"},
		{"", "", "", ""},
		{" \x00", "", "", ""},
		{"", "", "123", "||123"},
	} {
		if got := CameraKey(c.make, c.model, c.serial); got != c.want {
			t.Errorf("CameraKey(%q, %q, %q) = %q, want %q", c.make, c.model, c.serial, got, c.want)
		}
	}
}
