package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestGCExpiredRevokedTokens(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	// Two expired, one still valid.
	st.RevokeToken(ctx, "expired-1", time.Now().Add(-2*time.Hour))
	st.RevokeToken(ctx, "expired-2", time.Now().Add(-1*time.Minute))
	st.RevokeToken(ctx, "still-valid", time.Now().Add(1*time.Hour))

	n, err := st.GCExpiredRevokedTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected to remove 2 expired rows, removed %d", n)
	}
	if !st.IsRevoked(ctx, "still-valid") {
		t.Error("still-valid row should have been preserved")
	}
	if st.IsRevoked(ctx, "expired-1") || st.IsRevoked(ctx, "expired-2") {
		t.Error("expired rows should be gone")
	}

	// Idempotent: second call removes 0.
	n2, err := st.GCExpiredRevokedTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("expected 0 on second run, got %d", n2)
	}
}
