package database

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
	"time"
)

type usageStatsV2Baseline struct {
	totalRequests    int64
	totalTokens      int64
	accountBilled    float64
	userBilled       float64
	accurateSinceRaw interface{}
}

func reopenUsageStatsV2DB(t *testing.T, path string) *DB {
	t.Helper()
	db, err := New("sqlite", path)
	if err != nil {
		t.Fatalf("重新打开 SQLite 返回错误: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertUsageStatsV2Fixture(t *testing.T, db *DB, createdAt time.Time) {
	t.Helper()
	ctx := context.Background()
	rows := []struct {
		createdAt      time.Time
		statusCode     int
		isRetryAttempt interface{}
		attemptIndex   int
		inputTokens    int
		outputTokens   int
		cachedTokens   int
		accountBilled  float64
		userBilled     float64
		durationMs     int
		firstTokenMs   int
	}{
		{createdAt, 200, nil, 0, 10, 20, 5, 1, 2, 100, 40},
		{createdAt, 598, true, 1, 30, 40, 9, 3, 4, 900, 800},
		{createdAt, 400, false, 2, 3, 4, 0, 0.5, 1, 200, 0},
		{createdAt.Add(-2 * time.Minute), 499, false, 0, 100, 200, 50, 20, 30, 700, 600},
	}
	for _, row := range rows {
		_, err := db.conn.ExecContext(ctx, `
			INSERT INTO usage_logs (
				account_id, endpoint, inbound_endpoint, model, effective_model,
				prompt_tokens, completion_tokens, total_tokens,
				input_tokens, output_tokens, cached_tokens,
				status_code, duration_ms, first_token_ms,
				api_key_id, api_key_name, api_key_masked,
				account_billed, user_billed, is_retry_attempt, attempt_index, created_at
			) VALUES (
				1, '/v1/responses', '/v1/responses', 'gpt-fixture', 'gpt-fixture',
				$1, $2, $3, $1, $2, $4,
				$5, $6, $7, 9, 'fixture-key', 'sk-fixture',
				$8, $9, $10, $11, $12
			)
		`, row.inputTokens, row.outputTokens, row.inputTokens+row.outputTokens,
			row.cachedTokens, row.statusCode, row.durationMs, row.firstTokenMs,
			row.accountBilled, row.userBilled, row.isRetryAttempt, row.attemptIndex,
			db.timeArg(row.createdAt))
		if err != nil {
			t.Fatalf("插入 usage_stats v2 fixture 返回错误: %v", err)
		}
	}
}

func readUsageStatsV2Baseline(t *testing.T, db *DB) usageStatsV2Baseline {
	t.Helper()
	var got usageStatsV2Baseline
	err := db.conn.QueryRowContext(context.Background(), `
		SELECT total_requests, total_tokens, account_billed, user_billed, accurate_since
		FROM usage_stats_baseline_v2 WHERE id = 1
	`).Scan(&got.totalRequests, &got.totalTokens, &got.accountBilled, &got.userBilled, &got.accurateSinceRaw)
	if err != nil {
		t.Fatalf("读取 usage_stats_baseline_v2 返回错误: %v", err)
	}
	return got
}

func assertFloat64(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func assertUsageStatsV2Totals(t *testing.T, stats *UsageStats) {
	t.Helper()
	if stats.TotalRequests != 2 || stats.TotalTokens != 37 {
		t.Fatalf("total requests/tokens = %d/%d, want 2/37", stats.TotalRequests, stats.TotalTokens)
	}
	assertFloat64(t, "total account billed", stats.TotalAccountBilled, 4.5)
	assertFloat64(t, "total user billed", stats.TotalUserBilled, 3)
}

func TestUsageStatsV2UsesTerminalBusinessRowsAndAttemptAccountCost(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "usage_stats_v2.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	if _, err := db.conn.ExecContext(ctx, `DROP TABLE IF EXISTS usage_stats_baseline_v2`); err != nil {
		t.Fatalf("删除 v2 基线表返回错误: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	insertUsageStatsV2Fixture(t, db, now)
	if _, err := db.conn.ExecContext(ctx, `
		UPDATE usage_stats_baseline SET
			total_requests = 100, total_tokens = 1000,
			account_billed = 100, user_billed = 100
		WHERE id = 1
	`); err != nil {
		t.Fatalf("写入 legacy baseline 返回错误: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭迁移前数据库返回错误: %v", err)
	}

	db = reopenUsageStatsV2DB(t, dbPath)
	baseline := readUsageStatsV2Baseline(t, db)
	if baseline.totalRequests != 0 || baseline.totalTokens != 0 || baseline.accountBilled != 0 || baseline.userBilled != 0 {
		t.Fatalf("v2 初始基线应为零: %#v", baseline)
	}
	accurateSince, err := parseDBTimeValue(baseline.accurateSinceRaw)
	if err != nil {
		t.Fatalf("解析 accurate_since 返回错误: %v", err)
	}
	wantAccurateSince := now.Add(-2 * time.Minute)
	if !accurateSince.Equal(wantAccurateSince) {
		t.Fatalf("accurate_since = %s, want %s", accurateSince, wantAccurateSince)
	}

	stats, err := db.GetUsageStats(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("GetUsageStats 返回错误: %v", err)
	}
	assertUsageStatsV2Totals(t, stats)
	if stats.FeatureStats.RetryRequests != 1 || stats.FeatureStats.ErrorRequests != 1 {
		t.Fatalf("retry/error requests = %d/%d, want 1/1", stats.FeatureStats.RetryRequests, stats.FeatureStats.ErrorRequests)
	}
	if stats.TodayRequests != 2 || stats.TodayTokens != 37 {
		t.Fatalf("range requests/tokens = %d/%d, want 2/37", stats.TodayRequests, stats.TodayTokens)
	}
	assertFloat64(t, "range account billed", stats.TodayAccountBilled, 4.5)
	assertFloat64(t, "range user billed", stats.TodayUserBilled, 3)

	payload, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("编码 UsageStats 返回错误: %v", err)
	}
	var metadata struct {
		StatsVersion            int       `json:"stats_version"`
		AccurateSince           time.Time `json:"accurate_since"`
		LegacyBaselineAvailable bool      `json:"legacy_baseline_available"`
	}
	if err := json.Unmarshal(payload, &metadata); err != nil {
		t.Fatalf("解码 UsageStats 元数据返回错误: %v", err)
	}
	if metadata.StatsVersion != 2 || !metadata.AccurateSince.Equal(wantAccurateSince) || !metadata.LegacyBaselineAvailable {
		t.Fatalf("UsageStats 元数据错误: %#v", metadata)
	}

	if len(stats.ModelStats) != 1 {
		t.Fatalf("model stats 数量 = %d, want 1", len(stats.ModelStats))
	}
	model := stats.ModelStats[0]
	if model.Requests != 2 || model.Tokens != 37 || model.ErrorCount != 1 {
		t.Fatalf("model requests/tokens/errors = %d/%d/%d, want 2/37/1", model.Requests, model.Tokens, model.ErrorCount)
	}
	assertFloat64(t, "model account billed", model.AccountBilled, 4.5)
	assertFloat64(t, "model user billed", model.UserBilled, 3)

	window, err := db.GetAPIKeyWindowUsage(ctx, 9, time.Hour)
	if err != nil {
		t.Fatalf("GetAPIKeyWindowUsage 返回错误: %v", err)
	}
	if window.Requests != 2 || window.Tokens != 37 || window.UserBilled != 3 {
		t.Fatalf("API key window = %#v, want requests=2 tokens=37 user=3", window)
	}
	apiKeys, err := db.ListAPIKeyTokenStats(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("ListAPIKeyTokenStats 返回错误: %v", err)
	}
	if len(apiKeys) != 1 || apiKeys[0].Requests != 2 || apiKeys[0].TotalTokens != 37 || apiKeys[0].ErrorCount != 1 || apiKeys[0].UserBilled != 3 {
		t.Fatalf("API key stats = %#v", apiKeys)
	}

	account, err := db.GetAccountUsageStats(ctx, 1)
	if err != nil {
		t.Fatalf("GetAccountUsageStats 返回错误: %v", err)
	}
	if account.TotalRequests != 2 || account.TotalTokens != 37 || len(account.Models) != 1 || account.Models[0].Requests != 2 {
		t.Fatalf("account stats = %#v", account)
	}
	accounts, err := db.GetAccountTimeRangeUsage(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetAccountTimeRangeUsage 返回错误: %v", err)
	}
	accountRange := accounts[1]
	if accountRange == nil || accountRange.Requests != 2 || accountRange.Tokens != 37 || accountRange.UserBilled != 3 || accountRange.AccountBilled != 4.5 {
		t.Fatalf("account range stats = %#v", accountRange)
	}

	snapshot, err := db.GetTrafficSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetTrafficSnapshot 返回错误: %v", err)
	}
	assertFloat64(t, "snapshot qps", snapshot.QPS, 0.2)
	assertFloat64(t, "snapshot tps", snapshot.TPS, 3.7)
	assertFloat64(t, "snapshot qps peak", snapshot.QPSPeak, 2)
	assertFloat64(t, "snapshot tps peak", snapshot.TPSPeak, 37)

	chart, err := db.GetChartAggregation(ctx, now.Add(-time.Hour), now.Add(time.Hour), 5)
	if err != nil {
		t.Fatalf("GetChartAggregation 返回错误: %v", err)
	}
	if len(chart.Timeline) != 1 || chart.Timeline[0].Requests != 2 || chart.Timeline[0].Errors4xx != 1 || chart.Timeline[0].Errors5xx != 0 {
		t.Fatalf("chart timeline = %#v", chart.Timeline)
	}
	if len(chart.Models) != 1 || chart.Models[0].Requests != 2 {
		t.Fatalf("chart models = %#v", chart.Models)
	}

	deleted, err := db.PruneUsageLogsBefore(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("PruneUsageLogsBefore 返回错误: %v", err)
	}
	if deleted != 4 {
		t.Fatalf("retention deleted = %d, want 4", deleted)
	}
	afterRetention, err := db.GetUsageStats(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("retention 后 GetUsageStats 返回错误: %v", err)
	}
	assertUsageStatsV2Totals(t, afterRetention)
	baseline = readUsageStatsV2Baseline(t, db)
	if baseline.totalRequests != 2 || baseline.totalTokens != 37 || baseline.accountBilled != 4.5 || baseline.userBilled != 3 {
		t.Fatalf("retention v2 baseline = %#v", baseline)
	}

	var legacyRequests int64
	if err := db.conn.QueryRowContext(ctx, `SELECT total_requests FROM usage_stats_baseline WHERE id = 1`).Scan(&legacyRequests); err != nil {
		t.Fatalf("读取 legacy baseline 返回错误: %v", err)
	}
	if legacyRequests != 100 {
		t.Fatalf("legacy baseline total_requests = %d, want 100", legacyRequests)
	}

	if _, err := db.conn.ExecContext(ctx, `INSERT INTO usage_logs (status_code, total_tokens) VALUES (200, 9)`); err != nil {
		t.Fatalf("插入清理 fixture 返回错误: %v", err)
	}
	if err := db.ClearUsageLogs(ctx); err != nil {
		t.Fatalf("ClearUsageLogs 返回错误: %v", err)
	}
	baseline = readUsageStatsV2Baseline(t, db)
	if baseline.totalRequests != 0 || baseline.totalTokens != 0 || baseline.accountBilled != 0 || baseline.userBilled != 0 {
		t.Fatalf("ClearUsageLogs 后 v2 baseline = %#v, want zero", baseline)
	}
	if err := db.conn.QueryRowContext(ctx, `SELECT total_requests FROM usage_stats_baseline WHERE id = 1`).Scan(&legacyRequests); err != nil {
		t.Fatalf("清理后读取 legacy baseline 返回错误: %v", err)
	}
	if legacyRequests != 100 {
		t.Fatalf("清理后 legacy baseline total_requests = %d, want 100", legacyRequests)
	}
}

func TestUsageStatsV2MigrationIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage_stats_v2_idempotent.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("首次打开 SQLite 返回错误: %v", err)
	}
	first := readUsageStatsV2Baseline(t, db)
	firstAccurateSince, err := parseDBTimeValue(first.accurateSinceRaw)
	if err != nil || firstAccurateSince.IsZero() {
		t.Fatalf("首次 accurate_since = %v, err=%v", first.accurateSinceRaw, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("首次关闭数据库返回错误: %v", err)
	}

	db = reopenUsageStatsV2DB(t, dbPath)
	second := readUsageStatsV2Baseline(t, db)
	secondAccurateSince, err := parseDBTimeValue(second.accurateSinceRaw)
	if err != nil {
		t.Fatalf("二次解析 accurate_since 返回错误: %v", err)
	}
	if !secondAccurateSince.Equal(firstAccurateSince) {
		t.Fatalf("幂等迁移改变 accurate_since: first=%s second=%s", firstAccurateSince, secondAccurateSince)
	}
}

func TestAPIKeyQuotaUsageCountsOnlyNonRetryBusinessRows(t *testing.T) {
	ctx := context.Background()
	db, err := New("sqlite", filepath.Join(t.TempDir(), "api_key_quota_terminal.db"))
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	key := "sk-terminal-quota-1234567890"
	apiKeyID, err := db.InsertAPIKey(ctx, "terminal-quota", key)
	if err != nil {
		t.Fatalf("InsertAPIKey 返回错误: %v", err)
	}
	for _, usageLog := range []*UsageLogInput{
		{
			APIKeyID:       apiKeyID,
			Model:          "gpt-5.4",
			StatusCode:     598,
			InputTokens:    1000,
			IsRetryAttempt: true,
			AttemptIndex:   1,
		},
		{
			APIKeyID:     apiKeyID,
			Model:        "gpt-5.4",
			StatusCode:   400,
			InputTokens:  1000,
			AttemptIndex: 2,
		},
	} {
		if err := db.InsertUsageLog(ctx, usageLog); err != nil {
			t.Fatalf("InsertUsageLog 返回错误: %v", err)
		}
	}
	if err := db.flushLogsStrict(); err != nil {
		t.Fatalf("flushLogsStrict 返回错误: %v", err)
	}

	row, err := db.GetAPIKeyByValue(ctx, key)
	if err != nil {
		t.Fatalf("GetAPIKeyByValue 返回错误: %v", err)
	}
	assertFloat64(t, "quota_used", row.QuotaUsed, 0.0025)
}
