package admin

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
)

func TestRuntimeStatusIncludesActiveRequestDetails(t *testing.T) {
	db := newTestAdminDB(t)
	tc := cache.NewMemory(4)
	defer tc.Close()
	store := auth.NewStore(db, tc, nil)

	startedAt := time.Now().Add(-1500 * time.Millisecond)
	store.BeginActiveRequest(auth.ActiveRequestMeta{
		AccountID:        11,
		AccountName:      "Current API",
		AccountEmail:     "current@example.com",
		Endpoint:         "/v1/chat/completions",
		UpstreamEndpoint: "/v1/responses",
		Model:            "gpt-5-codex",
		EffectiveModel:   "gpt-5-codex",
		APIKeyID:         5,
		APIKeyName:       "desktop",
		APIKeyMasked:     "sk-...wxyz",
		Stream:           true,
		StartedAt:        startedAt,
	})

	handler := NewHandler(store, db, tc, nil, "admin-secret")
	payload := handler.buildRuntimeStatus(context.Background(), httptest.NewRequest("GET", "/api/admin/runtime-status", nil))

	if len(payload.Accounts.ActiveRequestDetails) != 1 {
		t.Fatalf("len(active_request_details) = %d, want 1", len(payload.Accounts.ActiveRequestDetails))
	}
	got := payload.Accounts.ActiveRequestDetails[0]
	if got.AccountID != 11 || got.AccountName != "Current API" || got.AccountEmail != "current@example.com" {
		t.Fatalf("active request account = %d %q %q, want 11 Current API current@example.com", got.AccountID, got.AccountName, got.AccountEmail)
	}
	if got.Model != "gpt-5-codex" || got.APIKeyName != "desktop" || got.APIKeyMasked != "sk-...wxyz" {
		t.Fatalf("active request metadata = model:%q api_key:%q masked:%q", got.Model, got.APIKeyName, got.APIKeyMasked)
	}
	if got.DurationMs < 1000 {
		t.Fatalf("DurationMs = %d, want >= 1000", got.DurationMs)
	}
}
