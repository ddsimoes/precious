package auth

import (
	"net/netip"
	"testing"
	"time"
)

// Task 8.7: delays double per consecutive failure up to the configured cap,
// both per address and for the account across addresses.
func TestThrottleBackoff(t *testing.T) {
	clk := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	th := NewThrottle(clk, 10*time.Second)
	a := netip.MustParseAddr("203.0.113.7")
	b := netip.MustParseAddr("2001:db8::1")

	for i, want := range []time.Duration{1, 2, 4, 8, 10, 10} {
		want *= time.Second
		if !th.Admit(a) {
			t.Fatalf("attempt %d refused after waiting out the backoff", i+1)
		}
		clk.Advance(want - time.Millisecond)
		if th.Admit(a) {
			t.Fatalf("attempt after failure %d admitted before %v", i+1, want)
		}
		if th.Admit(b) {
			t.Fatal("global account backoff did not apply to another address")
		}
		clk.Advance(time.Millisecond)
	}

	// Success clears both keys.
	if !th.Admit(a) {
		t.Fatal("refused after the window")
	}
	th.Succeeded(a)
	if !th.Admit(a) {
		t.Fatal("refused right after a success")
	}
	th.Succeeded(a)

	// Failures accumulate across waited-out windows, but a quiet period of a
	// whole maximum window forgets them.
	for _, d := range []time.Duration{1, 2, 4} {
		if !th.Admit(b) {
			t.Fatal("refused after waiting out the backoff")
		}
		clk.Advance(d * time.Second)
	}
	if !th.Admit(b) { // fourth consecutive failure: 8s
		t.Fatal("refused after waiting out the backoff")
	}
	clk.Advance(8*time.Second + 10*time.Second)
	if !th.Admit(b) {
		t.Fatal("refused after a quiet period")
	}
	clk.Advance(time.Second - time.Millisecond)
	if th.Admit(b) {
		t.Fatal("admitted within the first 1s window")
	}
	clk.Advance(time.Millisecond)
	if !th.Admit(b) {
		t.Fatal("backoff did not restart at 1s after a quiet period")
	}
}
