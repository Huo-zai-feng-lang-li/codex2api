package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/codex2api/security/upstreamguard"
	"github.com/gin-gonic/gin"
)

func TestSecurityEventsRoutesListAndClear(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	tc := cache.NewMemory(1)
	defer tc.Close()
	store := auth.NewStore(db, tc, nil)
	handler := NewHandler(store, db, tc, nil, "admin-secret")
	router := gin.New()
	handler.RegisterRoutes(router)

	if err := db.InsertSecurityEvent(context.Background(), &database.SecurityEventInput{
		Direction:   "request",
		Action:      "warn",
		RiskLevel:   "high",
		RiskScore:   90,
		Endpoint:    "/v1/responses",
		AccountID:   3,
		AccountName: "third-party",
		BaseURL:     "https://relay.example.com",
		SourceType:  "third_party",
		Rules:       `[{"rule_id":"dlp_token"}]`,
		Preview:     "OPENAI_API_KEY=[REDACTED_TOKEN]",
		ContentHash: "hash-admin",
		RequestID:   "req-admin",
	}); err != nil {
		t.Fatalf("InsertSecurityEvent returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/security-events?risk_level=high&direction=request&page=1&page_size=5", nil)
	req.Header.Set("X-Admin-Key", "admin-secret")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload struct {
		Events   []*database.SecurityEvent `json:"events"`
		Total    int                       `json:"total"`
		Page     int                       `json:"page"`
		PageSize int                       `json:"page_size"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if payload.Total != 1 || len(payload.Events) != 1 {
		t.Fatalf("got total=%d len=%d, want 1", payload.Total, len(payload.Events))
	}
	if payload.Events[0].Preview != "OPENAI_API_KEY=[REDACTED_TOKEN]" {
		t.Fatalf("preview = %q", payload.Events[0].Preview)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/admin/security-events", nil)
	req.Header.Set("X-Admin-Key", "admin-secret")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	_, total, err := db.ListSecurityEventsPage(context.Background(), database.SecurityEventQuery{Page: 1, PageSize: 5})
	if err != nil {
		t.Fatalf("ListSecurityEventsPage returned error: %v", err)
	}
	if total != 0 {
		t.Fatalf("total after clear = %d, want 0", total)
	}
}

func TestSecurityEventSuppressRouteAppendsDowngradeRule(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newTestAdminDB(t)
	tc := cache.NewMemory(1)
	defer tc.Close()
	store := auth.NewStore(db, tc, nil)
	handler := NewHandler(store, db, tc, nil, "admin-secret")
	router := gin.New()
	handler.RegisterRoutes(router)

	if err := db.InsertSecurityEvent(context.Background(), &database.SecurityEventInput{
		Direction:   "response",
		Action:      "warn",
		RiskLevel:   "high",
		RiskScore:   92,
		Endpoint:    "/v1/responses",
		AccountID:   42,
		AccountName: "relay-account",
		BaseURL:     "https://relay.example.com/v1",
		SourceType:  "third_party",
		Rules:       `[{"rule_id":"response_injection","evidence":"ignore prior instruction"}]`,
		Preview:     "safe preview",
		ContentHash: "hash-suppress",
		RequestID:   "req-suppress",
	}); err != nil {
		t.Fatalf("InsertSecurityEvent returned error: %v", err)
	}
	events, _, err := db.ListSecurityEventsPage(context.Background(), database.SecurityEventQuery{Page: 1, PageSize: 1})
	if err != nil || len(events) != 1 {
		t.Fatalf("load inserted security event err=%v len=%d", err, len(events))
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/security-events/"+strconv.FormatInt(events[0].ID, 10)+"/suppress", strings.NewReader(`{"rule_id":"response_injection"}`))
	req.Header.Set("X-Admin-Key", "admin-secret")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("suppress status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	settings, err := db.GetSystemSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSystemSettings returned error: %v", err)
	}
	rules, err := upstreamguard.ParseSuppressions(settings.UpstreamGuardSuppressions)
	if err != nil {
		t.Fatalf("ParseSuppressions returned error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("suppressions len = %d, want 1; raw=%s", len(rules), settings.UpstreamGuardSuppressions)
	}
	rule := rules[0]
	if rule.RuleID != "response_injection" || rule.Endpoint != "/v1/responses" || rule.AccountID != 42 || rule.BaseURL != "https://relay.example.com/v1" || rule.Action != upstreamguard.SuppressDowngrade {
		t.Fatalf("unexpected suppression rule: %+v", rule)
	}
	if got := store.GetUpstreamGuardConfig().Suppressions; len(got) != 1 || got[0].RuleID != "response_injection" {
		t.Fatalf("store suppressions not refreshed: %+v", got)
	}
}
