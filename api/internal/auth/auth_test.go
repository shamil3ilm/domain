package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/privatedns/api/internal/models"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("expected VerifyPassword to succeed")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Fatal("expected VerifyPassword to fail on wrong password")
	}
}

func TestJWTRoundTrip(t *testing.T) {
	j := NewJWT("this-is-a-32-byte-secret-value!!")
	u := &models.User{ID: "u1", Email: "a@b", Role: models.RoleAdmin}
	tok, err := j.Sign(u)
	if err != nil {
		t.Fatal(err)
	}
	c, err := j.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sub != "u1" || c.Email != "a@b" || c.Role != models.RoleAdmin {
		t.Fatalf("bad claims: %+v", c)
	}
	if c.ID == "" {
		t.Fatal("expected non-empty jti")
	}
	if time.Until(c.ExpiresAt.Time) <= 0 {
		t.Fatal("expected non-expired token")
	}
}

func TestJWTRejectsAlgSwitch(t *testing.T) {
	j := NewJWT("secret-secret-secret-secret-secret")
	// Craft an unsigned token — should fail to parse as HS256.
	badToken := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJoYWNrZXIifQ."
	if _, err := j.Parse(badToken); err == nil {
		t.Fatal("expected parse to reject alg=none")
	}
}

func TestGenerateAPIKey(t *testing.T) {
	k, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.Token, "pd_") {
		t.Fatalf("token should start with pd_, got %q", k.Token)
	}
	if len(k.Token) < 30 {
		t.Fatalf("token too short: %q", k.Token)
	}
	if HashAPIKey(k.Token) != k.TokenHash {
		t.Fatal("HashAPIKey produced different hash than GenerateAPIKey")
	}
	if !strings.HasPrefix(k.TokenPrefix, "pd_") {
		t.Fatalf("prefix should start with pd_, got %q", k.TokenPrefix)
	}
}
