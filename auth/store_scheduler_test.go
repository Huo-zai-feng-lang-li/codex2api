package auth

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/cache"
	"github.com/codex2api/database"
)

func int64Ptr(v int64) *int64 {
	return &v
}

func recomputeTestAccount(acc *Account, baseLimit int64) {
	acc.mu.Lock()
	acc.recomputeSchedulerLocked(baseLimit)
	acc.mu.Unlock()
}

func TestAccountPremiumPlanGetsDefaultScoreBias(t *testing.T) {
	acc := &Account{
		AccessToken: "token",
		Status:      StatusReady,
		PlanType:    "plus",
	}

	recomputeTestAccount(acc, 6)

	if acc.SchedulerScore != 100 {
		t.Fatalf("SchedulerScore = %v, want 100", acc.SchedulerScore)
	}
	if acc.DispatchScore != 150 {
		t.Fatalf("DispatchScore = %v, want 150", acc.DispatchScore)
	}
	if acc.ScoreBiasEffective != 50 {
		t.Fatalf("ScoreBiasEffective = %d, want 50", acc.ScoreBiasEffective)
	}
	if acc.BaseConcurrencyEffective != 6 {
		t.Fatalf("BaseConcurrencyEffective = %d, want 6", acc.BaseConcurrencyEffective)
	}
}

func TestAccountScoreBiasOverrideReplacesPlanDefault(t *testing.T) {
	acc := &Account{
		AccessToken:       "token",
		Status:            StatusReady,
		PlanType:          "team",
		ScoreBiasOverride: int64Ptr(12),
	}

	recomputeTestAccount(acc, 6)

	if acc.DispatchScore != 112 {
		t.Fatalf("DispatchScore = %v, want 112", acc.DispatchScore)
	}
	if acc.ScoreBiasEffective != 12 {
		t.Fatalf("ScoreBiasEffective = %d, want 12", acc.ScoreBiasEffective)
	}
}

func TestAccountRiskyTierDoesNotApplyScoreBias(t *testing.T) {
	acc := &Account{
		AccessToken:        "token",
		Status:             StatusReady,
		PlanType:           "pro",
		LastUnauthorizedAt: time.Now(),
	}

	recomputeTestAccount(acc, 6)

	if acc.HealthTier != HealthTierRisky {
		t.Fatalf("HealthTier = %s, want %s", acc.HealthTier, HealthTierRisky)
	}
	if acc.SchedulerScore >= 60 {
		t.Fatalf("SchedulerScore = %v, want < 60", acc.SchedulerScore)
	}
	if acc.DispatchScore != acc.SchedulerScore {
		t.Fatalf("DispatchScore = %v, want raw score %v when risky", acc.DispatchScore, acc.SchedulerScore)
	}
	if acc.ScoreBiasEffective != 0 {
		t.Fatalf("ScoreBiasEffective = %d, want 0", acc.ScoreBiasEffective)
	}
}

func TestAccountBaseConcurrencyOverrideControlsDynamicLimit(t *testing.T) {
	acc := &Account{
		AccessToken:             "token",
		Status:                  StatusReady,
		PlanType:                "plus",
		BaseConcurrencyOverride: int64Ptr(4),
	}

	recomputeTestAccount(acc, 10)
	if acc.DynamicConcurrencyLimit != 4 {
		t.Fatalf("healthy DynamicConcurrencyLimit = %d, want 4", acc.DynamicConcurrencyLimit)
	}

	acc.mu.Lock()
	acc.LastFailureAt = time.Now()
	acc.mu.Unlock()
	recomputeTestAccount(acc, 10)
	if acc.HealthTier != HealthTierWarm {
		t.Fatalf("warm HealthTier = %s, want %s", acc.HealthTier, HealthTierWarm)
	}
	if acc.DynamicConcurrencyLimit != 2 {
		t.Fatalf("warm DynamicConcurrencyLimit = %d, want 2", acc.DynamicConcurrencyLimit)
	}

	acc.mu.Lock()
	acc.LastUnauthorizedAt = time.Now()
	acc.mu.Unlock()
	recomputeTestAccount(acc, 10)
	if acc.HealthTier != HealthTierRisky {
		t.Fatalf("risky HealthTier = %s, want %s", acc.HealthTier, HealthTierRisky)
	}
	if acc.DynamicConcurrencyLimit != 1 {
		t.Fatalf("risky DynamicConcurrencyLimit = %d, want 1", acc.DynamicConcurrencyLimit)
	}
}

func TestAccountSkipWarmTierPromotesWarmScoreToHealthy(t *testing.T) {
	acc := &Account{
		AccessToken:   "token",
		Status:        StatusReady,
		PlanType:      "pro",
		SkipWarmTier:  true,
		LastTimeoutAt: time.Now(),
	}

	recomputeTestAccount(acc, 6)

	if acc.SchedulerScore >= 85 || acc.SchedulerScore < 60 {
		t.Fatalf("SchedulerScore = %v, want warm score range", acc.SchedulerScore)
	}
	if acc.HealthTier != HealthTierHealthy {
		t.Fatalf("HealthTier = %s, want %s", acc.HealthTier, HealthTierHealthy)
	}
	if acc.DynamicConcurrencyLimit != 6 {
		t.Fatalf("DynamicConcurrencyLimit = %d, want full healthy limit 6", acc.DynamicConcurrencyLimit)
	}
}

func TestAccountSkipWarmTierPromotesRecentFailureWarmToHealthy(t *testing.T) {
	acc := &Account{
		AccessToken:   "token",
		Status:        StatusReady,
		PlanType:      "pro",
		SkipWarmTier:  true,
		LastFailureAt: time.Now(),
	}

	recomputeTestAccount(acc, 4)

	if acc.HealthTier != HealthTierHealthy {
		t.Fatalf("HealthTier = %s, want %s", acc.HealthTier, HealthTierHealthy)
	}
	if acc.DynamicConcurrencyLimit != 4 {
		t.Fatalf("DynamicConcurrencyLimit = %d, want full healthy limit 4", acc.DynamicConcurrencyLimit)
	}
}

func TestAccountSkipWarmTierDoesNotPromoteRiskyOrBanned(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		acc  *Account
		want AccountHealthTier
	}{
		{
			name: "low score remains risky",
			acc: &Account{
				AccessToken:        "token",
				Status:             StatusReady,
				PlanType:           "pro",
				SkipWarmTier:       true,
				LastUnauthorizedAt: now,
			},
			want: HealthTierRisky,
		},
		{
			name: "banned remains banned",
			acc: &Account{
				AccessToken:  "token",
				Status:       StatusReady,
				PlanType:     "pro",
				HealthTier:   HealthTierBanned,
				SkipWarmTier: true,
			},
			want: HealthTierBanned,
		},
		{
			name: "premium 5h limit remains risky",
			acc: &Account{
				AccessToken:         "token",
				Status:              StatusReady,
				PlanType:            "plus",
				SkipWarmTier:        true,
				UsagePercent5h:      100,
				UsagePercent5hValid: true,
				Reset5hAt:           now.Add(time.Hour),
			},
			want: HealthTierRisky,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recomputeTestAccount(tc.acc, 6)
			if tc.acc.HealthTier != tc.want {
				t.Fatalf("HealthTier = %s, want %s", tc.acc.HealthTier, tc.want)
			}
		})
	}
}

func TestNeedsUsageProbeSkipsRateLimited(t *testing.T) {
	acc := &Account{
		AccessToken:    "token",
		Status:         StatusCooldown,
		CooldownReason: "rate_limited",
	}
	if acc.NeedsUsageProbe(10 * time.Minute) {
		t.Fatal("NeedsUsageProbe should return false for rate_limited cooldown")
	}
}

func TestNeedsUsageProbeSkipsUnauthorized(t *testing.T) {
	acc := &Account{
		AccessToken:    "token",
		Status:         StatusCooldown,
		CooldownReason: "unauthorized",
	}
	if acc.NeedsUsageProbe(10 * time.Minute) {
		t.Fatal("NeedsUsageProbe should return false for unauthorized cooldown")
	}
}

func TestNeedsUsageProbeAllowsReadyAccount(t *testing.T) {
	acc := &Account{
		AccessToken: "token",
		Status:      StatusReady,
	}
	// UsagePercent7dValid = false，应该返回 true
	if !acc.NeedsUsageProbe(10 * time.Minute) {
		t.Fatal("NeedsUsageProbe should return true for ready account without valid usage data")
	}
}

func TestRefreshSingleBypassesCachedAccessToken(t *testing.T) {
	ctx := context.Background()
	tokenCache := cache.NewMemory(1)
	if err := tokenCache.SetAccessToken(ctx, 7, "cached-token", time.Hour); err != nil {
		t.Fatalf("SetAccessToken 返回错误: %v", err)
	}

	store := NewStore(nil, tokenCache, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	store.AddAccount(&Account{
		DBID:        7,
		AccessToken: "old-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		Status:      StatusReady,
	})

	err := store.RefreshSingle(ctx, 7)
	if err == nil {
		t.Fatal("RefreshSingle should force upstream refresh instead of using cached token")
	}
	if !strings.Contains(err.Error(), "refresh_token 为空") {
		t.Fatalf("RefreshSingle error = %v, want missing refresh_token", err)
	}
}

func TestApplyRefreshedPlanTypeKeepsFreeUsageLimitAuthoritative(t *testing.T) {
	now := time.Now()
	acc := &Account{
		PlanType:            "free",
		UsagePercent7d:      100,
		UsagePercent7dValid: true,
		Reset7dAt:           now.Add(time.Hour),
	}

	acc.mu.Lock()
	plan, applied := acc.applyRefreshedPlanTypeLocked("pro", now)
	acc.mu.Unlock()

	if plan != "pro" {
		t.Fatalf("plan = %q, want pro", plan)
	}
	if applied {
		t.Fatal("refreshed pro plan should not override active free usage-limit metadata")
	}
	if got := acc.GetPlanType(); got != "free" {
		t.Fatalf("PlanType = %q, want free", got)
	}
}

func TestApplyRefreshedPlanTypeKeepsActiveFreeUsageWindowAuthoritative(t *testing.T) {
	now := time.Now()
	acc := &Account{
		PlanType:            "free",
		UsagePercent7d:      3,
		UsagePercent7dValid: true,
		Reset7dAt:           now.Add(24 * time.Hour),
	}

	acc.mu.Lock()
	plan, applied := acc.applyRefreshedPlanTypeLocked("pro", now)
	acc.mu.Unlock()

	if plan != "pro" {
		t.Fatalf("plan = %q, want pro", plan)
	}
	if applied {
		t.Fatal("refreshed pro plan should not override an active free 7d usage window")
	}
	if got := acc.GetPlanType(); got != "free" {
		t.Fatalf("PlanType = %q, want free", got)
	}
}

func TestApplyRefreshedPlanTypeAllowsPlanUpgradeAfterUsageReset(t *testing.T) {
	now := time.Now()
	acc := &Account{
		PlanType:            "free",
		UsagePercent7d:      100,
		UsagePercent7dValid: true,
		Reset7dAt:           now.Add(-time.Minute),
	}

	acc.mu.Lock()
	plan, applied := acc.applyRefreshedPlanTypeLocked("pro", now)
	acc.mu.Unlock()

	if plan != "pro" || !applied {
		t.Fatalf("plan=%q applied=%v, want pro true", plan, applied)
	}
	if got := acc.GetPlanType(); got != "pro" {
		t.Fatalf("PlanType = %q, want pro", got)
	}
}

func TestStoreNextPrefersHigherDispatchScoreWithinTier(t *testing.T) {
	premium := &Account{
		DBID:        1,
		AccessToken: "token",
		Status:      StatusReady,
		PlanType:    "pro",
	}
	regular := &Account{
		DBID:        2,
		AccessToken: "token",
		Status:      StatusReady,
		PlanType:    "free",
	}
	recomputeTestAccount(premium, 2)
	recomputeTestAccount(regular, 2)

	store := &Store{
		accounts: []*Account{regular, premium},
	}
	store.SetMaxConcurrency(2)

	got := store.Next()
	if got == nil {
		t.Fatal("Next() returned nil")
	}
	defer store.Release(got)

	if got.DBID != premium.DBID {
		t.Fatalf("Next() picked dbID=%d, want premium account %d", got.DBID, premium.DBID)
	}
}

func TestStoreNextConcurrentAcquireDoesNotExceedDynamicLimit(t *testing.T) {
	acc := &Account{
		DBID:        1,
		AccessToken: "token",
		Status:      StatusReady,
		PlanType:    "pro",
	}
	store := &Store{
		accounts:       []*Account{acc},
		maxConcurrency: 1,
	}

	const workers = 32
	var entered int64
	start := make(chan struct{})
	filterGate := make(chan struct{})
	results := make(chan *Account, workers)

	filter := func(candidate *Account) bool {
		if candidate != nil && candidate.DBID == acc.DBID {
			atomic.AddInt64(&entered, 1)
		}
		<-filterGate
		return true
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			results <- store.NextExcludingWithFilter(0, nil, filter)
		}()
	}
	close(start)

	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&entered) < workers {
		select {
		case <-deadline:
			close(filterGate)
			t.Fatalf("only %d/%d workers reached the scheduler filter", atomic.LoadInt64(&entered), workers)
		default:
			time.Sleep(time.Millisecond)
		}
	}

	acc.mu.Lock()
	close(filterGate)
	time.Sleep(20 * time.Millisecond)
	acc.mu.Unlock()

	wg.Wait()
	close(results)

	acquired := 0
	for got := range results {
		if got != nil {
			acquired++
		}
	}
	if acquired != 1 {
		t.Fatalf("acquired accounts = %d, want 1", acquired)
	}
	if got := atomic.LoadInt64(&acc.ActiveRequests); got != 1 {
		t.Fatalf("ActiveRequests = %d, want 1", got)
	}
	store.Release(acc)
}

func TestWaitForSessionAvailableTriggersRecoveryProbeForOnlyErroredBannedAccount(t *testing.T) {
	store := NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:               1,
		RecoveryProbeIntervalMinutes: 30,
	})
	acc := &Account{
		DBID:          1,
		AccessToken:   "token",
		RefreshToken:  "refresh-token",
		ExpiresAt:     time.Now().Add(time.Hour),
		Status:        StatusError,
		HealthTier:    HealthTierBanned,
		FailureStreak: 3,
	}
	atomic.StoreInt32(&acc.Disabled, 1)
	store.AddAccount(acc)

	var probed int32
	store.SetUsageProbeFunc(func(_ context.Context, account *Account) error {
		if account.DBID != acc.DBID {
			t.Fatalf("recovery probe account dbID=%d, want %d", account.DBID, acc.DBID)
		}
		atomic.AddInt32(&probed, 1)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got, _ := store.WaitForSessionAvailableWithFilter(ctx, "", 500*time.Millisecond, 0, nil, nil)
	if got == nil {
		t.Fatal("WaitForSessionAvailableWithFilter() returned nil; want recovered account")
	}
	defer store.Release(got)
	if got.DBID != acc.DBID {
		t.Fatalf("WaitForSessionAvailableWithFilter() picked dbID=%d, want %d", got.DBID, acc.DBID)
	}
	if calls := atomic.LoadInt32(&probed); calls != 1 {
		t.Fatalf("recovery probe calls = %d, want 1", calls)
	}

	acc.mu.RLock()
	status := acc.Status
	healthTier := acc.HealthTier
	failureStreak := acc.FailureStreak
	acc.mu.RUnlock()
	if atomic.LoadInt32(&acc.Disabled) != 0 {
		t.Fatal("Disabled flag should be cleared after successful recovery probe")
	}
	if status != StatusReady {
		t.Fatalf("Status = %v, want ready", status)
	}
	if healthTier == HealthTierBanned {
		t.Fatal("HealthTier should recover from banned")
	}
	if failureStreak != 0 {
		t.Fatalf("FailureStreak = %d, want 0", failureStreak)
	}
}

func TestDispatchPoolSnapshotClassifiesBusyFilteredAndExcluded(t *testing.T) {
	store := NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1})
	busy := &Account{DBID: 1, AccessToken: "tok-1", Status: StatusReady}
	filtered := &Account{DBID: 2, AccessToken: "tok-2", Status: StatusReady, PlanType: "free"}
	excluded := &Account{DBID: 3, AccessToken: "tok-3", Status: StatusReady}
	store.AddAccount(busy)
	store.AddAccount(filtered)
	store.AddAccount(excluded)
	atomic.StoreInt64(&busy.ActiveRequests, 1)

	snapshot := store.DispatchPoolSnapshotWithFilter(0, map[int64]bool{3: true}, func(acc *Account) bool {
		return acc.PlanType != "free"
	})

	if snapshot.Total != 3 {
		t.Fatalf("Total = %d, want 3", snapshot.Total)
	}
	if snapshot.Busy != 1 {
		t.Fatalf("Busy = %d, want 1", snapshot.Busy)
	}
	if snapshot.Filtered != 1 {
		t.Fatalf("Filtered = %d, want 1", snapshot.Filtered)
	}
	if snapshot.Excluded != 1 {
		t.Fatalf("Excluded = %d, want 1", snapshot.Excluded)
	}
	if snapshot.Available != 0 {
		t.Fatalf("Available = %d, want 0", snapshot.Available)
	}
}

func TestDispatchQueueCapacityAppliesBackpressure(t *testing.T) {
	store := NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1})
	store.AddAccount(&Account{DBID: 1, AccessToken: "tok-1", Status: StatusReady})

	for i := 0; i < 3; i++ {
		release, ok := store.TryEnterDispatchQueue(1)
		if !ok {
			t.Fatalf("queue entrant %d rejected before capacity", i+1)
		}
		defer release()
	}
	release, ok := store.TryEnterDispatchQueue(1)
	if ok {
		release()
		t.Fatal("fourth queue entrant should be rejected at capacity 3")
	}
}

func TestDispatchQueueCapacityUsesConfiguredLimit(t *testing.T) {
	store := NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:     1,
		DispatchQueueLimit: 2,
	})
	store.AddAccount(&Account{DBID: 1, AccessToken: "tok-1", Status: StatusReady})

	firstRelease, ok := store.TryEnterDispatchQueue(10)
	if !ok {
		t.Fatal("first queue entrant rejected")
	}
	defer firstRelease()
	secondRelease, ok := store.TryEnterDispatchQueue(10)
	if !ok {
		t.Fatal("second queue entrant rejected")
	}
	defer secondRelease()
	release, ok := store.TryEnterDispatchQueue(10)
	if ok {
		release()
		t.Fatal("third queue entrant should be rejected by configured limit 2")
	}
}

func TestWaitForSessionAvailableTriggersRecoveryProbeForOpenAIResponsesAccount(t *testing.T) {
	store := NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:               1,
		RecoveryProbeIntervalMinutes: 30,
	})
	acc := &Account{
		DBID:         1,
		UpstreamType: UpstreamOpenAIResponses,
		BaseURL:      "https://api.example.test",
		APIKey:       "sk-test",
		Models:       []string{"gpt-5.4"},
		Status:       StatusError,
		HealthTier:   HealthTierBanned,
	}
	atomic.StoreInt32(&acc.Disabled, 1)
	store.AddAccount(acc)

	var probed int32
	store.SetUsageProbeFunc(func(_ context.Context, account *Account) error {
		if account.DBID != acc.DBID {
			t.Fatalf("recovery probe account dbID=%d, want %d", account.DBID, acc.DBID)
		}
		atomic.AddInt32(&probed, 1)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got, _ := store.WaitForSessionAvailableWithFilter(ctx, "", 500*time.Millisecond, 0, nil, nil)
	if got == nil {
		t.Fatal("WaitForSessionAvailableWithFilter() returned nil; want recovered OpenAI Responses account")
	}
	defer store.Release(got)
	if got.DBID != acc.DBID {
		t.Fatalf("WaitForSessionAvailableWithFilter() picked dbID=%d, want %d", got.DBID, acc.DBID)
	}
	if calls := atomic.LoadInt32(&probed); calls != 1 {
		t.Fatalf("recovery probe calls = %d, want 1", calls)
	}
}

func TestWaitForSessionAvailableTriggersRecoveryProbeForErroredOpenAIResponsesAccount(t *testing.T) {
	store := NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:               1,
		RecoveryProbeIntervalMinutes: 30,
	})
	acc := &Account{
		DBID:         1,
		UpstreamType: UpstreamOpenAIResponses,
		BaseURL:      "https://api.example.test",
		APIKey:       "sk-test",
		Models:       []string{"gpt-5.4"},
		Status:       StatusError,
		HealthTier:   HealthTierRisky,
	}
	store.AddAccount(acc)

	var probed int32
	store.SetUsageProbeFunc(func(_ context.Context, account *Account) error {
		if account.DBID != acc.DBID {
			t.Fatalf("recovery probe account dbID=%d, want %d", account.DBID, acc.DBID)
		}
		atomic.AddInt32(&probed, 1)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got, _ := store.WaitForSessionAvailableWithFilter(ctx, "", 500*time.Millisecond, 0, nil, nil)
	if got == nil {
		t.Fatal("WaitForSessionAvailableWithFilter() returned nil; want recovered errored OpenAI Responses account")
	}
	defer store.Release(got)
	if calls := atomic.LoadInt32(&probed); calls != 1 {
		t.Fatalf("recovery probe calls = %d, want 1", calls)
	}
}

func TestAccountPremium5hUrgencyBonusOnlyAffectsDispatchScore(t *testing.T) {
	acc := &Account{
		DBID:                1,
		AccessToken:         "token",
		Status:              StatusReady,
		PlanType:            "plus",
		UsagePercent5h:      20,
		UsagePercent5hValid: true,
		Reset5hAt:           time.Now().Add(30 * time.Minute),
		UsagePercent7d:      45,
		UsagePercent7dValid: true,
		Reset7dAt:           time.Now().Add(4 * 24 * time.Hour),
	}

	snapshot := acc.GetSchedulerDebugSnapshot(4)

	if snapshot.SchedulerScore != 100 {
		t.Fatalf("SchedulerScore = %v, want 100", snapshot.SchedulerScore)
	}
	if snapshot.Breakdown.UsageUrgencyBonus5h <= 20 {
		t.Fatalf("UsageUrgencyBonus5h = %v, want > 20", snapshot.Breakdown.UsageUrgencyBonus5h)
	}
	if snapshot.DispatchScore <= 170 {
		t.Fatalf("DispatchScore = %v, want plan bias plus urgency bonus", snapshot.DispatchScore)
	}
	if snapshot.HealthTier != string(HealthTierHealthy) {
		t.Fatalf("HealthTier = %q, want %q", snapshot.HealthTier, HealthTierHealthy)
	}
}

func TestAccountPremium5hUrgencyBonusSkipsNearlyExhaustedWindow(t *testing.T) {
	acc := &Account{
		DBID:                1,
		AccessToken:         "token",
		Status:              StatusReady,
		PlanType:            "plus",
		UsagePercent5h:      96,
		UsagePercent5hValid: true,
		Reset5hAt:           time.Now().Add(30 * time.Minute),
	}

	snapshot := acc.GetSchedulerDebugSnapshot(4)

	if snapshot.Breakdown.UsageUrgencyBonus5h != 0 {
		t.Fatalf("UsageUrgencyBonus5h = %v, want 0", snapshot.Breakdown.UsageUrgencyBonus5h)
	}
	if snapshot.DispatchScore != 150 {
		t.Fatalf("DispatchScore = %v, want only plan bias", snapshot.DispatchScore)
	}
}

func TestAccountPremium7dUrgencyBonusOnlyAffectsDispatchScore(t *testing.T) {
	acc := &Account{
		DBID:                1,
		AccessToken:         "token",
		Status:              StatusReady,
		PlanType:            "plus",
		UsagePercent7d:      63,
		UsagePercent7dValid: true,
		Reset7dAt:           time.Now().Add(36 * time.Hour),
	}

	snapshot := acc.GetSchedulerDebugSnapshot(4)

	if snapshot.SchedulerScore != 100 {
		t.Fatalf("SchedulerScore = %v, want 100", snapshot.SchedulerScore)
	}
	if snapshot.Breakdown.UsageUrgencyBonus7d <= 20 {
		t.Fatalf("UsageUrgencyBonus7d = %v, want > 20", snapshot.Breakdown.UsageUrgencyBonus7d)
	}
	if snapshot.DispatchScore <= 170 {
		t.Fatalf("DispatchScore = %v, want plan bias plus 7d urgency bonus", snapshot.DispatchScore)
	}
	if snapshot.HealthTier != string(HealthTierHealthy) {
		t.Fatalf("HealthTier = %q, want %q", snapshot.HealthTier, HealthTierHealthy)
	}
}

func TestAccountPremium7dUrgencyBonusSkipsDistantReset(t *testing.T) {
	acc := &Account{
		DBID:                1,
		AccessToken:         "token",
		Status:              StatusReady,
		PlanType:            "plus",
		UsagePercent7d:      63,
		UsagePercent7dValid: true,
		Reset7dAt:           time.Now().Add(5 * 24 * time.Hour),
	}

	snapshot := acc.GetSchedulerDebugSnapshot(4)

	if snapshot.Breakdown.UsageUrgencyBonus7d != 0 {
		t.Fatalf("UsageUrgencyBonus7d = %v, want 0", snapshot.Breakdown.UsageUrgencyBonus7d)
	}
	if snapshot.DispatchScore != 150 {
		t.Fatalf("DispatchScore = %v, want only plan bias", snapshot.DispatchScore)
	}
}

func TestStoreNextPrefersPremium7dResetSoonOverProvenAccount(t *testing.T) {
	now := time.Now()
	soon := &Account{
		DBID:                1,
		AccessToken:         "token",
		Status:              StatusReady,
		PlanType:            "plus",
		UsagePercent7d:      63,
		UsagePercent7dValid: true,
		Reset7dAt:           now.Add(36 * time.Hour),
	}
	later := &Account{
		DBID:                2,
		AccessToken:         "token",
		Status:              StatusReady,
		PlanType:            "plus",
		UsagePercent7d:      68,
		UsagePercent7dValid: true,
		Reset7dAt:           now.Add(5 * 24 * time.Hour),
	}
	atomic.StoreInt64(&later.TotalRequests, 450)
	recomputeTestAccount(soon, 2)
	recomputeTestAccount(later, 2)

	store := &Store{
		accounts: []*Account{later, soon},
	}
	store.SetMaxConcurrency(2)

	got := store.Next()
	if got == nil {
		t.Fatal("Next() returned nil")
	}
	defer store.Release(got)

	if got.DBID != soon.DBID {
		t.Fatalf("Next() picked dbID=%d, want 7d reset-soon account %d", got.DBID, soon.DBID)
	}
}

func TestStoreNextPrefersPremium5hResetSoonWithinTier(t *testing.T) {
	now := time.Now()
	soon := &Account{
		DBID:                1,
		AccessToken:         "token",
		Status:              StatusReady,
		PlanType:            "plus",
		UsagePercent5h:      25,
		UsagePercent5hValid: true,
		Reset5hAt:           now.Add(30 * time.Minute),
	}
	later := &Account{
		DBID:                2,
		AccessToken:         "token",
		Status:              StatusReady,
		PlanType:            "plus",
		UsagePercent5h:      25,
		UsagePercent5hValid: true,
		Reset5hAt:           now.Add(5 * time.Hour),
	}
	recomputeTestAccount(soon, 2)
	recomputeTestAccount(later, 2)

	store := &Store{
		accounts: []*Account{later, soon},
	}
	store.SetMaxConcurrency(2)

	got := store.Next()
	if got == nil {
		t.Fatal("Next() returned nil")
	}
	defer store.Release(got)

	if got.DBID != soon.DBID {
		t.Fatalf("Next() picked dbID=%d, want reset-soon account %d", got.DBID, soon.DBID)
	}
}

func TestStoreNextPrefersLowerTTFTWithinHealthyTier(t *testing.T) {
	fast := &Account{
		DBID:        1,
		AccessToken: "token",
		Status:      StatusReady,
		PlanType:    "plus",
	}
	slow := &Account{
		DBID:        2,
		AccessToken: "token",
		Status:      StatusReady,
		PlanType:    "plus",
	}
	atomic.StoreInt64(&fast.TotalRequests, 20)
	atomic.StoreInt64(&slow.TotalRequests, 20)

	store := &Store{
		accounts: []*Account{slow, fast},
	}
	store.SetMaxConcurrency(1)
	store.ReportRequestSuccessTTFT(fast, 800*time.Millisecond, 1200*time.Millisecond)
	store.ReportRequestSuccessTTFT(slow, 9*time.Second, 10*time.Second)

	got := store.Next()
	if got == nil {
		t.Fatal("Next() returned nil")
	}
	defer store.Release(got)

	if got.DBID != fast.DBID {
		t.Fatalf("Next() picked dbID=%d, want lower-TTFT account %d", got.DBID, fast.DBID)
	}
}
