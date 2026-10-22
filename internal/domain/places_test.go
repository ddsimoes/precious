package domain

import (
	"math"
	"math/big"
	"math/rand/v2"
	"testing"
)

const (
	kib = int64(1) << 10
	gib = int64(1) << 30
)

func amt(files, bytes int64) PlaceAmount { return PlaceAmount{Files: files, Bytes: bytes} }

// design D1: folders and complete archives take the first place with files in
// the order only_copy, this_source, unchecked, unreadable, other_source.
func TestPlaceSumsPlace(t *testing.T) {
	all := PlaceSums{
		OnlyCopy: amt(1, 1), ThisSource: amt(1, 1), OtherSource: amt(1, 1),
		Unchecked: amt(1, 1), Unreadable: amt(1, 1),
	}
	cases := []struct {
		name     string
		sums     PlaceSums
		complete Place
		partial  Place
	}{
		{"every place", all, PlaceOnlyCopy, PlaceOnlyCopy},
		{"this source before unchecked", PlaceSums{ThisSource: amt(1, 1), Unchecked: amt(9, 900), Unreadable: amt(1, 1), OtherSource: amt(9, 900)}, PlaceThisSource, PlaceThisSource},
		{"unchecked before unreadable", PlaceSums{Unchecked: amt(1, 1), Unreadable: amt(9, 900), OtherSource: amt(9, 900)}, PlaceUnchecked, PlaceUnchecked},
		{"unreadable before other source", PlaceSums{Unreadable: amt(1, 1), OtherSource: amt(9, 900)}, PlaceUnreadable, PlaceUnreadable},
		{"other source alone", PlaceSums{OtherSource: amt(3, 30)}, PlaceOtherSource, PlaceUnreadable},
		{"no files", PlaceSums{}, PlaceNone, PlaceUnreadable},
		{"bytes without files do not decide", PlaceSums{OnlyCopy: amt(0, 500), OtherSource: amt(1, 1)}, PlaceOtherSource, PlaceUnreadable},
		{"disposable never decides", PlaceSums{Disposable: amt(40, 4000), OtherSource: amt(1, 1)}, PlaceOtherSource, PlaceUnreadable},
		{"disposable only is none", PlaceSums{Disposable: amt(40, 4000)}, PlaceNone, PlaceUnreadable},
		{"only copy kept when partial", PlaceSums{OnlyCopy: amt(1, 1), Disposable: amt(5, 5)}, PlaceOnlyCopy, PlaceOnlyCopy},
	}
	for _, c := range cases {
		if got := c.sums.Place(true); got != c.complete {
			t.Errorf("%s: Place(true) = %q, want %q", c.name, got, c.complete)
		}
		if got := c.sums.Place(false); got != c.partial {
			t.Errorf("%s: Place(false) = %q, want %q", c.name, got, c.partial)
		}
	}
}

// design D10: the only_here key counts only_copy and this_source bytes.
func TestOnlyHereBytes(t *testing.T) {
	s := PlaceSums{
		OnlyCopy: amt(1, 3), ThisSource: amt(1, 40), OtherSource: amt(1, 500),
		Unchecked: amt(1, 6000), Unreadable: amt(1, 70000), Disposable: amt(1, 800000),
	}
	if got := s.OnlyHereBytes(); got != 43 {
		t.Errorf("OnlyHereBytes = %d, want 43", got)
	}
}

// design D5: an archive's size is split among the places of its member bytes.
func TestSplitArchive(t *testing.T) {
	cases := []struct {
		name    string
		size    int64
		members PlaceSums
		want    PlaceSums
	}{
		{
			"5 GiB archive of 6 GiB in one place",
			5 * gib, PlaceSums{ThisSource: amt(700, 6*gib)},
			PlaceSums{ThisSource: amt(1, 5*gib)},
		},
		{
			"4 GiB archive split 3:1",
			4 * gib, PlaceSums{OnlyCopy: amt(3, 6*gib), OtherSource: amt(1, 2*gib)},
			PlaceSums{OnlyCopy: amt(1, 3*gib), OtherSource: amt(0, gib)},
		},
		{
			"1 KiB archive of 1 GiB members gives each place a byte",
			kib, PlaceSums{OnlyCopy: amt(9, gib-2), Unchecked: amt(1, 1), OtherSource: amt(1, 1)},
			PlaceSums{OnlyCopy: amt(1, kib-2), Unchecked: amt(0, 1), OtherSource: amt(0, 1)},
		},
		{
			"a place with files but no bytes gets nothing",
			100, PlaceSums{OnlyCopy: amt(1, 30), ThisSource: amt(1, 70), Unreadable: amt(1, 0), OtherSource: amt(1, 1)},
			// exact shares 29.70, 69.31, 0.99: floors 29, 69, 0; the two bytes left go to 0.99 and 0.70.
			PlaceSums{OnlyCopy: amt(1, 30), ThisSource: amt(0, 69), OtherSource: amt(0, 1)},
		},
		{
			"minimum taken from the largest share",
			10, PlaceSums{ThisSource: amt(1, 1), Unchecked: amt(1, 1), Unreadable: amt(1, 1000)},
			PlaceSums{ThisSource: amt(1, 1), Unchecked: amt(0, 1), Unreadable: amt(0, 8)},
		},
		{
			"largest remainder, not the first place",
			10, PlaceSums{OnlyCopy: amt(1, 2), Unchecked: amt(1, 7)},
			// 2.22 and 7.78: the left byte goes to unchecked.
			PlaceSums{OnlyCopy: amt(1, 2), Unchecked: amt(0, 8)},
		},
		{
			"equal remainders go to the earlier place",
			10, PlaceSums{OtherSource: amt(1, 1), Unreadable: amt(1, 1), Unchecked: amt(1, 1)},
			PlaceSums{Unchecked: amt(1, 4), Unreadable: amt(0, 3), OtherSource: amt(0, 3)},
		},
		{
			"the file goes to the first place with bytes",
			100, PlaceSums{OtherSource: amt(5, 900), Unreadable: amt(1, 100)},
			PlaceSums{Unreadable: amt(1, 10), OtherSource: amt(0, 90)},
		},
		{
			"no member bytes is an empty split",
			100, PlaceSums{OnlyCopy: amt(4, 0), OtherSource: amt(2, 0), Disposable: amt(3, 50)},
			PlaceSums{},
		},
		{
			"disposable members are ignored",
			100, PlaceSums{OtherSource: amt(2, 10), Disposable: amt(3, 990)},
			PlaceSums{OtherSource: amt(1, 100)},
		},
		{
			"an empty archive still places its file",
			0, PlaceSums{Unchecked: amt(1, 5), OtherSource: amt(1, 5)},
			PlaceSums{Unchecked: amt(1, 0)},
		},
		{
			// Fewer bytes than places: size places get 1 byte, the rest none.
			"2 bytes over five places",
			2, PlaceSums{OnlyCopy: amt(1, 5), ThisSource: amt(1, 5), OtherSource: amt(1, 5), Unchecked: amt(1, 5), Unreadable: amt(1, 5)},
			PlaceSums{OnlyCopy: amt(1, 1), ThisSource: amt(0, 1)},
		},
		{
			"3 bytes over four places, one share large",
			3, PlaceSums{OnlyCopy: amt(1, 1000), ThisSource: amt(1, 1), Unchecked: amt(1, 1), OtherSource: amt(1, 1)},
			PlaceSums{OnlyCopy: amt(1, 1), ThisSource: amt(0, 1), Unchecked: amt(0, 1)},
		},
		{
			"negative member bytes count as none",
			100, PlaceSums{OnlyCopy: amt(1, -50), OtherSource: amt(1, 50)},
			PlaceSums{OtherSource: amt(1, 100)},
		},
		{
			"members whose total overflows 64 bits",
			math.MaxInt64, PlaceSums{OnlyCopy: amt(1, math.MaxInt64), ThisSource: amt(1, math.MaxInt64), OtherSource: amt(1, math.MaxInt64), Unchecked: amt(1, 1)},
			// halved weights 2^62-1 (three) and 1: the total fits and the size splits evenly.
			PlaceSums{OnlyCopy: amt(1, math.MaxInt64/3), ThisSource: amt(0, math.MaxInt64/3), OtherSource: amt(0, math.MaxInt64/3), Unchecked: amt(0, 1)},
		},
	}
	for _, c := range cases {
		if got := SplitArchive(c.size, c.members); got != c.want {
			t.Errorf("%s: SplitArchive(%d, %+v)\n got %+v\nwant %+v", c.name, c.size, c.members, got, c.want)
		}
	}
}

// design D5: over many random archives the shares sum to the size, every place
// with member bytes gets a byte when the size allows, no share is more than a
// few bytes from its exact proportion, and the one file is in the first place
// of D1's order with member bytes.
func TestSplitArchiveInvariants(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 0x5c))
	order := []Place{PlaceOnlyCopy, PlaceThisSource, PlaceUnchecked, PlaceUnreadable, PlaceOtherSource}
	for n := range 20000 {
		var bytes [5]int64
		for i := range bytes {
			switch r.IntN(4) {
			case 0: // no member bytes in this place
			case 1:
				bytes[i] = r.Int64N(100) + 1
			case 2:
				bytes[i] = r.Int64N(1 << 40)
			default:
				bytes[i] = r.Int64N(1 << 60) // five of these still fit in an int64
			}
		}
		var size int64
		switch r.IntN(4) {
		case 0:
			size = r.Int64N(8)
		case 1:
			size = r.Int64N(1 << 20)
		case 2:
			size = r.Int64N(1 << 45)
		default:
			size = r.Int64N(math.MaxInt64)
		}
		m := PlaceSums{
			OnlyCopy: amt(1, bytes[0]), ThisSource: amt(1, bytes[1]), Unchecked: amt(1, bytes[2]),
			Unreadable: amt(1, bytes[3]), OtherSource: amt(1, bytes[4]),
			Disposable: amt(1, r.Int64N(1<<40)),
		}
		got := SplitArchive(size, m)
		shares := [5]PlaceAmount{got.OnlyCopy, got.ThisSource, got.Unchecked, got.Unreadable, got.OtherSource}

		var places, total int64
		for _, b := range bytes {
			if b > 0 {
				places++
				total += b
			}
		}
		if places == 0 {
			if got != (PlaceSums{}) {
				t.Fatalf("#%d: no member bytes gave %+v", n, got)
			}
			continue
		}
		if got.Disposable != (PlaceAmount{}) {
			t.Fatalf("#%d: disposable %+v in the split", n, got.Disposable)
		}
		var sum, files int64
		first := true
		for i, s := range shares {
			sum += s.Bytes
			files += s.Files
			if bytes[i] == 0 {
				if s != (PlaceAmount{}) {
					t.Fatalf("#%d: %s has no member bytes but got %+v", n, order[i], s)
				}
				continue
			}
			if s.Bytes < 0 || (size >= places && s.Bytes < 1) {
				t.Fatalf("#%d: %s got %d of %d bytes over %d places", n, order[i], s.Bytes, size, places)
			}
			want := int64(0)
			if first {
				want = 1
			}
			if s.Files != want {
				t.Fatalf("#%d: %s has %d files, want %d", n, order[i], s.Files, want)
			}
			first = false
			// |share*total - size*bytes| < places*total: within places bytes of exact.
			diff := new(big.Int).Sub(
				new(big.Int).Mul(big.NewInt(s.Bytes), big.NewInt(total)),
				new(big.Int).Mul(big.NewInt(size), big.NewInt(bytes[i])))
			bound := new(big.Int).Mul(big.NewInt(places), big.NewInt(total))
			if diff.Abs(diff).Cmp(bound) >= 0 {
				t.Fatalf("#%d: %s got %d of %d, %d of %d member bytes", n, order[i], s.Bytes, size, bytes[i], total)
			}
		}
		if sum != size {
			t.Fatalf("#%d: shares sum to %d, size %d (%+v)", n, sum, size, got)
		}
		if files != 1 {
			t.Fatalf("#%d: %d files in the split", n, files)
		}
	}
}

// design D14: a file is disposable when its family is disposable and its
// triage is not review.
func TestIsDisposable(t *testing.T) {
	named := []struct {
		c    Category
		k    FileKind
		tr   Triage
		want bool
	}{
		{CategoryUnknown, FileKindSystem, TriageDiscard, true}, // Thumbs.db
		{"", FileKindSystem, TriageKeep, true},                 // no category row
		{CategoryCache, FileKindImage, TriageDiscard, true},
		{CategoryTemporaryData, FileKindOther, TriageReview, false}, // recovered chunks
		{CategoryPersonalMedia, FileKindSystem, TriageKeep, false},
		{CategoryBackup, FileKindSystem, TriageDiscard, false},
		{CategoryUnknown, FileKindImage, TriageDiscard, false},
	}
	for _, c := range named {
		if got := IsDisposable(c.c, c.k, c.tr); got != c.want {
			t.Errorf("IsDisposable(%q, %q, %q) = %v, want %v", c.c, c.k, c.tr, got, c.want)
		}
	}

	categories := append(append([]Category{}, Categories...), "", "not_a_category")
	kinds := append(append([]FileKind{}, FileKinds...), "", "not_a_kind")
	triages := append(append([]Triage{}, Triages...), "")
	var yes, no int
	for _, c := range categories {
		for _, k := range kinds {
			for _, tr := range triages {
				want := FileFamily(c, k) == FamilyDisposable && tr != TriageReview
				if got := IsDisposable(c, k, tr); got != want {
					t.Errorf("IsDisposable(%q, %q, %q) = %v, want %v", c, k, tr, got, want)
				}
				if want {
					yes++
				} else {
					no++
				}
			}
		}
	}
	if yes == 0 || no == 0 {
		t.Errorf("combinations: %d disposable, %d not", yes, no)
	}
}
