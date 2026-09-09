package dnssrv

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cidr(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("bad CIDR %q: %v", s, err)
	}
	return n
}

func TestRateLimiter_BurstThenDrop(t *testing.T) {
	clk := NewFakeClock(time.Unix(0, 0))
	rl := NewRateLimiter(RateLimiterConfig{
		PerSecond: 10, Burst: 5, Clock: clk,
	})
	ip := net.ParseIP("192.0.2.1")

	// Burst = 5 → first 5 pass, next N drop.
	pass, drop := 0, 0
	for i := 0; i < 10; i++ {
		if rl.Allow(ip) {
			pass++
		} else {
			drop++
		}
	}
	if pass != 5 || drop != 5 {
		t.Fatalf("want pass=5 drop=5, got pass=%d drop=%d", pass, drop)
	}
	if got := rl.Dropped(); got != 5 {
		t.Errorf("Dropped()=%d, want 5", got)
	}
}

func TestRateLimiter_LoopbackExempt(t *testing.T) {
	clk := NewFakeClock(time.Unix(0, 0))
	rl := NewRateLimiter(RateLimiterConfig{
		PerSecond:   10,
		Burst:       1,
		ExemptCIDRs: []*net.IPNet{cidr(t, "127.0.0.0/8"), cidr(t, "::1/128")},
		Clock:       clk,
	})
	// Exempt IP: unlimited passes, no drops recorded.
	ip := net.ParseIP("127.0.0.1")
	for i := 0; i < 100; i++ {
		if !rl.Allow(ip) {
			t.Fatalf("exempt ip should always pass; failed at i=%d", i)
		}
	}
	if got := rl.Dropped(); got != 0 {
		t.Errorf("exempt should never bump Dropped, got %d", got)
	}
}

func TestRateLimiter_ConfiguredExemptCIDR(t *testing.T) {
	clk := NewFakeClock(time.Unix(0, 0))
	rl := NewRateLimiter(RateLimiterConfig{
		PerSecond:   1,
		Burst:       1,
		ExemptCIDRs: []*net.IPNet{cidr(t, "10.10.0.0/16")},
		Clock:       clk,
	})
	exempt := net.ParseIP("10.10.5.5")
	limited := net.ParseIP("8.8.8.8")

	for i := 0; i < 50; i++ {
		if !rl.Allow(exempt) {
			t.Fatalf("10.10.5.5 should be exempt; failed at i=%d", i)
		}
	}
	// Non-exempt: burst=1, second query drops.
	if !rl.Allow(limited) {
		t.Fatal("first query from limited IP should pass")
	}
	if rl.Allow(limited) {
		t.Fatal("second query from limited IP should drop (burst=1)")
	}
}

func TestRateLimiter_RefillOverTime(t *testing.T) {
	clk := NewFakeClock(time.Unix(0, 0))
	rl := NewRateLimiter(RateLimiterConfig{PerSecond: 10, Burst: 5, Clock: clk})
	ip := net.ParseIP("192.0.2.2")

	// Drain the bucket.
	for i := 0; i < 5; i++ {
		if !rl.Allow(ip) {
			t.Fatalf("initial burst should pass; failed at i=%d", i)
		}
	}
	if rl.Allow(ip) {
		t.Fatal("bucket should be empty")
	}

	// Advance 500ms → 10 QPS * 0.5s = 5 tokens back (capped at burst=5).
	clk.Advance(500 * time.Millisecond)

	pass := 0
	for i := 0; i < 5; i++ {
		if rl.Allow(ip) {
			pass++
		}
	}
	if pass != 5 {
		t.Errorf("after 500ms refill, expected 5 tokens available, got %d", pass)
	}
}

func TestRateLimiter_IndependentBucketsPerIP(t *testing.T) {
	clk := NewFakeClock(time.Unix(0, 0))
	rl := NewRateLimiter(RateLimiterConfig{PerSecond: 1, Burst: 1, Clock: clk})

	for i := 0; i < 3; i++ {
		ip := net.IPv4(192, 0, 2, byte(i))
		if !rl.Allow(ip) {
			t.Fatalf("first query from unique ip %v should pass", ip)
		}
		if rl.Allow(ip) {
			t.Fatalf("second query from same ip %v should drop", ip)
		}
	}
}

func TestRateLimiter_Disabled(t *testing.T) {
	// PerSecond=0 → limiter is a no-op (passthrough for everyone).
	rl := NewRateLimiter(RateLimiterConfig{PerSecond: 0, Burst: 0})
	ip := net.ParseIP("8.8.8.8")
	for i := 0; i < 1000; i++ {
		if !rl.Allow(ip) {
			t.Fatalf("disabled limiter must always pass")
		}
	}
}

func TestRateLimiter_NilReceiver(t *testing.T) {
	var rl *RateLimiter
	if !rl.Allow(net.ParseIP("1.1.1.1")) {
		t.Fatal("nil limiter must always pass")
	}
	if got := rl.Dropped(); got != 0 {
		t.Errorf("nil Dropped()=%d, want 0", got)
	}
}

func TestRateLimiter_Concurrency(t *testing.T) {
	// Race detector should surface any concurrent map/state issues.
	clk := NewFakeClock(time.Unix(0, 0))
	rl := NewRateLimiter(RateLimiterConfig{PerSecond: 1000, Burst: 100, Clock: clk})

	var pass, drop atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ip := net.IPv4(10, 0, byte(id), 1)
			for j := 0; j < 200; j++ {
				if rl.Allow(ip) {
					pass.Add(1)
				} else {
					drop.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()
	if pass.Load() == 0 {
		t.Error("expected at least some passes under concurrency")
	}
	// No assertion on exact drop count — the point is race-freedom.
}

func TestRateLimiter_EvictsIdleBuckets(t *testing.T) {
	clk := NewFakeClock(time.Unix(0, 0))
	rl := NewRateLimiter(RateLimiterConfig{
		PerSecond:   10,
		Burst:       5,
		MaxBuckets:  4,
		IdleTimeout: time.Minute,
		Clock:       clk,
	})
	// Fill 4 buckets.
	for i := 1; i <= 4; i++ {
		rl.Allow(net.IPv4(192, 0, 2, byte(i)))
	}
	// Move past idle timeout.
	clk.Advance(2 * time.Minute)
	// A fifth IP would trip capacity; the eviction pass should clear idles.
	rl.Allow(net.IPv4(198, 51, 100, 1))

	rl.mu.Lock()
	sz := len(rl.buckets)
	rl.mu.Unlock()
	if sz > rl.maxBuckets {
		t.Errorf("bucket map exceeded cap: %d > %d", sz, rl.maxBuckets)
	}
}
