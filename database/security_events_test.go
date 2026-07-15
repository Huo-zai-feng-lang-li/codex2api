package database

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecurityEventsPersistQueryAndClear(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	if err := db.InsertSecurityEvent(ctx, &SecurityEventInput{
		Direction:   "request",
		Action:      "warn",
		RiskLevel:   "high",
		RiskScore:   91,
		Endpoint:    "/v1/responses",
		Model:       "gpt-5",
		AccountID:   42,
		AccountName: "third-party",
		BaseURL:     "https://relay.example.com/v1",
		SourceType:  "third_party",
		Stream:      false,
		ToolCall:    false,
		Rules:       `[{"rule_id":"dlp_token"}]`,
		Preview:     "OPENAI_API_KEY=[REDACTED_TOKEN]",
		ContentHash: "hash-a",
		RequestID:   "req-a",
	}); err != nil {
		t.Fatalf("InsertSecurityEvent returned error: %v", err)
	}
	if err := db.InsertSecurityEvent(ctx, &SecurityEventInput{
		Direction:   "response",
		Action:      "warn",
		RiskLevel:   "medium",
		RiskScore:   55,
		Endpoint:    "/v1/chat/completions",
		Model:       "gpt-5",
		AccountID:   7,
		AccountName: "official",
		BaseURL:     "https://api.openai.com/v1",
		SourceType:  "official",
		Stream:      true,
		ToolCall:    true,
		Rules:       `[{"rule_id":"tool_call"}]`,
		Preview:     "tool call requested",
		ContentHash: "hash-b",
		RequestID:   "req-b",
	}); err != nil {
		t.Fatalf("InsertSecurityEvent returned error: %v", err)
	}

	events, total, err := db.ListSecurityEventsPage(ctx, SecurityEventQuery{
		Page:      1,
		PageSize:  10,
		RiskLevel: "high",
		Direction: "request",
		AccountID: 42,
	})
	if err != nil {
		t.Fatalf("ListSecurityEventsPage returned error: %v", err)
	}
	if total != 1 || len(events) != 1 {
		t.Fatalf("got total=%d len=%d, want 1", total, len(events))
	}
	got := events[0]
	if got.Direction != "request" || got.RiskLevel != "high" || got.AccountID != 42 {
		t.Fatalf("unexpected event: %+v", got)
	}
	if strings.Contains(got.Preview, "sk-proj") {
		t.Fatalf("preview leaked raw token: %q", got.Preview)
	}
	if got.Rules == "" || got.ContentHash == "" || got.RequestID == "" {
		t.Fatalf("missing expected metadata: %+v", got)
	}

	if err := db.ClearSecurityEvents(ctx); err != nil {
		t.Fatalf("ClearSecurityEvents returned error: %v", err)
	}
	events, total, err = db.ListSecurityEventsPage(ctx, SecurityEventQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListSecurityEventsPage after clear returned error: %v", err)
	}
	if total != 0 || len(events) != 0 {
		t.Fatalf("after clear got total=%d len=%d, want 0", total, len(events))
	}
}

func TestSecurityCapturesPersistFullRawBodies(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	eventID, err := db.InsertSecurityEventReturningID(ctx, &SecurityEventInput{
		Direction:   "request",
		Action:      "block",
		RiskLevel:   "high",
		RiskScore:   91,
		Endpoint:    "/v1/responses",
		AccountID:   42,
		BaseURL:     "https://relay.example.com/v1",
		SourceType:  "third_party",
		Rules:       `[{"rule_id":"dlp_token"}]`,
		Preview:     "redacted preview",
		ContentHash: "event-hash",
		RequestID:   "req-raw",
	})
	if err != nil {
		t.Fatalf("InsertSecurityEventReturningID returned error: %v", err)
	}

	rawBody := `{"input":"OPENAI_API_KEY=sk-proj-real-secret-token-value-1234567890"}`
	captureID, err := db.InsertSecurityCapture(ctx, &SecurityCaptureInput{
		SecurityEventID: eventID,
		CaptureReason:   SecurityCaptureReasonHit,
		Direction:       "request",
		Endpoint:        "/v1/responses",
		AccountID:       42,
		BaseURL:         "https://relay.example.com/v1",
		SourceType:      "third_party",
		RequestID:       "req-raw",
		Body:            rawBody,
		BodyHash:        "raw-hash",
		BodyBytes:       len([]byte(rawBody)),
		ExpiresAt:       time.Now().Add(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("InsertSecurityCapture returned error: %v", err)
	}
	if captureID == 0 {
		t.Fatal("captureID = 0, want inserted id")
	}

	captures, err := db.ListSecurityCaptures(ctx, SecurityCaptureQuery{SecurityEventID: eventID})
	if err != nil {
		t.Fatalf("ListSecurityCaptures returned error: %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("captures len = %d, want 1", len(captures))
	}
	got := captures[0]
	if got.Body != rawBody {
		t.Fatalf("raw body was changed:\n got: %q\nwant: %q", got.Body, rawBody)
	}
	if !strings.Contains(got.Body, "sk-proj-real-secret") {
		t.Fatalf("raw key was not preserved: %q", got.Body)
	}
	if got.CaptureReason != SecurityCaptureReasonHit || got.SecurityEventID != eventID {
		t.Fatalf("unexpected capture metadata: %+v", got)
	}
	if got.EventRiskLevel != "high" || got.EventRiskScore != 91 || got.EventRules != `[{"rule_id":"dlp_token"}]` || got.EventPreview != "redacted preview" {
		t.Fatalf("event evidence metadata was not joined into capture: %+v", got)
	}
}

func TestDefaultSystemSettingsDisableGuardAndUseHitRawOneDayRetention(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	if err := db.UpdateSystemSettings(ctx, DefaultSystemSettings()); err != nil {
		t.Fatalf("UpdateSystemSettings returned error: %v", err)
	}
	settings, err := db.GetSystemSettings(ctx)
	if err != nil {
		t.Fatalf("GetSystemSettings returned error: %v", err)
	}
	if settings == nil {
		t.Fatal("settings is nil")
	}
	if settings.SecurityAuditEnabled {
		t.Fatal("SecurityAuditEnabled = true, want default false")
	}
	if settings.UpstreamGuardMode != DefaultUpstreamGuardMode {
		t.Fatalf("UpstreamGuardMode = %q, want %q", settings.UpstreamGuardMode, DefaultUpstreamGuardMode)
	}
	if settings.SecurityCaptureMode != DefaultSecurityCaptureMode {
		t.Fatalf("SecurityCaptureMode = %q, want %q", settings.SecurityCaptureMode, DefaultSecurityCaptureMode)
	}
	if settings.SecurityCaptureRetentionDays != 1 {
		t.Fatalf("SecurityCaptureRetentionDays = %d, want 1", settings.SecurityCaptureRetentionDays)
	}
	if settings.SecurityCaptureMaxBodyBytes != DefaultSecurityCaptureMaxBodyBytes {
		t.Fatalf("SecurityCaptureMaxBodyBytes = %d, want %d", settings.SecurityCaptureMaxBodyBytes, DefaultSecurityCaptureMaxBodyBytes)
	}
}

func TestInsertSecurityCaptureDefaultsToOneDayExpiry(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	before := time.Now()
	if _, err := db.InsertSecurityCapture(ctx, &SecurityCaptureInput{
		CaptureReason: SecurityCaptureReasonFull,
		Direction:     "response",
		RequestID:     "default-expiry",
		Body:          "default-expiry",
		BodyHash:      "default-expiry-hash",
		BodyBytes:     len("default-expiry"),
	}); err != nil {
		t.Fatalf("InsertSecurityCapture returned error: %v", err)
	}

	captures, err := db.ListSecurityCaptures(ctx, SecurityCaptureQuery{RequestID: "default-expiry"})
	if err != nil {
		t.Fatalf("ListSecurityCaptures returned error: %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("captures len = %d, want 1", len(captures))
	}
	remaining := captures[0].ExpiresAt.Sub(before)
	if remaining < 23*time.Hour || remaining > 25*time.Hour {
		t.Fatalf("default expiry remaining = %s, want about 1 day", remaining)
	}
}

func TestUpgradeLegacySecurityCaptureDefaultsOnlyChangesOldDefaultTuple(t *testing.T) {
	settings := &SystemSettings{
		SecurityCaptureMode:          "hit_raw",
		SecurityCaptureRetentionDays: 7,
		SecurityCaptureMaxBodyBytes:  1024 * 1024,
	}

	if !UpgradeLegacySecurityCaptureDefaults(settings) {
		t.Fatal("UpgradeLegacySecurityCaptureDefaults returned false, want true")
	}
	if settings.SecurityCaptureMode != DefaultSecurityCaptureMode ||
		settings.SecurityCaptureRetentionDays != DefaultSecurityCaptureRetentionDays ||
		settings.SecurityCaptureMaxBodyBytes != DefaultSecurityCaptureMaxBodyBytes {
		t.Fatalf("settings = %+v, want new capture defaults", settings)
	}

	custom := &SystemSettings{
		SecurityCaptureMode:          "hit_raw",
		SecurityCaptureRetentionDays: 3,
		SecurityCaptureMaxBodyBytes:  1024 * 1024,
	}
	if UpgradeLegacySecurityCaptureDefaults(custom) {
		t.Fatal("custom retention tuple was upgraded, want unchanged")
	}
	if custom.SecurityCaptureRetentionDays != 3 {
		t.Fatalf("custom retention changed to %d", custom.SecurityCaptureRetentionDays)
	}
}

func TestPruneSecurityCapturesBeforeRemovesExpiredRows(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	for _, item := range []struct {
		requestID string
		expiresAt time.Time
	}{
		{requestID: "expired-capture", expiresAt: time.Now().Add(-time.Hour)},
		{requestID: "fresh-capture", expiresAt: time.Now().Add(time.Hour)},
	} {
		if _, err := db.InsertSecurityCapture(ctx, &SecurityCaptureInput{
			CaptureReason: SecurityCaptureReasonFull,
			Direction:     "response",
			RequestID:     item.requestID,
			Body:          item.requestID,
			BodyHash:      item.requestID + "-hash",
			BodyBytes:     len(item.requestID),
			ExpiresAt:     item.expiresAt,
		}); err != nil {
			t.Fatalf("InsertSecurityCapture(%s) returned error: %v", item.requestID, err)
		}
	}

	removed, err := db.PruneSecurityCapturesBefore(ctx, time.Now())
	if err != nil {
		t.Fatalf("PruneSecurityCapturesBefore returned error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	captures, err := db.ListSecurityCaptures(ctx, SecurityCaptureQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListSecurityCaptures returned error: %v", err)
	}
	if len(captures) != 1 || captures[0].RequestID != "fresh-capture" {
		t.Fatalf("captures = %+v, want only fresh-capture", captures)
	}
}

func TestPruneSecurityCapturesToMaxBytesRemovesOldestRows(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	for _, item := range []struct {
		requestID string
		bodyBytes int
	}{
		{requestID: "old-capture", bodyBytes: 50},
		{requestID: "middle-capture", bodyBytes: 40},
		{requestID: "new-capture", bodyBytes: 30},
	} {
		if _, err := db.InsertSecurityCapture(ctx, &SecurityCaptureInput{
			CaptureReason: SecurityCaptureReasonFull,
			Direction:     "response",
			RequestID:     item.requestID,
			Body:          item.requestID,
			BodyHash:      item.requestID + "-hash",
			BodyBytes:     item.bodyBytes,
			ExpiresAt:     time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("InsertSecurityCapture(%s) returned error: %v", item.requestID, err)
		}
	}

	removed, err := db.PruneSecurityCapturesToMaxBytes(ctx, 70)
	if err != nil {
		t.Fatalf("PruneSecurityCapturesToMaxBytes returned error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	captures, err := db.ListSecurityCaptures(ctx, SecurityCaptureQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListSecurityCaptures returned error: %v", err)
	}
	got := make([]string, 0, len(captures))
	for _, capture := range captures {
		got = append(got, capture.RequestID)
	}
	if strings.Join(got, ",") != "new-capture,middle-capture" {
		t.Fatalf("remaining captures = %v, want newest captures under cap", got)
	}
}

func TestSecurityCapturesFullRawCanStoreAuditWithoutEvent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	rawBody := `{"model":"gpt-5","input":"hello"}`
	if _, err := db.InsertSecurityCapture(ctx, &SecurityCaptureInput{
		CaptureReason: SecurityCaptureReasonFull,
		Direction:     "request",
		Endpoint:      "/v1/responses",
		Model:         "gpt-5",
		RequestID:     "req-full",
		Body:          rawBody,
		BodyHash:      "normal-hash",
		BodyBytes:     len(rawBody),
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("InsertSecurityCapture returned error: %v", err)
	}

	captures, err := db.ListSecurityCaptures(ctx, SecurityCaptureQuery{RequestID: "req-full"})
	if err != nil {
		t.Fatalf("ListSecurityCaptures returned error: %v", err)
	}
	if len(captures) != 1 || captures[0].SecurityEventID != 0 || captures[0].Body != rawBody {
		t.Fatalf("captures = %+v, want one standalone full raw capture", captures)
	}
}

func TestSecurityCaptureQueryFiltersByReasonSourceToolAndTime(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	if _, err := db.InsertSecurityCapture(ctx, &SecurityCaptureInput{
		CaptureReason: SecurityCaptureReasonHit,
		Direction:     "response",
		Endpoint:      "/v1/responses",
		Model:         "gpt-5",
		AccountID:     7,
		BaseURL:       "https://api.openai.com/v1",
		SourceType:    "official",
		ToolCall:      false,
		RequestID:     "req-old",
		Body:          `{"output":"old"}`,
		BodyHash:      "old-hash",
		BodyBytes:     16,
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("InsertSecurityCapture(old) returned error: %v", err)
	}
	if _, err := db.InsertSecurityCapture(ctx, &SecurityCaptureInput{
		CaptureReason: SecurityCaptureReasonFull,
		Direction:     "response",
		Endpoint:      "/v1/responses",
		Model:         "gpt-5.5",
		AccountID:     42,
		BaseURL:       "https://relay.example.com/v1",
		SourceType:    "third_party",
		ToolCall:      true,
		RequestID:     "req-fresh",
		Body:          `{"output":"needle fresh"}`,
		BodyHash:      "fresh-hash",
		BodyBytes:     25,
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("InsertSecurityCapture(fresh) returned error: %v", err)
	}
	if _, err := db.conn.ExecContext(ctx, `UPDATE security_captures SET created_at = $1 WHERE request_id = $2`, time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC), "req-old"); err != nil {
		t.Fatalf("update old created_at returned error: %v", err)
	}
	if _, err := db.conn.ExecContext(ctx, `UPDATE security_captures SET created_at = $1 WHERE request_id = $2`, time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC), "req-fresh"); err != nil {
		t.Fatalf("update fresh created_at returned error: %v", err)
	}

	toolCall := true
	captures, total, err := db.ListSecurityCapturesPage(ctx, SecurityCaptureQuery{
		Page:          1,
		PageSize:      10,
		CaptureReason: SecurityCaptureReasonFull,
		BaseURL:       "https://relay.example.com/v1",
		SourceType:    "third_party",
		ToolCall:      &toolCall,
		StartTime:     time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC),
		Query:         "needle",
	})
	if err != nil {
		t.Fatalf("ListSecurityCapturesPage returned error: %v", err)
	}
	if total != 1 || len(captures) != 1 || captures[0].RequestID != "req-fresh" {
		t.Fatalf("got total=%d captures=%+v, want only req-fresh", total, captures)
	}
}

func TestSecurityEventQuerySearchesPreviewRulesAndRequestID(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	if err := db.InsertSecurityEvent(ctx, &SecurityEventInput{
		Direction:   "response",
		Action:      "warn",
		RiskLevel:   "medium",
		RiskScore:   45,
		Endpoint:    "/v1/messages",
		Model:       "claude",
		AccountID:   9,
		BaseURL:     "https://relay.example.com",
		SourceType:  "third_party",
		Rules:       `[{"rule_id":"response_injection"}]`,
		Preview:     "safe redacted preview",
		ContentHash: "hash-c",
		RequestID:   "req-search",
	}); err != nil {
		t.Fatalf("InsertSecurityEvent returned error: %v", err)
	}

	events, total, err := db.ListSecurityEventsPage(ctx, SecurityEventQuery{Page: 1, PageSize: 10, Query: "response_injection"})
	if err != nil {
		t.Fatalf("ListSecurityEventsPage returned error: %v", err)
	}
	if total != 1 || len(events) != 1 {
		t.Fatalf("got total=%d len=%d, want 1", total, len(events))
	}
}

func TestSecurityEventQueryFiltersByTimeAccountAndBaseURL(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	for _, item := range []struct {
		requestID string
		accountID int64
		baseURL   string
		createdAt time.Time
	}{
		{
			requestID: "old-match",
			accountID: 42,
			baseURL:   "https://relay.example.com/v1",
			createdAt: time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC),
		},
		{
			requestID: "fresh-match",
			accountID: 42,
			baseURL:   "https://relay.example.com/v1",
			createdAt: time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC),
		},
		{
			requestID: "other-account",
			accountID: 7,
			baseURL:   "https://relay.example.com/v1",
			createdAt: time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC),
		},
		{
			requestID: "other-base-url",
			accountID: 42,
			baseURL:   "https://api.openai.com/v1",
			createdAt: time.Date(2026, 6, 8, 11, 0, 0, 0, time.UTC),
		},
	} {
		if err := db.InsertSecurityEvent(ctx, &SecurityEventInput{
			Direction:   "request",
			Action:      "warn",
			RiskLevel:   "high",
			AccountID:   item.accountID,
			BaseURL:     item.baseURL,
			Rules:       `[{"rule_id":"dlp_token"}]`,
			Preview:     item.requestID,
			ContentHash: "hash-" + item.requestID,
			RequestID:   item.requestID,
		}); err != nil {
			t.Fatalf("InsertSecurityEvent(%s) returned error: %v", item.requestID, err)
		}
		if _, err := db.conn.ExecContext(ctx, `UPDATE security_events SET created_at = $1 WHERE request_id = $2`, item.createdAt, item.requestID); err != nil {
			t.Fatalf("mark event %s time: %v", item.requestID, err)
		}
	}

	events, total, err := db.ListSecurityEventsPage(ctx, SecurityEventQuery{
		Page:      1,
		PageSize:  10,
		AccountID: 42,
		BaseURL:   "https://relay.example.com/v1",
		StartTime: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ListSecurityEventsPage returned error: %v", err)
	}
	if total != 1 || len(events) != 1 || events[0].RequestID != "fresh-match" {
		t.Fatalf("filtered events = total %d %+v, want fresh-match only", total, events)
	}
}

func TestPruneSecurityEventsBeforeRemovesOnlyExpiredRows(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	for _, requestID := range []string{"old-event", "fresh-event"} {
		if err := db.InsertSecurityEvent(ctx, &SecurityEventInput{
			Direction: "response",
			Action:    "warn",
			RiskLevel: "medium",
			Rules:     `[{"rule_id":"tool_call"}]`,
			Preview:   requestID,
			RequestID: requestID,
		}); err != nil {
			t.Fatalf("InsertSecurityEvent(%s) returned error: %v", requestID, err)
		}
	}
	oldTime := time.Now().Add(-45 * 24 * time.Hour)
	if _, err := db.conn.ExecContext(ctx, `UPDATE security_events SET created_at = $1 WHERE request_id = $2`, oldTime, "old-event"); err != nil {
		t.Fatalf("mark old event: %v", err)
	}

	removed, err := db.PruneSecurityEventsBefore(ctx, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("PruneSecurityEventsBefore returned error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	events, total, err := db.ListSecurityEventsPage(ctx, SecurityEventQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListSecurityEventsPage returned error: %v", err)
	}
	if total != 1 || len(events) != 1 || events[0].RequestID != "fresh-event" {
		t.Fatalf("remaining events = total %d %+v, want fresh-event only", total, events)
	}
}
