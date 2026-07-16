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

const openAIResponsesContinuityCleanupInterval = time.Minute

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
	Entries             int
	Bytes               int
	Evictions           uint64
	Persistent          bool
	PersistenceFailures uint64
}

type responsesContinuityPersistence interface {
	UpsertResponsesContinuation(context.Context, *database.ResponsesContinuationRow) error
	GetResponsesContinuation(context.Context, string) (database.ResponsesContinuationRow, bool, error)
	TouchResponsesContinuations(context.Context, []string, time.Time) error
	PruneResponsesContinuations(context.Context, time.Time) (int64, error)
	TrimResponsesContinuations(context.Context, int, int) (int64, error)
}

type openAIResponsesContinuityRegistry struct {
	mu          sync.Mutex
	entries     map[string]openAIResponsesContinuation
	totalBytes  int
	evictions   uint64
	limits      openAIResponsesContinuityLimits
	now         func() time.Time
	persistence responsesContinuityPersistence
	lastCleanup time.Time
	persistFail uint64
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
		entries: make(map[string]openAIResponsesContinuation),
		limits:  limits,
		now:     time.Now,
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
			accountID: row.AccountID, baseURL: row.BaseURL,
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

func shouldReplayOpenAIResponsesContinuationBeforeUpstream(body []byte, account *auth.Account) bool {
	if openAIResponsesContinuityMode == openAIResponsesContinuityModeUpstream || account == nil {
		return false
	}
	if account.IsOpenAIResponsesAPI() {
		baseURL, _ := account.OpenAIResponsesCredentials()
		if isOfficialOpenAIResponsesBaseURL(baseURL) {
			return false
		}
	}
	return canBuildOpenAIResponsesContinuationFallback(body)
}

func isOfficialOpenAIResponsesBaseURL(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	return err == nil && strings.EqualFold(parsed.Hostname(), "api.openai.com")
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

func (registry *openAIResponsesContinuityRegistry) store(responseID, parentID string, entry openAIResponsesContinuation) {
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
		ResponseID: responseID, ParentID: entry.parentID,
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
		if _, err := db.PruneResponsesContinuations(ctx, entry.accessedAt.Add(-registry.limits.ttl)); err != nil {
			registry.recordPersistenceFailure()
			log.Printf("Responses 续链过期数据清理失败: %v", err)
		}
		if _, err := db.TrimResponsesContinuations(ctx, registry.limits.maxEntries, registry.limits.maxBytes); err != nil {
			registry.recordPersistenceFailure()
			log.Printf("Responses 续链磁盘容量清理失败: %v", err)
		}
	}
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
	openAIResponsesContinuity.reset()
	openAIResponsesContinuityMode = openAIResponsesContinuityModeUpstream
}
