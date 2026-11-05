package proxy

import (
	"encoding/json"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/codex2api/database"
)

const (
	ClientCompatModePreserve = "preserve"
	ClientCompatModeAuto     = "auto"
	ClientCompatModeForce    = "force"

	StreamFlushPolicyImmediate = "immediate"
	StreamFlushPolicyCoalesce  = "coalesce"

	defaultClientCompatMode      = ClientCompatModePreserve
	defaultCodexMinCLIVersion    = "0.118.0"
	defaultStreamFlushPolicy     = StreamFlushPolicyImmediate
	defaultStreamFlushIntervalMS = 20
	minStreamFlushIntervalMS     = 1
	maxStreamFlushIntervalMS     = 1000
	defaultFirstTokenTimeoutSec  = 30
	maxFirstTokenTimeoutSec      = 600
)

type RuntimeSettings struct {
	ClientCompatMode      string
	CodexMinCLIVersion    string
	StreamFlushPolicy     string
	StreamFlushIntervalMS int
	FirstTokenTimeoutSec  int
}

// UpstreamCooldownRule 定义特定上游域名的 429 冷却规则
type UpstreamCooldownRule struct {
	DomainContains string `json:"domain_contains"` // 域名包含匹配，如 "agnes-ai.cn"
	CooldownSeconds int    `json:"cooldown_seconds"` // 冷却秒数
}

// 默认上游冷却规则（Agnes AI 纯 RPM 限流，冷却 2 分钟）
var defaultUpstreamCooldownRules = []UpstreamCooldownRule{
	{DomainContains: "agnes-ai.cn", CooldownSeconds: 120},
	{DomainContains: "agnes-ai.com", CooldownSeconds: 120},
}

var upstreamCooldownRules atomic.Value // stores []UpstreamCooldownRule
var runtimeSettings atomic.Value // stores RuntimeSettings

func init() {
	runtimeSettings.Store(DefaultRuntimeSettings())
	upstreamCooldownRules.Store(defaultUpstreamCooldownRules)
}

func DefaultRuntimeSettings() RuntimeSettings {
	return RuntimeSettings{
		ClientCompatMode:      defaultClientCompatMode,
		CodexMinCLIVersion:    defaultCodexMinCLIVersion,
		StreamFlushPolicy:     defaultStreamFlushPolicy,
		StreamFlushIntervalMS: defaultStreamFlushIntervalMS,
		FirstTokenTimeoutSec:  defaultFirstTokenTimeoutSec,
	}
}

func NormalizeClientCompatMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ClientCompatModePreserve:
		return ClientCompatModePreserve
	case ClientCompatModeAuto:
		return ClientCompatModeAuto
	case ClientCompatModeForce:
		return ClientCompatModeForce
	default:
		return ClientCompatModePreserve
	}
}

func NormalizeStreamFlushPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", StreamFlushPolicyImmediate:
		return StreamFlushPolicyImmediate
	case StreamFlushPolicyCoalesce:
		return StreamFlushPolicyCoalesce
	default:
		return StreamFlushPolicyImmediate
	}
}

func NormalizeRuntimeSettings(settings RuntimeSettings) RuntimeSettings {
	defaults := DefaultRuntimeSettings()
	settings.ClientCompatMode = NormalizeClientCompatMode(settings.ClientCompatMode)
	settings.StreamFlushPolicy = NormalizeStreamFlushPolicy(settings.StreamFlushPolicy)
	if strings.TrimSpace(settings.CodexMinCLIVersion) == "" {
		settings.CodexMinCLIVersion = defaults.CodexMinCLIVersion
	} else {
		settings.CodexMinCLIVersion = strings.TrimSpace(settings.CodexMinCLIVersion)
	}
	if settings.StreamFlushIntervalMS < minStreamFlushIntervalMS {
		settings.StreamFlushIntervalMS = defaults.StreamFlushIntervalMS
	}
	if settings.StreamFlushIntervalMS > maxStreamFlushIntervalMS {
		settings.StreamFlushIntervalMS = maxStreamFlushIntervalMS
	}
	if settings.FirstTokenTimeoutSec < 0 {
		settings.FirstTokenTimeoutSec = defaultFirstTokenTimeoutSec
	}
	if settings.FirstTokenTimeoutSec > maxFirstTokenTimeoutSec {
		settings.FirstTokenTimeoutSec = maxFirstTokenTimeoutSec
	}
	return settings
}

func ApplyRuntimeSettingsFromSystem(settings *database.SystemSettings) RuntimeSettings {
	next := DefaultRuntimeSettings()
	if settings != nil {
		next.ClientCompatMode = settings.ClientCompatMode
		next.CodexMinCLIVersion = settings.CodexMinCLIVersion
		next.StreamFlushPolicy = settings.StreamFlushPolicy
		next.StreamFlushIntervalMS = settings.StreamFlushIntervalMS
		next.FirstTokenTimeoutSec = settings.FirstTokenTimeoutSeconds
		if strings.TrimSpace(settings.UpstreamCooldownRules) != "" {
			if err := SetUpstreamCooldownRules(settings.UpstreamCooldownRules); err != nil {
				log.Printf("恢复上游冷却规则失败: %v", err)
			}
		}
		if strings.TrimSpace(settings.ExposedModels) != "" {
			if err := SetExposedModels(settings.ExposedModels); err != nil {
				log.Printf("恢复对外模型白名单失败: %v", err)
			}
		}
	}
	next = NormalizeRuntimeSettings(next)
	runtimeSettings.Store(next)
	return next
}

func ApplyRuntimeSettings(settings RuntimeSettings) RuntimeSettings {
	settings = NormalizeRuntimeSettings(settings)
	runtimeSettings.Store(settings)
	return settings
}

func CurrentRuntimeSettings() RuntimeSettings {
	if v, ok := runtimeSettings.Load().(RuntimeSettings); ok {
		return NormalizeRuntimeSettings(v)
	}
	return DefaultRuntimeSettings()
}

func currentStreamFlushInterval() time.Duration {
	ms := CurrentRuntimeSettings().StreamFlushIntervalMS
	if ms < minStreamFlushIntervalMS {
		ms = defaultStreamFlushIntervalMS
	}
	return time.Duration(ms) * time.Millisecond
}

func currentFirstTokenTimeout() time.Duration {
	seconds := CurrentRuntimeSettings().FirstTokenTimeoutSec
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// CurrentUpstreamCooldownRules 获取当前上游冷却规则
func CurrentUpstreamCooldownRules() []UpstreamCooldownRule {
	if v, ok := upstreamCooldownRules.Load().([]UpstreamCooldownRule); ok {
		return v
	}
	return defaultUpstreamCooldownRules
}

// SetUpstreamCooldownRules 从 JSON 字符串设置上游冷却规则，返回解析错误
func SetUpstreamCooldownRules(jsonStr string) error {
	if strings.TrimSpace(jsonStr) == "" {
		upstreamCooldownRules.Store(defaultUpstreamCooldownRules)
		return nil
	}
	var rules []UpstreamCooldownRule
	if err := json.Unmarshal([]byte(jsonStr), &rules); err != nil {
		return err
	}
	upstreamCooldownRules.Store(rules)
	return nil
}

// MatchUpstreamCooldown 根据 baseURL 匹配冷却规则，返回冷却秒数（0 表示不匹配）
func MatchUpstreamCooldown(baseURL string) int {
	if strings.TrimSpace(baseURL) == "" {
		return 0
	}
	lowerURL := strings.ToLower(baseURL)
	for _, rule := range CurrentUpstreamCooldownRules() {
		if rule.DomainContains == "" {
			continue
		}
		if strings.Contains(lowerURL, strings.ToLower(rule.DomainContains)) {
			return rule.CooldownSeconds
		}
	}
	return 0
}

// defaultExposedModels 是首次启动时的默认白名单（init 会装载它）。
// 语义：空配置 = 不启用过滤（输出全量）；显式配置 = 只输出白名单。
// 首次启动装载默认值，是为了收敛到用户预期的 6 个核心模型。
var defaultExposedModels = []string{
	"gpt-6-astra",
	"gpt-6-luna",
	"gpt-6-sol",
	"gpt-5.6-sol",
	"agnes-2.5-flash",
	"agnes-3.0-flash",
}

var exposedModels atomic.Value // stores []string

func init() {
	exposedModels.Store(defaultExposedModels)
}

// CurrentExposedModels 返回当前对外暴露的模型白名单。
// 返回空切片表示不启用过滤（/v1/models 输出全量模型）。
func CurrentExposedModels() []string {
	if v, ok := exposedModels.Load().([]string); ok {
		return v
	}
	return defaultExposedModels
}

// SetExposedModels 从 JSON 数组或换行/逗号分隔文本设置白名单。
//
// 语义约定：空配置 = 不启用过滤，/v1/models 返回全量模型（保持历史行为）。
// 只有显式配置了模型名才会收敛列表，避免用户无法关闭白名单。
func SetExposedModels(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		exposedModels.Store([]string{})
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var list []string
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			return err
		}
		exposedModels.Store(normalizeExposedModels(list))
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ' ', '\t', '\n', '\r':
			return true
		}
		return false
	})
	if len(fields) == 0 {
		exposedModels.Store([]string{})
		return nil
	}
	list := make([]string, 0, len(fields))
	for _, f := range fields {
		list = append(list, string(f))
	}
	fields = nil
	exposedModels.Store(normalizeExposedModels(list))
	return nil
}

// MarshalExposedModels 把当前白名单序列化为 JSON 数组字符串
func MarshalExposedModels() string {
	data, err := json.Marshal(CurrentExposedModels())
	if err != nil {
		return "[]"
	}
	return string(data)
}

func normalizeExposedModels(list []string) []string {
	out := make([]string, 0, len(list))
	seen := make(map[string]struct{}, len(list))
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}
