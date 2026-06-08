package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/security/upstreamguard"
	"github.com/gin-gonic/gin"
)

type securityEventsResponse struct {
	Events   []*database.SecurityEvent `json:"events"`
	Total    int                       `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"page_size"`
}

type suppressSecurityEventRequest struct {
	RuleID string `json:"rule_id"`
}

type suppressSecurityEventResponse struct {
	Suppressions string `json:"upstream_guard_suppressions"`
}

type securityEventRule struct {
	RuleID string `json:"rule_id"`
}

func (h *Handler) recordSecurityEvent(input *database.SecurityEventInput) {
	if h == nil || h.db == nil || input == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = h.db.InsertSecurityEvent(ctx, input)
}

func (h *Handler) ListSecurityEvents(c *gin.Context) {
	page := positiveQueryInt(c, "page", 1)
	pageSize := positiveQueryInt(c, "page_size", positiveQueryInt(c, "limit", 100))
	accountID := int64(0)
	if raw := strings.TrimSpace(c.Query("account_id")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			accountID = parsed
		}
	}

	query := database.SecurityEventQuery{
		Page:       page,
		PageSize:   pageSize,
		Direction:  c.Query("direction"),
		Action:     c.Query("action"),
		RiskLevel:  c.Query("risk_level"),
		Endpoint:   c.Query("endpoint"),
		Model:      c.Query("model"),
		AccountID:  accountID,
		BaseURL:    c.Query("base_url"),
		SourceType: c.Query("source_type"),
		ToolCall:   optionalBoolQuery(c, "tool_call"),
		Query:      c.Query("q"),
		StartTime:  optionalTimeQuery(c, "start"),
		EndTime:    optionalTimeQuery(c, "end"),
	}
	events, total, err := h.db.ListSecurityEventsPage(c.Request.Context(), query)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	if events == nil {
		events = []*database.SecurityEvent{}
	}
	c.JSON(http.StatusOK, securityEventsResponse{Events: events, Total: total, Page: page, PageSize: pageSize})
}

func (h *Handler) ClearSecurityEvents(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	if err := h.db.ClearSecurityEvents(ctx); err != nil {
		writeInternalError(c, err)
		return
	}
	writeMessage(c, http.StatusOK, "安全事件已清空")
}

func (h *Handler) SuppressSecurityEvent(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "无效的安全事件 ID")
		return
	}
	var req suppressSecurityEventRequest
	if c.Request.Body != nil {
		_ = c.ShouldBindJSON(&req)
	}

	event, err := h.db.GetSecurityEvent(c.Request.Context(), id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(c, http.StatusNotFound, "安全事件不存在")
			return
		}
		writeInternalError(c, err)
		return
	}
	ruleID := chooseSecurityEventRuleID(event.Rules, req.RuleID)
	if ruleID == "" {
		writeError(c, http.StatusBadRequest, "安全事件缺少可抑制的规则")
		return
	}

	settings, err := h.db.GetSystemSettings(c.Request.Context())
	if err != nil {
		writeInternalError(c, err)
		return
	}
	if settings == nil {
		settings = &database.SystemSettings{}
	}
	existing, err := upstreamguard.ParseSuppressions(settings.UpstreamGuardSuppressions)
	if err != nil {
		writeError(c, http.StatusBadRequest, "现有上游防护抑制规则无效: "+err.Error())
		return
	}
	next := appendSecurityEventSuppression(existing, upstreamguard.SuppressionRule{
		RuleID:    ruleID,
		Endpoint:  strings.TrimSpace(event.Endpoint),
		AccountID: event.AccountID,
		BaseURL:   strings.TrimSpace(event.BaseURL),
		Action:    upstreamguard.SuppressDowngrade,
	})
	raw, err := json.Marshal(next)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	normalized, rules, err := normalizeUpstreamGuardSuppressionsJSON(string(raw))
	if err != nil {
		writeInternalError(c, err)
		return
	}
	settings.UpstreamGuardSuppressions = normalized
	if err := h.db.UpdateSystemSettings(c.Request.Context(), settings); err != nil {
		writeInternalError(c, err)
		return
	}
	cfg := h.store.GetUpstreamGuardConfig()
	cfg.Suppressions = rules
	h.store.SetUpstreamGuardConfig(cfg)
	c.JSON(http.StatusOK, suppressSecurityEventResponse{Suppressions: normalized})
}

func optionalBoolQuery(c *gin.Context, key string) *bool {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" || raw == "all" {
		return nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil
	}
	return &value
}

func optionalTimeQuery(c *gin.Context, key string) time.Time {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		value, err := time.Parse(layout, raw)
		if err == nil {
			return value
		}
	}
	return time.Time{}
}

func chooseSecurityEventRuleID(rawRules, preferred string) string {
	preferred = strings.TrimSpace(preferred)
	var rules []securityEventRule
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawRules)), &rules); err != nil {
		if preferred != "" {
			return preferred
		}
		return ""
	}
	if preferred != "" {
		for _, rule := range rules {
			if strings.TrimSpace(rule.RuleID) == preferred {
				return preferred
			}
		}
		return ""
	}
	for _, rule := range rules {
		if ruleID := strings.TrimSpace(rule.RuleID); ruleID != "" && ruleID != "scanner_error" {
			return ruleID
		}
	}
	return ""
}

func appendSecurityEventSuppression(rules []upstreamguard.SuppressionRule, next upstreamguard.SuppressionRule) []upstreamguard.SuppressionRule {
	next.RuleID = strings.TrimSpace(next.RuleID)
	next.Endpoint = strings.TrimSpace(next.Endpoint)
	next.BaseURL = strings.TrimSpace(next.BaseURL)
	next.Action = upstreamguard.SuppressDowngrade
	for _, rule := range rules {
		if rule.RuleID == next.RuleID && rule.Endpoint == next.Endpoint && rule.AccountID == next.AccountID && rule.BaseURL == next.BaseURL {
			return rules
		}
	}
	return append(rules, next)
}
