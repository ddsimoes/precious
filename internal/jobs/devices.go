package jobs

import "precious/internal/domain"

// slot is what one running attempt holds (design D14): a slot of the device
// key it was claimed under, and its source. A job without a source has the
// empty key. A placeholder claim ("source:<id>", the source's device not yet
// recorded) also makes its source exclusive until the attempt finishes. A pool
// claim ("workers:<name>", design D11) holds a slot of its pool only: its source
// is left empty, so it neither waits for nor counts toward its source's
// exclusivity.
type slot struct {
	key         string
	source      domain.SourceID
	placeholder bool
}

// deviceSlots bounds concurrent attempts per device key (design D11): at most
// capacity attempts hold a slot of one key at a time, and at most the pool's
// own capacity hold a slot of a pool key. It also tracks the running attempts
// of each source, so that a placeholder claim runs alone in its source
// (design D14). An attempt that vacates its slot in Yield (design D13) still
// counts as running in its source. One that moves in UseSource (design D13)
// releases its slot, and takes the new one when granted as a claim does.
// Callers serialize access (Runner.mu).
type deviceSlots struct {
	capacity  int
	pools     map[string]int // capacity of each pool key
	busy      map[string]int
	running   map[domain.SourceID]int
	exclusive map[domain.SourceID]bool
}

func newDeviceSlots(capacity int) *deviceSlots {
	return &deviceSlots{
		capacity:  capacity,
		pools:     map[string]int{},
		busy:      map[string]int{},
		running:   map[domain.SourceID]int{},
		exclusive: map[domain.SourceID]bool{},
	}
}

// addPool gives the pool key its own capacity. It reports false when the key
// already has a different capacity.
func (d *deviceSlots) addPool(key string, capacity int) bool {
	if c, ok := d.pools[key]; ok {
		return c == capacity
	}
	d.pools[key] = capacity
	return true
}

func (d *deviceSlots) capacityOf(key string) int {
	if c, ok := d.pools[key]; ok {
		return c
	}
	return d.capacity
}

// blocked lists the sources a claim must exclude: those made exclusive by a
// running placeholder attempt (no job of theirs may start), and those with
// any running attempt (no placeholder job of theirs may start).
func (d *deviceSlots) blocked() (exclusive, running []domain.SourceID) {
	for src := range d.exclusive {
		exclusive = append(exclusive, src)
	}
	for src := range d.running {
		running = append(running, src)
	}
	return exclusive, running
}

// eligible reports whether s's source lets it start: the source is not
// exclusive, and a placeholder's source has no running attempt. It says
// nothing about free slots of s's key.
func (d *deviceSlots) eligible(s slot) bool {
	return !(s.source != "" && d.exclusive[s.source]) && !(s.placeholder && d.running[s.source] > 0)
}

// tryAcquire takes s, reporting false when its key has no free slot, its
// source is exclusive, or s is a placeholder and its source has a running
// attempt.
func (d *deviceSlots) tryAcquire(s slot) bool {
	if d.busy[s.key] >= d.capacityOf(s.key) || !d.eligible(s) {
		return false
	}
	d.busy[s.key]++
	if s.source != "" {
		d.running[s.source]++
	}
	if s.placeholder {
		d.exclusive[s.source] = true
	}
	return true
}

// vacate frees the slot of key that a running attempt gives up in Yield
// (design D13). The attempt still counts as running in its source.
func (d *deviceSlots) vacate(key string) {
	if d.busy[key] <= 1 {
		delete(d.busy, key)
	} else {
		d.busy[key]--
	}
}

// reoccupy takes a slot of key back for an attempt that vacated one,
// reporting false when key has no free slot.
func (d *deviceSlots) reoccupy(key string) bool {
	if d.busy[key] >= d.capacityOf(key) {
		return false
	}
	d.busy[key]++
	return true
}

// release returns s, taken by a successful tryAcquire. holdsKey is false
// when its attempt vacated its key's slot and did not get it back.
func (d *deviceSlots) release(s slot, holdsKey bool) {
	if holdsKey {
		d.vacate(s.key)
	}
	if s.source != "" {
		if d.running[s.source] <= 1 {
			delete(d.running, s.source)
		} else {
			d.running[s.source]--
		}
	}
	if s.placeholder {
		delete(d.exclusive, s.source)
	}
}
