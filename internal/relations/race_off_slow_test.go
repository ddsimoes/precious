//go:build slow && !race

package relations

// raceEnabled reports that the tests run under the race detector, which
// makes timings and memory meaningless for the D20 targets.
const raceEnabled = false
