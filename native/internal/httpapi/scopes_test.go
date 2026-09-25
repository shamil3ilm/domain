package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// mintScopedKey creates an API key with the given scopes and returns the
// plaintext token. Uses the admin JWT already sitting in te.
func (te *testEnv) mintScopedKey(t *testing.T, scopes ...string) string {
	t.Helper()
	req := map[string]any{"name": "test-scoped"}
	if len(scopes) > 0 {
		req["scopes"] = scopes
	}
	resp, buf := te.doJSON(t, http.MethodPost, "/api/v1/keys", req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("createKey status=%d body=%s", resp.StatusCode, buf)
	}
	var k struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(buf, &k); err != nil {
		t.Fatal(err)
	}
	if k.Token == "" {
		t.Fatal("createKey response missing token")
	}
	return k.Token
}

// doWithToken performs a request using an API key token instead of the
// admin JWT.
func doWithToken(t *testing.T, srvURL, method, path, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srvURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestKeyScopes_UnscopedIsGrandfathered(t *testing.T) {
	te := newTestEnv(t)
	// No scopes on the request → the key should behave like an admin JWT.
	tok := te.mintScopedKey(t)

	resp, _ := doWithToken(t, te.srv.URL, http.MethodGet, "/api/v1/zones", tok)
	if resp.StatusCode != 200 {
		t.Errorf("unscoped key should be granted zones:read; got %d", resp.StatusCode)
	}
	resp, _ = doWithToken(t, te.srv.URL, http.MethodGet, "/api/v1/audit", tok)
	if resp.StatusCode != 200 {
		t.Errorf("unscoped key should be granted audit:read; got %d", resp.StatusCode)
	}
}

func TestKeyScopes_ReadOnlyKeyRefusedWrite(t *testing.T) {
	te := newTestEnv(t)
	tok := te.mintScopedKey(t, ScopeZonesRead, ScopeRecordsRead)

	// zones:read allowed.
	resp, _ := doWithToken(t, te.srv.URL, http.MethodGet, "/api/v1/zones", tok)
	if resp.StatusCode != 200 {
		t.Errorf("zones:read key should GET /zones; got %d", resp.StatusCode)
	}
	// zones:write should be 403.
	req, _ := http.NewRequest(http.MethodPost, te.srv.URL+"/api/v1/zones",
		bytes.NewReader([]byte(`{"name":"scope-test.test"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("read-only key should be 403 on POST /zones; got %d", resp.StatusCode)
	}
}

func TestKeyScopes_Wildcard(t *testing.T) {
	te := newTestEnv(t)
	tok := te.mintScopedKey(t, ScopeAll)

	// Wildcard should pass every scope check.
	resp, _ := doWithToken(t, te.srv.URL, http.MethodGet, "/api/v1/zones", tok)
	if resp.StatusCode != 200 {
		t.Errorf("wildcard key should GET /zones; got %d", resp.StatusCode)
	}
	resp, _ = doWithToken(t, te.srv.URL, http.MethodGet, "/api/v1/audit", tok)
	if resp.StatusCode != 200 {
		t.Errorf("wildcard key should GET /audit; got %d", resp.StatusCode)
	}
}

func TestKeyScopes_UnknownScopeRejectedAtCreation(t *testing.T) {
	te := newTestEnv(t)
	req := map[string]any{"name": "bad", "scopes": []string{"not:real"}}
	resp, buf := te.doJSON(t, http.MethodPost, "/api/v1/keys", req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown scope should be rejected with 400; got %d body=%s", resp.StatusCode, buf)
	}
}

func TestKeyScopes_JWTSessionUnaffected(t *testing.T) {
	te := newTestEnv(t)
	// te.token is a JWT — it has empty Scopes and should pass every scope
	// check as long as the role permits.
	resp, _ := te.doJSON(t, http.MethodGet, "/api/v1/zones", nil)
	if resp.StatusCode != 200 {
		t.Errorf("JWT admin should GET /zones; got %d", resp.StatusCode)
	}
	resp, _ = te.doJSON(t, http.MethodGet, "/api/v1/audit", nil)
	if resp.StatusCode != 200 {
		t.Errorf("JWT admin should GET /audit; got %d", resp.StatusCode)
	}
}
