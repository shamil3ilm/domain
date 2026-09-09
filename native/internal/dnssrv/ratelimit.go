package dnssrv

import (
	"net"
	"sync"
	"time"
)

// Clock is the time source the limiter reads. Production wires stdClock;
// tests inject a fake to keep behavior deterministic.
type Clock interface {
	Now() time.Time
}

type stdClock struct{}

func (stdClock) Now() time.Time { return time.Now() }

// FakeClock lets tests advance time explicitly. Safe for concurrent use.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{now: t} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// RateLimiter is a per-source-IP token-bucket limiter. Callers ask Allow()
// with the source IP. Exempt CIDRs bypass entirely and never consume tokens.
// Buckets are held in a bounded map; when the cap is exceeded, buckets that
// haven't been touched for a while are evicted opportunistically.
type RateLimiter struct {
	clock       Clock
	ratePerSec  float64
	burst       float64
	exempt      []*net.IPNet
	maxBuckets  int
	idleTimeout time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
	dropped uint64 // debug metric — bumped when a query is dropped
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
	lastUsed   time.Time
}

// RateLimiterConfig configures a RateLimiter. Zero/negative rate disables
// limiting entirely (Allow always returns true).
type RateLimiterConfig struct {
	PerSecond   float64
	Burst       int
	ExemptCIDRs []*net.IPNet
	// MaxBuckets caps concurrent tracked sources. Buckets exceeding
	// IdleTimeout without traffic are evicted first. Zero → 65 536.
	MaxBuckets  int
	IdleTimeout time.Duration
	Clock       Clock
}

// NewRateLimiter returns a limiter. If cfg.PerSecond <= 0, the returned
// limiter admits everything — used to represent the "disabled" state.
func NewRateLimiter(cfg RateLimiterConfig) *RateLimiter {
	if cfg.Clock == nil {
		cfg.Clock = stdClock{}
	}
	if cfg.MaxBuckets <= 0 {
		cfg.MaxBuckets = 65536
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 10 * time.Minute
	}
	return &RateLimiter{
		clock:       cfg.Clock,
		ratePerSec:  cfg.PerSecond,
		burst:       float64(cfg.Burst),
		exempt:      cfg.ExemptCIDRs,
		maxBuckets:  cfg.MaxBuckets,
		idleTimeout: cfg.IdleTimeout,
		buckets:     make(map[string]*bucket),
	}
}

// Allow reports whether a query from ip may proceed. Exempt IPs always pass
// and never consume tokens. A nil ip is treated as non-exempt with key "".
func (r *RateLimiter) Allow(ip net.IP) bool {
	// A nil-configured limiter (disabled): pass through.
	if r == nil || r.ratePerSec <= 0 {
		return true
	}

	// Exempt sources: bypass entirely.
	if ip != nil {
		for _, cidr := range r.exempt {
			if cidr.Contains(ip) {
				return true
			}
		}
	}

	key := ""
	if ip != nil {
		key = ip.String()
	}

	now := r.clock.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	b := r.buckets[key]
	if b == nil {
		if len(r.buckets) >= r.maxBuckets {
			r.evictLocked(now)
		}
		b = &bucket{tokens: r.burst, lastRefill: now}
		r.buckets[key] = b
	} else {
		// Refill: elapsed * rate, capped at burst.
		elapsed := now.Sub(b.lastRefill).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * r.ratePerSec
			if b.tokens > r.burst {
				b.tokens = r.burst
			}
			b.lastRefill = now
		}
	}
	b.lastUsed = now

	if b.tokens < 1 {
		r.dropped++
		return false
	}
	b.tokens--
	return true
}

// Dropped returns the cumulative count of dropped queries — useful for
// metrics/debug logging. Safe for concurrent use.
func (r *RateLimiter) Dropped() uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// evictLocked drops idle buckets. Caller holds r.mu.
func (r *RateLimiter) evictLocked(now time.Time) {
	for k, b := range r.buckets {
		if now.Sub(b.lastUsed) > r.idleTimeout {
			delete(r.buckets, k)
		}
	}
	// If we're still at cap after idle eviction (attack from many fresh IPs),
	// drop half the map at random to bound memory. Under sustained abuse
	// this trades limiter accuracy for memory safety, which is the right
	// call for an authoritative DNS server.
	if len(r.buckets) >= r.maxBuckets {
		i := 0
		for k := range r.buckets {
			delete(r.buckets, k)
			i++
			if i >= r.maxBuckets/2 {
				break
			}
		}
	}
}
