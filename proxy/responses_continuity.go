package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openAIResponsesContinuityTTL          = time.Hour
	openAIResponsesContinuityMaxEntries   = 2000
	openAIResponsesContinuityMaxItems     = 400
	openAIResponsesContinuityMaxItemBytes = 4 << 20
	openAIResponsesContinuityMaxBytes     = 64 << 20
)

type openAIResponsesContinuation struct {
	accountID int64
	baseURL   string
	history   []json.RawMessage
	createdAt time.Time
	size      int
}

var openAIResponsesContinuity = struct {
	mu         sync.Mutex
	entries    map[string]openAIResponsesContinuation
	totalBytes int
}{entries: make(map[string]openAIResponsesContinuation)}

func bindOpenAIResponsesContinuationOwner(body []byte, base auth.AccountFilter) (auth.AccountFilter, bool) {
	previousID := gjson.GetBytes(body, "previous_response_id").String()
	entry, ok := getOpenAIResponsesContinuation(previousID)
	if !ok {
		return base, false
	}

	return func(account *auth.Account) bool {
		if base != nil && !base(account) {
			return false
		}
		if account == nil || account.ID() != entry.accountID {
			return false
		}
		if entry.baseURL == "" {
			return true
		}
		baseURL, _ := account.OpenAIResponsesCredentials()
		return normalizeContinuationBaseURL(baseURL) == entry.baseURL
	}, true
}

func buildOpenAIResponsesContinuationFallback(body []byte) ([]byte, bool) {
	previousID := gjson.GetBytes(body, "previous_response_id").String()
	entry, ok := getOpenAIResponsesContinuation(previousID)
	if !ok || len(entry.history) == 0 {
		return body, false
	}

	current, ok := openAIResponsesInputItems(body)
	if !ok || len(current) == 0 {
		return body, false
	}
	history := cloneRawMessages(entry.history)
	history = append(history, current...)
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

func cacheOpenAIResponsesContinuation(requestBody, responseData []byte, account *auth.Account) {
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

	history, ok := parentOpenAIResponsesHistory(requestBody)
	if ok {
		var input []json.RawMessage
		input, ok = openAIResponsesInputItems(requestBody)
		history = append(history, input...)
	}
	if ok {
		var output []json.RawMessage
		output, ok = openAIResponsesOutputItems(response.Get("output"))
		history = append(history, output...)
	}
	if !ok || len(history) > openAIResponsesContinuityMaxItems || rawMessagesSize(history) > openAIResponsesContinuityMaxItemBytes {
		history = nil
	}

	baseURL, _ := account.OpenAIResponsesCredentials()
	setOpenAIResponsesContinuation(responseID, openAIResponsesContinuation{
		accountID: account.ID(),
		baseURL:   normalizeContinuationBaseURL(baseURL),
		history:   history,
		createdAt: time.Now(),
		size:      rawMessagesSize(history),
	})
}

func parentOpenAIResponsesHistory(requestBody []byte) ([]json.RawMessage, bool) {
	previousID := gjson.GetBytes(requestBody, "previous_response_id").String()
	if previousID == "" {
		return nil, true
	}
	entry, ok := getOpenAIResponsesContinuation(previousID)
	if !ok || len(entry.history) == 0 {
		return nil, false
	}
	return cloneRawMessages(entry.history), true
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
	valid := true
	input.ForEach(func(_, item gjson.Result) bool {
		cleaned, keep := replayableOpenAIResponsesItem(item)
		if !keep {
			return true
		}
		if cleaned == nil {
			valid = false
			return false
		}
		items = append(items, cleaned)
		return true
	})
	return items, valid
}

func openAIResponsesOutputItems(output gjson.Result) ([]json.RawMessage, bool) {
	if !output.IsArray() {
		return nil, false
	}
	items := make([]json.RawMessage, 0, len(output.Array()))
	valid := true
	output.ForEach(func(_, item gjson.Result) bool {
		cleaned, keep := replayableOpenAIResponsesItem(item)
		if !keep {
			return true
		}
		if cleaned == nil {
			valid = false
			return false
		}
		items = append(items, cleaned)
		return true
	})
	return items, valid
}

func replayableOpenAIResponsesItem(item gjson.Result) (json.RawMessage, bool) {
	if item.Get("type").String() == "reasoning" && item.Get("encrypted_content").Exists() {
		return nil, false
	}
	cleaned, ok := stripResponseItemID(json.RawMessage(item.Raw))
	if !ok {
		return nil, true
	}
	return cleaned, true
}

func getOpenAIResponsesContinuation(responseID string) (openAIResponsesContinuation, bool) {
	if responseID == "" {
		return openAIResponsesContinuation{}, false
	}
	now := time.Now()
	openAIResponsesContinuity.mu.Lock()
	defer openAIResponsesContinuity.mu.Unlock()
	purgeExpiredOpenAIResponsesContinuityLocked(now)
	entry, ok := openAIResponsesContinuity.entries[responseID]
	if !ok {
		return openAIResponsesContinuation{}, false
	}
	entry.history = cloneRawMessages(entry.history)
	return entry, true
}

func setOpenAIResponsesContinuation(responseID string, entry openAIResponsesContinuation) {
	entry.history = cloneRawMessages(entry.history)
	openAIResponsesContinuity.mu.Lock()
	defer openAIResponsesContinuity.mu.Unlock()
	purgeExpiredOpenAIResponsesContinuityLocked(entry.createdAt)
	if previous, ok := openAIResponsesContinuity.entries[responseID]; ok {
		openAIResponsesContinuity.totalBytes -= previous.size
		delete(openAIResponsesContinuity.entries, responseID)
	}
	for len(openAIResponsesContinuity.entries) >= openAIResponsesContinuityMaxEntries ||
		openAIResponsesContinuity.totalBytes+entry.size > openAIResponsesContinuityMaxBytes {
		if !evictOldestOpenAIResponsesContinuationLocked() {
			break
		}
	}
	openAIResponsesContinuity.entries[responseID] = entry
	openAIResponsesContinuity.totalBytes += entry.size
}

func purgeExpiredOpenAIResponsesContinuityLocked(now time.Time) {
	for responseID, entry := range openAIResponsesContinuity.entries {
		if now.Sub(entry.createdAt) <= openAIResponsesContinuityTTL {
			continue
		}
		openAIResponsesContinuity.totalBytes -= entry.size
		delete(openAIResponsesContinuity.entries, responseID)
	}
}

func evictOldestOpenAIResponsesContinuationLocked() bool {
	oldestID := ""
	var oldest time.Time
	for responseID, entry := range openAIResponsesContinuity.entries {
		if oldestID == "" || entry.createdAt.Before(oldest) {
			oldestID = responseID
			oldest = entry.createdAt
		}
	}
	if oldestID == "" {
		return false
	}
	openAIResponsesContinuity.totalBytes -= openAIResponsesContinuity.entries[oldestID].size
	delete(openAIResponsesContinuity.entries, oldestID)
	return true
}

func cloneRawMessages(items []json.RawMessage) []json.RawMessage {
	cloned := make([]json.RawMessage, len(items))
	for i, item := range items {
		cloned[i] = append(json.RawMessage(nil), item...)
	}
	return cloned
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
	openAIResponsesContinuity.mu.Lock()
	openAIResponsesContinuity.entries = make(map[string]openAIResponsesContinuation)
	openAIResponsesContinuity.totalBytes = 0
	openAIResponsesContinuity.mu.Unlock()
}
