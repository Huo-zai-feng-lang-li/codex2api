package proxy

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/database"
)

const (
	OfficialCodexModelsURL = "https://developers.openai.com/codex/models"

	ModelCategoryCodex = "codex"
	ModelCategoryImage = "image"

	ModelSourceBuiltin           = "builtin"
	ModelSourceOfficialCodexDocs = "official_codex_docs"
)

// ModelInfo describes one model exposed by this proxy.
type ModelInfo struct {
	ID                  string     `json:"id"`
	Enabled             bool       `json:"enabled"`
	Category            string     `json:"category"`
	Source              string     `json:"source"`
	ProOnly             bool       `json:"pro_only"`
	APIKeyAuthAvailable bool       `json:"api_key_auth_available"`
	LastSeenAt          *time.Time `json:"last_seen_at,omitempty"`
	UpdatedAt           *time.Time `json:"updated_at,omitempty"`
}

// ModelCatalog is the admin-facing model list plus registry metadata.
type ModelCatalog struct {
	Models       []string    `json:"models"`
	Items        []ModelInfo `json:"items"`
	LastSyncedAt *time.Time  `json:"last_synced_at,omitempty"`
	SourceURL    string      `json:"source_url"`
	Warning      string      `json:"warning,omitempty"`
}

// ModelSyncResult is returned after a manual upstream sync.
type ModelSyncResult struct {
	Added        int         `json:"added"`
	Updated      int         `json:"updated"`
	Unchanged    int         `json:"unchanged"`
	Skipped      []string    `json:"skipped"`
	Models       []string    `json:"models"`
	Items        []ModelInfo `json:"items"`
	LastSyncedAt time.Time   `json:"last_synced_at"`
	SourceURL    string      `json:"source_url"`
}

var builtinModelInfos = []ModelInfo{
	modelInfoForID("gpt-5.6-sol", ModelSourceBuiltin),
	modelInfoForID("gpt-5.6-terra", ModelSourceBuiltin),
	modelInfoForID("gpt-5.6-luna", ModelSourceBuiltin),
	modelInfoForID("gpt-5.6", ModelSourceBuiltin),
	modelInfoForID("gpt-5.5", ModelSourceBuiltin),
	modelInfoForID("gpt-5.4", ModelSourceBuiltin),
	modelInfoForID("gpt-5.4-mini", ModelSourceBuiltin),
	modelInfoForID("gpt-5.3-codex", ModelSourceBuiltin),
	modelInfoForID("gpt-5.3-codex-spark", ModelSourceBuiltin),
	modelInfoForID("gpt-5.2", ModelSourceBuiltin),
	// codex-auto-review — Codex internal auto-review model.
	// Upstream confirms: returns effective model "gpt-5.4" (tested 2026-05-20).
	// Available on Plus/Pro/Team/Business per official catalog; excludes free.
	// Pricing: gpt-5.4 standard ($2.50/$15.00), priority ($5.00/$30.00).
	// Ref: codex_client_models.json via CLIProxyAPI model registry.
	modelInfoForID("codex-auto-review", ModelSourceBuiltin),
	modelInfoForID("gpt-image-2", ModelSourceBuiltin),
	modelInfoForID("gpt-image-2-2k", ModelSourceBuiltin),
	modelInfoForID("gpt-image-2-4k", ModelSourceBuiltin),
}

// SupportedModels is the static built-in fallback list. Runtime handlers use
// SupportedModelIDs so synced registry entries can take effect immediately.
var SupportedModels = BuiltinModelIDs()

func BuiltinModelIDs() []string {
	ids := make([]string, 0, len(builtinModelInfos))
	for _, model := range builtinModelInfos {
		ids = append(ids, model.ID)
	}
	return ids
}

func modelInfoForID(id string, source string) ModelInfo {
	id = strings.TrimSpace(id)
	if source == "" {
		source = ModelSourceBuiltin
	}
	info := ModelInfo{
		ID:                  id,
		Enabled:             true,
		Category:            ModelCategoryCodex,
		Source:              source,
		APIKeyAuthAvailable: true,
	}
	switch strings.ToLower(id) {
	case "gpt-5.3-codex-spark":
		info.ProOnly = true
	case "gpt-5.5":
		info.APIKeyAuthAvailable = false
	case "gpt-image-2":
		info.Category = ModelCategoryImage
	}
	if strings.Contains(strings.ToLower(id), "image") {
		info.Category = ModelCategoryImage
	}
	return info
}

func modelInfoFromRow(row database.ModelRegistryRow) ModelInfo {
	var lastSeenAt *time.Time
	if row.LastSeenAt.Valid {
		t := row.LastSeenAt.Time.UTC()
		lastSeenAt = &t
	}
	var updatedAt *time.Time
	if !row.UpdatedAt.IsZero() {
		t := row.UpdatedAt.UTC()
		updatedAt = &t
	}
	return ModelInfo{
		ID:                  row.ID,
		Enabled:             row.Enabled,
		Category:            valueOrDefault(row.Category, ModelCategoryCodex),
		Source:              valueOrDefault(row.Source, "manual"),
		ProOnly:             row.ProOnly,
		APIKeyAuthAvailable: row.APIKeyAuthAvailable,
		LastSeenAt:          lastSeenAt,
		UpdatedAt:           updatedAt,
	}
}

func modelInfoToRow(info ModelInfo, lastSeenAt time.Time) database.ModelRegistryRow {
	return database.ModelRegistryRow{
		ID:                  info.ID,
		Enabled:             info.Enabled,
		Category:            valueOrDefault(info.Category, ModelCategoryCodex),
		Source:              valueOrDefault(info.Source, "manual"),
		ProOnly:             info.ProOnly,
		APIKeyAuthAvailable: info.APIKeyAuthAvailable,
		LastSeenAt:          sql.NullTime{Time: lastSeenAt.UTC(), Valid: !lastSeenAt.IsZero()},
	}
}

func valueOrDefault(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

// ListModelCatalog returns enabled model IDs plus metadata. It falls back to
// built-ins if the registry cannot be read.
func ListModelCatalog(ctx context.Context, db *database.DB) (ModelCatalog, error) {
	catalog := builtinCatalog()
	if db == nil {
		return catalog, nil
	}

	rows, err := db.ListModelRegistry(ctx)
	if err != nil {
		catalog.Warning = err.Error()
		return catalog, err
	}

	merged := mergeModelInfos(rows)
	catalog.Items = merged
	catalog.Models = enabledModelIDs(merged, false)
	if len(catalog.Models) == 0 {
		catalog.Models = BuiltinModelIDs()
	}

	state, err := db.GetModelRegistrySyncState(ctx)
	if err != nil {
		catalog.Warning = err.Error()
		return catalog, err
	}
	if state != nil {
		catalog.SourceURL = valueOrDefault(state.SourceURL, OfficialCodexModelsURL)
		if state.LastSyncedAt.Valid {
			t := state.LastSyncedAt.Time.UTC()
			catalog.LastSyncedAt = &t
		}
	}
	return catalog, nil
}

func builtinCatalog() ModelCatalog {
	items := append([]ModelInfo(nil), builtinModelInfos...)
	return ModelCatalog{
		Models:    enabledModelIDs(items, false),
		Items:     items,
		SourceURL: OfficialCodexModelsURL,
	}
}

func mergeModelInfos(rows []database.ModelRegistryRow) []ModelInfo {
	byID := make(map[string]ModelInfo, len(builtinModelInfos)+len(rows))
	for _, info := range builtinModelInfos {
		byID[info.ID] = info
	}
	for _, row := range rows {
		info := modelInfoFromRow(row)
		if info.ID == "" {
			continue
		}
		byID[info.ID] = info
	}

	result := make([]ModelInfo, 0, len(byID))
	for _, info := range builtinModelInfos {
		if merged, ok := byID[info.ID]; ok {
			result = append(result, merged)
			delete(byID, info.ID)
		}
	}
	extras := make([]ModelInfo, 0, len(byID))
	for _, info := range byID {
		extras = append(extras, info)
	}
	sort.Slice(extras, func(i, j int) bool {
		return extras[i].ID < extras[j].ID
	})
	result = append(result, extras...)
	return result
}

// SupportedModelIDs returns enabled runtime model IDs.
// supportedModelIDsCache 缓存 SupportedModelIDs 的结果。
// 原实现每次调用都触发 ListModelCatalog 的两次 SQL 查询
// （ListModelRegistry + GetModelRegistrySyncState），而该函数在每次
// ChatCompletions / Messages / Responses 请求中都会被调用一次，
// 高并发下会产生无意义的 SQLite 锁竞争和堆内存逃逸。
// 模型注册表通过 SyncOfficialCodexModels 低频更新，TTL 取 15 秒兼顾实时性与性能；
// 同步成功后调用 InvalidateSupportedModelIDsCache 主动失效，保证切换模型后立即生效。
var supportedModelIDsCache struct {
	mu       sync.RWMutex
	models   []string
	expireAt time.Time
}

const supportedModelIDsCacheTTL = 15 * time.Second

// InvalidateSupportedModelIDsCache 主动失效 SupportedModelIDs 缓存。
// 任何写入 model_registry 的操作（同步、启用/禁用）后必须调用。
func InvalidateSupportedModelIDsCache() {
	supportedModelIDsCache.mu.Lock()
	supportedModelIDsCache.models = nil
	supportedModelIDsCache.expireAt = time.Time{}
	supportedModelIDsCache.mu.Unlock()
}

// SupportedModelIDs 返回注册表中启用的模型 ID 列表（不含账号实际支持的模型）。
// 结果带 15s TTL 缓存，避免热路径重复查库。返回的切片对所有调用方只读。
func SupportedModelIDs(ctx context.Context, db *database.DB) []string {
	supportedModelIDsCache.mu.RLock()
	if supportedModelIDsCache.models != nil && time.Now().Before(supportedModelIDsCache.expireAt) {
		cached := supportedModelIDsCache.models
		supportedModelIDsCache.mu.RUnlock()
		return cached
	}
	supportedModelIDsCache.mu.RUnlock()

	catalog, _ := ListModelCatalog(ctx, db)
	models := catalog.Models

	supportedModelIDsCache.mu.Lock()
	supportedModelIDsCache.models = models
	supportedModelIDsCache.expireAt = time.Now().Add(supportedModelIDsCacheTTL)
	supportedModelIDsCache.mu.Unlock()
	return models
}

// TextTestModelIDs returns enabled non-image models for account connection tests.
func TextTestModelIDs(ctx context.Context, db *database.DB) []string {
	catalog, _ := ListModelCatalog(ctx, db)
	return enabledModelIDs(catalog.Items, true)
}

func IsTextTestModelID(ctx context.Context, db *database.DB, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, id := range TextTestModelIDs(ctx, db) {
		if model == id {
			return true
		}
	}
	return false
}

func enabledModelIDs(items []ModelInfo, textOnly bool) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		if textOnly && isImageModelInfo(item) {
			continue
		}
		ids = append(ids, item.ID)
	}
	return ids
}

func isImageModelInfo(info ModelInfo) bool {
	return strings.EqualFold(info.Category, ModelCategoryImage) || strings.Contains(strings.ToLower(info.ID), "image")
}

var codexModelIDPattern = regexp.MustCompile(`\bgpt-[0-9]+(?:\.[0-9]+)*(?:-[a-z][a-z0-9]*(?:-[a-z0-9]+)*)?\b`)

// ParseOfficialCodexModelIDs extracts allowed Codex model IDs from the official docs HTML.
func ParseOfficialCodexModelIDs(html string) (models []string, skipped []string) {
	seen := map[string]struct{}{}
	skippedSeen := map[string]struct{}{}
	for _, match := range codexModelIDPattern.FindAllString(strings.ToLower(html), -1) {
		if isAllowedUpstreamCodexModel(match) {
			if _, ok := seen[match]; !ok {
				seen[match] = struct{}{}
				models = append(models, match)
			}
			continue
		}
		if _, ok := skippedSeen[match]; !ok {
			skippedSeen[match] = struct{}{}
			skipped = append(skipped, match)
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		return modelSortRank(models[i]) < modelSortRank(models[j])
	})
	sort.Strings(skipped)
	return models, skipped
}

func modelSortRank(id string) int {
	for index, info := range builtinModelInfos {
		if info.ID == id {
			return index
		}
	}
	return len(builtinModelInfos) + 1000
}

func isAllowedUpstreamCodexModel(id string) bool {
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" || id == "gpt-5.2-codex" || strings.Contains(id, "image") {
		return false
	}
	if !strings.HasPrefix(id, "gpt-") {
		return false
	}
	version := strings.TrimPrefix(id, "gpt-")
	if dash := strings.IndexByte(version, '-'); dash >= 0 {
		version = version[:dash]
	}
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	if major > 5 {
		return true
	}
	return major == 5 && minor >= 2
}

// SyncOfficialCodexModels fetches the fixed official docs page and merges discovered models.
func SyncOfficialCodexModels(ctx context.Context, db *database.DB) (*ModelSyncResult, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库不可用，无法同步模型注册表")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, OfficialCodexModelsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("官方模型页面暂时不可访问，已保留本地模型列表: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("官方模型页面返回 %d，已保留本地模型列表", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, err
	}
	return ApplyOfficialCodexModelSync(ctx, db, string(body), time.Now().UTC())
}

// ApplyOfficialCodexModelSync merges a fetched official docs page into the registry.
func ApplyOfficialCodexModelSync(ctx context.Context, db *database.DB, html string, syncedAt time.Time) (*ModelSyncResult, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库不可用，无法同步模型注册表")
	}
	ids, skipped := ParseOfficialCodexModelIDs(html)
	if len(ids) == 0 {
		return nil, fmt.Errorf("未从官方模型页面解析到可用模型，已保留本地模型列表")
	}

	existingRows, err := db.ListModelRegistry(ctx)
	if err != nil {
		return nil, err
	}
	existing := make(map[string]database.ModelRegistryRow, len(existingRows))
	for _, row := range existingRows {
		existing[row.ID] = row
	}

	rows := make([]database.ModelRegistryRow, 0, len(ids))
	result := &ModelSyncResult{
		Skipped:   skipped,
		SourceURL: OfficialCodexModelsURL,
	}
	for _, id := range ids {
		info := modelInfoForID(id, ModelSourceOfficialCodexDocs)
		row := modelInfoToRow(info, syncedAt)
		if previous, ok := existing[id]; ok {
			row.Enabled = previous.Enabled
			if modelRegistryMetadataEqual(previous, row) {
				result.Unchanged++
			} else {
				result.Updated++
			}
		} else {
			result.Added++
		}
		rows = append(rows, row)
	}

	if err := db.UpsertModelRegistryRows(ctx, rows); err != nil {
		return nil, err
	}
	if err := db.UpdateModelRegistrySyncState(ctx, OfficialCodexModelsURL, syncedAt); err != nil {
		return nil, err
	}
	// 注册表写入后主动失效 SupportedModelIDs 缓存，保证调度与校验立即看到新模型。
	InvalidateSupportedModelIDsCache()

	catalog, err := ListModelCatalog(ctx, db)
	if err != nil {
		return nil, err
	}
	result.Models = catalog.Models
	result.Items = catalog.Items
	result.LastSyncedAt = syncedAt.UTC()
	return result, nil
}

func modelRegistryMetadataEqual(a database.ModelRegistryRow, b database.ModelRegistryRow) bool {
	return a.Enabled == b.Enabled &&
		valueOrDefault(a.Category, ModelCategoryCodex) == valueOrDefault(b.Category, ModelCategoryCodex) &&
		valueOrDefault(a.Source, "manual") == valueOrDefault(b.Source, "manual") &&
		a.ProOnly == b.ProOnly &&
		a.APIKeyAuthAvailable == b.APIKeyAuthAvailable
}
