package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/codex2api/security/upstreamguard"
	"github.com/gin-gonic/gin"
)

func TestSettingsRoutesExposeAndUpdateUpstreamGuardMode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	tc := cache.NewMemory(1)
	defer tc.Close()
	store := auth.NewStore(db, tc, &database.SystemSettings{UpstreamGuardMode: upstreamguard.ModeWarn})
	handler := NewHandler(store, db, tc, proxy.NewRateLimiter(0), "admin-secret")
	router := gin.New()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	req.Header.Set("X-Admin-Key", "admin-secret")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var getPayload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if getPayload["upstream_guard_mode"] != upstreamguard.ModeWarn {
		t.Fatalf("GET upstream_guard_mode = %v, want %s", getPayload["upstream_guard_mode"], upstreamguard.ModeWarn)
	}

	body := bytes.NewBufferString(`{"upstream_guard_mode":"off","upstream_guard_suppressions":"[{\"rule_id\":\"response_injection\",\"endpoint\":\"/v1/responses\",\"account_id\":42,\"base_url\":\"https://relay.example.com/v1\",\"action\":\"downgrade\"}]","security_event_retention_days":14}`)
	req = httptest.NewRequest(http.MethodPut, "/api/admin/settings", body)
	req.Header.Set("X-Admin-Key", "admin-secret")
	req.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("put status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if cfg := store.GetUpstreamGuardConfig(); cfg.Mode != upstreamguard.ModeOff {
		t.Fatalf("store Mode = %q, want %q", cfg.Mode, upstreamguard.ModeOff)
	} else if len(cfg.Suppressions) != 1 {
		t.Fatalf("store Suppressions len = %d, want 1", len(cfg.Suppressions))
	}
	settings, err := db.GetSystemSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSystemSettings returned error: %v", err)
	}
	if settings.UpstreamGuardMode != upstreamguard.ModeOff {
		t.Fatalf("db UpstreamGuardMode = %q, want %q", settings.UpstreamGuardMode, upstreamguard.ModeOff)
	}
	if settings.SecurityEventRetentionDays != 14 {
		t.Fatalf("db SecurityEventRetentionDays = %d, want 14", settings.SecurityEventRetentionDays)
	}
	if settings.UpstreamGuardSuppressions == "" || settings.UpstreamGuardSuppressions == "[]" {
		t.Fatalf("db UpstreamGuardSuppressions not persisted: %q", settings.UpstreamGuardSuppressions)
	}
}
