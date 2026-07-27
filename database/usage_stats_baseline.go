package database

import (
	"context"
	"fmt"
	"time"
)

const usageStatsVersion = 2

type usageStatsBaselineV2 struct {
	totalRequests     int64
	totalTokens       int64
	promptTokens      int64
	completionTokens  int64
	cachedTokens      int64
	cacheHitRequests  int64
	cacheRateRequests int64
	firstTokenMSSum   float64
	firstTokenSamples int64
	accountBilled     float64
	userBilled        float64
	accurateSince     time.Time
}

func usageStatsBaselineV2Statements(sqlite bool) (string, string) {
	createTable := `
		CREATE TABLE IF NOT EXISTS usage_stats_baseline_v2 (
			id                  INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			total_requests      BIGINT NOT NULL DEFAULT 0,
			total_tokens        BIGINT NOT NULL DEFAULT 0,
			prompt_tokens       BIGINT NOT NULL DEFAULT 0,
			completion_tokens   BIGINT NOT NULL DEFAULT 0,
			cached_tokens       BIGINT NOT NULL DEFAULT 0,
			cache_hit_requests  BIGINT NOT NULL DEFAULT 0,
			cache_rate_requests BIGINT NOT NULL DEFAULT 0,
			first_token_ms_sum  DOUBLE PRECISION NOT NULL DEFAULT 0,
			first_token_samples BIGINT NOT NULL DEFAULT 0,
			account_billed      DOUBLE PRECISION NOT NULL DEFAULT 0,
			user_billed         DOUBLE PRECISION NOT NULL DEFAULT 0,
			accurate_since      TIMESTAMPTZ NOT NULL
		)
	`
	insertBaseline := `
		INSERT INTO usage_stats_baseline_v2 (id, accurate_since)
		SELECT 1, COALESCE(MIN(created_at), $1) FROM usage_logs
		ON CONFLICT DO NOTHING
	`
	if sqlite {
		createTable = `
			CREATE TABLE IF NOT EXISTS usage_stats_baseline_v2 (
				id                  INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
				total_requests      INTEGER NOT NULL DEFAULT 0,
				total_tokens        INTEGER NOT NULL DEFAULT 0,
				prompt_tokens       INTEGER NOT NULL DEFAULT 0,
				completion_tokens   INTEGER NOT NULL DEFAULT 0,
				cached_tokens       INTEGER NOT NULL DEFAULT 0,
				cache_hit_requests  INTEGER NOT NULL DEFAULT 0,
				cache_rate_requests INTEGER NOT NULL DEFAULT 0,
				first_token_ms_sum  REAL NOT NULL DEFAULT 0,
				first_token_samples INTEGER NOT NULL DEFAULT 0,
				account_billed      REAL NOT NULL DEFAULT 0,
				user_billed         REAL NOT NULL DEFAULT 0,
				accurate_since      TIMESTAMP NOT NULL
			)
		`
		insertBaseline = `
			INSERT OR IGNORE INTO usage_stats_baseline_v2 (id, accurate_since)
			SELECT 1, COALESCE(MIN(created_at), $1) FROM usage_logs
		`
	}
	return createTable, insertBaseline
}

func (db *DB) ensureUsageStatsBaselineV2(ctx context.Context) error {
	createTable, insertBaseline := usageStatsBaselineV2Statements(db.isSQLite())
	if _, err := db.conn.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("创建 usage_stats_baseline_v2 表失败: %w", err)
	}
	if _, err := db.conn.ExecContext(ctx, insertBaseline, db.timeArg(time.Now())); err != nil {
		return fmt.Errorf("初始化 usage_stats_baseline_v2 失败: %w", err)
	}
	return nil
}

func (db *DB) readUsageStatsBaselineV2(ctx context.Context) (usageStatsBaselineV2, error) {
	var baseline usageStatsBaselineV2
	var accurateSinceRaw interface{}
	err := db.conn.QueryRowContext(ctx, `
		SELECT total_requests, total_tokens, prompt_tokens, completion_tokens,
			cached_tokens, cache_hit_requests, cache_rate_requests,
			first_token_ms_sum, first_token_samples, account_billed, user_billed,
			accurate_since
		FROM usage_stats_baseline_v2 WHERE id = 1
	`).Scan(
		&baseline.totalRequests, &baseline.totalTokens,
		&baseline.promptTokens, &baseline.completionTokens, &baseline.cachedTokens,
		&baseline.cacheHitRequests, &baseline.cacheRateRequests,
		&baseline.firstTokenMSSum, &baseline.firstTokenSamples,
		&baseline.accountBilled, &baseline.userBilled, &accurateSinceRaw,
	)
	if err != nil {
		return usageStatsBaselineV2{}, err
	}
	baseline.accurateSince, err = parseDBTimeValue(accurateSinceRaw)
	if err != nil {
		return usageStatsBaselineV2{}, err
	}
	return baseline, nil
}

func (db *DB) legacyUsageStatsBaselineAvailable(ctx context.Context) (bool, error) {
	var available bool
	err := db.conn.QueryRowContext(ctx, `
		SELECT CASE WHEN EXISTS (
			SELECT 1 FROM usage_stats_baseline
			WHERE id = 1 AND (
				total_requests <> 0 OR total_tokens <> 0 OR prompt_tokens <> 0 OR
				completion_tokens <> 0 OR cached_tokens <> 0 OR
				cache_hit_requests <> 0 OR cache_rate_requests <> 0 OR
				first_token_ms_sum <> 0 OR first_token_samples <> 0 OR
				account_billed <> 0 OR user_billed <> 0
			)
		) THEN true ELSE false END
	`).Scan(&available)
	return available, err
}
