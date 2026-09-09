package handlers

import "testing"

func TestValidateZoneName(t *testing.T) {
	ok := []string{"example.myworld", "a.b.c", "with-dashes.example", "myworld"}
	bad := []string{
		"", "-leading-dash.com", "trailing-.com", "has_underscore.com",
		"has space.com", "too" + longLabel(63) + ".com",
	}
	for _, n := range ok {
		if err := validateZoneName(n); err != nil {
			t.Errorf("validateZoneName(%q) unexpected err: %v", n, err)
		}
	}
	for _, n := range bad {
		if err := validateZoneName(n); err == nil {
			t.Errorf("validateZoneName(%q) expected error", n)
		}
	}
}

func longLabel(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestTrimDot(t *testing.T) {
	if trimDot("example.com.") != "example.com" {
		t.Error("expected trailing dot removed")
	}
	if trimDot("example.com") != "example.com" {
		t.Error("expected unchanged")
	}
}
