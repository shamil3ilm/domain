package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/privatedns/native/internal/config"
	"github.com/privatedns/native/internal/metrics"
	"github.com/privatedns/native/internal/store"
)

// helper: spin up an in-memory-ish server with a fresh SQLite file, seed one
// zone + admin, log in, return (server, token).
type testEnv struct {
	srv   *httptest.Server
	token string
	store *store.Store
	cfg   *config.Config
	zone  string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Seed an admin manually.
	adminPW := "batchtest-password-12345"
	hash, err := hashPassword(adminPW)
	if err != nil {
		t.Fatal(err)
	}
	u := &store.User{
		ID: "test-admin", Email: "admin@test", PasswordHash: hash, Role: store.RoleAdmin,
	}
	ctx := context.Background()
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	zone := "example.test"
	if err := st.CreateZone(ctx, &store.Zone{Name: zone, IsPrivate: true}); err != nil {
		t.Fatalf("seed zone: %v", err)
	}

	cfg := &config.Config{
		JWTSecret:  "test-secret-do-not-use-in-prod",
		PrivateTLD: "test",
	}
	// Ensure the build_info gauge has at least one observation so /metrics
	// exposes it. In production this happens in main.
	metrics.SetBuildInfo("test")
	h := New(st, cfg)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// Log in.
	body, _ := json.Marshal(map[string]string{"email": u.Email, "password": adminPW})
	resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		t.Fatal(err)
	}

	return &testEnv{srv: srv, token: lr.Token, store: st, cfg: cfg, zone: zone}
}

func (te *testEnv) doJSON(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, te.srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+te.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestBatchUpsert_HappyPath(t *testing.T) {
	te := newTestEnv(t)

	batch := map[string]any{
		"records": []map[string]any{
			{"name": "www", "type": "A", "ttl": 60, "value": "10.0.0.1"},
			{"name": "www", "type": "AAAA", "value": "2001:db8::1"},
			{"name": "mail", "type": "MX", "value": "10 mail.example.test."},
			{"name": "example.test", "type": "TXT", "value": "v=spf1 mx -all"},
			{"name": "_dmarc", "type": "TXT", "value": "v=DMARC1; p=reject"},
		},
	}
	resp, body := te.doJSON(t, http.MethodPut, "/api/v1/zones/"+te.zone+"/records:batch", batch)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}

	// Verify all five persist by listing.
	resp2, body2 := te.doJSON(t, http.MethodGet, "/api/v1/zones/"+te.zone+"/records", nil)
	if resp2.StatusCode != 200 {
		t.Fatalf("list status=%d body=%s", resp2.StatusCode, body2)
	}
	if strings.Count(string(body2), `"records"`) != 5 {
		t.Errorf("expected 5 record sets in listing, got body:\n%s", body2)
	}
}

func TestBatchUpsert_AtomicRollbackOnInvalid(t *testing.T) {
	te := newTestEnv(t)

	// Pre-seed one valid record.
	pre := map[string]any{"name": "pre", "type": "A", "value": "10.0.0.9", "ttl": 60}
	if resp, _ := te.doJSON(t, http.MethodPut, "/api/v1/zones/"+te.zone+"/records", pre); resp.StatusCode != 200 && resp.StatusCode != 201 {
		t.Fatalf("pre-seed failed: %d", resp.StatusCode)
	}

	batch := map[string]any{
		"records": []map[string]any{
			{"name": "a", "type": "A", "value": "10.0.0.1"},
			{"name": "b", "type": "A", "value": "10.0.0.2"},
			{"name": "c", "type": "BOGUS", "value": "10.0.0.3"}, // invalid type
			{"name": "d", "type": "A", "value": "10.0.0.4"},
		},
	}
	resp, body := te.doJSON(t, http.MethodPut, "/api/v1/zones/"+te.zone+"/records:batch", batch)
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 on invalid type, got %d body=%s", resp.StatusCode, body)
	}

	// None of a/b/c/d should exist. Original 'pre' should still be there.
	_, listBody := te.doJSON(t, http.MethodGet, "/api/v1/zones/"+te.zone+"/records", nil)
	for _, name := range []string{"a.example.test", "b.example.test", "c.example.test", "d.example.test"} {
		if strings.Contains(string(listBody), fmt.Sprintf(`"name":"%s"`, name)) {
			t.Errorf("record %s was persisted despite batch failure — atomicity broken", name)
		}
	}
	if !strings.Contains(string(listBody), `"name":"pre.example.test"`) {
		t.Errorf("pre-existing 'pre' record was clobbered by failed batch; listing:\n%s", listBody)
	}
}

func TestBatchUpsert_ReplaceSemantics(t *testing.T) {
	te := newTestEnv(t)

	// Existing TXT record with three values via single-record endpoint.
	pre := map[string]any{"name": "example.test", "type": "TXT", "content": []string{"a", "b", "c"}}
	if resp, _ := te.doJSON(t, http.MethodPut, "/api/v1/zones/"+te.zone+"/records", pre); resp.StatusCode != 200 && resp.StatusCode != 201 {
		t.Fatalf("pre-seed failed: %d", resp.StatusCode)
	}

	// Batch replaces with a single value.
	batch := map[string]any{
		"records": []map[string]any{
			{"name": "example.test", "type": "TXT", "value": "replaced"},
		},
	}
	resp, body := te.doJSON(t, http.MethodPut, "/api/v1/zones/"+te.zone+"/records:batch", batch)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}

	_, listBody := te.doJSON(t, http.MethodGet, "/api/v1/zones/"+te.zone+"/records", nil)
	sb := string(listBody)
	if strings.Contains(sb, `"content":"a"`) || strings.Contains(sb, `"content":"b"`) || strings.Contains(sb, `"content":"c"`) {
		t.Errorf("REPLACE semantics broken — old values survive:\n%s", sb)
	}
	if !strings.Contains(sb, `"replaced"`) {
		t.Errorf("expected new value in listing, got:\n%s", sb)
	}
}

func TestBatchUpsert_RequiresAuth(t *testing.T) {
	te := newTestEnv(t)
	// Same request but without Authorization header.
	body, _ := json.Marshal(map[string]any{"records": []map[string]any{{"name": "x", "type": "A", "value": "1.1.1.1"}}})
	req, _ := http.NewRequest(http.MethodPut, te.srv.URL+"/api/v1/zones/"+te.zone+"/records:batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

func TestBatchUpsert_ViewerForbidden(t *testing.T) {
	te := newTestEnv(t)

	// Create a viewer and log them in.
	vpw := "viewer-password-12345"
	vhash, _ := hashPassword(vpw)
	_ = te.store.CreateUser(context.Background(), &store.User{
		ID: "test-viewer", Email: "viewer@test", PasswordHash: vhash, Role: store.RoleViewer,
	})
	loginBody, _ := json.Marshal(map[string]string{"email": "viewer@test", "password": vpw})
	resp, err := http.Post(te.srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var lr struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&lr)

	req, _ := http.NewRequest(http.MethodPut, te.srv.URL+"/api/v1/zones/"+te.zone+"/records:batch",
		bytes.NewReader([]byte(`{"records":[{"name":"x","type":"A","value":"1.1.1.1"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+lr.Token)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 403 {
		t.Errorf("viewer should be 403 on batch upsert, got %d", resp2.StatusCode)
	}
}

func TestBatchUpsert_DuplicateNameTypeRejected(t *testing.T) {
	te := newTestEnv(t)
	batch := map[string]any{
		"records": []map[string]any{
			{"name": "www", "type": "A", "value": "10.0.0.1"},
			{"name": "www", "type": "A", "value": "10.0.0.2"},
		},
	}
	resp, body := te.doJSON(t, http.MethodPut, "/api/v1/zones/"+te.zone+"/records:batch", batch)
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 on duplicate (name,type), got %d body=%s", resp.StatusCode, body)
	}
}
