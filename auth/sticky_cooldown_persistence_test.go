package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex2api/database"
)

func TestStoreInitPreservesExpiredStickyCooldownUntilVerification(t *testing.T) {
	for _, reason := range []string{"rate_limited", "payment_required"} {
		t.Run(reason, func(t *testing.T) {
			db, err := database.New("sqlite", filepath.Join(t.TempDir(), "accounts.db"))
			if err != nil {
				t.Fatalf("database.New() error = %v", err)
			}
			defer db.Close()

			ctx := context.Background()
			id, err := db.InsertAccount(ctx, "limited", "refresh-token", "")
			if err != nil {
				t.Fatalf("InsertAccount() error = %v", err)
			}
			if err := db.UpdateCredentials(ctx, id, map[string]interface{}{
				"refresh_token": "refresh-token",
				"access_token":  "access-token",
				"expires_at":    time.Now().Add(time.Hour).Format(time.RFC3339),
			}); err != nil {
				t.Fatalf("UpdateCredentials() error = %v", err)
			}
			if err := db.SetCooldown(ctx, id, reason, time.Now().Add(-time.Minute)); err != nil {
				t.Fatalf("SetCooldown() error = %v", err)
			}

			store := NewStore(db, nil, nil)
			if err := store.Init(ctx); err != nil {
				t.Fatalf("Store.Init() error = %v", err)
			}
			accounts := store.Accounts()
			if len(accounts) != 1 {
				t.Fatalf("accounts = %d, want 1", len(accounts))
			}
			if got := accounts[0].RuntimeStatus(); got != reason {
				t.Fatalf("RuntimeStatus() = %q, want %q", got, reason)
			}
			if accounts[0].IsAvailable() {
				t.Fatal("IsAvailable() = true after restart, want verification gate")
			}
			row, err := db.GetAccountByID(ctx, id)
			if err != nil {
				t.Fatalf("GetAccountByID() error = %v", err)
			}
			if row.CooldownReason != reason || !row.CooldownUntil.Valid {
				t.Fatalf("persisted cooldown = (%q, %v), want %q", row.CooldownReason, row.CooldownUntil.Valid, reason)
			}
		})
	}
}
