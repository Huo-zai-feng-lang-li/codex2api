package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func TestAccountListAvailabilityMatchesDashboardStats(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("database.New() error = %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	readyID := insertAvailabilityTestAccount(t, ctx, db, "ready")
	limitedID := insertAvailabilityTestAccount(t, ctx, db, "limited")
	if err := db.SetCooldown(ctx, limitedID, "rate_limited", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("SetCooldown() error = %v", err)
	}

	store := auth.NewStore(db, cache.NewMemory(1), nil)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Store.Init() error = %v", err)
	}
	handler := NewHandler(store, db, cache.NewMemory(1), nil, "")

	gin.SetMode(gin.TestMode)
	accountsRecorder := httptest.NewRecorder()
	accountsContext, _ := gin.CreateTestContext(accountsRecorder)
	accountsContext.Request = httptest.NewRequest(http.MethodGet, "/api/admin/accounts", nil)
	handler.ListAccounts(accountsContext)
	if accountsRecorder.Code != http.StatusOK {
		t.Fatalf("ListAccounts status = %d, body = %s", accountsRecorder.Code, accountsRecorder.Body.String())
	}

	var payload struct {
		Accounts []struct {
			ID          int64 `json:"id"`
			IsAvailable bool  `json:"is_available"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(accountsRecorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode accounts: %v", err)
	}
	availableRows := 0
	for _, account := range payload.Accounts {
		if account.IsAvailable {
			availableRows++
		}
		if account.ID == limitedID && account.IsAvailable {
			t.Fatal("expired sticky rate limit appeared available")
		}
		if account.ID == readyID && !account.IsAvailable {
			t.Fatal("ready account appeared unavailable")
		}
	}

	statsRecorder := httptest.NewRecorder()
	statsContext, _ := gin.CreateTestContext(statsRecorder)
	statsContext.Request = httptest.NewRequest(http.MethodGet, "/api/admin/stats", nil)
	handler.GetStats(statsContext)
	if statsRecorder.Code != http.StatusOK {
		t.Fatalf("GetStats status = %d, body = %s", statsRecorder.Code, statsRecorder.Body.String())
	}
	var stats struct {
		Available int `json:"available"`
	}
	if err := json.Unmarshal(statsRecorder.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Available != availableRows {
		t.Fatalf("stats.available = %d, account rows = %d", stats.Available, availableRows)
	}
}

func insertAvailabilityTestAccount(t *testing.T, ctx context.Context, db *database.DB, name string) int64 {
	t.Helper()
	id, err := db.InsertAccount(ctx, name, "refresh-"+name, "")
	if err != nil {
		t.Fatalf("InsertAccount(%q) error = %v", name, err)
	}
	if err := db.UpdateCredentials(ctx, id, map[string]interface{}{
		"refresh_token": "refresh-" + name,
		"access_token":  "access-" + name,
		"expires_at":    time.Now().Add(time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("UpdateCredentials(%q) error = %v", name, err)
	}
	return id
}
