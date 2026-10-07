//go:build slow && !race

package api

// raceEnabled reports that the tests run under the race detector, which
// makes timings meaningless for the Search targets (r2b design D8).
const raceEnabled = false
