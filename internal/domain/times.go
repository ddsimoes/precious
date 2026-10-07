package domain

import "time"

// knownTimeFrom is the first modification time that counts as a date. A lost
// or zeroed time reads as the epoch, and on the owner's disk it came with
// sub-second noise (1970-01-01T00:00:00.34Z); local-time filesystems shift it
// by hours either way. So the epoch's whole first day is unknown (design B20).
var knownTimeFrom = time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC).UnixNano()

// KnownModTime reports whether a modification time, in nanoseconds since
// the epoch, is a date rather than a placeholder for a lost time. An unknown
// time sets no folder date and counts under no year.
func KnownModTime(ns int64) bool { return ns >= knownTimeFrom }
