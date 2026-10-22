package domain

import "math/bits"

// Place is where a file's content has other physical copies, relative to the
// file's own source (design D1). A member's source is its archive entry's.
type Place string

const (
	PlaceOnlyCopy    Place = "only_copy"
	PlaceThisSource  Place = "this_source"
	PlaceOtherSource Place = "other_source"
	PlaceUnchecked   Place = "unchecked"
	PlaceUnreadable  Place = "unreadable"
	PlaceNone        Place = "none"
	PlaceNotComputed Place = "not_computed" // a present folder or complete archive with no entry_places row yet
)

// PlaceAmount is a number of files and their bytes in one place.
type PlaceAmount struct{ Files, Bytes int64 }

// PlaceSums is a folder's or complete archive's files and bytes per place
// over its non-disposable files, plus its disposable files (D4, D5, D14).
type PlaceSums struct {
	OnlyCopy, ThisSource, OtherSource, Unchecked, Unreadable PlaceAmount
	Disposable                                               PlaceAmount
}

// placeOrder is D1's order for folders and complete archives.
var placeOrder = [5]Place{PlaceOnlyCopy, PlaceThisSource, PlaceUnchecked, PlaceUnreadable, PlaceOtherSource}

// ordered returns p's five place amounts in placeOrder.
func (p *PlaceSums) ordered() [5]*PlaceAmount {
	return [5]*PlaceAmount{&p.OnlyCopy, &p.ThisSource, &p.Unchecked, &p.Unreadable, &p.OtherSource}
}

// Place applies D1's order; complete=false (the folder or anything below it
// unreadable, partial, or a mount boundary) turns other_source and none into
// unreadable. The relate pass never knows completeness: the read API alone
// computes it, in one unexported helper (api/rows.go, complete(row)), from
// entries.state, partial, mount_boundary, and dir_stats.unreadable and
// mount_boundaries (D1).
//
// Only file counts decide; Disposable never does (D14).
func (p PlaceSums) Place(complete bool) Place {
	place := PlaceNone
	for i, a := range p.ordered() {
		if a.Files > 0 {
			place = placeOrder[i]
			break
		}
	}
	if !complete && (place == PlaceOtherSource || place == PlaceNone) {
		return PlaceUnreadable
	}
	return place
}

// OnlyHereBytes is OnlyCopy.Bytes + ThisSource.Bytes (D10's key).
func (p PlaceSums) OnlyHereBytes() int64 { return p.OnlyCopy.Bytes + p.ThisSource.Bytes }

// SplitArchive splits size by the member sums m (D5): exact 128-bit
// shares, largest remainder, at least 1 byte per place with member bytes,
// the one file in the first place of D1's order with member bytes.
//
// Only m's five place byte counts are read: its file counts and its
// Disposable amount are ignored, and the result's Disposable is zero (a
// disposable archive is the caller's, D14). Negative member bytes count as
// none, and a negative size as 0. With no member bytes the split is empty.
//
// Remainder bytes go to the largest remainders, ties to the earlier place in
// D1's order. Then each place with member bytes but no share, in D1's order,
// takes 1 byte from the largest share (ties to the earlier place) while that
// share holds more than 1 byte. So when size is smaller than the number of
// places with member bytes, size of them get 1 byte each and the others none.
// The shares always sum to size.
func SplitArchive(size int64, m PlaceSums) PlaceSums {
	var w [5]uint64
	has := false
	for i, a := range m.ordered() {
		if a.Bytes > 0 {
			w[i] = uint64(a.Bytes)
			has = true
		}
	}
	if !has {
		return PlaceSums{}
	}
	if size < 0 {
		size = 0
	}
	total := weightTotal(&w)

	var share [5]int64
	var rem [5]uint64
	left := size
	for i, wi := range w {
		hi, lo := bits.Mul64(uint64(size), wi)
		q, r := bits.Div64(hi, lo, total) // q <= size, so hi < total
		share[i], rem[i] = int64(q), r
		left -= int64(q)
	}
	// left < number of places with weight: each floor loses less than 1.
	for ; left > 0; left-- {
		best := -1
		for i := range w {
			if w[i] > 0 && (best < 0 || rem[i] > rem[best]) {
				best = i
			}
		}
		share[best]++
		rem[best] = 0
	}
	for i := range w {
		if w[i] == 0 || share[i] > 0 {
			continue
		}
		largest := 0
		for j := range share {
			if share[j] > share[largest] {
				largest = j
			}
		}
		if share[largest] <= 1 {
			break
		}
		share[largest]--
		share[i] = 1
	}

	var out PlaceSums
	file := true
	for i, a := range out.ordered() {
		if w[i] == 0 {
			continue
		}
		a.Bytes = share[i]
		if file {
			a.Files, file = 1, false
		}
	}
	return out
}

// weightTotal returns the sum of w. Should it not fit in 64 bits (members
// above 2^62 bytes), every weight is halved first, a weight above 0 staying
// above 0, until it does; the shares then follow the halved weights.
func weightTotal(w *[5]uint64) uint64 {
	for {
		var total, carry uint64
		for _, wi := range w {
			var c uint64
			total, c = bits.Add64(total, wi, 0)
			carry |= c
		}
		if carry == 0 {
			return total
		}
		for i, wi := range w {
			if wi > 0 {
				w[i] = max(wi>>1, 1)
			}
		}
	}
}

// IsDisposable is D14's test: FileFamily(category, kind) is disposable and
// triage is not review.
func IsDisposable(category Category, kind FileKind, triage Triage) bool {
	return FileFamily(category, kind) == FamilyDisposable && triage != TriageReview
}
