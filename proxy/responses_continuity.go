package proxy

import (
	"encoding/json"
	"net/http"
	"os"
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
	input      []json.RawMessage
	output     []json.RawMessage
	replayable bool
	createdAt  time.Time
	accessedAt time.Time
	size       int
}

type openAIResponsesContinuityStats struct {
	Entries   int
	Bytes     int
	Evictions uint64
}

type openAIResponsesContinuityRegistry struct {
	mu         sync.Mutex
	entries    map[string]openAIResponsesContinuation
	totalBytes int
	evictions  uint64
	limits     openAIResponsesContinuityLimits
	now        func() time.Time
}

var openAIResponsesContinuity = newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimitsFromEnv(os.Getenv))

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
		entries: make(map[string]openAIResponsesContinuation),
		limits:  limits,
		now:     time.Now,
	}
}

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
	history, ok := openAIResponsesContinuity.materialize(previousID)
	if !ok || len(history) == 0 {
		return body, false
	}
	current, ok := openAIResponsesInputItems(body)
	if !ok || len(current) == 0 {
		return body, false
	}
	history = append(history, current...)
	if len(history) > openAIResponsesContinuity.limits.maxItems || rawMessagesSize(history) > openAIResponsesContinuity.limits.maxItemBytes {
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

func canBuildOpenAIResponsesContinuationFallback(body []byte) bool {
	previousID := gjson.GetBytes(body, "previous_response_id").String()
	if !openAIResponsesContinuity.isReplayable(previousID) {
		return false
	}
	current, ok := openAIResponsesInputItems(body)
	return ok && len(current) > 0
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
	input, inputOK := openAIResponsesInputItems(requestBody)
	output, outputOK := openAIResponsesOutputItems(response.Get("output"))
	if !inputOK || !outputOK {
		input = nil
		output = nil
	}
	baseURL, _ := account.OpenAIResponsesCredentials()
	openAIResponsesContinuity.store(responseID, gjson.GetBytes(requestBody, "previous_response_id").String(), openAIResponsesContinuation{
		accountID: account.ID(),
		baseURL:   normalizeContinuationBaseURL(baseURL),
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
	return openAIResponsesContinuity.get(responseID)
}

func setOpenAIResponsesContinuation(responseID string, entry openAIResponsesContinuation) {
	openAIResponsesContinuity.store(responseID, entry.parentID, entry)
}

func (registry *openAIResponsesContinuityRegistry) get(responseID string) (openAIResponsesContinuation, bool) {
	if responseID == "" {
		return openAIResponsesContinuation{}, false
	}
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

func (registry *openAIResponsesContinuityRegistry) store(responseID, parentID string, entry openAIResponsesContinuation) {
	if responseID == "" {
		return
	}
	entry.input = cloneRawMessages(entry.input)
	entry.output = cloneRawMessages(entry.output)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	now := registry.now()
	registry.purgeExpiredLocked(now)
	registry.removeSubtreeLocked(responseID, false)
	entry.createdAt = now
	entry.accessedAt = now
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
	registry.entries[responseID] = entry
	registry.totalBytes += entry.size
	registry.enforceLimitsLocked()
}

func (registry *openAIResponsesContinuityRegistry) canStoreReplayableLocked(parentID string, input, output []json.RawMessage) bool {
	if len(input) == 0 {
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
		Entries:   len(registry.entries),
		Bytes:     registry.totalBytes,
		Evictions: registry.evictions,
	}
}

func (registry *openAIResponsesContinuityRegistry) reset() {
	registry.mu.Lock()
	registry.entries = make(map[string]openAIResponsesContinuation)
	registry.totalBytes = 0
	registry.evictions = 0
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
	openAIResponsesContinuity.reset()
}
