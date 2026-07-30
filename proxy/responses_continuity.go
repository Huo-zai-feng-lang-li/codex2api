package proxy

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openAIResponsesContinuityTTL          = time.Hour
	openAIResponsesContinuityMaxEntries   = 2000
	openAIResponsesContinuityMaxItems     = 400
	openAIResponsesContinuityMaxItemBytes = 4 << 20
	openAIResponsesContinuityMaxBytes     = 64 << 20
	openAIResponsesContinuityModeAuto     = "auto"
	openAIResponsesContinuityModeUpstream = "upstream"
)

var openAIResponsesContinuityDBTimeout = 250 * time.Millisecond

const (
	openAIResponsesContinuityCleanupInterval = time.Minute
	openAIResponsesContinuityCleanupTimeout  = 2 * time.Second
)

type openAIResponsesContinuityLimits struct {
	ttl          time.Duration
	maxEntries   int
	maxItems     int
	maxItemBytes int
	maxBytes     int
}

type openAIResponsesContinuation struct {
	accountID  int64
	baseURL    string
	parentID   string
	sessionID  string
	input      []json.RawMessage
	output     []json.RawMessage
	replayable bool
	createdAt  time.Time
	accessedAt time.Time
	size       int
	pending    bool
}

type openAIResponsesContinuityStats struct {
	Entries             int
	Bytes               int
	Evictions           uint64
	Persistent          bool
	PersistenceFailures uint64
}

type responsesContinuityPersistence interface {
	UpsertResponsesContinuation(context.Context, *database.ResponsesContinuationRow) error
	GetResponsesContinuation(context.Context, string) (database.ResponsesContinuationRow, bool, error)
	GetLatestResponseBySessionID(context.Context, string) (database.ResponsesContinuationRow, bool, error)
	GetLatestReplayableResponseBySessionID(context.Context, string) (database.ResponsesContinuationRow, bool, error)
	TouchResponsesContinuations(context.Context, []string, time.Time) error
	PruneResponsesContinuations(context.Context, time.Time) (int64, error)
	TrimResponsesContinuations(context.Context, int, int) (int64, error)
}

type openAIResponsesContinuityCleanupRequest struct {
	persistence responsesContinuityPersistence
	accessedAt  time.Time
}

type openAIResponsesContinuityRegistry struct {
	mu                      sync.Mutex
	entries                 map[string]openAIResponsesContinuation
	sessionLatest           map[string]string
	sessionLatestReplayable map[string]string
	totalBytes              int
	evictions               uint64
	limits                  openAIResponsesContinuityLimits
	now                     func() time.Time
	persistence             responsesContinuityPersistence
	lastCleanup             time.Time
	persistFail             uint64
	cleanupMu               sync.Mutex
	cleanupRunning          bool
	cleanupPending          *openAIResponsesContinuityCleanupRequest
}

var openAIResponsesContinuity = newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimitsFromEnv(os.Getenv))
var openAIResponsesContinuityMode = openAIResponsesContinuityModeFromEnv(os.Getenv)

func openAIResponsesContinuityModeFromEnv(getenv func(string) string) string {
	if strings.EqualFold(strings.TrimSpace(getenv("CODEX_RESPONSES_CONTINUITY_MODE")), openAIResponsesContinuityModeUpstream) {
		return openAIResponsesContinuityModeUpstream
	}
	return openAIResponsesContinuityModeAuto
}

func openAIResponsesContinuityLimitsFromEnv(getenv func(string) string) openAIResponsesContinuityLimits {
	return openAIResponsesContinuityLimits{
		ttl:          openAIResponsesContinuityTTL,
		maxEntries:   positiveEnvInt(getenv, "CODEX_RESPONSES_CONTINUITY_MAX_ENTRIES", openAIResponsesContinuityMaxEntries),
		maxItems:     openAIResponsesContinuityMaxItems,
		maxItemBytes: positiveEnvInt(getenv, "CODEX_RESPONSES_CONTINUITY_MAX_CHAIN_MB", openAIResponsesContinuityMaxItemBytes>>20) << 20,
		maxBytes:     positiveEnvInt(getenv, "CODEX_RESPONSES_CONTINUITY_MAX_BYTES_MB", openAIResponsesContinuityMaxBytes>>20) << 20,
	}
}

func newOpenAIResponsesContinuityRegistry(limits openAIResponsesContinuityLimits) *openAIResponsesContinuityRegistry {
	if limits.ttl <= 0 {
		limits.ttl = openAIResponsesContinuityTTL
	}
	if limits.maxEntries <= 0 {
		limits.maxEntries = openAIResponsesContinuityMaxEntries
	}
	if limits.maxItems <= 0 {
		limits.maxItems = openAIResponsesContinuityMaxItems
	}
	if limits.maxItemBytes <= 0 {
		limits.maxItemBytes = openAIResponsesContinuityMaxItemBytes
	}
	if limits.maxBytes <= 0 {
		limits.maxBytes = openAIResponsesContinuityMaxBytes
	}
	return &openAIResponsesContinuityRegistry{
		entries:                 make(map[string]openAIResponsesContinuation),
		sessionLatest:           make(map[string]string),
		sessionLatestReplayable: make(map[string]string),
		limits:                  limits,
		now:                     time.Now,
	}
}

// ConfigureOpenAIResponsesContinuityPersistence enables bounded lazy restore
// from the application database. Request handling stays memory-only if storage is busy.
func ConfigureOpenAIResponsesContinuityPersistence(ctx context.Context, db *database.DB) error {
	return openAIResponsesContinuity.setPersistence(ctx, db)
}

func (registry *openAIResponsesContinuityRegistry) setPersistence(ctx context.Context, db responsesContinuityPersistence) error {
	registry.mu.Lock()
	registry.persistence = db
	registry.mu.Unlock()
	if db == nil {
		return nil
	}

	now := registry.now()
	if _, err := db.PruneResponsesContinuations(ctx, now.Add(-registry.limits.ttl)); err != nil {
		registry.recordPersistenceFailure()
		return err
	}
	if _, err := db.TrimResponsesContinuations(ctx, registry.limits.maxEntries, registry.limits.maxBytes); err != nil {
		registry.recordPersistenceFailure()
		return err
	}
	registry.mu.Lock()
	registry.lastCleanup = now
	registry.mu.Unlock()
	return nil
}

func (registry *openAIResponsesContinuityRegistry) mergePersisted(rows []database.ResponsesContinuationRow, now time.Time) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, row := range rows {
		if row.ResponseID == "" {
			continue
		}
		if _, exists := registry.entries[row.ResponseID]; exists {
			continue
		}
		input, inputOK := decodePersistedRawMessages(row.InputJSON)
		output, outputOK := decodePersistedRawMessages(row.OutputJSON)
		if !inputOK || !outputOK {
			continue
		}
		entry := openAIResponsesContinuation{
			accountID: row.AccountID, baseURL: row.BaseURL, sessionID: row.SessionID,
			createdAt: row.CreatedAt, accessedAt: now,
		}
		if entry.createdAt.IsZero() {
			entry.createdAt = now
		}
		if entry.accessedAt.IsZero() {
			entry.accessedAt = entry.createdAt
		}
		if row.Replayable && registry.canStoreReplayableLocked(row.ParentID, input, output) {
			entry.parentID = row.ParentID
			entry.input = input
			entry.output = output
			entry.replayable = true
			entry.size = rawMessagesSize(input) + rawMessagesSize(output)
		}
		registry.entries[row.ResponseID] = entry
		registry.totalBytes += entry.size
		if row.SessionID != "" {
			registry.sessionLatest[row.SessionID] = row.ResponseID
			if entry.replayable {
				registry.sessionLatestReplayable[row.SessionID] = row.ResponseID
			}
		}
	}
	registry.purgeExpiredLocked(now)
	registry.enforceLimitsLocked()
}

func (registry *openAIResponsesContinuityRegistry) ensureLoaded(responseID string) bool {
	if responseID == "" {
		return false
	}
	registry.mu.Lock()
	_, loaded := registry.entries[responseID]
	persistence := registry.persistence
	now := registry.now()
	registry.mu.Unlock()
	if loaded {
		return true
	}
	if persistence == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), openAIResponsesContinuityDBTimeout)
	defer cancel()
	rows := make([]database.ResponsesContinuationRow, 0, 8)
	seen := make(map[string]struct{})
	currentID := responseID
	for currentID != "" && len(rows) < registry.limits.maxItems {
		if _, exists := seen[currentID]; exists {
			return false
		}
		seen[currentID] = struct{}{}
		row, ok, err := persistence.GetResponsesContinuation(ctx, currentID)
		if err != nil {
			registry.recordPersistenceFailure()
			log.Printf("Responses 续链磁盘读取失败，已按缓存未命中处理: %v", err)
			return false
		}
		if !ok || now.Sub(row.AccessedAt) > registry.limits.ttl {
			return false
		}
		rows = append(rows, row)
		if !row.Replayable {
			break
		}
		currentID = row.ParentID
	}
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
	registry.mergePersisted(rows, now)
	loadedIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		loadedIDs = append(loadedIDs, row.ResponseID)
	}
	if err := persistence.TouchResponsesContinuations(ctx, loadedIDs, now); err != nil {
		registry.recordPersistenceFailure()
		log.Printf("Responses 续链磁盘访问时间更新失败: %v", err)
	}
	registry.mu.Lock()
	_, loaded = registry.entries[responseID]
	registry.mu.Unlock()
	return loaded
}

func decodePersistedRawMessages(data []byte) ([]json.RawMessage, bool) {
	var items []json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &items) != nil {
		return nil, false
	}
	return items, true
}

func bindOpenAIResponsesContinuationOwner(body []byte, sessionID string, base auth.AccountFilter) (auth.AccountFilter, bool) {
	previousID := gjson.GetBytes(body, "previous_response_id").String()
	var entry openAIResponsesContinuation
	var ok bool
	if previousID != "" {
		entry, ok = getOpenAIResponsesContinuation(previousID)
	} else if sessionID != "" {
		_, entry, ok = openAIResponsesContinuity.getLatestResponse(sessionID)
	}
	if !ok {
		return base, false
	}
	return func(account *auth.Account) bool {
		if base != nil && !base(account) {
			return false
		}
		if account == nil || (entry.accountID != 0 && account.ID() != entry.accountID) {
			return false
		}
		if entry.baseURL == "" {
			return true
		}
		baseURL, _ := account.OpenAIResponsesCredentials()
		return normalizeContinuationBaseURL(baseURL) == entry.baseURL
	}, true
}

func prepareOpenAIResponsesWebSocketContinuation(body []byte, sessionID string, base auth.AccountFilter) ([]byte, auth.AccountFilter, bool, bool) {
	ownerFilter, ownerBound := bindOpenAIResponsesContinuationOwner(body, sessionID, base)
	previousID := gjson.GetBytes(body, "previous_response_id").String()
	if previousID != "" && openAIResponsesContinuity.isReplayable(previousID) {
		if !canBuildOpenAIResponsesContinuationFallback(body, sessionID) {
			return body, ownerFilter, false, ownerBound
		}
		fallback, ok := buildOpenAIResponsesContinuationFallback(body, sessionID)
		if !ok {
			return body, ownerFilter, false, ownerBound
		}
		return fallback, base, true, false
	}
	return body, ownerFilter, false, ownerBound
}

func buildOpenAIResponsesContinuationFallback(body []byte, sessionID string) ([]byte, bool) {
	current, ok := openAIResponsesInputItems(body)
	if !ok || len(current) == 0 {
		return body, false
	}
	previousID := replayableOpenAIResponsesContinuationID(body, sessionID)
	history, historyOk := openAIResponsesContinuity.materialize(previousID)
	if !historyOk || len(history) == 0 {
		ownerFilter, ownerBound := bindOpenAIResponsesContinuationOwner(body, sessionID, nil)
		if !ownerBound && ownerFilter == nil && previousID != "" {
			return body, false
		}
		history = current
	} else {
		history = sanitizeReplayableOpenAIResponsesItems(history)
		history = append(history, current...)
	}
	if len(history) > openAIResponsesContinuity.limits.maxItems || rawMessagesSize(history) > openAIResponsesContinuity.limits.maxItemBytes {
		return body, false
	}
	history, ok = normalizeMatchedOpenAIResponsesToolOutputs(history)
	if !ok {
		return body, false
	}
	input, err := json.Marshal(history)
	if err != nil {
		return body, false
	}
	fallback, err := sjson.DeleteBytes(body, "previous_response_id")
	if err != nil {
		return body, false
	}
	fallback, err = sjson.SetRawBytes(fallback, "input", input)
	return fallback, err == nil
}

func canBuildOpenAIResponsesContinuationFallback(body []byte, sessionID string) bool {
	current, ok := openAIResponsesInputItems(body)
	if !ok || len(current) == 0 {
		return false
	}
	previousID := replayableOpenAIResponsesContinuationID(body, sessionID)
	if previousID == "" {
		return false
	}
	history, historyOk := openAIResponsesContinuity.materialize(previousID)
	if !historyOk || len(history) == 0 {
		return false
	}
	history = sanitizeReplayableOpenAIResponsesItems(history)
	history = append(history, current...)
	_, ok = normalizeMatchedOpenAIResponsesToolOutputs(history)
	return ok
}

func shouldReplayOpenAIResponsesContinuationBeforeUpstream(body []byte, account *auth.Account, sessionID string) bool {
	if openAIResponsesContinuityMode == openAIResponsesContinuityModeUpstream || account == nil {
		return false
	}
	if account.IsOpenAIResponsesAPI() {
		baseURL, _ := account.OpenAIResponsesCredentials()
		if isOfficialOpenAIResponsesBaseURL(baseURL) {
			return false
		}
		previousID := gjson.GetBytes(body, "previous_response_id").String()
		if previousID == "" {
			previousID = openAIResponsesContinuity.getLatestReplayableResponseID(sessionID)
		}
		return openAIResponsesContinuity.isReplayable(previousID)
	}
	return false
}

func isOfficialOpenAIResponsesBaseURL(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	return err == nil && strings.EqualFold(parsed.Hostname(), "api.openai.com")
}

func replayableOpenAIResponsesContinuationID(body []byte, sessionID string) string {
	previousID := gjson.GetBytes(body, "previous_response_id").String()
	if previousID != "" && openAIResponsesContinuity.isReplayable(previousID) {
		return previousID
	}
	return openAIResponsesContinuity.getLatestReplayableResponseID(sessionID)
}



var openAIResponsesAutoCompleteToolOutputs = openAIResponsesAutoCompleteToolOutputsFromEnv(os.Getenv)

func openAIResponsesAutoCompleteToolOutputsFromEnv(getenv func(string) string) bool {
	val := strings.TrimSpace(getenv("CODEX_RESPONSES_AUTO_COMPLETE_TOOL_OUTPUTS"))
	if val == "" {
		return true // 默认开启智能自动闭环补齐，实现账号池无缝丝滑切换
	}
	return strings.EqualFold(val, "true") || val == "1"
}

func normalizeMatchedOpenAIResponsesToolOutputs(items []json.RawMessage) ([]json.RawMessage, bool) {
	expectedOutputs := make(map[string]string)
	requiredOutputs := make(map[string]struct{})
	providedOutputs := make(map[string]struct{})

	for _, item := range items {
		parsed := gjson.ParseBytes(item)
		itemType := parsed.Get("type").String()
		if isOpenAIResponsesToolOutputType(itemType) {
			callID := openAIResponsesToolCorrelationID(parsed)
			if callID != "" {
				providedOutputs[callID] = struct{}{}
			}
		}
		expectedType, isCall := openAIResponsesToolOutputType(itemType)
		if !isCall {
			continue
		}
		callID := openAIResponsesToolCorrelationID(parsed)
		if callID == "" {
			return nil, false
		}
		if _, duplicate := expectedOutputs[callID]; duplicate {
			return nil, false
		}
		expectedOutputs[callID] = expectedType
		if openAIResponsesToolCallRequiresOutput(parsed) {
			requiredOutputs[callID] = struct{}{}
		}
	}

	normalized := make([]json.RawMessage, 0, len(items)+len(requiredOutputs))
	seenOutputs := make(map[string]struct{})
	for _, item := range items {
		parsed := gjson.ParseBytes(item)
		actualType := parsed.Get("type").String()
		if !isOpenAIResponsesToolOutputType(actualType) {
			normalized = append(normalized, item)
			expectedType, isCall := openAIResponsesToolOutputType(actualType)
			if openAIResponsesAutoCompleteToolOutputs && isCall && openAIResponsesToolCallRequiresOutput(parsed) {
				callID := openAIResponsesToolCorrelationID(parsed)
				if callID != "" {
					if _, hasMatchingOutput := providedOutputs[callID]; !hasMatchingOutput {
						syntheticOutput := buildSyntheticOpenAIResponsesToolOutput(expectedType, callID)
						normalized = append(normalized, syntheticOutput)
						seenOutputs[callID] = struct{}{}
						delete(requiredOutputs, callID)
					}
				}
			}
			continue
		}
		callID := openAIResponsesToolCorrelationID(parsed)
		if callID == "" {
			return nil, false
		}
		expectedType, ok := expectedOutputs[callID]
		if !ok {
			return nil, false
		}
		if _, duplicate := seenOutputs[callID]; duplicate {
			return nil, false
		}
		seenOutputs[callID] = struct{}{}
		delete(requiredOutputs, callID)
		if actualType != expectedType {
			var err error
			item, err = normalizeOpenAIResponsesToolOutput(item, actualType, expectedType, callID)
			if err != nil {
				return nil, false
			}
		}
		normalized = append(normalized, item)
	}
	return normalized, len(requiredOutputs) == 0
}

func buildSyntheticOpenAIResponsesToolOutput(expectedType, callID string) json.RawMessage {
	switch expectedType {
	case "function_call_output", "custom_tool_call_output", "mcp_tool_call_output":
		msg, _ := json.Marshal(map[string]any{
			"type":    expectedType,
			"call_id": callID,
			"output":  "[System: Auto-completed tool output for seamless account switch]",
		})
		return msg
	case "tool_search_call_output":
		msg, _ := json.Marshal(map[string]any{
			"type":    expectedType,
			"call_id": callID,
			"output":  []any{},
			"status":  "completed",
		})
		return msg
	case "local_shell_call_output":
		msg, _ := json.Marshal(map[string]any{
			"type":   expectedType,
			"id":     callID,
			"output": `{"stdout":"[System: Auto-completed tool output for seamless account switch]"}`,
			"status": "completed",
		})
		return msg
	default:
		msg, _ := json.Marshal(map[string]any{
			"type":    expectedType,
			"call_id": callID,
			"output":  "ok",
		})
		return msg
	}
}

func normalizeOpenAIResponsesToolOutput(item json.RawMessage, actualType, expectedType, callID string) (json.RawMessage, error) {
	normalized, err := sjson.SetBytes(item, "type", expectedType)
	if err != nil {
		return nil, err
	}
	if expectedType == "local_shell_call_output" {
		normalized, err = sjson.SetBytes(normalized, "id", callID)
		if err == nil {
			normalized, err = sjson.DeleteBytes(normalized, "call_id")
		}
		return normalized, err
	}
	normalized, err = sjson.SetBytes(normalized, "call_id", callID)
	if err == nil && actualType == "local_shell_call_output" {
		normalized, err = sjson.DeleteBytes(normalized, "id")
	}
	return normalized, err
}

func openAIResponsesToolCallRequiresOutput(item gjson.Result) bool {
	return item.Get("type").String() != "tool_search_call" || item.Get("execution").String() != "server"
}

func openAIResponsesToolCorrelationID(item gjson.Result) string {
	if item.Get("type").String() == "local_shell_call_output" || item.Get("type").String() == "local_shell_call" {
		if id := item.Get("id").String(); id != "" {
			return id
		}
	}
	if callID := item.Get("call_id").String(); callID != "" {
		return callID
	}
	if id := item.Get("id").String(); id != "" {
		return id
	}
	return ""
}

func openAIResponsesToolOutputType(callType string) (string, bool) {
	switch callType {
	case "function_call":
		return "function_call_output", true
	case "tool_call":
		return "tool_call_output", true
	case "local_shell_call":
		return "local_shell_call_output", true
	case "tool_search_call":
		return "tool_search_call_output", true
	case "custom_tool_call":
		return "custom_tool_call_output", true
	case "mcp_tool_call":
		return "mcp_tool_call_output", true
	default:
		return "", false
	}
}

func isOpenAIResponsesToolOutputType(itemType string) bool {
	switch itemType {
	case "function_call_output", "tool_call_output", "local_shell_call_output",
		"tool_search_call_output", "custom_tool_call_output", "mcp_tool_call_output":
		return true
	default:
		return false
	}
}

func shouldFallbackOpenAIResponsesContinuation(statusCode int, requestBody, errorBody []byte) bool {
	if statusCode != http.StatusBadRequest || gjson.GetBytes(requestBody, "previous_response_id").String() == "" {
		return false
	}
	if gjson.GetBytes(errorBody, "error.code").String() == "previous_response_not_found" {
		return true
	}
	message := strings.ToLower(gjson.GetBytes(errorBody, "error.message").String())
	return strings.Contains(message, "previous_response_id is only supported on responses websocket v2") ||
		(strings.Contains(message, "previous_response_id") && strings.Contains(message, "not found"))
}

func isMissingOpenAIResponsesToolOutputError(statusCode int, errorBody []byte) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	message := strings.ToLower(gjson.GetBytes(errorBody, "error.message").String())
	for _, callKind := range []string{
		"function call",
		"custom tool call",
		"mcp tool call",
		"mcp call",
		"tool search call",
		"local shell call",
	} {
		if strings.Contains(message, "no tool output found for "+callKind) {
			return true
		}
	}
	return false
}

func shouldReplayOpenAIResponsesContinuationAfterHTTPFailure(statusCode int, requestBody, errorBody []byte) bool {
	if shouldFallbackOpenAIResponsesContinuation(statusCode, requestBody, errorBody) {
		return true
	}
	if gjson.GetBytes(requestBody, "previous_response_id").String() == "" {
		return false
	}
	switch statusCode {
	case http.StatusPaymentRequired, http.StatusForbidden, http.StatusRequestTimeout,
		http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func RegisterPendingOpenAIResponsesContinuation(responseID, parentID, sessionID string, account *auth.Account) {
	if account == nil || responseID == "" {
		return
	}
	baseURL, _ := account.OpenAIResponsesCredentials()
	openAIResponsesContinuity.registerPending(responseID, parentID, sessionID, account.ID(), normalizeContinuationBaseURL(baseURL))
}

func AppendPendingOpenAIResponsesOutput(responseID string, requestBody []byte, item gjson.Result) bool {
	if responseID == "" {
		return false
	}
	input, _ := openAIResponsesInputItems(requestBody)
	replayable, ok := completeReplayableOpenAIResponsesItem(item)
	if !ok {
		return false
	}
	return openAIResponsesContinuity.appendPendingOutput(responseID, input, replayable)
}

func trackOpenAIResponsesContinuationSSEEvent(event gjson.Result, requestBody []byte, sessionID string, account *auth.Account, responseID string) string {
	switch event.Get("type").String() {
	case "response.created":
		responseID = event.Get("response.id").String()
		if responseID != "" {
			RegisterPendingOpenAIResponsesContinuation(responseID, gjson.GetBytes(requestBody, "previous_response_id").String(), sessionID, account)
		}
	case "response.output_item.done":
		if responseID == "" {
			responseID = event.Get("response_id").String()
		}
		if responseID != "" {
			AppendPendingOpenAIResponsesOutput(responseID, requestBody, event.Get("item"))
		}
	}
	return responseID
}

func cacheOpenAIResponsesContinuation(requestBody, responseData []byte, account *auth.Account, sessionID string) {
	if account == nil {
		return
	}
	response := gjson.ParseBytes(responseData)
	if response.Get("type").String() == "response.completed" {
		response = response.Get("response")
	}
	responseID := response.Get("id").String()
	if responseID == "" {
		return
	}
	input, _ := openAIResponsesInputItems(requestBody)
	output, _ := openAIResponsesOutputItems(response.Get("output"))
	baseURL, _ := account.OpenAIResponsesCredentials()
	parentID := gjson.GetBytes(requestBody, "previous_response_id").String()
	if parentID == "" && sessionID != "" {
		if pending, found := openAIResponsesContinuity.get(responseID); found {
			parentID = pending.parentID
		} else if latest := openAIResponsesContinuity.getLatestReplayableResponseID(sessionID); latest != responseID {
			parentID = latest
		}
	}
	openAIResponsesContinuity.store(responseID, parentID, sessionID, openAIResponsesContinuation{
		accountID: account.ID(),
		baseURL:   normalizeContinuationBaseURL(baseURL),
		sessionID: sessionID,
		input:     input,
		output:    output,
	})
}

func parentOpenAIResponsesHistory(requestBody []byte) ([]json.RawMessage, bool) {
	previousID := gjson.GetBytes(requestBody, "previous_response_id").String()
	if previousID == "" {
		return nil, true
	}
	return openAIResponsesContinuity.materialize(previousID)
}

func openAIResponsesInputItems(body []byte) ([]json.RawMessage, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() {
		return nil, false
	}
	if input.Type == gjson.String {
		message, err := json.Marshal(map[string]any{
			"type":    "message",
			"role":    "user",
			"content": input.String(),
		})
		return []json.RawMessage{message}, err == nil
	}
	if !input.IsArray() {
		return nil, false
	}
	items := make([]json.RawMessage, 0, len(input.Array()))
	input.ForEach(func(_, item gjson.Result) bool {
		cleaned, keep := replayableOpenAIResponsesItem(item)
		if !keep || cleaned == nil {
			return true
		}
		items = append(items, cleaned)
		return true
	})
	return items, len(items) > 0
}

func openAIResponsesOutputItems(output gjson.Result) ([]json.RawMessage, bool) {
	if !output.IsArray() {
		return nil, false
	}
	items := make([]json.RawMessage, 0, len(output.Array()))
	output.ForEach(func(_, item gjson.Result) bool {
		cleaned, keep := replayableOpenAIResponsesItem(item)
		if !keep || cleaned == nil {
			return true
		}
		items = append(items, cleaned)
		return true
	})
	return items, len(items) > 0
}

func replayableOpenAIResponsesItem(item gjson.Result) (json.RawMessage, bool) {
	if !item.IsObject() {
		return nil, false
	}
	if item.Get("type").String() == "reasoning" {
		encrypted := item.Get("encrypted_content")
		if encrypted.Type != gjson.String || strings.TrimSpace(encrypted.String()) == "" {
			return nil, false
		}
		return json.RawMessage(item.Raw), true
	}
	itemType := item.Get("type").String()
	cleaned := json.RawMessage(item.Raw)
	if itemType != "mcp_call" && itemType != "local_shell_call_output" {
		if stripped, strippedOK := stripResponseItemID(cleaned); strippedOK {
			cleaned = stripped
		}
	}
	if itemType == "function_call" && !item.Get("arguments").Exists() {
		fixed, err := sjson.SetBytes(cleaned, "arguments", "{}")
		if err == nil {
			cleaned = fixed
		}
	}
	return cleaned, true
}

func validOpenAIResponsesToolCallItem(item gjson.Result) bool {
	itemType := item.Get("type").String()
	switch itemType {
	case "function_call":
		return validOpenAIResponsesFunctionCallItem(item)
	case "custom_tool_call", "mcp_tool_call", "tool_search_call", "local_shell_call", "web_search_call":
		callID := openAIResponsesToolCorrelationID(item)
		return strings.TrimSpace(callID) != ""
	case "image_generation_call":
		return item.Get("id").Exists() || item.Get("result").Exists() || item.Get("status").String() == "completed"
	default:
		return item.Get("status").String() == "completed"
	}
}

func completeReplayableOpenAIResponsesItem(item gjson.Result) (json.RawMessage, bool) {
	replayable, ok := replayableOpenAIResponsesItem(item)
	if !ok {
		return nil, false
	}
	itemType := item.Get("type").String()
	switch itemType {
	case "function_call", "custom_tool_call", "mcp_tool_call", "tool_search_call", "local_shell_call", "web_search_call", "image_generation_call":
		return replayable, validOpenAIResponsesToolCallItem(item)
	case "message":
		return replayable, item.Get("role").Type == gjson.String && item.Get("content").Exists()
	case "reasoning":
		return replayable, true
	default:
		return replayable, item.Get("status").String() == "completed"
	}
}

func validOpenAIResponsesFunctionCallItem(item gjson.Result) bool {
	callID := item.Get("call_id")
	if callID.Type != gjson.String || strings.TrimSpace(callID.String()) == "" {
		return false
	}
	name := item.Get("name")
	if name.Type != gjson.String || strings.TrimSpace(name.String()) == "" {
		return false
	}
	arguments := item.Get("arguments")
	return arguments.Type == gjson.String && json.Valid([]byte(arguments.String()))
}

func openAIResponsesItemStableKey(item json.RawMessage) string {
	parsed := gjson.ParseBytes(item)
	if callID := parsed.Get("call_id").String(); callID != "" {
		return parsed.Get("type").String() + "\x00" + callID
	}
	return string(item)
}

func containsOpenAIResponsesItem(items []json.RawMessage, candidate json.RawMessage) bool {
	key := openAIResponsesItemStableKey(candidate)
	for _, item := range items {
		if openAIResponsesItemStableKey(item) == key {
			return true
		}
	}
	return false
}

func sanitizeReplayableOpenAIResponsesItems(items []json.RawMessage) []json.RawMessage {
	cleaned := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		replayable, keep := replayableOpenAIResponsesItem(gjson.ParseBytes(item))
		if keep {
			cleaned = append(cleaned, replayable)
		}
	}
	return cleaned
}

func getOpenAIResponsesContinuation(responseID string) (openAIResponsesContinuation, bool) {
	return openAIResponsesContinuity.get(responseID)
}

func setOpenAIResponsesContinuation(responseID string, entry openAIResponsesContinuation) {
	openAIResponsesContinuity.store(responseID, entry.parentID, entry.sessionID, entry)
}

func (registry *openAIResponsesContinuityRegistry) get(responseID string) (openAIResponsesContinuation, bool) {
	if responseID == "" {
		return openAIResponsesContinuation{}, false
	}
	registry.ensureLoaded(responseID)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	now := registry.now()
	registry.purgeExpiredLocked(now)
	entry, ok := registry.entries[responseID]
	if !ok {
		return openAIResponsesContinuation{}, false
	}
	registry.touchAncestorsLocked(responseID, now)
	entry.input = cloneRawMessages(entry.input)
	entry.output = cloneRawMessages(entry.output)
	return entry, true
}

func (registry *openAIResponsesContinuityRegistry) registerPending(responseID, parentID, sessionID string, accountID int64, baseURL string) {
	if responseID == "" {
		return
	}
	if parentID == "" && sessionID != "" {
		parentID = registry.getLatestReplayableResponseID(sessionID)
	}
	registry.mu.Lock()
	now := registry.now()
	existing, found := registry.entries[responseID]
	if !found {
		entry := openAIResponsesContinuation{
			accountID:  accountID,
			baseURL:    baseURL,
			parentID:   parentID,
			sessionID:  sessionID,
			createdAt:  now,
			accessedAt: now,
			pending:    true,
		}
		registry.entries[responseID] = entry
	} else if existing.sessionID == "" && sessionID != "" {
		existing.sessionID = sessionID
		registry.entries[responseID] = existing
	}
	if sessionID != "" {
		registry.sessionLatest[sessionID] = responseID
	}
	storedEntry, retained := registry.entries[responseID]
	persistence := registry.persistence
	registry.mu.Unlock()

	if persistence != nil && retained {
		registry.persist(persistence, responseID, storedEntry, nil, false)
	}
}

func (registry *openAIResponsesContinuityRegistry) appendPendingOutput(responseID string, input []json.RawMessage, output json.RawMessage) bool {
	registry.mu.Lock()
	now := registry.now()
	previousEvictions := registry.evictions
	registry.purgeExpiredLocked(now)
	entry, ok := registry.entries[responseID]
	if !ok || !entry.pending {
		registry.mu.Unlock()
		return false
	}
	if containsOpenAIResponsesItem(entry.output, output) {
		registry.mu.Unlock()
		return true
	}
	entry.input = cloneRawMessages(input)
	entry.output = appendClonedRawMessages(entry.output, []json.RawMessage{output})
	if !registry.canStoreReplayableLocked(entry.parentID, entry.input, entry.output) {
		registry.mu.Unlock()
		return false
	}
	registry.totalBytes -= entry.size
	entry.replayable = true
	entry.accessedAt = now
	entry.size = rawMessagesSize(entry.input) + rawMessagesSize(entry.output)
	registry.entries[responseID] = entry
	registry.totalBytes += entry.size
	registry.touchAncestorsLocked(entry.parentID, now)
	registry.enforceLimitsLocked()
	storedEntry, retained := registry.entries[responseID]
	if retained && storedEntry.sessionID != "" && storedEntry.replayable {
		registry.sessionLatestReplayable[storedEntry.sessionID] = responseID
	}
	persistence := registry.persistence
	ancestorIDs := registry.ancestorIDsLocked(entry.parentID)
	cleanup := registry.evictions > previousEvictions || now.Sub(registry.lastCleanup) >= openAIResponsesContinuityCleanupInterval
	if cleanup {
		registry.lastCleanup = now
	}
	registry.mu.Unlock()
	if persistence != nil && retained {
		registry.persist(persistence, responseID, storedEntry, ancestorIDs, cleanup)
	}
	return retained
}

func (registry *openAIResponsesContinuityRegistry) store(responseID, parentID, sessionID string, entry openAIResponsesContinuation) {
	if responseID == "" {
		return
	}
	if parentID != "" {
		registry.ensureLoaded(parentID)
	}
	entry.input = cloneRawMessages(entry.input)
	entry.output = cloneRawMessages(entry.output)
	registry.mu.Lock()
	now := registry.now()
	previousEvictions := registry.evictions
	registry.purgeExpiredLocked(now)

	existing, found := registry.entries[responseID]
	createdAt := now
	if found && !existing.createdAt.IsZero() {
		createdAt = existing.createdAt
	}
	if sessionID == "" && found {
		sessionID = existing.sessionID
	}
	if parentID == "" && found && existing.parentID != "" {
		parentID = existing.parentID
	}

	entry.createdAt = createdAt
	entry.accessedAt = now
	entry.sessionID = sessionID
	entry.pending = false
	entry.parentID = ""
	entry.replayable = registry.canStoreReplayableLocked(parentID, entry.input, entry.output)
	if entry.replayable {
		entry.parentID = parentID
		entry.size = rawMessagesSize(entry.input) + rawMessagesSize(entry.output)
		registry.touchAncestorsLocked(parentID, now)
	} else {
		entry.input = nil
		entry.output = nil
		entry.size = 0
	}
	if found {
		registry.totalBytes -= existing.size
	}
	registry.entries[responseID] = entry
	registry.totalBytes += entry.size
	if sessionID != "" {
		registry.sessionLatest[sessionID] = responseID
		if entry.replayable {
			registry.sessionLatestReplayable[sessionID] = responseID
		}
	}
	registry.enforceLimitsLocked()
	persistence := registry.persistence
	ancestorIDs := registry.ancestorIDsLocked(parentID)
	storedEntry, retained := registry.entries[responseID]
	cleanupPersistence := registry.evictions > previousEvictions || now.Sub(registry.lastCleanup) >= openAIResponsesContinuityCleanupInterval
	if cleanupPersistence {
		registry.lastCleanup = now
	}
	registry.mu.Unlock()
	if persistence != nil && retained {
		registry.persist(persistence, responseID, storedEntry, ancestorIDs, cleanupPersistence)
	}
}

func (registry *openAIResponsesContinuityRegistry) getLatestResponse(sessionID string) (string, openAIResponsesContinuation, bool) {
	if sessionID == "" {
		return "", openAIResponsesContinuation{}, false
	}
	registry.mu.Lock()
	respID, hasL1 := registry.sessionLatest[sessionID]
	var entry openAIResponsesContinuation
	var entryFound bool
	if hasL1 {
		entry, entryFound = registry.entries[respID]
	}
	persistence := registry.persistence
	now := registry.now()
	registry.mu.Unlock()

	if hasL1 && entryFound {
		return respID, entry, true
	}

	if persistence == nil {
		return "", openAIResponsesContinuation{}, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), openAIResponsesContinuityDBTimeout)
	defer cancel()

	row, ok, err := persistence.GetLatestResponseBySessionID(ctx, sessionID)
	if err != nil || !ok || now.Sub(row.AccessedAt) > registry.limits.ttl {
		return "", openAIResponsesContinuation{}, false
	}

	registry.ensureLoaded(row.ResponseID)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry, ok = registry.entries[row.ResponseID]
	if ok {
		registry.sessionLatest[sessionID] = row.ResponseID
		return row.ResponseID, entry, true
	}
	return "", openAIResponsesContinuation{}, false
}

func (registry *openAIResponsesContinuityRegistry) getLatestReplayableResponseID(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	registry.mu.Lock()
	responseID := registry.sessionLatestReplayable[sessionID]
	persistence := registry.persistence
	now := registry.now()
	registry.mu.Unlock()
	if responseID != "" && registry.isReplayable(responseID) {
		return responseID
	}
	if persistence == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), openAIResponsesContinuityDBTimeout)
	defer cancel()
	row, ok, err := persistence.GetLatestReplayableResponseBySessionID(ctx, sessionID)
	if err != nil || !ok || now.Sub(row.AccessedAt) > registry.limits.ttl {
		return ""
	}
	registry.ensureLoaded(row.ResponseID)
	if !registry.isReplayable(row.ResponseID) {
		return ""
	}
	registry.mu.Lock()
	registry.sessionLatestReplayable[sessionID] = row.ResponseID
	registry.mu.Unlock()
	return row.ResponseID
}

func (registry *openAIResponsesContinuityRegistry) ancestorIDsLocked(responseID string) []string {
	ids := make([]string, 0, 8)
	seen := make(map[string]struct{})
	for responseID != "" {
		if _, exists := seen[responseID]; exists {
			break
		}
		seen[responseID] = struct{}{}
		entry, ok := registry.entries[responseID]
		if !ok {
			break
		}
		ids = append(ids, responseID)
		responseID = entry.parentID
	}
	return ids
}

func (registry *openAIResponsesContinuityRegistry) persist(db responsesContinuityPersistence, responseID string, entry openAIResponsesContinuation, ancestorIDs []string, cleanup bool) {
	ctx, cancel := context.WithTimeout(context.Background(), openAIResponsesContinuityDBTimeout)
	defer cancel()
	inputJSON, inputErr := json.Marshal(entry.input)
	outputJSON, outputErr := json.Marshal(entry.output)
	if inputErr != nil || outputErr != nil {
		return
	}
	row := database.ResponsesContinuationRow{
		ResponseID: responseID, ParentID: entry.parentID, SessionID: entry.sessionID,
		AccountID: entry.accountID, BaseURL: entry.baseURL,
		InputJSON: inputJSON, OutputJSON: outputJSON,
		Replayable: entry.replayable, CreatedAt: entry.createdAt,
		AccessedAt: entry.accessedAt, SizeBytes: entry.size,
	}
	if err := db.UpsertResponsesContinuation(ctx, &row); err != nil {
		registry.recordPersistenceFailure()
		log.Printf("Responses 续链持久化失败，已降级为内存缓存: %v", err)
		return
	}
	if err := db.TouchResponsesContinuations(ctx, ancestorIDs, entry.accessedAt); err != nil {
		registry.recordPersistenceFailure()
		log.Printf("Responses 续链访问时间持久化失败: %v", err)
	}
	if cleanup {
		registry.schedulePersistenceCleanup(db, entry.accessedAt)
	}
}

func (registry *openAIResponsesContinuityRegistry) schedulePersistenceCleanup(db responsesContinuityPersistence, accessedAt time.Time) {
	request := &openAIResponsesContinuityCleanupRequest{persistence: db, accessedAt: accessedAt}
	registry.cleanupMu.Lock()
	registry.cleanupPending = request
	if registry.cleanupRunning {
		registry.cleanupMu.Unlock()
		return
	}
	registry.cleanupRunning = true
	registry.cleanupMu.Unlock()
	go registry.runPersistenceCleanup()
}

func (registry *openAIResponsesContinuityRegistry) runPersistenceCleanup() {
	for {
		request := registry.takePersistenceCleanup()
		if request == nil {
			return
		}
		registry.cleanupPersistence(request)
	}
}

func (registry *openAIResponsesContinuityRegistry) takePersistenceCleanup() *openAIResponsesContinuityCleanupRequest {
	registry.cleanupMu.Lock()
	defer registry.cleanupMu.Unlock()
	request := registry.cleanupPending
	registry.cleanupPending = nil
	if request == nil {
		registry.cleanupRunning = false
	}
	return request
}

func (registry *openAIResponsesContinuityRegistry) cleanupPersistence(request *openAIResponsesContinuityCleanupRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), openAIResponsesContinuityCleanupTimeout)
	defer cancel()
	if _, err := request.persistence.PruneResponsesContinuations(ctx, request.accessedAt.Add(-registry.limits.ttl)); err != nil {
		registry.recordPersistenceFailure()
		log.Printf("Responses 续链过期数据清理失败: %v", err)
		return
	}
	if _, err := request.persistence.TrimResponsesContinuations(ctx, registry.limits.maxEntries, registry.limits.maxBytes); err != nil {
		registry.recordPersistenceFailure()
		log.Printf("Responses 续链磁盘容量清理失败: %v", err)
	}
}

func (registry *openAIResponsesContinuityRegistry) canStoreReplayableLocked(parentID string, input, output []json.RawMessage) bool {
	if len(input) == 0 && parentID == "" {
		return false
	}
	if len(input) == 0 && len(output) == 0 {
		return false
	}
	items := len(input) + len(output)
	bytes := rawMessagesSize(input) + rawMessagesSize(output)
	if parentID != "" {
		parentItems, parentBytes, ok := registry.chainUsageLocked(parentID)
		if !ok {
			return false
		}
		items += parentItems
		bytes += parentBytes
	}
	return items <= registry.limits.maxItems && bytes <= registry.limits.maxItemBytes && bytes <= registry.limits.maxBytes
}

func (registry *openAIResponsesContinuityRegistry) chainUsageLocked(responseID string) (int, int, bool) {
	items := 0
	bytes := 0
	seen := make(map[string]struct{})
	for responseID != "" {
		if _, exists := seen[responseID]; exists {
			return 0, 0, false
		}
		seen[responseID] = struct{}{}
		entry, ok := registry.entries[responseID]
		if !ok || !entry.replayable {
			return 0, 0, false
		}
		items += len(entry.input) + len(entry.output)
		bytes += entry.size
		responseID = entry.parentID
	}
	return items, bytes, true
}

func (registry *openAIResponsesContinuityRegistry) materialize(responseID string) ([]json.RawMessage, bool) {
	if responseID == "" {
		return nil, false
	}
	registry.ensureLoaded(responseID)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	now := registry.now()
	registry.purgeExpiredLocked(now)
	path := make([]openAIResponsesContinuation, 0, 8)
	seen := make(map[string]struct{})
	currentID := responseID
	totalItems := 0
	totalBytes := 0
	for currentID != "" {
		if _, exists := seen[currentID]; exists {
			return nil, false
		}
		seen[currentID] = struct{}{}
		entry, ok := registry.entries[currentID]
		if !ok || !entry.replayable {
			return nil, false
		}
		path = append(path, entry)
		totalItems += len(entry.input) + len(entry.output)
		totalBytes += entry.size
		currentID = entry.parentID
	}
	if totalItems > registry.limits.maxItems || totalBytes > registry.limits.maxItemBytes {
		return nil, false
	}
	history := make([]json.RawMessage, 0, totalItems)
	for index := len(path) - 1; index >= 0; index-- {
		history = appendClonedRawMessages(history, path[index].input)
		history = appendClonedRawMessages(history, path[index].output)
	}
	registry.touchAncestorsLocked(responseID, now)
	return history, true
}

func (registry *openAIResponsesContinuityRegistry) isReplayable(responseID string) bool {
	if responseID == "" {
		return false
	}
	registry.ensureLoaded(responseID)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	now := registry.now()
	registry.purgeExpiredLocked(now)
	_, _, ok := registry.chainUsageLocked(responseID)
	if ok {
		registry.touchAncestorsLocked(responseID, now)
	}
	return ok
}

func (registry *openAIResponsesContinuityRegistry) touchAncestorsLocked(responseID string, now time.Time) {
	seen := make(map[string]struct{})
	for responseID != "" {
		if _, exists := seen[responseID]; exists {
			return
		}
		seen[responseID] = struct{}{}
		entry, ok := registry.entries[responseID]
		if !ok {
			return
		}
		entry.accessedAt = now
		registry.entries[responseID] = entry
		responseID = entry.parentID
	}
}

func (registry *openAIResponsesContinuityRegistry) purgeExpiredLocked(now time.Time) {
	for {
		expiredID := ""
		for responseID, entry := range registry.entries {
			if now.Sub(entry.accessedAt) > registry.limits.ttl {
				expiredID = responseID
				break
			}
		}
		if expiredID == "" {
			return
		}
		registry.removeSubtreeLocked(expiredID, true)
	}
}

func (registry *openAIResponsesContinuityRegistry) enforceLimitsLocked() {
	for len(registry.entries) > registry.limits.maxEntries || registry.totalBytes > registry.limits.maxBytes {
		if !registry.evictOldestLocked() {
			return
		}
	}
}

func (registry *openAIResponsesContinuityRegistry) evictOldestLocked() bool {
	oldestID := ""
	var oldest time.Time
	for responseID, entry := range registry.entries {
		if oldestID == "" || entry.accessedAt.Before(oldest) {
			oldestID = responseID
			oldest = entry.accessedAt
		}
	}
	if oldestID == "" {
		return false
	}
	registry.removeSubtreeLocked(oldestID, true)
	return true
}

func (registry *openAIResponsesContinuityRegistry) removeSubtreeLocked(responseID string, countEviction bool) {
	remove := map[string]struct{}{responseID: {}}
	for {
		changed := false
		for childID, entry := range registry.entries {
			if _, marked := remove[entry.parentID]; !marked {
				continue
			}
			if _, marked := remove[childID]; marked {
				continue
			}
			remove[childID] = struct{}{}
			changed = true
		}
		if !changed {
			break
		}
	}
	for entryID := range remove {
		entry, ok := registry.entries[entryID]
		if !ok {
			continue
		}
		registry.totalBytes -= entry.size
		if entry.sessionID != "" && registry.sessionLatest[entry.sessionID] == entryID {
			delete(registry.sessionLatest, entry.sessionID)
		}
		if entry.sessionID != "" && registry.sessionLatestReplayable[entry.sessionID] == entryID {
			delete(registry.sessionLatestReplayable, entry.sessionID)
		}
		delete(registry.entries, entryID)
		if countEviction {
			registry.evictions++
		}
	}
}

func (registry *openAIResponsesContinuityRegistry) stats() openAIResponsesContinuityStats {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.purgeExpiredLocked(registry.now())
	return openAIResponsesContinuityStats{
		Entries:             len(registry.entries),
		Bytes:               registry.totalBytes,
		Evictions:           registry.evictions,
		Persistent:          registry.persistence != nil,
		PersistenceFailures: registry.persistFail,
	}
}

func (registry *openAIResponsesContinuityRegistry) recordPersistenceFailure() {
	registry.mu.Lock()
	registry.persistFail++
	registry.mu.Unlock()
}

func (registry *openAIResponsesContinuityRegistry) reset() {
	registry.mu.Lock()
	registry.entries = make(map[string]openAIResponsesContinuation)
	registry.sessionLatest = make(map[string]string)
	registry.sessionLatestReplayable = make(map[string]string)
	registry.totalBytes = 0
	registry.evictions = 0
	registry.persistence = nil
	registry.lastCleanup = time.Time{}
	registry.persistFail = 0
	registry.mu.Unlock()
}

func cloneRawMessages(items []json.RawMessage) []json.RawMessage {
	return appendClonedRawMessages(make([]json.RawMessage, 0, len(items)), items)
}

func appendClonedRawMessages(destination, items []json.RawMessage) []json.RawMessage {
	for _, item := range items {
		destination = append(destination, append(json.RawMessage(nil), item...))
	}
	return destination
}

func rawMessagesSize(items []json.RawMessage) int {
	size := 0
	for _, item := range items {
		size += len(item)
	}
	return size
}

func normalizeContinuationBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

func resetOpenAIResponsesContinuityForTest() {
	SetResinConfig(nil)
	openAIResponsesContinuity.reset()
	openAIResponsesContinuityMode = openAIResponsesContinuityModeAuto
	openAIResponsesAutoCompleteToolOutputs = true
}
