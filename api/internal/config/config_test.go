package config

import (
	"os"
	"testing"
)

func TestLoadValidates(t *testing.T) {
	// Missing everything.
	os.Clearenv()
	if _, err := Load(); err == nil {
		t.Fatal("expected error when env is empty")
	}
}

func TestLoadHappyPath(t *testing.T) {
	os.Clearenv()
	os.Setenv("API_JWT_SECRET", "0123456789abcdef0123456789abcdef") // 32 chars
	os.Setenv("PGHOST", "postgres")
	os.Setenv("PGDATABASE", "privatedns")
	os.Setenv("PGUSER", "privatedns")
	os.Setenv("PGPASSWORD", "secret")
	os.Setenv("PDNS_API_URL", "http://pdns:8081/api/v1")
	os.Setenv("PDNS_API_KEY", "k")
	os.Setenv("PRIVATE_TLD", "myworld")
	os.Setenv("ADMIN_EMAIL", "admin@local")
	os.Setenv("ADMIN_PASSWORD", "12345678901234")
	os.Setenv("BOOTSTRAP_ZONES", "example.myworld , home.myworld ,")

	c, err := Load()
	if err != nil { t.Fatal(err) }
	if c.RateLimit == 0 { t.Error("expected default rate limit") }
	if len(c.BootstrapZones) != 2 {
		t.Errorf("expected 2 bootstrap zones, got %v", c.BootstrapZones)
	}
	if c.DatabaseDSN() == "" { t.Error("expected DSN") }
}
