// Package clock provides the injectable time source used by every component that
// makes time-based decisions (sessions, leases, retention, observations).
package clock

import "time"

// Clock returns the current time.
type Clock interface {
	Now() time.Time
}

// Real is the wall clock.
type Real struct{}

// Now returns time.Now().
func (Real) Now() time.Time { return time.Now() }

// Millis converts t to the Unix-millisecond form stored in `*_at` columns.
func Millis(t time.Time) int64 { return t.UnixMilli() }

// FromMillis converts a stored Unix-millisecond value back to a time.Time in UTC.
func FromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
