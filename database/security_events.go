package database

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSecurityEventPageSize = 100
	maxSecurityEventPageSize     = 500
	maxSecurityEventPreviewRunes = 1000

	SecurityCaptureReasonHit  = "hit"
	SecurityCaptureReasonFull = "full"
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

type SecurityCapture struct {
	ID                      int64     `json:"id"`
	CreatedAt               time.Time `json:"created_at"`
	SecurityEventID         int64     `json:"security_event_id"`
	CaptureReason           string    `json:"capture_reason"`
	Direction               string    `json:"direction"`
	Endpoint                string    `json:"endpoint"`
	Model                   string    `json:"model"`
	AccountID               int64     `json:"account_id"`
	AccountName             string    `json:"account_name"`
	BaseURL                 string    `json:"base_url"`
	SourceType              string    `json:"source_type"`
	Stream                  bool      `json:"stream"`
	ToolCall                bool      `json:"tool_call"`
	RequestID               string    `json:"request_id"`
	Body                    string    `json:"body"`
	BodyHash                string    `json:"body_hash"`
	BodyBytes               int       `json:"body_bytes"`
	Truncated               bool      `json:"truncated"`
	ExpiresAt               time.Time `json:"expires_at"`
	EventAction             string    `json:"event_action"`
	EventRiskLevel          string    `json:"event_risk_level"`
	EventRiskScore          int       `json:"event_risk_score"`
	EventConfidence         int       `json:"event_confidence"`
	EventRules              string    `json:"event_rules"`
	EventPreview            string    `json:"event_preview"`
	EventScannerError       string    `json:"event_scanner_error"`
	EventFalsePositiveHints string    `json:"event_false_positive_hints"`
}

type SecurityCaptureInput struct {
	SecurityEventID int64
	CaptureReason   string
	Direction       string
	Endpoint        string
	Model           string
	AccountID       int64
	AccountName     string
	BaseURL         string
	SourceType      string
	Stream          bool
	ToolCall        bool
	RequestID       string
	Body            string
	BodyHash        string
	BodyBytes       int
	Truncated       bool
	ExpiresAt       time.Time
}

type SecurityCaptureQuery struct {
	Page            int
	PageSize        int
	SecurityEventID int64
	RequestID       string
	CaptureReason   string
	Direction       string
	Endpoint        string
	Model           string
	AccountID       int64
	BaseURL         string
	SourceType      string
	ToolCall        *bool
	Query           string
	Limit           int
	StartTime       time.Time
	EndTime         time.Time
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
	_, err := db.InsertSecurityEventReturningID(ctx, input)
	return err
}

func (db *DB) InsertSecurityEventReturningID(ctx context.Context, input *SecurityEventInput) (int64, error) {
	if db == nil || input == nil {
		return 0, nil
	}
	args := []any{
		strings.TrimSpace(input.Direction), strings.TrimSpace(input.Action), strings.TrimSpace(input.RiskLevel),
		input.RiskScore, input.Confidence, strings.TrimSpace(input.Endpoint), strings.TrimSpace(input.Model),
		input.AccountID, strings.TrimSpace(input.AccountName), strings.TrimSpace(input.BaseURL),
		strings.TrimSpace(input.SourceType), input.Stream, input.ToolCall, normalizeSecurityEventJSON(input.Rules),
		sanitizeSecurityEventPreview(input.Preview), strings.TrimSpace(input.ContentHash),
		strings.TrimSpace(input.RequestID), sanitizeSecurityEventPreview(input.ScannerError),
		normalizeSecurityEventJSON(input.FalsePositiveHints),
	}
	if !db.isSQLite() {
		var id int64
		err := db.conn.QueryRowContext(ctx, `
			INSERT INTO security_events (
				direction, action, risk_level, risk_score, confidence, endpoint, model,
				account_id, account_name, base_url, source_type, stream, tool_call, rules,
				preview, content_hash, request_id, scanner_error, false_positive_hints
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
			RETURNING id
		`, args...).Scan(&id)
		return id, err
	}
	result, err := db.conn.ExecContext(ctx, `
		INSERT INTO security_events (
			direction, action, risk_level, risk_score, confidence, endpoint, model,
			account_id, account_name, base_url, source_type, stream, tool_call, rules,
			preview, content_hash, request_id, scanner_error, false_positive_hints
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
	`, args...)
	if err != nil {
		return 0, err
	}
	id, _ := result.LastInsertId()
	return id, nil
}

func (db *DB) InsertSecurityCapture(ctx context.Context, input *SecurityCaptureInput) (int64, error) {
	if db == nil || input == nil {
		return 0, nil
	}
	expiresAt := input.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(7 * 24 * time.Hour)
	}
	args := []any{
		input.SecurityEventID, normalizeSecurityCaptureReason(input.CaptureReason),
		strings.TrimSpace(input.Direction), strings.TrimSpace(input.Endpoint), strings.TrimSpace(input.Model),
		input.AccountID, strings.TrimSpace(input.AccountName), strings.TrimSpace(input.BaseURL),
		strings.TrimSpace(input.SourceType), input.Stream, input.ToolCall, strings.TrimSpace(input.RequestID),
		input.Body, strings.TrimSpace(input.BodyHash), input.BodyBytes, input.Truncated, expiresAt,
	}
	if !db.isSQLite() {
		var id int64
		err := db.conn.QueryRowContext(ctx, `
			INSERT INTO security_captures (
				security_event_id, capture_reason, direction, endpoint, model, account_id, account_name,
				base_url, source_type, stream, tool_call, request_id, body, body_hash, body_bytes, truncated, expires_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			RETURNING id
		`, args...).Scan(&id)
		return id, err
	}
	result, err := db.conn.ExecContext(ctx, `
		INSERT INTO security_captures (
			security_event_id, capture_reason, direction, endpoint, model, account_id, account_name,
			base_url, source_type, stream, tool_call, request_id, body, body_hash, body_bytes, truncated, expires_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`, args...)
	if err != nil {
		return 0, err
	}
	id, _ := result.LastInsertId()
	return id, nil
}

func (db *DB) ListSecurityCaptures(ctx context.Context, query SecurityCaptureQuery) ([]*SecurityCapture, error) {
	captures, _, err := db.ListSecurityCapturesPage(ctx, query)
	return captures, err
}

func (db *DB) ListSecurityCapturesPage(ctx context.Context, query SecurityCaptureQuery) ([]*SecurityCapture, int, error) {
	if db == nil {
		return nil, 0, nil
	}
	page, pageSize := normalizeSecurityCapturePage(query)
	clauses := make([]string, 0, 8)
	args := make([]any, 0, 8)
	if query.SecurityEventID > 0 {
		args = append(args, query.SecurityEventID)
		clauses = append(clauses, "security_event_id = "+sqlPlaceholder(len(args)))
	}
	if strings.TrimSpace(query.RequestID) != "" {
		args = append(args, strings.TrimSpace(query.RequestID))
		clauses = append(clauses, "request_id = "+sqlPlaceholder(len(args)))
	}
	if reason := strings.TrimSpace(query.CaptureReason); reason != "" && reason != "all" {
		args = append(args, normalizeSecurityCaptureReason(reason))
		clauses = append(clauses, "capture_reason = "+sqlPlaceholder(len(args)))
	}
	if strings.TrimSpace(query.Direction) != "" {
		args = append(args, strings.TrimSpace(query.Direction))
		clauses = append(clauses, "direction = "+sqlPlaceholder(len(args)))
	}
	if strings.TrimSpace(query.Endpoint) != "" {
		args = append(args, strings.TrimSpace(query.Endpoint))
		clauses = append(clauses, "endpoint = "+sqlPlaceholder(len(args)))
	}
	if strings.TrimSpace(query.Model) != "" {
		args = append(args, strings.TrimSpace(query.Model))
		clauses = append(clauses, "model = "+sqlPlaceholder(len(args)))
	}
	if query.AccountID > 0 {
		args = append(args, query.AccountID)
		clauses = append(clauses, "account_id = "+sqlPlaceholder(len(args)))
	}
	if strings.TrimSpace(query.BaseURL) != "" {
		args = append(args, strings.TrimSpace(query.BaseURL))
		clauses = append(clauses, "base_url = "+sqlPlaceholder(len(args)))
	}
	if strings.TrimSpace(query.SourceType) != "" {
		args = append(args, strings.TrimSpace(query.SourceType))
		clauses = append(clauses, "source_type = "+sqlPlaceholder(len(args)))
	}
	if query.ToolCall != nil {
		args = append(args, *query.ToolCall)
		clauses = append(clauses, "tool_call = "+sqlPlaceholder(len(args)))
	}
	if !query.StartTime.IsZero() {
		args = append(args, query.StartTime)
		clauses = append(clauses, "created_at >= "+sqlPlaceholder(len(args)))
	}
	if !query.EndTime.IsZero() {
		args = append(args, query.EndTime)
		clauses = append(clauses, "created_at < "+sqlPlaceholder(len(args)))
	}
	if q := strings.TrimSpace(query.Query); q != "" {
		args = append(args, "%"+q+"%")
		ph := sqlPlaceholder(len(args))
		clauses = append(clauses, "(body LIKE "+ph+" OR body_hash LIKE "+ph+" OR request_id LIKE "+ph+" OR endpoint LIKE "+ph+")")
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	var total int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_captures`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := db.conn.QueryContext(ctx, `
		SELECT c.id, c.created_at, c.security_event_id, c.capture_reason, c.direction, c.endpoint, c.model,
		       c.account_id, c.account_name, c.base_url, c.source_type, c.stream, c.tool_call, c.request_id,
		       c.body, c.body_hash, c.body_bytes, c.truncated, c.expires_at,
		       COALESCE(e.action, ''), COALESCE(e.risk_level, ''), COALESCE(e.risk_score, 0),
		       COALESCE(e.confidence, 0), COALESCE(e.rules, '[]'), COALESCE(e.preview, ''),
		       COALESCE(e.scanner_error, ''), COALESCE(e.false_positive_hints, '[]')
		FROM (SELECT * FROM security_captures`+where+`) c
		LEFT JOIN security_events e ON e.id = c.security_event_id
		ORDER BY c.id DESC
		LIMIT `+sqlPlaceholder(len(args)-1)+` OFFSET `+sqlPlaceholder(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	captures := make([]*SecurityCapture, 0)
	for rows.Next() {
		item := &SecurityCapture{}
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.SecurityEventID, &item.CaptureReason, &item.Direction,
			&item.Endpoint, &item.Model, &item.AccountID, &item.AccountName, &item.BaseURL, &item.SourceType,
			&item.Stream, &item.ToolCall, &item.RequestID, &item.Body, &item.BodyHash, &item.BodyBytes,
			&item.Truncated, &item.ExpiresAt, &item.EventAction, &item.EventRiskLevel, &item.EventRiskScore,
			&item.EventConfidence, &item.EventRules, &item.EventPreview, &item.EventScannerError,
			&item.EventFalsePositiveHints); err != nil {
			return nil, 0, err
		}
		captures = append(captures, item)
	}
	return captures, total, rows.Err()
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
		if _, err := db.conn.ExecContext(ctx, `DELETE FROM security_captures`); err != nil {
			return err
		}
		if _, err := db.conn.ExecContext(ctx, `DELETE FROM security_events`); err != nil {
			return err
		}
		_, _ = db.conn.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name = 'security_captures'`)
		_, err := db.conn.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name = 'security_events'`)
		return err
	}
	if _, err := db.conn.ExecContext(ctx, `TRUNCATE TABLE security_captures RESTART IDENTITY`); err != nil {
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

func (db *DB) PruneSecurityCapturesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if db == nil || cutoff.IsZero() {
		return 0, nil
	}
	result, err := db.conn.ExecContext(ctx, `DELETE FROM security_captures WHERE expires_at < $1`, cutoff)
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

func normalizeSecurityCapturePage(query SecurityCaptureQuery) (int, int) {
	page := query.Page
	if page <= 0 {
		page = 1
	}
	pageSize := query.PageSize
	if query.Limit > 0 {
		pageSize = query.Limit
	}
	if pageSize <= 0 || pageSize > maxSecurityEventPageSize {
		pageSize = defaultSecurityEventPageSize
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

func sqlPlaceholder(index int) string {
	return "$" + strconv.Itoa(index)
}

func normalizeSecurityCaptureReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case SecurityCaptureReasonFull:
		return SecurityCaptureReasonFull
	default:
		return SecurityCaptureReasonHit
	}
}
