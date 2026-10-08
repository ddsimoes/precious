//go:build slow && !race

package index

// raceEnabled reports that the tests run under the race detector, which
// makes timings meaningless for the r3 scale target.
const raceEnabled = false
