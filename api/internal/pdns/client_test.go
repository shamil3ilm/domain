package pdns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCanonical(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"example.com":     "example.com.",
		"example.com.":    "example.com.",
		"  spaces  ":      "spaces.",
	}
	for in, want := range cases {
		if got := canonical(in); got != want {
			t.Errorf("canonical(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestClientCreateZone(t *testing.T) {
	var seenBody map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized); return
		}
		_ = json.NewDecoder(r.Body).Decode(&seenBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"example.myworld.","kind":"Native","serial":1}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-key")
	z, err := c.CreateZone(context.Background(), Zone{
		Name: "example.myworld", Kind: "Native", Nameservers: []string{"ns1.myworld"},
	})
	if err != nil { t.Fatal(err) }
	if z.Name != "example.myworld." { t.Errorf("bad zone name: %q", z.Name) }
	if seenBody["name"] != "example.myworld." {
		t.Errorf("expected canonical name in request, got %v", seenBody["name"])
	}
	// Nameservers should also have been canonicalized to trailing-dot form.
	if ns, ok := seenBody["nameservers"].([]interface{}); !ok || ns[0] != "ns1.myworld." {
		t.Errorf("nameservers not canonicalized: %v", seenBody["nameservers"])
	}
}

func TestClientNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not found"}`))
	}))
	defer ts.Close()
	c := NewClient(ts.URL, "k")
	_, err := c.GetZone(context.Background(), "missing")
	if err == nil { t.Fatal("expected error") }
	pe, ok := err.(*APIError)
	if !ok { t.Fatalf("expected *APIError, got %T", err) }
	if !pe.NotFound() { t.Error("expected NotFound true") }
}
