package proxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/database"
)

func TestSecurityCaptureCleanupIntervalIsHourly(t *testing.T) {
	if securityCaptureCleanupInterval > time.Hour {
		t.Fatalf("securityCaptureCleanupInterval = %s, want <= 1h", securityCaptureCleanupInterval)
	}
}

func TestRunSecurityCaptureCleanupPrunesExpiredAndOverLimit(t *testing.T) {
	db := newProxyGuardTestDB(t)
	now := time.Now()
	for _, item := range []struct {
		requestID string
		bodyBytes int
		expiresAt time.Time
	}{
		{requestID: "expired-capture", bodyBytes: 5, expiresAt: now.Add(-time.Hour)},
		{requestID: "old-capture", bodyBytes: 60, expiresAt: now.Add(time.Hour)},
		{requestID: "new-capture", bodyBytes: 50, expiresAt: now.Add(time.Hour)},
	} {
		if _, err := db.InsertSecurityCapture(context.Background(), &database.SecurityCaptureInput{
			CaptureReason: database.SecurityCaptureReasonFull,
			Direction:     "response",
			RequestID:     item.requestID,
			Body:          item.requestID,
			BodyHash:      item.requestID + "-hash",
			BodyBytes:     item.bodyBytes,
			ExpiresAt:     item.expiresAt,
		}); err != nil {
			t.Fatalf("InsertSecurityCapture(%s) returned error: %v", item.requestID, err)
		}
	}

	removedExpired, removedOverLimit, err := runSecurityCaptureCleanupWithLimit(db, now, 70)
	if err != nil {
		t.Fatalf("runSecurityCaptureCleanupWithLimit returned error: %v", err)
	}
	if removedExpired != 1 || removedOverLimit != 1 {
		t.Fatalf("removed expired/over-limit = %d/%d, want 1/1", removedExpired, removedOverLimit)
	}
	captures, err := db.ListSecurityCaptures(context.Background(), database.SecurityCaptureQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListSecurityCaptures returned error: %v", err)
	}
	got := make([]string, 0, len(captures))
	for _, capture := range captures {
		got = append(got, capture.RequestID)
	}
	if strings.Join(got, ",") != "new-capture" {
		t.Fatalf("remaining captures = %v, want only new-capture", got)
	}
}
