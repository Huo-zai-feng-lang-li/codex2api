package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type usageBaseline struct {
	totalRequests     int64
	totalTokens       int64
	promptTokens      int64
	completionTokens  int64
	cachedTokens      int64
	cacheHitRequests  int64
	cacheRateRequests int64
	firstTokenMSSum   int64
	firstTokenSamples int64
	accountBilled     float64
	userBilled        float64
}

func newRetentionTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New("sqlite", filepath.Join(t.TempDir(), "retention.db"))
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertRetentionUsageLog(t *testing.T, db *DB, createdAt time.Time, statusCode int, prompt, completion, total, cached, firstToken int, accountBilled, userBilled float64) {
	t.Helper()
	_, err := db.conn.ExecContext(context.Background(), `
		INSERT INTO usage_logs (
			created_at, status_code, prompt_tokens, completion_tokens, total_tokens,
			cached_tokens, first_token_ms, account_billed, user_billed
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, db.timeArg(createdAt), statusCode, prompt, completion, total, cached, firstToken, accountBilled, userBilled)
	if err != nil {
		t.Fatalf("插入 usage_logs 返回错误: %v", err)
	}
}

func readUsageBaseline(t *testing.T, db *DB) usageBaseline {
	t.Helper()
	var got usageBaseline
	err := db.conn.QueryRowContext(context.Background(), `
		SELECT total_requests, total_tokens, prompt_tokens, completion_tokens,
			cached_tokens, cache_hit_requests, cache_rate_requests,
			first_token_ms_sum, first_token_samples, account_billed, user_billed
		FROM usage_stats_baseline WHERE id = 1
	`).Scan(
		&got.totalRequests, &got.totalTokens, &got.promptTokens, &got.completionTokens,
		&got.cachedTokens, &got.cacheHitRequests, &got.cacheRateRequests,
		&got.firstTokenMSSum, &got.firstTokenSamples, &got.accountBilled, &got.userBilled,
	)
	if err != nil {
		t.Fatalf("读取 usage_stats_baseline 返回错误: %v", err)
	}
	return got
}

func tableCount(t *testing.T, db *DB, table string) int64 {
	t.Helper()
	var count int64
	if err := db.conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		t.Fatalf("统计 %s 返回错误: %v", table, err)
	}
	return count
}

func TestPruneUsageLogsBeforePreservesLifecycleTotals(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()
	cutoff := time.Now().UTC().Truncate(time.Second)

	insertRetentionUsageLog(t, db, cutoff.Add(-3*time.Hour), 200, 10, 20, 30, 8, 120, 1.25, 2.5)
	insertRetentionUsageLog(t, db, cutoff.Add(-2*time.Hour), 500, 3, 4, 7, 6, 900, 0.75, 1.5)
	insertRetentionUsageLog(t, db, cutoff.Add(-time.Hour), 499, 100, 200, 300, 250, 800, 20, 30)
	insertRetentionUsageLog(t, db, cutoff.Add(time.Hour), 200, 5, 6, 11, 0, 60, 0.5, 1)

	deleted, err := db.PruneUsageLogsBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneUsageLogsBefore 返回错误: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted = %d, want 3", deleted)
	}
	if got := tableCount(t, db, "usage_logs"); got != 1 {
		t.Fatalf("usage_logs count = %d, want 1", got)
	}

	want := usageBaseline{
		totalRequests: 2, totalTokens: 37, promptTokens: 13, completionTokens: 24,
		cachedTokens: 14, cacheHitRequests: 1, cacheRateRequests: 1,
		firstTokenMSSum: 120, firstTokenSamples: 1,
		accountBilled: 2, userBilled: 4,
	}
	if got := readUsageBaseline(t, db); got != want {
		t.Fatalf("baseline = %#v, want %#v", got, want)
	}

	deleted, err = db.PruneUsageLogsBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("第二次 PruneUsageLogsBefore 返回错误: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("第二次 deleted = %d, want 0", deleted)
	}
	if got := readUsageBaseline(t, db); got != want {
		t.Fatalf("第二次 baseline = %#v, want %#v", got, want)
	}
}

func TestPruneUsageLogsBeforeFlushesBufferedLogs(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()
	if err := db.InsertUsageLog(ctx, &UsageLogInput{
		StatusCode: 200, PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10,
		FirstTokenMs: 75,
	}); err != nil {
		t.Fatalf("InsertUsageLog 返回错误: %v", err)
	}

	deleted, err := db.PruneUsageLogsBefore(ctx, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("PruneUsageLogsBefore 返回错误: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if got := readUsageBaseline(t, db); got.totalTokens != 10 || got.firstTokenMSSum != 75 {
		t.Fatalf("baseline = %#v, want flushed totals", got)
	}
}

func TestPruneUsageLogsBeforeRollsBackOnDeleteFailure(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()
	cutoff := time.Now().Add(time.Hour)
	insertRetentionUsageLog(t, db, cutoff.Add(-time.Hour), 200, 1, 2, 3, 1, 50, 1, 2)
	if _, err := db.conn.ExecContext(ctx, `
		CREATE TRIGGER fail_usage_delete BEFORE DELETE ON usage_logs
		BEGIN SELECT RAISE(ABORT, 'forced usage delete failure'); END
	`); err != nil {
		t.Fatalf("创建失败触发器返回错误: %v", err)
	}

	if _, err := db.PruneUsageLogsBefore(ctx, cutoff); err == nil {
		t.Fatal("PruneUsageLogsBefore 应返回删除失败")
	}
	if got := tableCount(t, db, "usage_logs"); got != 1 {
		t.Fatalf("回滚后 usage_logs count = %d, want 1", got)
	}
	if got := readUsageBaseline(t, db); got != (usageBaseline{}) {
		t.Fatalf("回滚后 baseline = %#v, want zero", got)
	}
}

func TestPruneOperationalDataBeforeUsesDurationsWithoutDeletingImages(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := db.timeArg(now.Add(-48 * time.Hour))
	recent := db.timeArg(now.Add(-time.Hour))

	insertRetentionUsageLog(t, db, now.Add(-48*time.Hour), 200, 1, 2, 3, 0, 40, 0.25, 0.5)
	for _, stmt := range []string{
		`INSERT INTO security_events (created_at) VALUES ($1), ($2)`,
		`INSERT INTO prompt_filter_logs (created_at) VALUES ($1), ($2)`,
		`INSERT INTO account_events (event_type, created_at) VALUES ('old', $1), ('recent', $2)`,
	} {
		if _, err := db.conn.ExecContext(ctx, stmt, old, recent); err != nil {
			t.Fatalf("插入生命周期数据返回错误: %v", err)
		}
	}
	if _, err := db.conn.ExecContext(ctx, `
		INSERT INTO image_assets (filename, storage_path, created_at) VALUES ('old.png', 'old.png', $1)
	`, old); err != nil {
		t.Fatalf("插入 image_assets 返回错误: %v", err)
	}

	result, err := db.PruneOperationalDataBefore(ctx, RetentionPolicy{
		UsageLogs:        24 * time.Hour,
		SecurityEvents:   24 * time.Hour,
		PromptFilterLogs: 24 * time.Hour,
		AccountEvents:    24 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("PruneOperationalDataBefore 返回错误: %v", err)
	}
	if result != (RetentionResult{UsageLogs: 1, SecurityEvents: 1, PromptFilterLogs: 1, AccountEvents: 1}) {
		t.Fatalf("result = %#v, want each count 1", result)
	}
	for _, table := range []string{"security_events", "prompt_filter_logs", "account_events"} {
		if got := tableCount(t, db, table); got != 1 {
			t.Fatalf("%s count = %d, want 1", table, got)
		}
	}
	if got := tableCount(t, db, "image_assets"); got != 1 {
		t.Fatalf("image_assets count = %d, want 1", got)
	}
}

func TestPruneOperationalDataBeforeZeroDurationsDoNothing(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()
	old := db.timeArg(time.Now().Add(-365 * 24 * time.Hour))
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO security_events (created_at) VALUES ($1)`, old); err != nil {
		t.Fatalf("插入 security_events 返回错误: %v", err)
	}

	result, err := db.PruneOperationalDataBefore(ctx, RetentionPolicy{}, time.Now())
	if err != nil {
		t.Fatalf("PruneOperationalDataBefore 返回错误: %v", err)
	}
	if result != (RetentionResult{}) || tableCount(t, db, "security_events") != 1 {
		t.Fatalf("零 duration 不应删除数据: result=%#v", result)
	}
}

func TestPruneOperationalDataBeforeRollsBackAllTables(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := db.timeArg(now.Add(-48 * time.Hour))
	insertRetentionUsageLog(t, db, now.Add(-48*time.Hour), 200, 1, 2, 3, 0, 40, 0.25, 0.5)
	for _, stmt := range []string{
		`INSERT INTO security_events (created_at) VALUES ($1)`,
		`INSERT INTO prompt_filter_logs (created_at) VALUES ($1)`,
		`INSERT INTO account_events (event_type, created_at) VALUES ('old', $1)`,
	} {
		if _, err := db.conn.ExecContext(ctx, stmt, old); err != nil {
			t.Fatalf("插入回滚数据返回错误: %v", err)
		}
	}
	if _, err := db.conn.ExecContext(ctx, `
		CREATE TRIGGER fail_account_event_delete BEFORE DELETE ON account_events
		BEGIN SELECT RAISE(ABORT, 'forced account event delete failure'); END
	`); err != nil {
		t.Fatalf("创建失败触发器返回错误: %v", err)
	}

	policy := RetentionPolicy{
		UsageLogs: 24 * time.Hour, SecurityEvents: 24 * time.Hour,
		PromptFilterLogs: 24 * time.Hour, AccountEvents: 24 * time.Hour,
	}
	if _, err := db.PruneOperationalDataBefore(ctx, policy, now); err == nil {
		t.Fatal("PruneOperationalDataBefore 应返回删除失败")
	}
	for _, table := range []string{"usage_logs", "security_events", "prompt_filter_logs", "account_events"} {
		if got := tableCount(t, db, table); got != 1 {
			t.Fatalf("回滚后 %s count = %d, want 1", table, got)
		}
	}
	if got := readUsageBaseline(t, db); got != (usageBaseline{}) {
		t.Fatalf("回滚后 baseline = %#v, want zero", got)
	}
}
