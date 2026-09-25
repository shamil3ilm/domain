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
	"github.com/privatedns/native/internal/tlscerts"

	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
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

// TestIntegration_AXFR: seed a zone via the API, then act as a secondary
// DNS server and pull the whole zone via AXFR. Assert the SOA framing is
// correct and every record we wrote comes back.
func TestIntegration_AXFR(t *testing.T) {
	s := bootStack(t)
	// The default bootStack config has AXFRAllowFrom empty (deny). Reboot
	// on top of the same store isn't possible mid-test; instead, monkey-
	// patch the server's ACL by rebuilding a Server with an allow list.
	//
	// Simpler and honest: create a second bootstrap that allows loopback.
	_ = s // unused — we build our own here so the ACL is right.

	dir := t.TempDir()
	apiPort, _ := freePort()
	dnsPort, _ := freePort()
	cfg := &config.Config{
		DataDir:            dir,
		PrivateTLD:         "test",
		DNSAddr:            fmt.Sprintf("127.0.0.1:%d", dnsPort),
		APIAddr:            fmt.Sprintf("127.0.0.1:%d", apiPort),
		AdminEmail:         "admin@integration",
		AdminPassword:      "integration-axfr-pw-12345",
		AllowRecursionFrom: []string{"127.0.0.0/8"},
		AXFRAllowFrom:      []string{"127.0.0.0/8"},
		DNSRateLimitPerSec: 1000,
		DNSRateLimitBurst:  1000,
		DNSRateLimitExempt: []string{"127.0.0.0/8"},
		LoginMaxAttempts:   5,
		LoginLockoutWindow: 15 * time.Minute,
		JWTSecret:          "integration-secret-axfr",
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := bootstrap.Run(context.Background(), st, cfg); err != nil {
		t.Fatal(err)
	}

	dnsSrv := dnssrv.New(st, cfg)
	dnsCtx, dnsCancel := context.WithCancel(context.Background())
	go func() { _ = dnsSrv.Start(dnsCtx) }()
	t.Cleanup(func() { dnsCancel(); dnsSrv.Shutdown() })

	httpSrv := &http.Server{Addr: cfg.APIAddr, Handler: httpapi.New(st, cfg), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpSrv.ListenAndServe() }()
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	})
	if err := waitTCP(cfg.APIAddr, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitTCP(cfg.DNSAddr, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	// Log in + seed a few records.
	loginBody, _ := json.Marshal(map[string]string{
		"email": cfg.AdminEmail, "password": cfg.AdminPassword,
	})
	lr, err := http.Post("http://"+cfg.APIAddr+"/api/v1/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	var tokPayload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(lr.Body).Decode(&tokPayload); err != nil {
		t.Fatal(err)
	}
	lr.Body.Close()

	authed := func(method, path string, body any) *http.Response {
		var r *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		} else {
			r = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, "http://"+cfg.APIAddr+path, r)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokPayload.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if r := authed(http.MethodPost, "/api/v1/zones",
		map[string]string{"name": "axfr.test"}); r.StatusCode != 201 {
		t.Fatalf("create zone status=%d", r.StatusCode)
	}
	seed := []map[string]any{
		{"name": "www", "type": "A", "value": "10.99.0.1"},
		{"name": "api", "type": "A", "value": "10.99.0.2"},
		{"name": "mx", "type": "MX", "value": "10 mail.axfr.test."},
	}
	for _, rec := range seed {
		if r := authed(http.MethodPut, "/api/v1/zones/axfr.test/records", rec); r.StatusCode != 200 {
			t.Fatalf("upsert %v status=%d", rec, r.StatusCode)
		}
	}

	// Perform an AXFR as a loopback client would. miekg's Transfer.In gives
	// us the envelope stream directly.
	tr := new(dns.Transfer)
	m := new(dns.Msg)
	m.SetAxfr("axfr.test.")
	envelopes, err := tr.In(m, cfg.DNSAddr)
	if err != nil {
		t.Fatalf("axfr.In: %v", err)
	}

	var rrs []dns.RR
	for env := range envelopes {
		if env.Error != nil {
			t.Fatalf("envelope error: %v", env.Error)
		}
		rrs = append(rrs, env.RR...)
	}

	// Contract: SOA at the head, SOA at the tail, our three records in between.
	if len(rrs) < 5 {
		t.Fatalf("expected >=5 RRs (SOA + 3 + SOA), got %d", len(rrs))
	}
	if _, ok := rrs[0].(*dns.SOA); !ok {
		t.Errorf("first RR should be SOA, got %T", rrs[0])
	}
	if _, ok := rrs[len(rrs)-1].(*dns.SOA); !ok {
		t.Errorf("last RR should be SOA, got %T", rrs[len(rrs)-1])
	}

	// Every seeded value shows up somewhere in the middle.
	got := ""
	for _, rr := range rrs {
		got += rr.String() + "\n"
	}
	for _, want := range []string{"10.99.0.1", "10.99.0.2", "mail.axfr.test."} {
		if !strings.Contains(got, want) {
			t.Errorf("AXFR output missing %q\nfull dump:\n%s", want, got)
		}
	}
}

// TestIntegration_AXFR_Refused: a client outside the AXFR ACL gets REFUSED.
// Our loopback ACL trick means we prove this by leaving the default empty
// AXFRAllowFrom in place — every source (including loopback) is denied.
func TestIntegration_AXFR_Refused(t *testing.T) {
	s := bootStack(t) // default AXFRAllowFrom is empty
	tr := new(dns.Transfer)
	m := new(dns.Msg)
	m.SetAxfr("myworld.")
	envelopes, err := tr.In(m, s.dnsAddr)
	if err != nil {
		// A miekg-side error is also an acceptable "no dice" — server may
		// close the connection or return REFUSED. Both mean transfer denied.
		return
	}
	for env := range envelopes {
		if env.Error != nil {
			return // acceptable: server signalled failure through the channel.
		}
		// If we get here with real RRs, the ACL is broken.
		if len(env.RR) > 0 {
			t.Fatalf("expected AXFR to be refused, but received %d RRs", len(env.RR))
		}
	}
}

// TestIntegration_HTTPS: same auth+CRUD flow but the API is served over
// HTTPS using a self-signed cert generated on first boot. The client trusts
// only that specific cert — no InsecureSkipVerify — so the test proves the
// operator-facing SHA-256 fingerprint story really works end to end.
func TestIntegration_HTTPS(t *testing.T) {
	dir := t.TempDir()
	apiPort, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	dnsPort, err := freePort()
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		DataDir:            dir,
		PrivateTLD:         "test",
		DNSAddr:            fmt.Sprintf("127.0.0.1:%d", dnsPort),
		APIAddr:            fmt.Sprintf("127.0.0.1:%d", apiPort),
		AdminEmail:         "admin@integration",
		AdminPassword:      "integration-tls-pw-12345",
		AllowRecursionFrom: []string{"127.0.0.0/8"},
		DNSRateLimitPerSec: 1000,
		DNSRateLimitBurst:  1000,
		DNSRateLimitExempt: []string{"127.0.0.0/8"},
		LoginMaxAttempts:   5,
		LoginLockoutWindow: 15 * time.Minute,
		JWTSecret:          "integration-secret-tls",
		APITLSMode:         "auto",
		APITLSHosts:        []string{"127.0.0.1", "localhost"},
	}

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := bootstrap.Run(context.Background(), st, cfg); err != nil {
		t.Fatal(err)
	}

	pair, _, err := tlscerts.EnsureSelfSigned(cfg.DataDir, cfg.APITLSHosts)
	if err != nil {
		t.Fatalf("gen self-signed: %v", err)
	}

	// Start the DNS side so bootStack invariants (shutdown wiring, etc.) hold.
	dnsSrv := dnssrv.New(st, cfg)
	dnsCtx, dnsCancel := context.WithCancel(context.Background())
	go func() { _ = dnsSrv.Start(dnsCtx) }()
	t.Cleanup(func() {
		dnsCancel()
		dnsSrv.Shutdown()
	})

	httpSrv := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           httpapi.New(st, cfg),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = httpSrv.ListenAndServeTLS(pair.CertPath, pair.KeyPath) }()
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	})
	if err := waitTCP(cfg.APIAddr, 5*time.Second); err != nil {
		t.Fatalf("api never came up: %v", err)
	}

	// Build a client that trusts only the server's own cert. NO
	// InsecureSkipVerify — verification must succeed against the pool.
	certBytes, err := os.ReadFile(pair.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certBytes)
	if block == nil {
		t.Fatal("no PEM block in server cert")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}

	apiURL := "https://" + cfg.APIAddr

	// Log in over HTTPS.
	body, _ := json.Marshal(map[string]string{
		"email":    cfg.AdminEmail,
		"password": cfg.AdminPassword,
	})
	resp, err := client.Post(apiURL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login over HTTPS: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("login: status=%d", resp.StatusCode)
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Any authed endpoint proves the full HTTPS stack works.
	req, _ := http.NewRequest(http.MethodGet, apiURL+"/api/v1/zones", nil)
	req.Header.Set("Authorization", "Bearer "+lr.Token)
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatalf("list zones over HTTPS: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("list zones: status=%d", resp2.StatusCode)
	}
}
