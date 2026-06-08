package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/codex2api/api"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/security/upstreamguard"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const upstreamGuardBlockedCode api.ErrorCode = "upstream_guard_blocked"

type upstreamGuardInspectFunc func([]byte, upstreamguard.ScanContext, upstreamguard.Config) upstreamguard.Verdict

type upstreamGuardScanner func(context.Context, upstreamGuardInput) (upstreamguard.Verdict, error)

type upstreamGuardScanResult struct {
	Verdict upstreamguard.Verdict
	Err     error
}

type upstreamGuardInput struct {
	Direction    string
	Endpoint     string
	Model        string
	AccountID    int64
	AccountName  string
	BaseURL      string
	Source       upstreamguard.SourceType
	Stream       bool
	Body         []byte
	RequestID    string
	Config       upstreamguard.Config
	InspectFunc  upstreamGuardInspectFunc
	Scanner      upstreamGuardScanner
	DeferCapture bool
}

type upstreamGuardAudit struct {
	h               *Handler
	ctx             context.Context
	requestInput    upstreamGuardInput
	responseInput   upstreamGuardInput
	requestBody     []byte
	responseBody    bytes.Buffer
	requestEventID  int64
	responseEventID int64
	hit             bool
	toolCall        bool
	finished        bool
}

func (h *Handler) newUpstreamGuardAudit(ctx context.Context, endpoint, model string, account *auth.Account, requestBody []byte, stream bool, requestID string) *upstreamGuardAudit {
	requestInput := h.newUpstreamGuardInput(upstreamguard.DirectionRequest, endpoint, model, account, requestBody, stream, requestID)
	requestInput.DeferCapture = true
	responseInput := h.newUpstreamGuardInput(upstreamguard.DirectionResponse, endpoint, model, account, nil, stream, requestID)
	responseInput.DeferCapture = true
	return &upstreamGuardAudit{
		h:             h,
		ctx:           ctx,
		requestInput:  requestInput,
		responseInput: responseInput,
		requestBody:   append([]byte(nil), requestBody...),
	}
}

func upstreamGuardRequestID(c *gin.Context) string {
	if c == nil {
		return uuid.New().String()
	}
	if requestID := strings.TrimSpace(c.GetHeader("X-Request-Id")); requestID != "" {
		return requestID
	}
	if value, ok := c.Get("upstream_guard_request_id"); ok {
		if requestID := strings.TrimSpace(fmt.Sprint(value)); requestID != "" {
			return requestID
		}
	}
	requestID := uuid.New().String()
	c.Set("upstream_guard_request_id", requestID)
	return requestID
}

func (a *upstreamGuardAudit) InspectRequest() upstreamguard.Verdict {
	if a == nil || a.h == nil {
		return upstreamguard.Verdict{}
	}
	input := a.requestInput
	input.Body = a.requestBody
	verdict, eventID := a.h.inspectUpstreamGuardWithEventID(a.ctx, input)
	if eventID > 0 {
		a.requestEventID = eventID
		a.hit = true
	}
	return verdict
}

func (a *upstreamGuardAudit) InspectResponseBody(body []byte, stream bool) upstreamguard.Verdict {
	if a == nil || a.h == nil {
		return upstreamguard.Verdict{}
	}
	a.responseBody.Write(body)
	return a.inspectResponseForVerdict(body, stream)
}

func (a *upstreamGuardAudit) ScanResponseBody(body []byte, stream bool) upstreamguard.Verdict {
	if a == nil || a.h == nil {
		return upstreamguard.Verdict{}
	}
	return a.inspectResponseForVerdict(body, stream)
}

func (a *upstreamGuardAudit) inspectResponseForVerdict(body []byte, stream bool) upstreamguard.Verdict {
	input := a.responseInput
	input.Body = body
	input.Stream = stream
	input.DeferCapture = true
	verdict, eventID := a.h.inspectUpstreamGuardWithEventID(a.ctx, input)
	a.observeResponseVerdict(verdict, eventID)
	return verdict
}

func (a *upstreamGuardAudit) InspectResponseSSE(data []byte) upstreamguard.Verdict {
	if a == nil || a.h == nil {
		return upstreamguard.Verdict{}
	}
	a.responseBody.WriteString("data: ")
	a.responseBody.Write(data)
	a.responseBody.WriteString("\n\n")
	input := a.responseInput
	input.Body = data
	input.Stream = true
	input.DeferCapture = true
	verdict, eventID := a.h.inspectUpstreamGuardWithEventID(a.ctx, input)
	a.observeResponseVerdict(verdict, eventID)
	return verdict
}

func (a *upstreamGuardAudit) observeResponseVerdict(verdict upstreamguard.Verdict, eventID int64) {
	if eventID > 0 {
		a.responseEventID = eventID
		a.hit = true
	}
	if verdict.ToolCall {
		a.toolCall = true
	}
}

func (a *upstreamGuardAudit) Finish() {
	if a == nil || a.h == nil || a.h.db == nil || a.finished {
		return
	}
	a.finished = true
	cfg := upstreamguard.NormalizeConfig(a.requestInput.Config)
	if cfg.CaptureMode == upstreamguard.CaptureModeOff {
		return
	}
	reason := database.SecurityCaptureReasonFull
	if a.hit {
		reason = database.SecurityCaptureReasonHit
	} else if cfg.CaptureMode != upstreamguard.CaptureModeFullRaw {
		return
	}
	if len(a.requestBody) > 0 {
		input := a.requestInput
		input.Body = a.requestBody
		input.Config = cfg
		eventID := a.requestEventID
		if eventID == 0 {
			eventID = a.responseEventID
		}
		a.h.recordUpstreamGuardCapture(a.ctx, input, upstreamguard.Verdict{
			Direction: input.Direction,
			Source:    input.Source,
		}, eventID, reason)
	}
	if a.responseBody.Len() > 0 {
		input := a.responseInput
		input.Body = append([]byte(nil), a.responseBody.Bytes()...)
		input.Config = cfg
		eventID := a.responseEventID
		if eventID == 0 {
			eventID = a.requestEventID
		}
		a.h.recordUpstreamGuardCapture(a.ctx, input, upstreamguard.Verdict{
			Direction: input.Direction,
			Source:    input.Source,
			ToolCall:  a.toolCall,
		}, eventID, reason)
	}
}

func (h *Handler) inspectUpstreamGuard(ctx context.Context, input upstreamGuardInput) upstreamguard.Verdict {
	verdict, _ := h.inspectUpstreamGuardWithEventID(ctx, input)
	return verdict
}

func (h *Handler) inspectUpstreamGuardWithEventID(ctx context.Context, input upstreamGuardInput) (upstreamguard.Verdict, int64) {
	cfg := upstreamguard.NormalizeConfig(input.Config)
	input.Config = cfg
	if cfg.Mode == upstreamguard.ModeOff || !cfg.Enabled {
		verdict := upstreamguard.Verdict{Enabled: false, Direction: input.Direction, Action: "allow", RiskLevel: upstreamguard.RiskNone}
		if shouldCaptureFullRaw(cfg) && !input.DeferCapture {
			h.recordUpstreamGuardCapture(ctx, input, verdict, 0, database.SecurityCaptureReasonFull)
		}
		return verdict, 0
	}
	scanner := input.Scanner
	if scanner == nil {
		scanner = defaultUpstreamGuardScanner
	}
	verdict, err := runUpstreamGuardScanner(ctx, scanner, input)
	if err != nil {
		verdict = upstreamguard.Verdict{
			Enabled:      true,
			Direction:    input.Direction,
			Action:       "allow",
			RiskLevel:    upstreamguard.RiskLow,
			ScannerError: err.Error(),
			Reason:       "upstream security scanner failed; warn mode allows traffic",
		}
	}
	if shouldRecordUpstreamGuardEvent(verdict) {
		eventID := h.recordUpstreamGuardEvent(ctx, input, verdict)
		if shouldCaptureHitRaw(cfg) && !input.DeferCapture {
			h.recordUpstreamGuardCapture(ctx, input, verdict, eventID, database.SecurityCaptureReasonHit)
		}
		return verdict, eventID
	} else if shouldCaptureFullRaw(cfg) && !input.DeferCapture {
		h.recordUpstreamGuardCapture(ctx, input, verdict, 0, database.SecurityCaptureReasonFull)
	}
	return verdict, 0
}

func runUpstreamGuardScanner(ctx context.Context, scanner upstreamGuardScanner, input upstreamGuardInput) (upstreamguard.Verdict, error) {
	if input.Config.ScanTimeout <= 0 {
		return scanner(ctx, input)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	scanCtx, cancel := context.WithTimeout(ctx, input.Config.ScanTimeout)
	defer cancel()
	done := make(chan upstreamGuardScanResult, 1)
	go func() {
		verdict, err := scanner(scanCtx, input)
		done <- upstreamGuardScanResult{Verdict: verdict, Err: err}
	}()
	select {
	case result := <-done:
		return result.Verdict, result.Err
	case <-scanCtx.Done():
		return upstreamguard.Verdict{}, scanCtx.Err()
	}
}

func (h *Handler) inspectUpstreamGuardRequest(ctx context.Context, endpoint, model string, account *auth.Account, body []byte, stream bool, requestID string) upstreamguard.Verdict {
	return h.inspectUpstreamGuard(ctx, h.newUpstreamGuardInput(upstreamguard.DirectionRequest, endpoint, model, account, body, stream, requestID))
}

func (h *Handler) inspectUpstreamGuardResponse(ctx context.Context, endpoint, model string, account *auth.Account, body []byte, stream bool, requestID string) upstreamguard.Verdict {
	return h.inspectUpstreamGuard(ctx, h.newUpstreamGuardInput(upstreamguard.DirectionResponse, endpoint, model, account, body, stream, requestID))
}

func (h *Handler) newUpstreamGuardInput(direction, endpoint, model string, account *auth.Account, body []byte, stream bool, requestID string) upstreamGuardInput {
	cfg := h.currentUpstreamGuardConfig()
	accountID, accountName, baseURL, source := upstreamGuardAccountMeta(account, cfg)
	input := upstreamGuardInput{
		Direction:   direction,
		Endpoint:    endpoint,
		Model:       model,
		AccountID:   accountID,
		AccountName: accountName,
		BaseURL:     baseURL,
		Source:      source,
		Stream:      stream,
		Body:        body,
		RequestID:   requestID,
		Config:      cfg,
	}
	if direction == upstreamguard.DirectionRequest {
		input.InspectFunc = upstreamguard.InspectRequest
	} else {
		input.InspectFunc = upstreamguard.InspectResponse
	}
	return input
}

func (h *Handler) currentUpstreamGuardConfig() upstreamguard.Config {
	if h != nil && h.store != nil {
		return h.store.GetUpstreamGuardConfig()
	}
	return upstreamguard.DefaultConfig()
}

func upstreamGuardAccountMeta(account *auth.Account, cfg upstreamguard.Config) (int64, string, string, upstreamguard.SourceType) {
	if account == nil {
		return 0, "", "", upstreamguard.SourceUnknown
	}
	baseURL := strings.TrimSpace(account.BaseURL)
	if account.IsOpenAIResponsesAPI() {
		if responsesBaseURL, _ := account.OpenAIResponsesCredentials(); strings.TrimSpace(responsesBaseURL) != "" {
			baseURL = strings.TrimSpace(responsesBaseURL)
		}
	}
	sourceVerdict := upstreamguard.InspectSource(baseURL, cfg)
	return account.ID(), strings.TrimSpace(account.Name), baseURL, sourceVerdict.Source
}

func defaultUpstreamGuardScanner(_ context.Context, input upstreamGuardInput) (upstreamguard.Verdict, error) {
	inspect := input.InspectFunc
	if inspect == nil {
		if input.Direction == upstreamguard.DirectionRequest {
			inspect = upstreamguard.InspectRequest
		} else {
			inspect = upstreamguard.InspectResponse
		}
	}
	return inspect(input.Body, upstreamguard.ScanContext{
		Endpoint:    input.Endpoint,
		Model:       input.Model,
		AccountID:   input.AccountID,
		AccountName: input.AccountName,
		BaseURL:     input.BaseURL,
		Source:      input.Source,
		Stream:      input.Stream,
	}, input.Config), nil
}

func shouldRecordUpstreamGuardEvent(verdict upstreamguard.Verdict) bool {
	return verdict.ScannerError != "" || verdict.RiskLevel != upstreamguard.RiskNone || len(verdict.RuleIDs) > 0
}

func shouldBlockUpstreamGuard(verdict upstreamguard.Verdict) bool {
	return verdict.Enabled && verdict.Action == "block"
}

func shouldCaptureHitRaw(cfg upstreamguard.Config) bool {
	return cfg.CaptureMode == upstreamguard.CaptureModeHitRaw || cfg.CaptureMode == upstreamguard.CaptureModeFullRaw
}

func shouldCaptureFullRaw(cfg upstreamguard.Config) bool {
	return cfg.CaptureMode == upstreamguard.CaptureModeFullRaw
}

func upstreamGuardBlockMessage(verdict upstreamguard.Verdict) string {
	reason := strings.TrimSpace(verdict.Reason)
	if reason == "" {
		return "Request or response blocked by upstream guard"
	}
	return "Request or response blocked by upstream guard: " + reason
}

func upstreamGuardBlockAPIError(verdict upstreamguard.Verdict) *api.APIError {
	return api.NewAPIErrorWithDetails(
		upstreamGuardBlockedCode,
		upstreamGuardBlockMessage(verdict),
		api.ErrorTypePermission,
		gin.H{
			"risk_level": verdict.RiskLevel,
			"risk_score": verdict.RiskScore,
			"rule_ids":   verdict.RuleIDs,
		},
	)
}

func writeUpstreamGuardBlock(c *gin.Context, verdict upstreamguard.Verdict) bool {
	if !shouldBlockUpstreamGuard(verdict) {
		return false
	}
	api.SendErrorWithStatus(c, upstreamGuardBlockAPIError(verdict), http.StatusForbidden)
	return true
}

func buildUpstreamGuardBlockedEvent(verdict upstreamguard.Verdict) []byte {
	payload := map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"status":      "failed",
			"status_code": http.StatusForbidden,
			"error": map[string]any{
				"code":    upstreamGuardBlockedCode,
				"message": upstreamGuardBlockMessage(verdict),
				"type":    api.ErrorTypePermission,
			},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return buildResponseFailedEvent(http.StatusForbidden, "Request or response blocked by upstream guard")
	}
	return encoded
}

func buildUpstreamGuardBlockedErrorPayload(verdict upstreamguard.Verdict) []byte {
	payload := api.ErrorResponse{Error: *upstreamGuardBlockAPIError(verdict)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"error":{"code":"upstream_guard_blocked","message":"Request or response blocked by upstream guard","type":"permission_error"}}`)
	}
	return encoded
}

func (h *Handler) recordUpstreamGuardEvent(ctx context.Context, input upstreamGuardInput, verdict upstreamguard.Verdict) int64 {
	if h == nil || h.db == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id, _ := h.db.InsertSecurityEventReturningID(ctx, &database.SecurityEventInput{
		Direction:          input.Direction,
		Action:             verdict.Action,
		RiskLevel:          string(verdict.RiskLevel),
		RiskScore:          verdict.RiskScore,
		Confidence:         verdict.Confidence,
		Endpoint:           input.Endpoint,
		Model:              input.Model,
		AccountID:          input.AccountID,
		AccountName:        input.AccountName,
		BaseURL:            input.BaseURL,
		SourceType:         string(verdict.Source),
		Stream:             input.Stream,
		ToolCall:           verdict.ToolCall,
		Rules:              marshalUpstreamGuardRules(verdict),
		Preview:            verdict.Preview,
		ContentHash:        upstreamguard.ContentHash(input.Body),
		RequestID:          input.RequestID,
		ScannerError:       verdict.ScannerError,
		FalsePositiveHints: marshalStringList(verdict.FalsePositiveHints),
	})
	return id
}

func (h *Handler) recordUpstreamGuardCapture(ctx context.Context, input upstreamGuardInput, verdict upstreamguard.Verdict, eventID int64, reason string) {
	if h == nil || h.db == nil || len(input.Body) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	body, truncated := captureBody(input.Body, input.Config.CaptureMaxBodyBytes)
	sourceType := verdict.Source
	if sourceType == "" {
		sourceType = input.Source
	}
	_, _ = h.db.InsertSecurityCapture(ctx, &database.SecurityCaptureInput{
		SecurityEventID: eventID,
		CaptureReason:   reason,
		Direction:       input.Direction,
		Endpoint:        input.Endpoint,
		Model:           input.Model,
		AccountID:       input.AccountID,
		AccountName:     input.AccountName,
		BaseURL:         input.BaseURL,
		SourceType:      string(sourceType),
		Stream:          input.Stream,
		ToolCall:        verdict.ToolCall,
		RequestID:       input.RequestID,
		Body:            body,
		BodyHash:        upstreamguard.ContentHash(input.Body),
		BodyBytes:       len(input.Body),
		Truncated:       truncated,
		ExpiresAt:       time.Now().Add(time.Duration(input.Config.CaptureRetentionDays) * 24 * time.Hour),
	})
}

func captureBody(body []byte, maxBytes int) (string, bool) {
	if maxBytes > 0 && len(body) > maxBytes {
		return string(body[:maxBytes]), true
	}
	return string(body), false
}

func marshalUpstreamGuardRules(verdict upstreamguard.Verdict) string {
	type ruleEvent struct {
		RuleID   string `json:"rule_id"`
		Evidence string `json:"evidence,omitempty"`
		Field    string `json:"field,omitempty"`
		Match    string `json:"match,omitempty"`
	}
	rules := make([]ruleEvent, 0, len(verdict.RuleIDs))
	for _, ruleID := range verdict.RuleIDs {
		rules = append(rules, ruleEvent{RuleID: ruleID})
	}
	for i := range rules {
		for _, evidence := range verdict.Evidence {
			if evidence.RuleID == rules[i].RuleID {
				rules[i].Evidence = evidence.Snippet
				rules[i].Field = evidence.Field
				rules[i].Match = evidence.Match
				break
			}
		}
	}
	if len(rules) == 0 && verdict.ScannerError != "" {
		rules = append(rules, ruleEvent{RuleID: "scanner_error", Evidence: verdict.ScannerError})
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func marshalStringList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(raw)
}
