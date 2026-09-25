package httpapi

import (
	"strings"
	"sync"
	"time"
)

// loginLimiter tracks recent failed login attempts keyed by (email, source
// IP). Once a key crosses the configured attempt threshold within the
// window, every further attempt is refused with 429 until the earliest
// tracked failure ages out. A successful login clears the key.
//
// Separate from the general per-IP rate limiter because:
//   - The general limiter allows ~120 rpm — plenty of headroom for a
//     brute-force script.
//   - Keying on IP alone lets an attacker rotate emails without penalty;
//     keying on email alone lets one attacker DoS a real user from many
//     IPs. Compound (email, IP) is the reasonable middle.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptWindow
	max      int
	window   time.Duration
	now      func() time.Time // injectable for tests
}

type attemptWindow struct {
	// timestamps of the failed attempts still inside the window. Kept as a
	// slice because N is small (bounded by max) and iterating a slice on
	// every check is cheaper than a heap.
	times []time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	if max <= 0 {
		max = 5
	}
	if window <= 0 {
		window = 15 * time.Minute
	}
	return &loginLimiter{
		attempts: map[string]*attemptWindow{},
		max:      max,
		window:   window,
		now:      time.Now,
	}
}

func loginKey(email, ip string) string {
	return strings.ToLower(strings.TrimSpace(email)) + "|" + ip
}

// allow returns (allowed, retryAfter). When !allowed the caller responds 429
// with Retry-After: retryAfter (integer seconds, always ≥ 1).
func (l *loginLimiter) allow(email, ip string) (bool, time.Duration) {
	if l == nil || l.max <= 0 {
		return true, 0
	}
	key := loginKey(email, ip)
	now := l.now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.attempts[key]
	if w == nil {
		return true, 0
	}
	// Drop expired timestamps.
	fresh := w.times[:0]
	for _, t := range w.times {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	w.times = fresh
	if len(w.times) == 0 {
		delete(l.attempts, key)
		return true, 0
	}
	if len(w.times) < l.max {
		return true, 0
	}
	// Locked out. Retry-After = when the oldest tracked failure ages out.
	retry := w.times[0].Add(l.window).Sub(now)
	if retry < time.Second {
		retry = time.Second
	}
	return false, retry
}

// recordFailure adds a timestamp for (email, ip). Idempotent-friendly: two
// concurrent failures both record.
func (l *loginLimiter) recordFailure(email, ip string) {
	if l == nil || l.max <= 0 {
		return
	}
	key := loginKey(email, ip)
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.attempts[key]
	if w == nil {
		w = &attemptWindow{}
		l.attempts[key] = w
	}
	w.times = append(w.times, now)
	// Cap at max — anything above is redundant for the allow() decision.
	if len(w.times) > l.max {
		w.times = w.times[len(w.times)-l.max:]
	}
}

// clear removes the counter for (email, ip). Called on successful login so
// the user isn't punished for a typo followed by a correct password.
func (l *loginLimiter) clear(email, ip string) {
	if l == nil {
		return
	}
	key := loginKey(email, ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
