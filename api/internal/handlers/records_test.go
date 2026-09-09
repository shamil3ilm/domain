package handlers

import "testing"

func TestQualify(t *testing.T) {
	cases := []struct {
		name, zone, want string
	}{
		{"www", "example.myworld", "www.example.myworld."},
		{"www.example.myworld", "example.myworld", "www.example.myworld."},
		{"@", "example.myworld", "example.myworld."},
		{"", "example.myworld", "example.myworld."},
		{"api.v1", "example.myworld", "api.v1.example.myworld."},
	}
	for _, c := range cases {
		got := qualify(c.name, c.zone)
		if got != c.want {
			t.Errorf("qualify(%q, %q) = %q; want %q", c.name, c.zone, got, c.want)
		}
	}
}

func TestNormalizeContent(t *testing.T) {
	cases := []struct{ typ, in, want string }{
		{"A", "10.0.0.1", "10.0.0.1"},
		{"TXT", "hello", `"hello"`},
		{"TXT", `"already quoted"`, `"already quoted"`},
		{"CNAME", "target.example.com", "target.example.com."},
		{"NS", "ns1.example.com.", "ns1.example.com."},
	}
	for _, c := range cases {
		got, err := normalizeContent(c.typ, c.in)
		if err != nil {
			t.Fatalf("normalizeContent(%q, %q) err: %v", c.typ, c.in, err)
		}
		if got != c.want {
			t.Errorf("normalizeContent(%q, %q) = %q; want %q", c.typ, c.in, got, c.want)
		}
	}
	if _, err := normalizeContent("A", ""); err == nil {
		t.Error("expected error for empty content")
	}
}

func TestValidateRRType(t *testing.T) {
	if err := validateRRType("A"); err != nil {
		t.Errorf("A should be valid: %v", err)
	}
	if err := validateRRType("BOGUS"); err == nil {
		t.Error("BOGUS should be invalid")
	}
}
