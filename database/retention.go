package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RetentionPolicy defines retention windows supplied by the maintenance layer.
// Non-positive durations disable pruning for the corresponding table.
type RetentionPolicy struct {
	UsageLogs        time.Duration
	SecurityEvents   time.Duration
	PromptFilterLogs time.Duration
	AccountEvents    time.Duration
}

// RetentionResult reports rows deleted by one atomic maintenance pass.
type RetentionResult struct {
	UsageLogs        int64
	SecurityEvents   int64
	PromptFilterLogs int64
	AccountEvents    int64
}

type usageLogPruneScope struct {
	cutoff *time.Time
}

type usageLogBaselineDelta struct {
	maxID             int64
	rows              int64
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

// PruneUsageLogsBefore atomically rolls old usage totals into the lifecycle
// baseline and removes exactly the rows included in that rollup.
func (db *DB) PruneUsageLogsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return db.pruneUsageLogs(ctx, usageLogPruneScope{cutoff: &cutoff})
}

func (db *DB) pruneUsageLogs(ctx context.Context, scope usageLogPruneScope) (int64, error) {
	if err := db.flushLogsStrict(); err != nil {
		return 0, fmt.Errorf("刷新 usage_logs 失败: %w", err)
	}
	var deleted int64
	err := db.withRetentionTx(ctx, func(tx *sql.Tx) error {
		var err error
		deleted, err = db.pruneUsageLogsTx(ctx, tx, scope)
		return err
	})
	return deleted, err
}

func (db *DB) pruneUsageLogsTx(ctx context.Context, tx *sql.Tx, scope usageLogPruneScope) (int64, error) {
	if err := db.lockUsageLogsTx(ctx, tx, scope); err != nil {
		return 0, err
	}
	if err := lockUsageBaseline(ctx, tx); err != nil {
		return 0, err
	}
	delta, err := db.aggregateUsageLogsTx(ctx, tx, scope)
	if err != nil {
		return 0, err
	}
	if delta.rows == 0 {
		if scope.cutoff == nil {
			return 0, db.resetUsageLogIdentityTx(ctx, tx)
		}
		return 0, nil
	}
	if err := applyUsageBaselineDelta(ctx, tx, delta); err != nil {
		return 0, err
	}
	return db.deleteUsageLogsAndResetTx(ctx, tx, scope, delta)
}

func (db *DB) lockUsageLogsTx(ctx context.Context, tx *sql.Tx, scope usageLogPruneScope) error {
	if db.isSQLite() {
		return nil
	}
	mode := "SHARE ROW EXCLUSIVE"
	if scope.cutoff == nil {
		mode = "ACCESS EXCLUSIVE"
	}
	if _, err := tx.ExecContext(ctx, "LOCK TABLE usage_logs IN "+mode+" MODE"); err != nil {
		return fmt.Errorf("锁定 usage_logs 失败: %w", err)
	}
	return nil
}

func (db *DB) deleteUsageLogsAndResetTx(ctx context.Context, tx *sql.Tx, scope usageLogPruneScope, delta usageLogBaselineDelta) (int64, error) {
	deleted, err := db.deleteAggregatedUsageLogsTx(ctx, tx, scope, delta.maxID)
	if err != nil {
		return 0, err
	}
	if deleted != delta.rows {
		return 0, fmt.Errorf("usage_logs 原子删除计数不一致: aggregated=%d deleted=%d", delta.rows, deleted)
	}
	if scope.cutoff == nil {
		if err := db.resetUsageLogIdentityTx(ctx, tx); err != nil {
			return 0, err
		}
	}
	return deleted, nil
}

func (db *DB) resetUsageLogIdentityTx(ctx context.Context, tx *sql.Tx) error {
	query := `TRUNCATE TABLE usage_logs RESTART IDENTITY`
	if db.isSQLite() {
		query = `DELETE FROM sqlite_sequence WHERE name = 'usage_logs'`
	}
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("重置 usage_logs ID 失败: %w", err)
	}
	return nil
}

func lockUsageBaseline(ctx context.Context, tx *sql.Tx) error {
	result, err := tx.ExecContext(ctx, `UPDATE usage_stats_baseline SET id = id WHERE id = 1`)
	if err != nil {
		return fmt.Errorf("锁定使用统计基线失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("检查使用统计基线失败: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("使用统计基线不存在: affected=%d", affected)
	}
	return nil
}

func (db *DB) aggregateUsageLogsTx(ctx context.Context, tx *sql.Tx, scope usageLogPruneScope) (usageLogBaselineDelta, error) {
	query := `
		SELECT
			COALESCE(MAX(id), 0),
			COUNT(*),
			COALESCE(SUM(CASE WHEN status_code <> 499 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status_code <> 499 THEN total_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status_code <> 499 THEN prompt_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status_code <> 499 THEN completion_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status_code <> 499 THEN cached_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status_code = 200 AND cached_tokens > 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status_code = 200 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status_code = 200 AND first_token_ms > 0 THEN first_token_ms ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status_code = 200 AND first_token_ms > 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status_code <> 499 THEN account_billed ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status_code <> 499 THEN user_billed ELSE 0 END), 0)
		FROM usage_logs`
	args := []interface{}{}
	if scope.cutoff != nil {
		query += ` WHERE created_at < $1`
		args = append(args, db.timeArg(*scope.cutoff))
	}

	var delta usageLogBaselineDelta
	err := tx.QueryRowContext(ctx, query, args...).Scan(
		&delta.maxID, &delta.rows, &delta.totalRequests, &delta.totalTokens,
		&delta.promptTokens, &delta.completionTokens, &delta.cachedTokens,
		&delta.cacheHitRequests, &delta.cacheRateRequests,
		&delta.firstTokenMSSum, &delta.firstTokenSamples,
		&delta.accountBilled, &delta.userBilled,
	)
	if err != nil {
		return usageLogBaselineDelta{}, fmt.Errorf("聚合待清理 usage_logs 失败: %w", err)
	}
	return delta, nil
}

func applyUsageBaselineDelta(ctx context.Context, tx *sql.Tx, delta usageLogBaselineDelta) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE usage_stats_baseline SET
			total_requests = total_requests + $1,
			total_tokens = total_tokens + $2,
			prompt_tokens = prompt_tokens + $3,
			completion_tokens = completion_tokens + $4,
			cached_tokens = cached_tokens + $5,
			cache_hit_requests = cache_hit_requests + $6,
			cache_rate_requests = cache_rate_requests + $7,
			first_token_ms_sum = first_token_ms_sum + $8,
			first_token_samples = first_token_samples + $9,
			account_billed = account_billed + $10,
			user_billed = user_billed + $11
		WHERE id = 1
	`, delta.totalRequests, delta.totalTokens, delta.promptTokens,
		delta.completionTokens, delta.cachedTokens, delta.cacheHitRequests,
		delta.cacheRateRequests, delta.firstTokenMSSum, delta.firstTokenSamples,
		delta.accountBilled, delta.userBilled)
	if err != nil {
		return fmt.Errorf("更新使用统计基线失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("检查使用统计基线更新失败: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("使用统计基线更新异常: affected=%d", affected)
	}
	return nil
}

func (db *DB) deleteAggregatedUsageLogsTx(ctx context.Context, tx *sql.Tx, scope usageLogPruneScope, maxID int64) (int64, error) {
	query := `DELETE FROM usage_logs WHERE id <= $1`
	args := []interface{}{maxID}
	if scope.cutoff != nil {
		query += ` AND created_at < $2`
		args = append(args, db.timeArg(*scope.cutoff))
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("删除 usage_logs 失败: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("统计 usage_logs 删除数量失败: %w", err)
	}
	return deleted, nil
}

// PruneOperationalDataBefore prunes configured operational tables in one
// transaction. Retention defaults remain the caller's responsibility.
func (db *DB) PruneOperationalDataBefore(ctx context.Context, policy RetentionPolicy, now time.Time) (RetentionResult, error) {
	if err := db.flushLogsStrict(); err != nil {
		return RetentionResult{}, fmt.Errorf("刷新 usage_logs 失败: %w", err)
	}
	var result RetentionResult
	err := db.withRetentionTx(ctx, func(tx *sql.Tx) error {
		if policy.UsageLogs > 0 {
			cutoff := now.Add(-policy.UsageLogs)
			deleted, err := db.pruneUsageLogsTx(ctx, tx, usageLogPruneScope{cutoff: &cutoff})
			if err != nil {
				return err
			}
			result.UsageLogs = deleted
		}
		operations := []struct {
			table    string
			duration time.Duration
			deleted  *int64
		}{
			{table: "security_events", duration: policy.SecurityEvents, deleted: &result.SecurityEvents},
			{table: "prompt_filter_logs", duration: policy.PromptFilterLogs, deleted: &result.PromptFilterLogs},
			{table: "account_events", duration: policy.AccountEvents, deleted: &result.AccountEvents},
		}
		for _, operation := range operations {
			if operation.duration <= 0 {
				continue
			}
			deleted, err := db.deleteRowsBeforeTx(ctx, tx, operation.table, now.Add(-operation.duration))
			if err != nil {
				return err
			}
			*operation.deleted = deleted
		}
		return nil
	})
	return result, err
}

func (db *DB) deleteRowsBeforeTx(ctx context.Context, tx *sql.Tx, table string, cutoff time.Time) (int64, error) {
	query := fmt.Sprintf("DELETE FROM %s WHERE created_at < $1", table)
	result, err := tx.ExecContext(ctx, query, db.timeArg(cutoff))
	if err != nil {
		return 0, fmt.Errorf("清理 %s 失败: %w", table, err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("统计 %s 删除数量失败: %w", table, err)
	}
	return deleted, nil
}

func (db *DB) withRetentionTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始保留事务失败: %w", err)
	}
	if err := fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return fmt.Errorf("%w; 回滚保留事务失败: %v", err, rollbackErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("提交保留事务失败: %w", err)
	}
	return nil
}
