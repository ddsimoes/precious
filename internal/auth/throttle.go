package auth

import (
	"net/netip"
	"sync"
	"time"

	"precious/internal/clock"
)

const (
	// throttleBase is the delay after the first failure; each further
	// consecutive failure doubles it up to the configured maximum.
	throttleBase = time.Second
	// globalThrottleKey throttles the single account across all addresses.
	globalThrottleKey = "account:admin"
)

// Throttle is the in-memory exponential login backoff (design D12), keyed by
// client address and by a global account key. A restart resets it.
//
// An attempt is charged as a failure when it is admitted, before the password
// is verified, and refunded on success. Concurrent attempts therefore see the
// backoff immediately instead of racing past it while Argon2 runs.
type Throttle struct {
	clk clock.Clock
	max time.Duration

	mu        sync.Mutex
	entries   map[string]*throttleEntry
	nextSweep time.Time
}

type throttleEntry struct {
	failures int
	// blockedUntil is when the next attempt is admitted.
	blockedUntil time.Time
}

// NewThrottle returns a throttle whose backoff never exceeds max.
func NewThrottle(clk clock.Clock, max time.Duration) *Throttle {
	if max < throttleBase {
		max = throttleBase
	}
	return &Throttle{clk: clk, max: max, entries: map[string]*throttleEntry{}}
}

func throttleKeys(addr netip.Addr) [2]string {
	return [2]string{"addr:" + addr.String(), globalThrottleKey}
}

// Admit reports whether an attempt from addr may proceed. An admitted
// attempt is charged as a failure until Succeeded refunds it.
func (t *Throttle) Admit(addr netip.Addr) bool {
	now := t.clk.Now()
	keys := throttleKeys(addr)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweep(now)
	for _, k := range keys {
		if e := t.entries[k]; e != nil && now.Before(e.blockedUntil) {
			return false
		}
	}
	for _, k := range keys {
		e := t.entries[k]
		if e == nil || now.Sub(e.blockedUntil) >= t.max {
			// Unknown, or quiet for a whole maximum window: start over.
			e = &throttleEntry{}
			t.entries[k] = e
		}
		e.failures++
		e.blockedUntil = now.Add(t.delay(e.failures))
	}
	return true
}

// Succeeded clears the backoff of addr and of the account.
func (t *Throttle) Succeeded(addr netip.Addr) {
	keys := throttleKeys(addr)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, k := range keys {
		delete(t.entries, k)
	}
}

func (t *Throttle) delay(failures int) time.Duration {
	d := throttleBase
	for i := 1; i < failures; i++ {
		d *= 2
		if d >= t.max {
			return t.max
		}
	}
	return min(d, t.max)
}

// sweep drops entries that have been quiet for a whole maximum window, at
// most once per minute, so the map stays bounded.
func (t *Throttle) sweep(now time.Time) {
	if now.Before(t.nextSweep) {
		return
	}
	t.nextSweep = now.Add(time.Minute)
	for k, e := range t.entries {
		if now.Sub(e.blockedUntil) >= t.max {
			delete(t.entries, k)
		}
	}
}
