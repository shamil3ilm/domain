package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestMetricsEndpoint boots the whole handler stack and hits /metrics,
// asserting that the process metrics and our custom counters are exposed.
// Rounds out coverage: verifies the Prometheus registry is actually wired
// up + the route is unauthenticated.
func TestMetricsEndpoint(t *testing.T) {
	te := newTestEnv(t) // reuses helper from batch_test.go

	// Exercise a few code paths so counters have non-zero values before
	// scraping. Login is already exercised inside newTestEnv.
	resp, _ := te.doJSON(t, http.MethodGet, "/api/v1/zones", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("prep list zones status=%d", resp.StatusCode)
	}

	// Scrape.
	req, err := http.NewRequest(http.MethodGet, te.srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	scr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer scr.Body.Close()
	if scr.StatusCode != 200 {
		t.Fatalf("scrape status=%d", scr.StatusCode)
	}
	body, err := io.ReadAll(scr.Body)
	if err != nil {
		t.Fatal(err)
	}

	must := []string{
		// Ours
		"privatedns_http_requests_total",
		"privatedns_http_request_duration_seconds",
		"privatedns_build_info",
		// Runtime
		"go_goroutines",
		"process_resident_memory_bytes",
	}
	got := string(body)
	for _, s := range must {
		if !strings.Contains(got, s) {
			t.Errorf("scrape missing %q; body start:\n%s", s, got[:min(len(got), 500)])
		}
	}

	// The HTTP request counter should have picked up the login + list-zones
	// calls under real route patterns, not "unknown".
	if !strings.Contains(got, `route="/api/v1/auth/login"`) {
		t.Errorf("expected login route label; body did not contain it")
	}
	if strings.Contains(got, `route="unknown"`) {
		t.Errorf("saw route=\"unknown\" — every request should map to a chi pattern")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
