package httpguard

import (
	"net"
	"sync"
	"time"
)

// Limits of the login limiter: five failed attempts from one address in one
// minute, then no more attempts until the oldest failure is a minute old.
const (
	maxFailures = 5
	failWindow  = time.Minute
)

// maxWrites is the number of records that the limiter takes before it drops the
// addresses that have no recent record. One pass costs one step for each
// address, and it runs once in maxWrites records, so the cost stays small.
const maxWrites = 1024

// maxKeys is the number of addresses that the limiter holds. A pass runs at this
// size as well as every maxWrites records, so a caller that arrives from a new
// address each time cannot grow the map for a whole window. A homelab fleet is a
// few hundred addresses, so the value is far above any real load.
const maxKeys = 20000

// The limits of the pending-enrollment limiter: five new screens from one address
// in one hour. An open route that makes a row must have a limit of its own, and a
// batch of cards behind one address still gets five new screens an hour, which is
// a rate that a person can follow.
const (
	maxPending    = 5
	pendingWindow = time.Hour
)

// Limiter counts failed logins for each remote address. It makes a password
// guess attack slow without a lock-out that an attacker could use to keep the
// true admin out for a long time.
type Limiter struct {
	// max is the number of records that close the address, and window is how long
	// a record counts.
	max    int
	window time.Duration

	mu       sync.Mutex
	failures map[string][]time.Time
	// open holds the attempts that Allow permitted and that did not end yet. An
	// open attempt counts against the limit. Without it a burst of requests that
	// arrive together would all pass the check, because each one of them reads
	// the count before any one of them reports a failure.
	open   map[string][]time.Time
	writes int
	now    func() time.Time
}

// NewLimiter makes an empty limiter with the login limits: five records a minute.
func NewLimiter() *Limiter { return newLimiter(maxFailures, failWindow) }

// NewPendingLimiter makes a limiter for the enroll requests that make a row in the
// pending list: five an hour from one address.
func NewPendingLimiter() *Limiter { return newLimiter(maxPending, pendingWindow) }

func newLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{
		max:      max,
		window:   window,
		failures: make(map[string][]time.Time),
		open:     make(map[string][]time.Time),
		now:      time.Now,
	}
}

// Allow reports if this address may try to log in now. It counts the attempt
// that it permits. Fail or Reset ends that attempt; an attempt that ends in no
// other way stops counting after one window.
func (l *Limiter) Allow(remoteAddr string) bool {
	key := limiterKey(remoteAddr)

	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.recent(l.failures, key))+len(l.recent(l.open, key)) >= l.max {
		return false
	}
	l.open[key] = append(l.open[key], l.now())
	l.sweep()
	return true
}

// Fail records one failed login. It ends the open attempt of the address, so
// that one failed login counts one time.
func (l *Limiter) Fail(remoteAddr string) {
	key := limiterKey(remoteAddr)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.endOpen(key)
	l.failures[key] = append(l.recent(l.failures, key), l.now())
	l.sweep()
}

// Done ends the open attempt of an address and records nothing.
//
// The caller uses it on a path that is neither a failure nor a proof: an enroll
// request with no token that worked is one. Without it the attempt that Allow
// counted stays open for a whole window, so five such requests would lock the
// address out although nothing went wrong.
func (l *Limiter) Done(remoteAddr string) {
	key := limiterKey(remoteAddr)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.endOpen(key)
}

// Reset removes the failures of an address. The caller calls it after a good
// login, so that one wrong keystroke does not count against the admin later.
func (l *Limiter) Reset(remoteAddr string) {
	key := limiterKey(remoteAddr)

	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.failures, key)
	delete(l.open, key)
}

// recent gives the times of key in m that are inside the window and drops the
// older ones. The caller holds the lock.
func (l *Limiter) recent(m map[string][]time.Time, key string) []time.Time {
	cut := l.now().Add(-l.window)
	kept := m[key][:0]
	for _, t := range m[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(m, key)
		return nil
	}
	m[key] = kept
	return kept
}

// endOpen ends the oldest open attempt of key. The caller holds the lock. A Fail
// with no open attempt is not a fault: a caller may count a failure that it
// found in another way.
func (l *Limiter) endOpen(key string) {
	open := l.recent(l.open, key)
	if len(open) == 0 {
		return
	}
	if len(open) == 1 {
		delete(l.open, key)
		return
	}
	l.open[key] = open[1:]
}

// sweep drops every address that has no recent record. recent works on one
// address only, so without this pass an address that never comes back keeps its
// record for as long as the daemon runs: one failed login from each address of
// one network would fill the memory of a small device. The caller holds the
// lock.
func (l *Limiter) sweep() {
	l.writes++
	if l.writes < maxWrites && len(l.failures)+len(l.open) < maxKeys {
		return
	}
	l.writes = 0
	for _, m := range []map[string][]time.Time{l.failures, l.open} {
		for key := range m {
			l.recent(m, key)
		}
	}
}

// v6Prefix is the number of bytes of an IPv6 address that make one bucket. Eight
// bytes are a /64, which is the smallest block that a provider gives to one
// customer.
const v6Prefix = 8

// limiterKey gives the bucket of an address.
//
// The port goes away, because it changes with each connection. It uses the one
// helper of the package, so that the limiter and the host allowlist never disagree
// about what one address is.
//
// An IPv6 address counts by its /64 and not by its full 128 bits. One customer
// holds a /64 or more, and a source address inside that block costs the caller
// nothing, so a limiter that keyed the whole address would give an attacker 2^64
// buckets and no limit at all. An IPv4 address counts whole.
func limiterKey(remoteAddr string) string {
	host := HostOf(remoteAddr)
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(v6Prefix*8, 128)).String() + "/64"
}
