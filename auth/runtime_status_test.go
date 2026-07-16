package auth

import (
	"strings"
	"testing"
	"time"
)

func TestRuntimeStatusShowsRefreshingForRTWithoutAccessToken(t *testing.T) {
	acc := &Account{
		RefreshToken: "rt-test",
		Status:       StatusReady,
	}

	if got := acc.RuntimeStatus(); got != "refreshing" {
		t.Fatalf("RuntimeStatus() = %q, want refreshing", got)
	}
}

func TestRuntimeStatusKeepsErrorForFailedRTAccount(t *testing.T) {
	acc := &Account{
		RefreshToken: "rt-test",
		Status:       StatusError,
		ErrorMsg:     "invalid_grant",
	}

	if got := acc.RuntimeStatus(); got != "error" {
		t.Fatalf("RuntimeStatus() = %q, want error", got)
	}
}

func TestMarkErrorAndClearCooldownRoundTrip(t *testing.T) {
	store := NewStore(nil, nil, nil)
	acc := &Account{
		DBID:        1,
		AccessToken: "at-test",
		Status:      StatusReady,
	}

	store.MarkError(acc, "batch test failed")
	if got := acc.RuntimeStatus(); got != "error" {
		t.Fatalf("RuntimeStatus() after MarkError = %q, want error", got)
	}

	store.ClearCooldown(acc)
	if got := acc.RuntimeStatus(); got != "active" {
		t.Fatalf("RuntimeStatus() after ClearCooldown = %q, want active", got)
	}
}

func TestMarkCooldownWithErrorKeepsUnauthorizedStatusAndMessage(t *testing.T) {
	store := NewStore(nil, nil, nil)
	acc := &Account{
		DBID:        1,
		AccessToken: "at-test",
		Status:      StatusReady,
		HealthTier:  HealthTierHealthy,
	}

	store.MarkCooldownWithError(acc, 24*time.Hour, "unauthorized", "上游返回 401: token_invalidated")

	if got := acc.RuntimeStatus(); got != "unauthorized" {
		t.Fatalf("RuntimeStatus() = %q, want unauthorized", got)
	}
	acc.Mu().RLock()
	errorMsg := acc.ErrorMsg
	cooldownReason := acc.CooldownReason
	cooldownUntil := acc.CooldownUtil
	status := acc.Status
	acc.Mu().RUnlock()
	if status != StatusCooldown {
		t.Fatalf("Status = %v, want cooldown", status)
	}
	if cooldownReason != "unauthorized" || cooldownUntil.IsZero() {
		t.Fatalf("cooldown = (%q, %s), want unauthorized with deadline", cooldownReason, cooldownUntil)
	}
	if !strings.Contains(errorMsg, "token_invalidated") {
		t.Fatalf("ErrorMsg = %q, want token_invalidated", errorMsg)
	}
}

func TestRuntimeStatusKeepsPaymentRequiredAfterCooldownExpires(t *testing.T) {
	store := NewStore(nil, nil, nil)
	acc := &Account{
		AccessToken:    "at-test",
		Status:         StatusCooldown,
		CooldownReason: "payment_required",
		CooldownUtil:   time.Now().Add(-time.Minute),
	}

	if got := acc.RuntimeStatus(); got != "payment_required" {
		t.Fatalf("RuntimeStatus() = %q, want payment_required until a successful connection test", got)
	}
	if !acc.IsAvailable() {
		t.Fatal("IsAvailable() = false, want expired cooldown to remain dispatchable")
	}

	store.RecordManualTestSuccess(acc, time.Millisecond)
	if got := acc.RuntimeStatus(); got != "active" {
		t.Fatalf("RuntimeStatus() after successful connection test = %q, want active", got)
	}
}

func TestReportRequestSuccessClearsExpiredRateLimitClassification(t *testing.T) {
	store := NewStore(nil, nil, nil)
	acc := &Account{
		DBID:           7,
		AccessToken:    "at-test",
		Status:         StatusCooldown,
		CooldownReason: "rate_limited",
		CooldownUtil:   time.Now().Add(-time.Minute),
		HealthTier:     HealthTierRisky,
		ErrorMsg:       "upstream returned 429",
	}

	store.ReportRequestSuccess(acc, 25*time.Millisecond)

	if got := acc.RuntimeStatus(); got != "active" {
		t.Fatalf("RuntimeStatus() = %q, want active after a real successful request", got)
	}
	acc.Mu().RLock()
	status := acc.Status
	reason := acc.CooldownReason
	until := acc.CooldownUtil
	errorMsg := acc.ErrorMsg
	acc.Mu().RUnlock()
	if status != StatusReady || reason != "" || !until.IsZero() || errorMsg != "" {
		t.Fatalf("recovered state = (%v, %q, %s, %q), want ready with cleared cooldown and error", status, reason, until, errorMsg)
	}
}

func TestNeedsRecoveryProbeIncludesOnlyEligibleUnhealthyAccounts(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		acc  *Account
		want bool
	}{
		{
			name: "normal account",
			acc:  &Account{AccessToken: "token", Status: StatusReady},
			want: false,
		},
		{
			name: "active rate limit cooldown",
			acc:  &Account{AccessToken: "token", Status: StatusCooldown, CooldownReason: "rate_limited", CooldownUtil: now.Add(time.Minute)},
			want: false,
		},
		{
			name: "expired rate limit cooldown",
			acc:  &Account{AccessToken: "token", Status: StatusCooldown, CooldownReason: "rate_limited", CooldownUtil: now.Add(-time.Minute)},
			want: true,
		},
		{
			name: "expired payment required cooldown",
			acc:  &Account{AccessToken: "token", Status: StatusCooldown, CooldownReason: "payment_required", CooldownUtil: now.Add(-time.Minute)},
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.acc.NeedsRecoveryProbe(30 * time.Minute); got != test.want {
				t.Fatalf("NeedsRecoveryProbe() = %t, want %t", got, test.want)
			}
		})
	}
}
