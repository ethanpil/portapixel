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

// Limiter counts failed logins for each remote address. It makes a password
// guess attack slow without a lock-out that an attacker could use to keep the
// true admin out for a long time.
type Limiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	now      func() time.Time
}

// NewLimiter makes an empty limiter.
func NewLimiter() *Limiter {
	return &Limiter{failures: make(map[string][]time.Time), now: time.Now}
}

// Allow reports if this address may try to log in now.
func (l *Limiter) Allow(remoteAddr string) bool {
	key := limiterKey(remoteAddr)

	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.recent(key)) < maxFailures
}

// Fail records one failed login.
func (l *Limiter) Fail(remoteAddr string) {
	key := limiterKey(remoteAddr)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.failures[key] = append(l.recent(key), l.now())
}

// Reset removes the failures of an address. The caller calls it after a good
// login, so that one wrong keystroke does not count against the admin later.
func (l *Limiter) Reset(remoteAddr string) {
	key := limiterKey(remoteAddr)

	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.failures, key)
}

// recent gives the failures of key that are inside the window and drops the
// older ones. The caller holds the lock.
func (l *Limiter) recent(key string) []time.Time {
	cut := l.now().Add(-failWindow)
	kept := l.failures[key][:0]
	for _, t := range l.failures[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
		return nil
	}
	l.failures[key] = kept
	return kept
}

// limiterKey gives the address without the port, because the port changes with
// each connection.
func limiterKey(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}
