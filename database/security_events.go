package database

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	defaultSecurityEventPageSize = 100
	maxSecurityEventPageSize     = 500
	maxSecurityEventPreviewRunes = 1000
)

var securityEventTokenPattern = regexp.MustCompile(`(?i)\b(?:sk-(?:proj-)?[A-Za-z0-9_-]{12,}|ghp_[A-Za-z0-9_]{12,}|xoxb-[A-Za-z0-9-]{12,}|AKIA[0-9A-Z]{16})\b`)

type SecurityEvent struct {
	ID                 int64     `json:"id"`
	CreatedAt          time.Time `json:"created_at"`
	Direction          string    `json:"direction"`
	Action             string    `json:"action"`
	RiskLevel          string    `json:"risk_level"`
	RiskScore          int       `json:"risk_score"`
	Confidence         int       `json:"confidence"`
	Endpoint           string    `json:"endpoint"`
	Model              string    `json:"model"`
	AccountID          int64     `json:"account_id"`
	AccountName        string    `json:"account_name"`
	BaseURL            string    `json:"base_url"`
	SourceType         string    `json:"source_type"`
	Stream             bool      `json:"stream"`
	ToolCall           bool      `json:"tool_call"`
	Rules              string    `json:"rules"`
	Preview            string    `json:"preview"`
	ContentHash        string    `json:"content_hash"`
	RequestID          string    `json:"request_id"`
	ScannerError       string    `json:"scanner_error"`
	FalsePositiveHints string    `json:"false_positive_hints"`
}

type SecurityEventInput struct {
	Direction          string
	Action             string
	RiskLevel          string
	RiskScore          int
	Confidence         int
	Endpoint           string
	Model              string
	AccountID          int64
	AccountName        string
	BaseURL            string
	SourceType         string
	Stream             bool
	ToolCall           bool
	Rules              string
	Preview            string
	ContentHash        string
	RequestID          string
	ScannerError       string
	FalsePositiveHints string
}

type SecurityEventQuery struct {
	Page       int
	PageSize   int
	Limit      int
	Direction  string
	Action     string
	RiskLevel  string
	Endpoint   string
	Model      string
	AccountID  int64
	BaseURL    string
	SourceType string
	ToolCall   *bool
	Query      string
	StartTime  time.Time
	EndTime    time.Time
}

func (db *DB) InsertSecurityEvent(ctx context.Context, input *SecurityEventInput) error {
	if db == nil || input == nil {
		return nil
	}
	_, err := db.conn.ExecContext(ctx, `
		INSERT INTO security_events (
			direction, action, risk_level, risk_score, confidence, endpoint, model,
			account_id, account_name, base_url, source_type, stream, tool_call, rules,
			preview, content_hash, request_id, scanner_error, false_positive_hints
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
	`, strings.TrimSpace(input.Direction), strings.TrimSpace(input.Action), strings.TrimSpace(input.RiskLevel),
		input.RiskScore, input.Confidence, strings.TrimSpace(input.Endpoint), strings.TrimSpace(input.Model),
		input.AccountID, strings.TrimSpace(input.AccountName), strings.TrimSpace(input.BaseURL),
		strings.TrimSpace(input.SourceType), input.Stream, input.ToolCall, normalizeSecurityEventJSON(input.Rules),
		sanitizeSecurityEventPreview(input.Preview), strings.TrimSpace(input.ContentHash),
		strings.TrimSpace(input.RequestID), sanitizeSecurityEventPreview(input.ScannerError),
		normalizeSecurityEventJSON(input.FalsePositiveHints))
	return err
}

func (db *DB) ListSecurityEventsPage(ctx context.Context, query SecurityEventQuery) ([]*SecurityEvent, int, error) {
	page, pageSize := normalizeSecurityEventPage(query)
	where, args := securityEventWhere(query)

	var total int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_events`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := db.conn.QueryContext(ctx, `
		SELECT id, created_at, COALESCE(direction, ''), COALESCE(action, ''), COALESCE(risk_level, ''),
		       COALESCE(risk_score, 0), COALESCE(confidence, 0), COALESCE(endpoint, ''), COALESCE(model, ''),
		       COALESCE(account_id, 0), COALESCE(account_name, ''), COALESCE(base_url, ''),
		       COALESCE(source_type, ''), COALESCE(stream, false), COALESCE(tool_call, false),
		       COALESCE(rules, '[]'), COALESCE(preview, ''), COALESCE(content_hash, ''),
		       COALESCE(request_id, ''), COALESCE(scanner_error, ''), COALESCE(false_positive_hints, '[]')
		FROM security_events
		`+where+`
		ORDER BY id DESC
		LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args))+`
	`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	events, err := scanSecurityEvents(rows)
	return events, total, err
}

func (db *DB) GetSecurityEvent(ctx context.Context, id int64) (*SecurityEvent, error) {
	if db == nil || id <= 0 {
		return nil, sql.ErrNoRows
	}
	rows, err := db.conn.QueryContext(ctx, `
		SELECT id, created_at, COALESCE(direction, ''), COALESCE(action, ''), COALESCE(risk_level, ''),
		       COALESCE(risk_score, 0), COALESCE(confidence, 0), COALESCE(endpoint, ''), COALESCE(model, ''),
		       COALESCE(account_id, 0), COALESCE(account_name, ''), COALESCE(base_url, ''),
		       COALESCE(source_type, ''), COALESCE(stream, false), COALESCE(tool_call, false),
		       COALESCE(rules, '[]'), COALESCE(preview, ''), COALESCE(content_hash, ''),
		       COALESCE(request_id, ''), COALESCE(scanner_error, ''), COALESCE(false_positive_hints, '[]')
		FROM security_events
		WHERE id = $1
		LIMIT 1
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events, err := scanSecurityEvents(rows)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, sql.ErrNoRows
	}
	return events[0], nil
}

func (db *DB) ClearSecurityEvents(ctx context.Context) error {
	if db == nil {
		return nil
	}
	if db.isSQLite() {
		if _, err := db.conn.ExecContext(ctx, `DELETE FROM security_events`); err != nil {
			return err
		}
		_, err := db.conn.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name = 'security_events'`)
		return err
	}
	_, err := db.conn.ExecContext(ctx, `TRUNCATE TABLE security_events RESTART IDENTITY`)
	return err
}

func (db *DB) PruneSecurityEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if db == nil || cutoff.IsZero() {
		return 0, nil
	}
	result, err := db.conn.ExecContext(ctx, `DELETE FROM security_events WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func normalizeSecurityEventPage(query SecurityEventQuery) (int, int) {
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = query.Limit
	}
	if pageSize <= 0 || pageSize > maxSecurityEventPageSize {
		pageSize = defaultSecurityEventPageSize
	}
	page := query.Page
	if page <= 0 {
		page = 1
	}
	return page, pageSize
}

func securityEventWhere(query SecurityEventQuery) (string, []any) {
	clauses := make([]string, 0, 10)
	args := make([]any, 0, 10)
	addExact := func(column, value string) {
		value = strings.TrimSpace(value)
		if value == "" || value == "all" {
			return
		}
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	addExact("direction", query.Direction)
	addExact("action", query.Action)
	addExact("risk_level", query.RiskLevel)
	addExact("endpoint", query.Endpoint)
	addExact("model", query.Model)
	addExact("base_url", query.BaseURL)
	addExact("source_type", query.SourceType)
	if query.AccountID > 0 {
		args = append(args, query.AccountID)
		clauses = append(clauses, fmt.Sprintf("account_id = $%d", len(args)))
	}
	if !query.StartTime.IsZero() {
		args = append(args, query.StartTime)
		clauses = append(clauses, fmt.Sprintf("created_at >= $%d", len(args)))
	}
	if !query.EndTime.IsZero() {
		args = append(args, query.EndTime)
		clauses = append(clauses, fmt.Sprintf("created_at < $%d", len(args)))
	}
	if query.ToolCall != nil {
		args = append(args, *query.ToolCall)
		clauses = append(clauses, fmt.Sprintf("tool_call = $%d", len(args)))
	}
	addSecurityEventSearch(&clauses, &args, query.Query)
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func addSecurityEventSearch(clauses *[]string, args *[]any, query string) {
	q := strings.TrimSpace(query)
	if q == "" {
		return
	}
	*args = append(*args, "%"+strings.ToLower(q)+"%")
	idx := len(*args)
	*clauses = append(*clauses, fmt.Sprintf(`(
		LOWER(COALESCE(preview, '')) LIKE $%d OR
		LOWER(COALESCE(rules, '')) LIKE $%d OR
		LOWER(COALESCE(request_id, '')) LIKE $%d OR
		LOWER(COALESCE(account_name, '')) LIKE $%d OR
		LOWER(COALESCE(base_url, '')) LIKE $%d OR
		LOWER(COALESCE(scanner_error, '')) LIKE $%d
	)`, idx, idx, idx, idx, idx, idx))
}

func scanSecurityEvents(rows rowsScanner) ([]*SecurityEvent, error) {
	events := make([]*SecurityEvent, 0)
	for rows.Next() {
		item := &SecurityEvent{}
		var createdAtRaw interface{}
		if err := rows.Scan(&item.ID, &createdAtRaw, &item.Direction, &item.Action, &item.RiskLevel,
			&item.RiskScore, &item.Confidence, &item.Endpoint, &item.Model, &item.AccountID, &item.AccountName,
			&item.BaseURL, &item.SourceType, &item.Stream, &item.ToolCall, &item.Rules, &item.Preview,
			&item.ContentHash, &item.RequestID, &item.ScannerError, &item.FalsePositiveHints); err != nil {
			return nil, err
		}
		createdAt, err := parseDBTimeValue(createdAtRaw)
		if err != nil {
			return nil, err
		}
		item.CreatedAt = createdAt
		events = append(events, item)
	}
	return events, rows.Err()
}

type rowsScanner interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
}

func sanitizeSecurityEventPreview(text string) string {
	text = securityEventTokenPattern.ReplaceAllString(text, "[REDACTED_TOKEN]")
	text = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\t':
			return ' '
		case '\u001b':
			return -1
		default:
			if r < 32 {
				return -1
			}
			return r
		}
	}, text)
	return limitSecurityEventRunes(strings.Join(strings.Fields(text), " "), maxSecurityEventPreviewRunes)
}

func normalizeSecurityEventJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "[]"
	}
	return sanitizeSecurityEventPreview(raw)
}

func limitSecurityEventRunes(text string, maxRunes int) string {
	count := 0
	for idx := range text {
		if count == maxRunes {
			return text[:idx]
		}
		count++
	}
	return text
}
