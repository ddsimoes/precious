package media

import (
	"errors"
	"testing"
	"time"
	_ "time/tzdata" // America/Sao_Paulo wherever the tests run
)

var (
	utc   = time.UTC
	recif = time.FixedZone("BRT", -3*3600)
	now   = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
)

func intp(v int) *int { return &v }

func TestNameDate(t *testing.T) {
	for _, c := range []struct {
		name    string
		instant time.Time
		local   string
		prec    Precision
	}{
		{"IMG_20150312_143000.jpg", time.Date(2015, 3, 12, 17, 30, 0, 0, utc), "2015-03-12T14:30:00", PrecisionSecond},
		{"img_20150312_143000_1.JPG", time.Date(2015, 3, 12, 17, 30, 0, 0, utc), "2015-03-12T14:30:00", PrecisionSecond},
		{"VID_20110423_101500.mp4", time.Date(2011, 4, 23, 13, 15, 0, 0, utc), "2011-04-23T10:15:00", PrecisionSecond},
		{"PXL_20210704_183015123.jpg", time.Date(2021, 7, 4, 18, 30, 15, 123e6, utc), "2021-07-04T15:30:15", PrecisionSecond},
		{"20150312_143000.jpg", time.Date(2015, 3, 12, 17, 30, 0, 0, utc), "2015-03-12T14:30:00", PrecisionSecond},
		{"2015-03-12 14.30.00.jpg", time.Date(2015, 3, 12, 17, 30, 0, 0, utc), "2015-03-12T14:30:00", PrecisionSecond},
		{"Screenshot_2011-05-02-21-14-07.png", time.Date(2011, 5, 3, 0, 14, 7, 0, utc), "2011-05-02T21:14:07", PrecisionSecond},
		{"screenshot_20110502-211407.png", time.Date(2011, 5, 3, 0, 14, 7, 0, utc), "2011-05-02T21:14:07", PrecisionSecond},
		{"Screenshot_2011-05-02.png", time.Date(2011, 5, 2, 3, 0, 0, 0, utc), "2011-05-02", PrecisionDay},
		{"Screenshot_2011-05-02-2114.png", time.Date(2011, 5, 2, 3, 0, 0, 0, utc), "2011-05-02", PrecisionDay},
		{"IMG-20110416-WA0003.jpg", time.Date(2011, 4, 16, 3, 0, 0, 0, utc), "2011-04-16", PrecisionDay},
		{"VID-20110416-WA0001.mp4", time.Date(2011, 4, 16, 3, 0, 0, 0, utc), "2011-04-16", PrecisionDay},
		{"img-20090612-wa0001.jpg", time.Date(2009, 6, 12, 3, 0, 0, 0, utc), "2009-06-12", PrecisionDay},
	} {
		d, ok := NameDate([]byte(c.name), recif)
		if !ok || !d.Instant.Equal(c.instant) || d.Local != c.local || d.Precision != c.prec || d.OffsetMin != nil {
			t.Errorf("NameDate(%q) = %+v %v, want %v %s %s", c.name, d, ok, c.instant, c.local, c.prec)
		}
	}
	for _, name := range []string{
		"IMG_20151399_120000.jpg",  // no 13th month
		"IMG_20150230_120000.jpg",  // no February 30th
		"IMG_20150312_250000.jpg",  // no 25th hour
		"IMG_20150312_1430001.jpg", // a digit too many
		"IMG_2015031_143000.jpg",
		"IMG_0101.JPG", "DSC00301.JPG", "DSCN0001.JPG", "foto.jpg",
		"IMG-20110416.jpg", "IMG-20110416-XX0003.jpg", "IMG-20111316-WA0003.jpg",
		"fotos_2005_do_pendrive.jpg", "celular_backup_2009.jpg",
		"Screenshot_2011-13-02.png", "Screenshot_x.png",
		"PXL_20210704_183015.jpg",
		"x IMG_20150312_143000.jpg", "2015-03-12.jpg", "2015.jpg",
		"\xffIMG_20150312_143000.jpg",
	} {
		if d, ok := NameDate([]byte(name), utc); ok {
			t.Errorf("NameDate(%q) = %+v, want none", name, d)
		}
	}
}

// r5 review, Addendum G7: in America/Sao_Paulo, daylight saving started
// at midnight of 2018-11-04 (00:00 -03 became 01:00 -02, at 03:00 UTC).
// The day named IMG-20181104-WA0001.jpg begins at that change and ends at
// midnight of the 5th; the 3rd ends at the change. So neither the 3rd's
// last minutes nor 00:30 on the 5th are taken as the 4th's time.
func TestPeriodsAroundADaylightSavingStart(t *testing.T) {
	sp, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	d, ok := NameDate([]byte("IMG-20181104-WA0001.jpg"), sp)
	if !ok || d.Local != "2018-11-04" {
		t.Fatalf("NameDate = %+v %v", d, ok)
	}
	change := time.Date(2018, 11, 4, 3, 0, 0, 0, time.UTC)
	if !d.Instant.Equal(change) {
		t.Errorf("the 4th begins at %v, want %v", d.Instant, change)
	}
	if end, want := d.end(sp), time.Date(2018, 11, 5, 2, 0, 0, 0, time.UTC); !end.Equal(want) {
		t.Errorf("the 4th ends at %v, want %v", end, want)
	}
	day3, _ := NameDate([]byte("IMG-20181103-WA0001.jpg"), sp)
	if end := day3.end(sp); !end.Equal(change) {
		t.Errorf("the 3rd ends at %v, want %v", end, change)
	}
	month, _ := FolderDate([]byte("2018-10"), sp, now)
	if end, want := month.end(sp), time.Date(2018, 11, 1, 3, 0, 0, 0, time.UTC); !end.Equal(want) {
		t.Errorf("October ends at %v, want %v", end, want)
	}
	for _, mtime := range []time.Time{time.Date(2018, 11, 3, 23, 30, 0, 0, sp), time.Date(2018, 11, 5, 0, 30, 0, 0, sp)} {
		if d.contains(mtime, sp, 0) {
			t.Errorf("the 4th contains %v", mtime)
		}
		in := Inputs{Path: []byte("Fotos/IMG-20181104-WA0001.jpg"), Mtime: &mtime, MetaState: MetaNone, Zone: sp, Now: now}
		if e := Derive(in); e.Refined || e.Date == nil || e.Date.Local != "2018-11-04" {
			t.Errorf("modified at %v: Derive = %+v refined %v, want the 4th unrefined", mtime, e.Date, e.Refined)
		}
	}
}

func TestFolderDate(t *testing.T) {
	for _, c := range []struct {
		name  string
		local string
		prec  Precision
	}{
		{"2006", "2006", PrecisionYear},
		{"2005-12", "2005-12", PrecisionMonth},
		{"2010-07 Bahia", "2010-07", PrecisionMonth},
		{"2010-07-17_praia", "2010-07-17", PrecisionDay},
		{"2010-07-17", "2010-07-17", PrecisionDay},
		{"2008-03 Ouro Preto", "2008-03", PrecisionMonth},
		{"2010.ferias", "2010", PrecisionYear},
		{"2010 - Natal", "2010", PrecisionYear},
		{"1990", "1990", PrecisionYear},
		{"2026", "2026", PrecisionYear},
		{"2010-13 x", "2010", PrecisionYear}, // no 13th month: the year alone
	} {
		d, ok := FolderDate([]byte(c.name), recif, now)
		if !ok || d.Local != c.local || d.Precision != c.prec {
			t.Errorf("FolderDate(%q) = %+v %v, want %s %s", c.name, d, ok, c.local, c.prec)
			continue
		}
		w, _, _ := parseWall(c.local)
		want := time.Date(w.Year(), w.Month(), w.Day(), 0, 0, 0, 0, recif)
		if !d.Instant.Equal(want) {
			t.Errorf("FolderDate(%q) starts at %v, want %v", c.name, d.Instant, want)
		}
	}
	for _, name := range []string{
		"fotos_2005_do_pendrive", "celular_backup_2009", "Backup_PC_2004", "Natal", "1989", "2027",
		"20100717", "2010x", "201", "", "Fotos 2005",
	} {
		if d, ok := FolderDate([]byte(name), utc, now); ok {
			t.Errorf("FolderDate(%q) = %+v, want none", name, d)
		}
	}
}

// r5 review, Addendum G12: the layout a date organize writes,
// `{year}/{month}` and `{year}/{month}/{day}`, carries the month or day.
func TestFolderPathDate(t *testing.T) {
	for _, c := range []struct {
		path, local string
		prec        Precision
	}{
		{"Fotos/2010/07", "2010-07", PrecisionMonth},
		{"2010/07", "2010-07", PrecisionMonth},
		{"Fotos/2010/07/17", "2010-07-17", PrecisionDay},
		{"Fotos/2010/07 Bahia", "2010-07", PrecisionMonth},
		{"Fotos/2010/07/17_praia", "2010-07-17", PrecisionDay},
		{"Fotos/2010", "2010", PrecisionYear},
		{"Fotos/2010/13", "", ""},          // no 13th month, and "13" is no D6 date
		{"Fotos/2010/02/30", "", ""},       // no February 30th: "30" alone is no date
		{"Fotos/2010/7", "", ""},           // one digit
		{"Fotos/2010/070", "", ""},         // three digits
		{"Fotos/Album 2010/07", "", ""},    // the year must be alone
		{"Fotos/2010-07 Bahia/08", "", ""}, // nor a month
		{"Fotos/2027/07", "", ""},          // after now's year
		{"Fotos/2010/07/2011-03", "2011-03", PrecisionMonth},
	} {
		d, ok := FolderPathDate([]byte(c.path), recif, now)
		if ok != (c.local != "") || d.Local != c.local || d.Precision != c.prec {
			t.Errorf("FolderPathDate(%q) = %+v %v, want %q %s", c.path, d, ok, c.local, c.prec)
		}
	}
	// A file moved from "Viagens/2010-07 Bahia" into "Fotos/2010/07" keeps
	// its month: the modification time, in December, is outside it, so the
	// date stays the month, unrefined, from the folder name.
	mtime := tp(2010, 12, 26, 15, 0, 0)
	for _, p := range []string{"Viagens/2010-07 Bahia/scan.jpg", "Fotos/2010/07/scan.jpg"} {
		checkEff(t, p, Derive(in(p, mtime, nil)), wantEff{source: SourceFolderName, conf: ConfidenceLow, local: "2010-07",
			prec: PrecisionMonth, flags: FlagNoDateMetadata})
	}
	checkEff(t, "a day folder", Derive(in("Fotos/2010/07/17/scan.jpg", tp(2010, 7, 17, 15, 0, 0), nil)), wantEff{
		source: SourceFolderName, conf: ConfidenceMedium, local: "2010-07-17T15:00:00", prec: PrecisionSecond,
		refined: true, flags: FlagNoDateMetadata})
}

func TestParseLocal(t *testing.T) {
	for _, c := range []struct {
		s       string
		off     *int
		instant time.Time
		prec    Precision
	}{
		{"1978", nil, time.Date(1978, 1, 1, 0, 0, 0, 0, recif), PrecisionYear},
		{"2010-07", nil, time.Date(2010, 7, 1, 0, 0, 0, 0, recif), PrecisionMonth},
		{"2010-07-17", nil, time.Date(2010, 7, 17, 0, 0, 0, 0, recif), PrecisionDay},
		{"2010-07-17T10:20:30", nil, time.Date(2010, 7, 17, 10, 20, 30, 0, recif), PrecisionSecond},
		{"2010-07-17T10:20:30", intp(120), time.Date(2010, 7, 17, 8, 20, 30, 0, utc), PrecisionSecond},
	} {
		d, err := ParseLocal(c.s, recif, c.off)
		if err != nil || !d.Instant.Equal(c.instant) || d.Local != c.s || d.Precision != c.prec ||
			(c.off == nil) != (d.OffsetMin == nil) || d.Instant.Location() != time.UTC {
			t.Errorf("ParseLocal(%q) = %+v, %v", c.s, d, err)
		}
	}
	for _, c := range []struct {
		s   string
		off *int
	}{
		{"", nil}, {"78", nil}, {"2010-7", nil}, {"2010-13", nil}, {"2010-02-30", nil}, {"2010/07/17", nil},
		{"2010-07-17 10:20:30", nil}, {"2010-07-17T24:00:00", nil}, {"2010-07-17T10:20", nil},
		{"0000", nil}, {"2010-07-17", intp(60)}, {"2010-07-17T10:20:30", intp(900)}, {"abcd", nil},
	} {
		if d, err := ParseLocal(c.s, utc, c.off); err == nil {
			t.Errorf("ParseLocal(%q, %v) = %+v, want an error", c.s, c.off, d)
		}
	}
}

func TestDateFromRow(t *testing.T) {
	for _, d := range []Date{
		mustLocal(t, "1978", nil), mustLocal(t, "2010-07", nil), mustLocal(t, "2010-07-17", nil),
		mustLocal(t, "2010-07-17T10:20:30", intp(-180)),
	} {
		got, err := DateFromRow(d.Instant.UnixNano(), d.Local, d.OffsetMin, string(d.Precision))
		if err != nil || !got.Instant.Equal(d.Instant) || got.Local != d.Local || got.Precision != d.Precision ||
			(got.OffsetMin == nil) != (d.OffsetMin == nil) {
			t.Errorf("DateFromRow(%+v) = %+v, %v", d, got, err)
		}
	}
	for _, c := range []struct{ local, prec string }{
		{"2010", "month"}, {"2010-07", "second"}, {"2010-13", "month"}, {"2010", ""}, {"", "year"}, {"2010", "week"},
	} {
		if _, err := DateFromRow(0, c.local, nil, c.prec); err == nil {
			t.Errorf("DateFromRow(%q, %q) accepted", c.local, c.prec)
		}
	}
}

func mustLocal(t *testing.T, s string, off *int) Date {
	t.Helper()
	d, err := ParseLocal(s, recif, off)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTemplates(t *testing.T) {
	day := mustLocal(t, "2010-07-17T10:20:30", nil)
	month := mustLocal(t, "2010-07", nil)
	for _, c := range []struct {
		tmpl  string
		event string
		d     Date
		want  []string
	}{
		{"", "Bahia", day, []string{"2010", "07"}},
		{"{year}/{month}", "", month, []string{"2010", "07"}},
		{"{year}/{month}/{day}", "", day, []string{"2010", "07", "17"}},
		{"Fotos {year}/{month}-{day}", "", day, []string{"Fotos 2010", "07-17"}},
		{"{year}/{month} {event}", "Bahia", day, []string{"2010", "07 Bahia"}},
		{"{year}/{month} {event}", "", day, []string{"2010", "07"}},
		{"{year}/{event}", "", day, []string{"2010"}},
		{"{year}/{event}/{month}", "", day, []string{"2010", "07"}},
		{"{year} - {event}", "", day, []string{"2010"}},
		{"{event}", "", day, nil},
		{"{event}", "Natal", Date{}, []string{"Natal"}},
		{"Eventos/{event}", "Natal", day, []string{"Eventos", "Natal"}},
		{"{event}.", "", day, nil},
	} {
		tp, err := ParseTemplate(c.tmpl)
		if err != nil {
			t.Fatalf("ParseTemplate(%q): %v", c.tmpl, err)
		}
		got, err := tp.Folders(c.d, []byte(c.event))
		if err != nil {
			t.Errorf("%q with %q: %v", c.tmpl, c.event, err)
			continue
		}
		var gs []string
		for _, g := range got {
			gs = append(gs, string(g))
		}
		if len(gs) != len(c.want) || (len(gs) > 0 && joinS(gs) != joinS(c.want)) {
			t.Errorf("%q with %q = %q, want %q", c.tmpl, c.event, gs, c.want)
		}
	}
	if tp, _ := ParseTemplate(""); tp.String() != "{year}/{month}" {
		t.Errorf("default template %q", tp.String())
	}
	if tp, _ := ParseTemplate("Fotos/{year}"); tp.String() != "Fotos/{year}" {
		t.Errorf("String() = %q", tp.String())
	}
	for _, s := range []string{
		"/{year}", "{year}/", "{year}//{month}", "a/b/c/d/e", "{year}/.", "../{year}", "{yaer}", "{year",
		"year}", "{year}/{mon}th}", "a\x00b", "{Year}", "{}",
	} {
		if _, err := ParseTemplate(s); !errors.Is(err, ErrInvalidTemplate) {
			t.Errorf("ParseTemplate(%q) = %v, want ErrInvalidTemplate", s, err)
		}
	}
	// A date coarser than the finest token is too coarse.
	for _, c := range []struct {
		tmpl string
		d    Date
	}{
		{"{year}/{month}", mustLocal(t, "1978", nil)},
		{"{year}/{month}/{day}", month},
		{"{day}", mustLocal(t, "2010-07", nil)},
		{"{year}", Date{}},
	} {
		tp, _ := ParseTemplate(c.tmpl)
		if _, err := tp.Folders(c.d, nil); !errors.Is(err, ErrTooCoarse) {
			t.Errorf("%q on %q: %v, want ErrTooCoarse", c.tmpl, c.d.Local, err)
		}
	}
	tp, _ := ParseTemplate("{year}")
	if got, err := tp.Folders(mustLocal(t, "1978", nil), nil); err != nil || len(got) != 1 || string(got[0]) != "1978" {
		t.Errorf("{year} on 1978: %q %v", got, err)
	}
}

func joinS(s []string) string {
	out := ""
	for _, x := range s {
		out += x + "/"
	}
	return out
}

func TestEventName(t *testing.T) {
	for in, want := range map[string]string{
		"2010-07 Bahia": "Bahia", "2010-12 Natal": "Natal", "Natal": "Natal", "2010": "", "2010-07-17_-. praia": "praia",
		"2008-03 Ouro Preto": "Ouro Preto", "do celular da Ana": "do celular da Ana", "fotos_2005": "fotos_2005",
		"2010x": "2010x", "2010 caf\xe9": "caf\xe9",
	} {
		if got := string(EventName([]byte(in))); got != want {
			t.Errorf("EventName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenamedName(t *testing.T) {
	d := mustLocal(t, "2010-07-17T10:20:30", nil)
	got, err := RenamedName([]byte("IMG_0101.JPG"), d)
	if err != nil || string(got) != "20100717_102030_IMG_0101.JPG" {
		t.Fatalf("RenamedName = %q, %v", got, err)
	}
	again, err := RenamedName(got, d)
	if err != nil || string(again) != string(got) {
		t.Errorf("renaming again = %q, %v; want it unchanged", again, err)
	}
	// Another date's prefix is not this one's.
	other, _ := RenamedName(got, mustLocal(t, "2011-01-01T00:00:01", nil))
	if string(other) != "20110101_000001_20100717_102030_IMG_0101.JPG" {
		t.Errorf("another date: %q", other)
	}
	for _, d := range []Date{mustLocal(t, "2010-07-17", nil), mustLocal(t, "2010-07", nil), mustLocal(t, "2010", nil), {}} {
		if _, err := RenamedName([]byte("a.jpg"), d); !errors.Is(err, ErrTooCoarse) {
			t.Errorf("RenamedName at %q: %v, want ErrTooCoarse", d.Precision, err)
		}
	}
}
