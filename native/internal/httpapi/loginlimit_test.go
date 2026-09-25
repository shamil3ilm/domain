package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestLoginLimiter_UnderMax(t *testing.T) {
	l := newLoginLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("a@b", "1.1.1.1"); !ok {
			t.Fatalf("attempt %d should be allowed", i)
		}
		l.recordFailure("a@b", "1.1.1.1")
	}
	// 3 failures accumulated → next attempt denied.
	if ok, _ := l.allow("a@b", "1.1.1.1"); ok {
		t.Fatal("expected deny after 3 failures with max=3")
	}
}

func TestLoginLimiter_RetryAfter(t *testing.T) {
	now := time.Now()
	l := newLoginLimiter(1, 5*time.Minute)
	l.now = func() time.Time { return now }
	// One failure, one more attempt — denied.
	l.recordFailure("a@b", "1.1.1.1")
	ok, retry := l.allow("a@b", "1.1.1.1")
	if ok {
		t.Fatal("expected deny after 1 failure with max=1")
	}
	if retry < 4*time.Minute+59*time.Second || retry > 5*time.Minute {
		t.Errorf("retry-after expected ~5m, got %v", retry)
	}

	// Advance the clock past the window — allowed again.
	l.now = func() time.Time { return now.Add(6 * time.Minute) }
	if ok, _ := l.allow("a@b", "1.1.1.1"); !ok {
		t.Fatal("expected allow after window expired")
	}
}

func TestLoginLimiter_ClearOnSuccess(t *testing.T) {
	l := newLoginLimiter(3, time.Minute)
	l.recordFailure("a@b", "1.1.1.1")
	l.recordFailure("a@b", "1.1.1.1")
	l.clear("a@b", "1.1.1.1")
	// Should be back to fresh state.
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("a@b", "1.1.1.1"); !ok {
			t.Fatalf("after clear, attempt %d should pass", i)
		}
		l.recordFailure("a@b", "1.1.1.1")
	}
}

func TestLoginLimiter_KeysAreCompound(t *testing.T) {
	l := newLoginLimiter(2, time.Minute)
	l.recordFailure("a@b", "1.1.1.1")
	l.recordFailure("a@b", "1.1.1.1")
	// Same email different IP → separate bucket.
	if ok, _ := l.allow("a@b", "2.2.2.2"); !ok {
		t.Error("different IP should have its own bucket")
	}
	// Same IP different email → separate bucket.
	if ok, _ := l.allow("c@d", "1.1.1.1"); !ok {
		t.Error("different email should have its own bucket")
	}
	// Original key still locked.
	if ok, _ := l.allow("a@b", "1.1.1.1"); ok {
		t.Error("original key should still be locked")
	}
}

func TestLoginLimiter_Disabled(t *testing.T) {
	l := &loginLimiter{max: 0} // disabled
	if ok, _ := l.allow("a@b", "1.1.1.1"); !ok {
		t.Fatal("disabled limiter must always allow")
	}
	// recordFailure/clear are no-ops but must not panic.
	l.recordFailure("a@b", "1.1.1.1")
	l.clear("a@b", "1.1.1.1")
}

func TestLoginLimiter_NilReceiver(t *testing.T) {
	var l *loginLimiter
	if ok, _ := l.allow("a@b", "1.1.1.1"); !ok {
		t.Fatal("nil limiter must always allow")
	}
	// Must not panic:
	l.recordFailure("a@b", "1.1.1.1")
	l.clear("a@b", "1.1.1.1")
}

// End-to-end: the login handler returns 429 with Retry-After after N wrong
// passwords, and a valid login after clears the counter.
func TestLoginHandler_LocksOut(t *testing.T) {
	te := newTestEnv(t)
	// The test env's config uses defaults from the New() path — max=5. Fire
	// six wrong-password attempts; the sixth should be 429.
	badBody, _ := json.Marshal(map[string]string{
		"email": "admin@test", "password": "wrong-password",
	})
	for i := 0; i < 5; i++ {
		resp, err := http.Post(te.srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(badBody))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d", i, resp.StatusCode)
		}
	}
	resp, err := http.Post(te.srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(badBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: want 429, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After header")
	}
}
