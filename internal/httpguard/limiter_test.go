package httpguard

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := NewLimiter()
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return at }

	const addr = "192.168.1.9:41234"

	// Five failures are permitted. The sixth attempt is not.
	for i := range maxFailures {
		if !l.Allow(addr) {
			t.Fatalf("attempt %d must be permitted", i+1)
		}
		l.Fail(addr)
	}
	if l.Allow(addr) {
		t.Fatal("the sixth attempt must be refused")
	}

	// Another address is not affected.
	if !l.Allow("192.168.1.10:1") {
		t.Fatal("the limiter must count each address by itself")
	}

	// The port of the same address does not matter.
	if l.Allow("192.168.1.9:59999") {
		t.Fatal("the limiter must ignore the port")
	}

	// A minute later the failures are old and the address may try again.
	at = at.Add(failWindow + time.Second)
	if !l.Allow(addr) {
		t.Fatal("the address must be permitted after the window")
	}
	if len(l.failures) != 0 {
		t.Fatalf("the limiter keeps %d old records", len(l.failures))
	}
}

func TestLimiterWindowSlides(t *testing.T) {
	l := NewLimiter()
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return at }

	const addr = "10.0.0.5:1"
	// Four failures, then a wait of half a minute, then one more failure.
	for range 4 {
		l.Fail(addr)
	}
	at = at.Add(30 * time.Second)
	l.Fail(addr)
	if l.Allow(addr) {
		t.Fatal("five failures in one minute must refuse the next attempt")
	}

	// The first four failures leave the window. One failure stays, so the
	// address may try again.
	at = at.Add(31 * time.Second)
	if !l.Allow(addr) {
		t.Fatal("the address must be permitted when only one failure is recent")
	}
}

// TestLimiterCountsAttemptsThatDidNotEnd covers the burst: many logins arrive
// together, so each one of them asks Allow before any one of them reports a
// failure. The limiter must count the attempt that it permits, or one window
// gives an attacker as many password guesses as it has connections.
func TestLimiterCountsAttemptsThatDidNotEnd(t *testing.T) {
	tests := []struct {
		name string
		// open is the number of attempts that start and do not end.
		open int
		want bool
	}{
		{name: "four attempts in flight", open: maxFailures - 1, want: true},
		{name: "five attempts in flight", open: maxFailures, want: false},
		{name: "more attempts than the limit", open: maxFailures + 3, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewLimiter()
			const addr = "10.0.0.7:1"
			for range tt.open {
				l.Allow(addr)
			}
			if got := l.Allow(addr); got != tt.want {
				t.Fatalf("Allow after %d attempts in flight = %v, want %v", tt.open, got, tt.want)
			}
		})
	}
}

// TestLimiterAttemptsInFlightExpire makes sure that the count above cannot lock
// an address out for good. A caller that asks Allow and then reports nothing
// must cost one window and no more.
func TestLimiterAttemptsInFlightExpire(t *testing.T) {
	l := NewLimiter()
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return at }

	const addr = "10.0.0.8:1"
	for range maxFailures {
		l.Allow(addr)
	}
	if l.Allow(addr) {
		t.Fatal("the sixth attempt must be refused")
	}
	at = at.Add(failWindow + time.Second)
	if !l.Allow(addr) {
		t.Fatal("an attempt that never ended must stop counting after the window")
	}
}

// TestLimiterDropsOldAddresses covers the memory of the limiter. A flood of
// addresses that never come back must not grow the maps for as long as the
// daemon runs: a small device has 512 MB.
func TestLimiterDropsOldAddresses(t *testing.T) {
	l := NewLimiter()
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return at }

	for i := range maxWrites {
		l.Fail(fmt.Sprintf("10.1.%d.%d:1", i/256, i%256))
	}
	// Every one of those addresses is now old. The next batch must take their
	// place, not stand beside them.
	at = at.Add(failWindow + time.Second)
	for i := range maxWrites {
		l.Fail(fmt.Sprintf("10.2.%d.%d:1", i/256, i%256))
	}

	if len(l.failures) > maxWrites {
		t.Fatalf("the limiter keeps %d addresses, want %d or fewer", len(l.failures), maxWrites)
	}
}

func TestLimiterReset(t *testing.T) {
	l := NewLimiter()
	const addr = "10.0.0.6:1"
	for range maxFailures {
		l.Fail(addr)
	}
	if l.Allow(addr) {
		t.Fatal("the address must be refused")
	}
	l.Reset(addr)
	if !l.Allow(addr) {
		t.Fatal("Reset must clear the failures")
	}
}
