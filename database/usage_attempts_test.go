package database

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestShouldApplyAPIKeyQuotaUsage(t *testing.T) {
	tests := []struct {
		name  string
		entry usageLogEntry
		want  bool
	}{
		{
			name: "terminal attempt with nonzero index",
			entry: usageLogEntry{
				APIKeyID: 1, UserBilled: 1, StatusCode: 400, AttemptIndex: 2,
			},
			want: true,
		},
		{
			name: "retry attempt",
			entry: usageLogEntry{
				APIKeyID: 1, UserBilled: 1, StatusCode: 598, IsRetryAttempt: true,
			},
		},
		{
			name:  "canceled request",
			entry: usageLogEntry{APIKeyID: 1, UserBilled: 1, StatusCode: 499},
		},
		{
			name:  "missing api key",
			entry: usageLogEntry{UserBilled: 1, StatusCode: 200},
		},
		{
			name:  "zero billed",
			entry: usageLogEntry{APIKeyID: 1, StatusCode: 200},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldApplyAPIKeyQuotaUsage(tt.entry); got != tt.want {
				t.Fatalf("shouldApplyAPIKeyQuotaUsage() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestUsageAttemptSQLPredicatesWithAndWithoutAlias(t *testing.T) {
	ctx := context.Background()
	db, err := New("sqlite", filepath.Join(t.TempDir(), "usage_attempt_predicates.db"))
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, row := range []struct {
		statusCode     int
		isRetryAttempt interface{}
		attemptIndex   int
	}{
		{200, nil, 0},
		{598, true, 1},
		{400, false, 2},
		{499, false, 0},
		{499, true, 1},
	} {
		if _, err := db.conn.ExecContext(ctx, `
			INSERT INTO usage_logs (status_code, is_retry_attempt, attempt_index)
			VALUES ($1, $2, $3)
		`, row.statusCode, row.isRetryAttempt, row.attemptIndex); err != nil {
			t.Fatalf("插入 usage attempt fixture 返回错误: %v", err)
		}
	}

	tests := []struct {
		name  string
		alias string
		from  string
	}{
		{name: "without alias", from: "usage_logs"},
		{name: "with alias", alias: "u", from: "usage_logs u"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			predicates := usageAttemptSQLPredicates(tt.alias)
			query := fmt.Sprintf(`
				SELECT
					COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END), 0),
					COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END), 0)
				FROM %s
			`, predicates.Terminal, predicates.Retry, tt.from)
			var terminal, retry int64
			if err := db.conn.QueryRowContext(ctx, query).Scan(&terminal, &retry); err != nil {
				t.Fatalf("执行 usage attempt predicate 返回错误: %v", err)
			}
			if terminal != 2 || retry != 1 {
				t.Fatalf("terminal/retry = %d/%d, want 2/1", terminal, retry)
			}
		})
	}
}
