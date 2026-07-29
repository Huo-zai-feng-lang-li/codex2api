package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const responsesContinuityMaxTouchBatch = 200

// ResponsesContinuationRow is one incremental node in a Responses conversation chain.
type ResponsesContinuationRow struct {
	ResponseID string
	ParentID   string
	SessionID  string
	AccountID  int64
	BaseURL    string
	InputJSON  []byte
	OutputJSON []byte
	Replayable bool
	CreatedAt  time.Time
	AccessedAt time.Time
	SizeBytes  int
}

func (db *DB) UpsertResponsesContinuation(ctx context.Context, row *ResponsesContinuationRow) error {
	if db == nil || db.conn == nil || row == nil || strings.TrimSpace(row.ResponseID) == "" {
		return nil
	}
	query := `
		INSERT INTO responses_continuity (
			response_id, parent_id, session_id, account_id, base_url, input_json, output_json,
			replayable, created_at, accessed_at, size_bytes
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT(response_id) DO UPDATE SET
			parent_id = EXCLUDED.parent_id, session_id = EXCLUDED.session_id,
			account_id = EXCLUDED.account_id, base_url = EXCLUDED.base_url,
			input_json = EXCLUDED.input_json, output_json = EXCLUDED.output_json,
			replayable = EXCLUDED.replayable, created_at = EXCLUDED.created_at,
			accessed_at = EXCLUDED.accessed_at, size_bytes = EXCLUDED.size_bytes`
	if db.isSQLite() {
		query = `
			INSERT INTO responses_continuity (
				response_id, parent_id, session_id, account_id, base_url, input_json, output_json,
				replayable, created_at, accessed_at, size_bytes
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(response_id) DO UPDATE SET
				parent_id = excluded.parent_id, session_id = excluded.session_id,
				account_id = excluded.account_id, base_url = excluded.base_url,
				input_json = excluded.input_json, output_json = excluded.output_json,
				replayable = excluded.replayable, created_at = excluded.created_at,
				accessed_at = excluded.accessed_at, size_bytes = excluded.size_bytes`
	}
	_, err := db.conn.ExecContext(ctx, query,
		row.ResponseID, row.ParentID, row.SessionID, row.AccountID, row.BaseURL, row.InputJSON, row.OutputJSON,
		row.Replayable, row.CreatedAt.UTC(), row.AccessedAt.UTC(), row.SizeBytes,
	)
	return err
}

func (db *DB) GetResponsesContinuation(ctx context.Context, responseID string) (ResponsesContinuationRow, bool, error) {
	if db == nil || db.conn == nil || strings.TrimSpace(responseID) == "" {
		return ResponsesContinuationRow{}, false, nil
	}
	query := `
		SELECT response_id, parent_id, session_id, account_id, base_url, input_json, output_json,
			replayable, created_at, accessed_at, size_bytes
		FROM responses_continuity WHERE response_id = $1`
	if db.isSQLite() {
		query = `
			SELECT response_id, parent_id, session_id, account_id, base_url, input_json, output_json,
				replayable, created_at, accessed_at, size_bytes
			FROM responses_continuity WHERE response_id = ?`
	}
	var row ResponsesContinuationRow
	err := db.conn.QueryRowContext(ctx, query, responseID).Scan(
		&row.ResponseID, &row.ParentID, &row.SessionID, &row.AccountID, &row.BaseURL, &row.InputJSON,
		&row.OutputJSON, &row.Replayable, &row.CreatedAt, &row.AccessedAt, &row.SizeBytes,
	)
	if err == sql.ErrNoRows {
		return ResponsesContinuationRow{}, false, nil
	}
	return row, err == nil, err
}

func (db *DB) GetLatestResponseBySessionID(ctx context.Context, sessionID string) (ResponsesContinuationRow, bool, error) {
	if db == nil || db.conn == nil || strings.TrimSpace(sessionID) == "" {
		return ResponsesContinuationRow{}, false, nil
	}
	query := `
		SELECT response_id, parent_id, session_id, account_id, base_url, input_json, output_json,
			replayable, created_at, accessed_at, size_bytes
		FROM responses_continuity
		WHERE session_id = $1
		ORDER BY accessed_at DESC, created_at DESC
		LIMIT 1`
	if db.isSQLite() {
		query = `
			SELECT response_id, parent_id, session_id, account_id, base_url, input_json, output_json,
				replayable, created_at, accessed_at, size_bytes
			FROM responses_continuity
			WHERE session_id = ?
			ORDER BY accessed_at DESC, created_at DESC
			LIMIT 1`
	}
	var row ResponsesContinuationRow
	err := db.conn.QueryRowContext(ctx, query, sessionID).Scan(
		&row.ResponseID, &row.ParentID, &row.SessionID, &row.AccountID, &row.BaseURL, &row.InputJSON,
		&row.OutputJSON, &row.Replayable, &row.CreatedAt, &row.AccessedAt, &row.SizeBytes,
	)
	if err == sql.ErrNoRows {
		return ResponsesContinuationRow{}, false, nil
	}
	return row, err == nil, err
}

func (db *DB) TouchResponsesContinuations(ctx context.Context, responseIDs []string, accessedAt time.Time) error {
	if db == nil || db.conn == nil || len(responseIDs) == 0 {
		return nil
	}
	for start := 0; start < len(responseIDs); start += responsesContinuityMaxTouchBatch {
		end := min(start+responsesContinuityMaxTouchBatch, len(responseIDs))
		if err := db.touchResponsesContinuationsBatch(ctx, responseIDs[start:end], accessedAt); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) touchResponsesContinuationsBatch(ctx context.Context, responseIDs []string, accessedAt time.Time) error {
	args := make([]any, 0, len(responseIDs)+1)
	args = append(args, accessedAt.UTC())
	placeholders := make([]string, 0, len(responseIDs))
	for _, responseID := range responseIDs {
		if responseID = strings.TrimSpace(responseID); responseID == "" {
			continue
		}
		args = append(args, responseID)
		if db.isSQLite() {
			placeholders = append(placeholders, "?")
		} else {
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
	}
	if len(placeholders) == 0 {
		return nil
	}
	timePlaceholder := "$1"
	if db.isSQLite() {
		timePlaceholder = "?"
	}
	query := fmt.Sprintf(
		"UPDATE responses_continuity SET accessed_at = %s WHERE response_id IN (%s)",
		timePlaceholder, strings.Join(placeholders, ","),
	)
	_, err := db.conn.ExecContext(ctx, query, args...)
	return err
}

func (db *DB) PruneResponsesContinuations(ctx context.Context, accessedBefore time.Time) (int64, error) {
	if db == nil || db.conn == nil {
		return 0, nil
	}
	query := "DELETE FROM responses_continuity WHERE accessed_at < $1"
	if db.isSQLite() {
		query = "DELETE FROM responses_continuity WHERE accessed_at < ?"
	}
	result, err := db.conn.ExecContext(ctx, query, accessedBefore.UTC())
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return deleted, err
}

type responsesContinuationMeta struct {
	responseID string
	parentID   string
	sizeBytes  int
}

func (db *DB) TrimResponsesContinuations(ctx context.Context, maxEntries, maxBytes int) (int64, error) {
	if db == nil || db.conn == nil || maxEntries <= 0 || maxBytes <= 0 {
		return 0, nil
	}
	count, totalBytes, err := db.responsesContinuityUsage(ctx)
	if err != nil {
		return 0, err
	}
	if count <= maxEntries && totalBytes <= maxBytes {
		return 0, nil
	}
	metas, err := db.listResponsesContinuationMeta(ctx, count)
	if err != nil {
		return 0, err
	}
	removed := selectResponsesContinuationRemovals(metas, count, totalBytes, maxEntries, maxBytes)
	return db.deleteResponsesContinuations(ctx, removed)
}

func (db *DB) responsesContinuityUsage(ctx context.Context) (int, int, error) {
	var count, totalBytes int
	err := db.conn.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(size_bytes), 0) FROM responses_continuity",
	).Scan(&count, &totalBytes)
	return count, totalBytes, err
}

func (db *DB) listResponsesContinuationMeta(ctx context.Context, capacity int) ([]responsesContinuationMeta, error) {
	rows, err := db.conn.QueryContext(ctx, `
		SELECT response_id, parent_id, size_bytes
		FROM responses_continuity
		ORDER BY accessed_at ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	metas := make([]responsesContinuationMeta, 0, capacity)
	for rows.Next() {
		var meta responsesContinuationMeta
		if err := rows.Scan(&meta.responseID, &meta.parentID, &meta.sizeBytes); err != nil {
			return nil, err
		}
		metas = append(metas, meta)
	}
	return metas, rows.Err()
}

func selectResponsesContinuationRemovals(metas []responsesContinuationMeta, count, totalBytes, maxEntries, maxBytes int) map[string]struct{} {
	children := make(map[string][]string)
	sizes := make(map[string]int, len(metas))
	for _, meta := range metas {
		children[meta.parentID] = append(children[meta.parentID], meta.responseID)
		sizes[meta.responseID] = meta.sizeBytes
	}
	removed := make(map[string]struct{})
	for _, meta := range metas {
		if count <= maxEntries && totalBytes <= maxBytes {
			break
		}
		if _, exists := removed[meta.responseID]; exists {
			continue
		}
		removedCount, removedBytes := markResponsesContinuationSubtree(meta.responseID, children, sizes, removed)
		count -= removedCount
		totalBytes -= removedBytes
	}
	return removed
}

func markResponsesContinuationSubtree(root string, children map[string][]string, sizes map[string]int, removed map[string]struct{}) (int, int) {
	count, totalBytes := 0, 0
	stack := []string{root}
	for len(stack) > 0 {
		last := len(stack) - 1
		responseID := stack[last]
		stack = stack[:last]
		if _, exists := removed[responseID]; exists {
			continue
		}
		removed[responseID] = struct{}{}
		count++
		totalBytes += sizes[responseID]
		stack = append(stack, children[responseID]...)
	}
	return count, totalBytes
}

func (db *DB) deleteResponsesContinuations(ctx context.Context, responseIDs map[string]struct{}) (int64, error) {
	if len(responseIDs) == 0 {
		return 0, nil
	}
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ids := make([]string, 0, len(responseIDs))
	for responseID := range responseIDs {
		ids = append(ids, responseID)
	}
	var deleted int64
	for start := 0; start < len(ids); start += responsesContinuityMaxTouchBatch {
		end := min(start+responsesContinuityMaxTouchBatch, len(ids))
		args := make([]any, end-start)
		placeholders := make([]string, end-start)
		for i, responseID := range ids[start:end] {
			args[i] = responseID
			if db.isSQLite() {
				placeholders[i] = "?"
			} else {
				placeholders[i] = fmt.Sprintf("$%d", i+1)
			}
		}
		result, err := tx.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM responses_continuity WHERE response_id IN (%s)", strings.Join(placeholders, ",")),
			args...,
		)
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		deleted += count
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}
