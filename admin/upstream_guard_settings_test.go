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
	store := auth.NewStore(db, tc, &database.SystemSettings{
		SecurityAuditEnabled: true,
		UpstreamGuardMode:    upstreamguard.ModeWarn,
	})
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
	if getPayload["security_audit_enabled"] != true {
		t.Fatalf("GET security_audit_enabled = %v, want true", getPayload["security_audit_enabled"])
	}

	body := bytes.NewBufferString(`{"security_audit_enabled":false,"upstream_guard_mode":"off","upstream_guard_suppressions":"[{\"rule_id\":\"response_injection\",\"endpoint\":\"/v1/responses\",\"account_id\":42,\"base_url\":\"https://relay.example.com/v1\",\"action\":\"downgrade\"}]","security_event_retention_days":14}`)
	req = httptest.NewRequest(http.MethodPut, "/api/admin/settings", body)
	req.Header.Set("X-Admin-Key", "admin-secret")
	req.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("put status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if cfg := store.GetUpstreamGuardConfig(); cfg.Enabled {
		t.Fatal("store Enabled = true, want false")
	} else if cfg.Mode != upstreamguard.ModeOff {
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
	if settings.SecurityAuditEnabled {
		t.Fatal("db SecurityAuditEnabled = true, want false")
	}
	if settings.SecurityEventRetentionDays != 14 {
		t.Fatalf("db SecurityEventRetentionDays = %d, want 14", settings.SecurityEventRetentionDays)
	}
	if settings.UpstreamGuardSuppressions == "" || settings.UpstreamGuardSuppressions == "[]" {
		t.Fatalf("db UpstreamGuardSuppressions not persisted: %q", settings.UpstreamGuardSuppressions)
	}
}

func TestSettingsRoutesExposeAndUpdateSecurityCaptureConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	tc := cache.NewMemory(1)
	defer tc.Close()
	store := auth.NewStore(db, tc, &database.SystemSettings{
		UpstreamGuardMode:            upstreamguard.ModeWarn,
		SecurityCaptureMode:          upstreamguard.CaptureModeHitRaw,
		SecurityCaptureRetentionDays: 7,
		SecurityCaptureMaxBodyBytes:  1048576,
	})
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
	if getPayload["security_capture_mode"] != upstreamguard.CaptureModeHitRaw {
		t.Fatalf("GET security_capture_mode = %v, want %s", getPayload["security_capture_mode"], upstreamguard.CaptureModeHitRaw)
	}

	body := bytes.NewBufferString(`{"security_capture_mode":"full_raw","security_capture_retention_days":3,"security_capture_max_body_bytes":2097152}`)
	req = httptest.NewRequest(http.MethodPut, "/api/admin/settings", body)
	req.Header.Set("X-Admin-Key", "admin-secret")
	req.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("put status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	cfg := store.GetUpstreamGuardConfig()
	if cfg.CaptureMode != upstreamguard.CaptureModeFullRaw {
		t.Fatalf("store CaptureMode = %q, want %q", cfg.CaptureMode, upstreamguard.CaptureModeFullRaw)
	}
	if cfg.CaptureRetentionDays != 3 || cfg.CaptureMaxBodyBytes != 2097152 {
		t.Fatalf("store capture retention/max = %d/%d", cfg.CaptureRetentionDays, cfg.CaptureMaxBodyBytes)
	}
	settings, err := db.GetSystemSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSystemSettings returned error: %v", err)
	}
	if settings.SecurityCaptureMode != upstreamguard.CaptureModeFullRaw {
		t.Fatalf("db SecurityCaptureMode = %q, want %q", settings.SecurityCaptureMode, upstreamguard.CaptureModeFullRaw)
	}
	if settings.SecurityCaptureRetentionDays != 3 || settings.SecurityCaptureMaxBodyBytes != 2097152 {
		t.Fatalf("db capture retention/max = %d/%d", settings.SecurityCaptureRetentionDays, settings.SecurityCaptureMaxBodyBytes)
	}
}

func TestSettingsRoutesExposeAndUpdateProxyPromptRewriteConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	tc := cache.NewMemory(1)
	defer tc.Close()
	store := auth.NewStore(db, tc, &database.SystemSettings{
		ProxyRequestSystemPromptEnabled: true,
		ProxyRequestSystemPrompt:        "initial request prompt",
		ProxyResponseRewriteEnabled:     false,
		ProxyResponseRewritePrompt:      "initial response prompt",
	})
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
	if getPayload["proxy_request_system_prompt_enabled"] != true || getPayload["proxy_request_system_prompt"] != "initial request prompt" {
		t.Fatalf("GET request prompt config mismatch: %#v", getPayload)
	}

	body := bytes.NewBufferString(`{"proxy_request_system_prompt_enabled":false,"proxy_request_system_prompt":" rewritten request ","proxy_response_rewrite_enabled":true,"proxy_response_rewrite_prompt":" rewritten response "}`)
	req = httptest.NewRequest(http.MethodPut, "/api/admin/settings", body)
	req.Header.Set("X-Admin-Key", "admin-secret")
	req.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("put status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	cfg := store.GetPromptRewriteConfig()
	if cfg.RequestSystemPromptEnabled || cfg.RequestSystemPrompt != "rewritten request" {
		t.Fatalf("store request prompt config = enabled:%t prompt:%q", cfg.RequestSystemPromptEnabled, cfg.RequestSystemPrompt)
	}
	if !cfg.ResponseRewriteEnabled || cfg.ResponseRewritePrompt != "rewritten response" {
		t.Fatalf("store response prompt config = enabled:%t prompt:%q", cfg.ResponseRewriteEnabled, cfg.ResponseRewritePrompt)
	}
	settings, err := db.GetSystemSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSystemSettings returned error: %v", err)
	}
	if settings.ProxyRequestSystemPromptEnabled || settings.ProxyRequestSystemPrompt != "rewritten request" {
		t.Fatalf("db request prompt config = enabled:%t prompt:%q", settings.ProxyRequestSystemPromptEnabled, settings.ProxyRequestSystemPrompt)
	}
	if !settings.ProxyResponseRewriteEnabled || settings.ProxyResponseRewritePrompt != "rewritten response" {
		t.Fatalf("db response prompt config = enabled:%t prompt:%q", settings.ProxyResponseRewriteEnabled, settings.ProxyResponseRewritePrompt)
	}
}
