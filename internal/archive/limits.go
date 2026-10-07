package archive

import (
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"precious/internal/domain"
)

// Limits are the budgets of one opening (m4b D6). A zip's budgets cover
// its listing and every later member read together.
type Limits struct {
	// MaxEntries, MaxBytes, and MaxRatio must be positive. An opening stops
	// when its entries exceed MaxEntries, when its unpacked bytes exceed
	// MaxBytes, or when they exceed MaxRatio times the packed bytes read
	// plus RatioGrace.
	//
	// Entries are every future archive_members row: each member, and each
	// folder a member's path implies before any member names it. A zip's
	// declared member count is checked against MaxEntries before its
	// central directory is parsed, as a lower bound.
	MaxEntries, MaxBytes, MaxRatio int64
	// Deadline ends the opening when Now is after it; zero means no time
	// budget. Budgets are checked at every read of unpacked output.
	Deadline time.Time
	// Now reads the clock for Deadline; nil means time.Now. The readers of
	// one Zip may call it concurrently.
	Now func() time.Time
}

// RatioGrace is the number of unpacked bytes an opening may produce beyond
// MaxRatio times its packed bytes read (m4b D6). It lets a small archive
// of a highly compressible file open, while a deflate bomb of about 1,000:1
// stops within a few megabytes of packed input.
const RatioGrace = 64 << 20

func (l Limits) validate() error {
	if l.MaxEntries < 1 || l.MaxBytes < 1 || l.MaxRatio < 1 {
		return fmt.Errorf("archive: limits must be positive: %d entries, %d bytes, ratio %d",
			l.MaxEntries, l.MaxBytes, l.MaxRatio)
	}
	return nil
}

// entryCount counts an opening's entries against MaxEntries.
type entryCount struct{ n, max int64 }

// add counts n more entries.
func (e *entryCount) add(n int) error {
	e.n += int64(n)
	if e.n > e.max {
		return stop(domain.ArchivePartial, detailEntries, nil)
	}
	return nil
}

// meter counts an opening's packed and unpacked bytes and tells when a
// budget is reached. Its counters are atomic, so a Zip's members may be read
// concurrently.
type meter struct {
	lim Limits
	now func() time.Time
	// packed counts the bytes read from the archive file.
	packed atomic.Int64
	// unpacked counts the decompressor's output, tar headers included.
	unpacked atomic.Int64
}

func newMeter(lim Limits) *meter {
	m := &meter{lim: lim, now: lim.Now}
	if m.now == nil {
		m.now = time.Now
	}
	return m
}

// reached returns the detail of the first budget reached, in the order
// unpacked bytes, ratio, time, or "" when none is.
func (m *meter) reached() string {
	unpacked := m.unpacked.Load()
	if unpacked > m.lim.MaxBytes {
		return detailBytes
	}
	if unpacked > ratioAllowance(m.lim.MaxRatio, m.packed.Load()) {
		return detailRatio
	}
	if !m.lim.Deadline.IsZero() && m.now().After(m.lim.Deadline) {
		return detailTime
	}
	return ""
}

// ratioAllowance is ratio × packed + RatioGrace, saturated at MaxInt64.
func ratioAllowance(ratio, packed int64) int64 {
	if packed > (math.MaxInt64-RatioGrace)/ratio {
		return math.MaxInt64
	}
	return ratio*packed + RatioGrace
}
