package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex2api/cache"
	"github.com/codex2api/database"
)

type blockingAccessTokenCache struct {
	cache.TokenCache
	started chan struct{}
	release chan struct{}
}

func (c *blockingAccessTokenCache) GetAccessToken(context.Context, int64) (string, error) {
	close(c.started)
	<-c.release
	return "refreshed-token", nil
}

func TestRefreshKeepsStickyCooldownCreatedWhileRefreshIsInFlight(t *testing.T) {
	for _, reason := range []string{"rate_limited", "payment_required"} {
		t.Run(reason, func(t *testing.T) {
			tokenCache := &blockingAccessTokenCache{
				TokenCache: cache.NewMemory(1),
				started:    make(chan struct{}),
				release:    make(chan struct{}),
			}
			store := NewStore(nil, tokenCache, nil)
			acc := &Account{DBID: 1, AccessToken: "old-token", Status: StatusReady}
			done := make(chan error, 1)
			go func() { done <- store.refreshAccount(context.Background(), acc) }()

			<-tokenCache.started
			store.MarkCooldownWithError(acc, time.Hour, reason, "new upstream failure")
			close(tokenCache.release)
			if err := <-done; err != nil {
				t.Fatalf("refreshAccount() error = %v", err)
			}

			if got := acc.RuntimeStatus(); got != reason {
				t.Fatalf("RuntimeStatus() = %q, want current sticky cooldown %q", got, reason)
			}
			if acc.IsAvailable() {
				t.Fatal("IsAvailable() = true after sticky cooldown won refresh race")
			}
			acc.Mu().RLock()
			errorMsg := acc.ErrorMsg
			acc.Mu().RUnlock()
			if errorMsg != "new upstream failure" {
				t.Fatalf("ErrorMsg = %q, want newest upstream failure", errorMsg)
			}
		})
	}
}

func TestRefreshKeepsNewestStickyCooldownInDatabase(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "refresh-race.db"))
	if err != nil {
		t.Fatalf("database.New() error = %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	id, err := db.InsertAccount(ctx, "limited", "refresh-token", "")
	if err != nil {
		t.Fatalf("InsertAccount() error = %v", err)
	}
	tokenCache := &blockingAccessTokenCache{
		TokenCache: cache.NewMemory(1),
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	store := NewStore(db, tokenCache, nil)
	acc := &Account{DBID: id, AccessToken: "old-token", Status: StatusReady}
	done := make(chan error, 1)
	go func() { done <- store.refreshAccount(ctx, acc) }()

	<-tokenCache.started
	store.MarkCooldownWithError(acc, time.Hour, "payment_required", "new payment failure")
	close(tokenCache.release)
	if err := <-done; err != nil {
		t.Fatalf("refreshAccount() error = %v", err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatalf("GetAccountByID() error = %v", err)
	}
	if row.CooldownReason != "payment_required" || !row.CooldownUntil.Valid {
		t.Fatalf("persisted cooldown = (%q, %v), want payment_required", row.CooldownReason, row.CooldownUntil.Valid)
	}
	if row.ErrorMessage != "new payment failure" {
		t.Fatalf("persisted ErrorMessage = %q, want newest failure", row.ErrorMessage)
	}
}
