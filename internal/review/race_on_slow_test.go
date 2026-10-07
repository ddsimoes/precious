//go:build slow && race

package review

// raceEnabled reports that the tests run under the race detector, which
// makes timings meaningless for the D20 targets.
const raceEnabled = true
