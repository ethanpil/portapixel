package httpguard

import (
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
