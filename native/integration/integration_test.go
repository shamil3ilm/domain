//go:build integration

// Package integration spins up the full binary in-process on random ports
// and exercises DNS resolution + the HTTP API end-to-end. Guarded by the
// `integration` build tag so it doesn't run in the default `go test ./...`
// sweep — invoke via `go test -tags integration ./integration/...` (or via
// the make target once added).
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/privatedns/native/internal/bootstrap"
	"github.com/privatedns/native/internal/config"
	"github.com/privatedns/native/internal/dnssrv"
	"github.com/privatedns/native/internal/httpapi"
	"github.com/privatedns/native/internal/store"
)

// stack is a running privatedns instance we tear down at test end.
type stack struct {
	cfg     *config.Config
	dns     *dnssrv.Server
	httpSrv *http.Server
	apiURL  string
	dnsAddr string
}

// bootStack picks free ports, opens a temp SQLite, runs bootstrap, and
// starts both the DNS server and HTTP API in the background. Returns once
// both are actually accepting connections.
func bootStack(t *testing.T) *stack {
	t.Helper()

	// Random ports so parallel `go test` runs don't collide.
	dnsPort, err := freePort()
	if err != nil {
		t.Fatalf("free dns port: %v", err)
	}
	apiPort, err := freePort()
	if err != nil {
		t.Fatalf("free api port: %v", err)
	}

	dir := t.TempDir()

	// Force the recursion ACL to include loopback so the test can exercise
	// the forward path. Also allow queries from anywhere so the client
	// (also on loopback) isn't refused.
	cfg := &config.Config{
		DataDir:            dir,
		PrivateTLD:         "test",
		DNSAddr:            fmt.Sprintf("127.0.0.1:%d", dnsPort),
		APIAddr:            fmt.Sprintf("127.0.0.1:%d", apiPort),
		Upstreams:          []string{"1.1.1.1:53"},
		AdminEmail:         "admin@integration",
		AdminPassword:      "integration-test-pw-12345",
		AllowRecursionFrom: []string{"127.0.0.0/8"},
		DNSRateLimitPerSec: 1000, // irrelevant here, but disables 20 rps default
		DNSRateLimitBurst:  1000,
		DNSRateLimitExempt: []string{"127.0.0.0/8"},
		LoginMaxAttempts:   5,
		LoginLockoutWindow: 15 * time.Minute,
		JWTSecret:          "integration-secret-not-for-prod-use-only-a-test",
	}

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := bootstrap.Run(context.Background(), st, cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	dnsSrv := dnssrv.New(st, cfg)
	dnsCtx, dnsCancel := context.WithCancel(context.Background())
	go func() {
		if err := dnsSrv.Start(dnsCtx); err != nil {
			t.Logf("dns.start returned: %v", err)
		}
	}()
	t.Cleanup(func() {
		dnsCancel()
		dnsSrv.Shutdown()
	})

	apiHandler := httpapi.New(st, cfg)
	httpSrv := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           apiHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Logf("http.serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	})

	s := &stack{
		cfg:     cfg,
		dns:     dnsSrv,
		httpSrv: httpSrv,
		apiURL:  "http://" + cfg.APIAddr,
		dnsAddr: cfg.DNSAddr,
	}

	// Wait for both endpoints to accept connections (bounded so a bad boot
	// doesn't hang the test suite).
	if err := waitTCP(cfg.APIAddr, 5*time.Second); err != nil {
		t.Fatalf("api never came up: %v", err)
	}
	if err := waitTCP(cfg.DNSAddr, 5*time.Second); err != nil {
		t.Fatalf("dns never came up: %v", err)
	}
	return s
}

// freePort asks the kernel for a random free port by binding :0 briefly.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitTCP polls until the address accepts a TCP connection or timeout.
func waitTCP(addr string, d time.Duration) error {
	deadline := time.Now().Add(d)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout dialing %s: %w", addr, lastErr)
}

// login returns a JWT for the bootstrap admin.
func (s *stack) login(t *testing.T) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"email":    s.cfg.AdminEmail,
		"password": s.cfg.AdminPassword,
	})
	resp, err := http.Post(s.apiURL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login: status=%d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Token
}

func (s *stack) doAuthed(t *testing.T, tok, method, path string, body any) *http.Response {
	t.Helper()
	var buf *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewReader(b)
	} else {
		buf = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, s.apiURL+path, buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// digA queries the stack's DNS listener over UDP.
func (s *stack) digA(t *testing.T, name string) *dns.Msg {
	t.Helper()
	c := &dns.Client{Timeout: 3 * time.Second}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	resp, _, err := c.Exchange(m, s.dnsAddr)
	if err != nil {
		t.Fatalf("dig %s: %v", name, err)
	}
	return resp
}

// -------------------------------------------------------------------- tests

func TestIntegration_FullLoop(t *testing.T) {
	s := bootStack(t)
	tok := s.login(t)

	// 1. Create a zone.
	resp := s.doAuthed(t, tok, http.MethodPost, "/api/v1/zones",
		map[string]string{"name": "example.test"})
	if resp.StatusCode != 201 {
		t.Fatalf("create zone: status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Add an A record.
	resp = s.doAuthed(t, tok, http.MethodPut, "/api/v1/zones/example.test/records",
		map[string]any{"name": "www", "type": "A", "value": "10.42.0.1", "ttl": 60})
	if resp.StatusCode != 200 {
		t.Fatalf("upsert record: status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// 3. Query DNS: expect the A record we just wrote.
	got := s.digA(t, "www.example.test")
	if got.Rcode != dns.RcodeSuccess {
		t.Fatalf("dig rcode: %s", dns.RcodeToString[got.Rcode])
	}
	if len(got.Answer) == 0 {
		t.Fatalf("dig returned no answer")
	}
	a, ok := got.Answer[0].(*dns.A)
	if !ok || a.A.String() != "10.42.0.1" {
		t.Fatalf("dig returned unexpected answer: %v", got.Answer)
	}

	// 4. Batch upsert two records atomically.
	resp = s.doAuthed(t, tok, http.MethodPut, "/api/v1/zones/example.test/records:batch",
		map[string]any{
			"records": []map[string]any{
				{"name": "a", "type": "A", "value": "10.42.0.10"},
				{"name": "b", "type": "A", "value": "10.42.0.11"},
			},
		})
	if resp.StatusCode != 200 {
		t.Fatalf("batch upsert: status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	if s.digA(t, "a.example.test").Answer[0].(*dns.A).A.String() != "10.42.0.10" {
		t.Error("batch record a did not resolve")
	}
	if s.digA(t, "b.example.test").Answer[0].(*dns.A).A.String() != "10.42.0.11" {
		t.Error("batch record b did not resolve")
	}

	// 5. Delete the original record and confirm NXDOMAIN.
	resp = s.doAuthed(t, tok, http.MethodDelete,
		"/api/v1/zones/example.test/records?name=www.example.test&type=A", nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete: status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	nx := s.digA(t, "www.example.test")
	if nx.Rcode != dns.RcodeNameError {
		t.Errorf("expected NXDOMAIN after delete, got %s", dns.RcodeToString[nx.Rcode])
	}

	// 6. Metrics endpoint reachable and reports the queries.
	mResp, err := http.Get(s.apiURL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer mResp.Body.Close()
	if mResp.StatusCode != 200 {
		t.Fatalf("metrics: status=%d", mResp.StatusCode)
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(mResp.Body)
	metrics := buf.String()
	if !strings.Contains(metrics, "privatedns_dns_queries_total") {
		t.Error("metrics missing dns_queries_total")
	}
	if !strings.Contains(metrics, "privatedns_http_requests_total") {
		t.Error("metrics missing http_requests_total")
	}
}

func TestIntegration_LoginLockout(t *testing.T) {
	s := bootStack(t)
	bad, _ := json.Marshal(map[string]string{
		"email":    s.cfg.AdminEmail,
		"password": "wrong-password",
	})
	for i := 0; i < s.cfg.LoginMaxAttempts; i++ {
		resp, err := http.Post(s.apiURL+"/api/v1/auth/login",
			"application/json", bytes.NewReader(bad))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d want 401, got %d", i, resp.StatusCode)
		}
	}
	resp, err := http.Post(s.apiURL+"/api/v1/auth/login",
		"application/json", bytes.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429 after N failures, got %d", resp.StatusCode)
	}
	if retry := resp.Header.Get("Retry-After"); retry == "" {
		t.Error("429 missing Retry-After")
	} else if n, err := strconv.Atoi(retry); err != nil || n < 1 {
		t.Errorf("Retry-After should be a positive integer, got %q", retry)
	}
}
