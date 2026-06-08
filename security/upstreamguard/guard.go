package upstreamguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	actionAllow = "allow"
	actionWarn  = "warn"
	actionBlock = "block"
)

var (
	tokenPattern   = regexp.MustCompile(`(?i)\b(?:sk-(?:proj-)?[A-Za-z0-9_-]{32,}|ghp_[A-Za-z0-9_]{30,}|xoxb-[A-Za-z0-9-]{20,}|AKIA[0-9A-Z]{16})\b`)
	dbURLPattern   = regexp.MustCompile(`(?i)\b(?:postgres|postgresql|mysql|mongodb|redis)://[^:\s/@]+:[^@\s]+@[^)\s'"<>]+`)
	envLinePattern = regexp.MustCompile(`(?m)^[A-Z][A-Z0-9_]{2,64}\s*=\s*\S+`)
)

func InspectRequest(body []byte, ctx ScanContext, cfg Config) Verdict {
	cfg = normalizeConfig(cfg)
	verdict := baseVerdict(DirectionRequest, body, ctx, cfg)
	if !cfg.Enabled || !cfg.RequestDLPEnabled || strings.TrimSpace(verdict.Preview) == "" {
		return verdict
	}

	text := scanText(body, cfg)
	applyFalsePositiveHints(&verdict, text)
	matchRequestRules(&verdict, text)
	applySuppressions(&verdict, cfg, ctx)
	finalizeVerdict(&verdict, cfg)
	return verdict
}

func InspectResponse(body []byte, ctx ScanContext, cfg Config) Verdict {
	cfg = normalizeConfig(cfg)
	verdict := baseVerdict(DirectionResponse, body, ctx, cfg)
	if !cfg.Enabled || !cfg.ResponseFirewall || strings.TrimSpace(verdict.Preview) == "" {
		return verdict
	}

	text := scanText(body, cfg)
	applyFalsePositiveHints(&verdict, text)
	matchResponseRules(&verdict, text, cfg)
	applySuppressions(&verdict, cfg, ctx)
	finalizeVerdict(&verdict, cfg)
	return verdict
}

func InspectSource(baseURL string, cfg Config) Verdict {
	cfg = normalizeConfig(cfg)
	verdict := baseVerdict(DirectionSource, []byte(baseURL), ScanContext{BaseURL: baseURL}, cfg)
	if !cfg.Enabled {
		return verdict
	}

	source, level, score, rule, reason := classifySource(baseURL, cfg)
	verdict.Source = source
	addRule(&verdict, rule, reason, score, score)
	verdict.RiskLevel = level
	verdict.Reason = reason
	finalizeVerdict(&verdict, cfg)
	return verdict
}

func ContentHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func NormalizeConfig(cfg Config) Config {
	return normalizeConfig(cfg)
}

func ParseSuppressions(raw string) ([]SuppressionRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rules []SuppressionRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, err
	}
	result := make([]SuppressionRule, 0, len(rules))
	for idx, rule := range rules {
		rule.RuleID = strings.TrimSpace(rule.RuleID)
		rule.Endpoint = strings.TrimSpace(rule.Endpoint)
		rule.BaseURL = strings.TrimSpace(rule.BaseURL)
		rule.Action = strings.TrimSpace(rule.Action)
		if rule.Action == "" {
			rule.Action = SuppressDowngrade
		}
		if rule.RuleID == "" {
			return nil, fmt.Errorf("suppression %d missing rule_id", idx+1)
		}
		if rule.Action != SuppressDowngrade {
			return nil, fmt.Errorf("suppression %d action %q is not supported", idx+1, rule.Action)
		}
		result = append(result, rule)
	}
	return result, nil
}

func normalizeConfig(cfg Config) Config {
	defaults := DefaultConfig()
	if strings.TrimSpace(cfg.Mode) == "" {
		cfg.Mode = defaults.Mode
	}
	switch cfg.Mode {
	case ModeOff, ModeWarn, ModeHighBlock, ModeStrict:
	default:
		cfg.Mode = ModeWarn
	}
	if cfg.MaxScanBytes <= 0 {
		cfg.MaxScanBytes = defaults.MaxScanBytes
	}
	if cfg.MaxPreviewChars <= 0 {
		cfg.MaxPreviewChars = defaults.MaxPreviewChars
	}
	if len(cfg.OfficialHostSuffix) == 0 {
		cfg.OfficialHostSuffix = defaults.OfficialHostSuffix
	}
	if cfg.ScanTimeout <= 0 {
		cfg.ScanTimeout = defaults.ScanTimeout
	}
	return cfg
}

func baseVerdict(direction string, body []byte, ctx ScanContext, cfg Config) Verdict {
	source := ctx.Source
	if source == "" {
		source = SourceUnknown
	}
	return Verdict{
		Enabled:    cfg.Enabled,
		Direction:  direction,
		Action:     actionAllow,
		RiskLevel:  RiskNone,
		Preview:    sanitizePreview(string(body), cfg.MaxPreviewChars),
		Source:     source,
		Confidence: 0,
	}
}

func scanText(body []byte, cfg Config) string {
	if len(body) > cfg.MaxScanBytes {
		body = body[:cfg.MaxScanBytes]
	}
	return string(body)
}

func matchRequestRules(verdict *Verdict, text string) {
	if strings.Contains(text, "-----BEGIN") && strings.Contains(text, "PRIVATE KEY-----") {
		addRule(verdict, RuleDLPPrivateKey, "request contains a private key block", 100, 95)
	}
	if m := tokenPattern.FindString(text); m != "" && !looksLikeExample(text, m) {
		addRule(verdict, RuleDLPToken, "request contains a real-looking access token", 90, 90)
	}
	if m := dbURLPattern.FindString(text); m != "" && !looksLikeExample(text, m) {
		addRule(verdict, RuleDLPDatabaseURL, "request contains a database URL with credentials", 85, 88)
	}
	if envLineCount(text) >= 3 && !hasHint(verdict.FalsePositiveHints, HintDocumentation) {
		addRule(verdict, RuleDLPEnvBulk, "request contains multiple environment-style secrets", 80, 82)
	}
}

func matchResponseRules(verdict *Verdict, text string, cfg Config) {
	lower := strings.ToLower(text)
	if cfg.ToolCallWarning && hasToolCall(lower) {
		verdict.ToolCall = true
		addRule(verdict, RuleToolCall, "upstream response contains a tool call request", 45, 80)
	}
	if hasInjectionIntent(lower) && !hasHint(verdict.FalsePositiveHints, HintSecurityAnalysis) {
		addRule(verdict, RuleResponseInjection, "response combines unsafe instruction, sensitive target, and concealment/bypass intent", 92, 88)
	}
	if hasUnknownResponseField(lower) {
		addRule(verdict, RuleUnknownField, "response contains non-standard top-level fields", 25, 60)
	}
}

func addRule(verdict *Verdict, ruleID, reason string, score, confidence int) {
	if !hasHint(verdict.RuleIDs, ruleID) {
		verdict.RuleIDs = append(verdict.RuleIDs, ruleID)
	}
	verdict.Evidence = append(verdict.Evidence, Evidence{RuleID: ruleID, Snippet: reason})
	if score > verdict.RiskScore {
		verdict.RiskScore = score
	}
	if confidence > verdict.Confidence {
		verdict.Confidence = confidence
	}
	if verdict.Reason == "" {
		verdict.Reason = reason
	}
}

func applyFalsePositiveHints(verdict *Verdict, text string) {
	lower := strings.ToLower(text)
	if strings.Contains(text, "```") {
		verdict.FalsePositiveHints = appendUnique(verdict.FalsePositiveHints, HintCodeBlock)
	}
	if strings.Contains(lower, "documentation") || strings.Contains(lower, "template") || strings.Contains(lower, "example") || strings.Contains(text, "sk-xxxx") {
		verdict.FalsePositiveHints = appendUnique(verdict.FalsePositiveHints, HintDocumentation)
		verdict.FalsePositiveHints = appendUnique(verdict.FalsePositiveHints, HintExampleSecret)
	}
	if strings.Contains(lower, "prompt injection analysis") || strings.Contains(lower, "security analysis") || strings.Contains(lower, "unsafe") {
		verdict.FalsePositiveHints = appendUnique(verdict.FalsePositiveHints, HintSecurityAnalysis)
	}
}

func applySuppressions(verdict *Verdict, cfg Config, ctx ScanContext) {
	for _, suppression := range cfg.Suppressions {
		if suppression.Action != SuppressDowngrade || !suppressionMatches(suppression, verdict, ctx) {
			continue
		}
		verdict.FalsePositiveHints = appendUnique(verdict.FalsePositiveHints, HintSuppressed)
		if verdict.RiskScore > 60 {
			verdict.RiskScore = 60
		}
	}
}

func suppressionMatches(rule SuppressionRule, verdict *Verdict, ctx ScanContext) bool {
	if rule.RuleID != "" && !hasHint(verdict.RuleIDs, rule.RuleID) {
		return false
	}
	if rule.Endpoint != "" && rule.Endpoint != ctx.Endpoint {
		return false
	}
	if rule.AccountID > 0 && rule.AccountID != ctx.AccountID {
		return false
	}
	if rule.BaseURL != "" && rule.BaseURL != ctx.BaseURL {
		return false
	}
	return true
}

func finalizeVerdict(verdict *Verdict, cfg Config) {
	if len(verdict.FalsePositiveHints) > 0 && verdict.RiskScore >= 80 && !hasCriticalEvidence(verdict.RuleIDs) {
		verdict.RiskScore = 60
	}
	verdict.RiskLevel = levelForScore(verdict.RiskScore)
	switch cfg.Mode {
	case ModeOff:
		verdict.Action = actionAllow
	case ModeHighBlock:
		verdict.Action = actionForBlockThreshold(verdict.RiskLevel, RiskHigh)
	case ModeStrict:
		verdict.Action = actionForBlockThreshold(verdict.RiskLevel, RiskMedium)
	default:
		verdict.Action = actionWarn
		if verdict.RiskLevel == RiskNone {
			verdict.Action = actionAllow
		}
	}
}

func classifySource(baseURL string, cfg Config) (SourceType, RiskLevel, int, string, string) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return SourceUnknown, RiskHigh, 80, RuleSourceUnknown, "missing upstream base_url"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return SourceUnknown, RiskHigh, 80, RuleSourceUnknown, "invalid upstream base_url"
	}
	if parsed.Scheme == "http" {
		return SourceThirdParty, RiskHigh, 82, RuleSourceInsecureHTTP, "upstream base_url uses insecure HTTP"
	}
	if isOfficialHost(parsed.Hostname(), cfg.OfficialHostSuffix) {
		return SourceOfficial, RiskLow, 20, RuleSourceOfficial, "upstream host is official"
	}
	return SourceThirdParty, RiskMedium, 50, RuleSourceThirdParty, "upstream host is third-party"
}

func isOfficialHost(host string, suffixes []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, suffix := range suffixes {
		suffix = strings.ToLower(strings.TrimSpace(suffix))
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func sanitizePreview(text string, maxChars int) string {
	text = strings.Map(func(r rune) rune {
		switch r {
		case '\u001b':
			return -1
		case '\r', '\n', '\t':
			return ' '
		default:
			if r < 32 {
				return -1
			}
			return r
		}
	}, text)
	text = tokenPattern.ReplaceAllString(text, "[REDACTED_TOKEN]")
	text = dbURLPattern.ReplaceAllString(text, "[REDACTED_DATABASE_URL]")
	text = limitRunes(text, maxChars)
	return strings.Join(strings.Fields(text), " ")
}

func levelForScore(score int) RiskLevel {
	switch {
	case score >= 95:
		return RiskCritical
	case score >= 80:
		return RiskHigh
	case score >= 50:
		return RiskMedium
	case score > 0:
		return RiskLow
	default:
		return RiskNone
	}
}

func actionForBlockThreshold(level, threshold RiskLevel) string {
	if riskRank(level) >= riskRank(threshold) {
		return actionBlock
	}
	if level == RiskNone {
		return actionAllow
	}
	return actionWarn
}

func riskRank(level RiskLevel) int {
	switch level {
	case RiskCritical:
		return 4
	case RiskHigh:
		return 3
	case RiskMedium:
		return 2
	case RiskLow:
		return 1
	default:
		return 0
	}
}

func hasInjectionIntent(lower string) bool {
	return hasAny(lower, []string{"ignore", "bypass", "disable", "do not tell", "without telling", "忽略", "绕过", "关闭", "不要告诉"}) &&
		hasAny(lower, []string{"upload", "send", "leak", "copy", "read", "上传", "发送", "泄露", "复制", "读取"}) &&
		hasAny(lower, []string{"source", "api key", "token", "system prompt", "environment", "源码", "密钥", "私钥", "环境变量", "系统提示"})
}

func hasToolCall(lower string) bool {
	return strings.Contains(lower, `"tool_calls"`) ||
		strings.Contains(lower, `"function_call"`) ||
		(strings.Contains(lower, `"type"`) && strings.Contains(lower, `"function_call"`))
}

func hasUnknownResponseField(lower string) bool {
	return strings.Contains(lower, `"x-injected"`) || strings.Contains(lower, `"developer_override"`)
}

func envLineCount(text string) int {
	return len(envLinePattern.FindAllString(text, -1))
}

func looksLikeExample(text, match string) bool {
	near := strings.ToLower(text)
	return strings.Contains(strings.ToLower(match), "xxxx") ||
		strings.Contains(near, "example") ||
		strings.Contains(near, "template") ||
		strings.Contains(near, "documentation") ||
		strings.Contains(near, "password@localhost")
}

func hasCriticalEvidence(ruleIDs []string) bool {
	return hasHint(ruleIDs, RuleDLPPrivateKey)
}

func hasAny(text string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	if hasHint(values, value) {
		return values
	}
	return append(values, value)
}

func hasHint(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func limitRunes(text string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	var b strings.Builder
	for i, r := range text {
		if i >= maxRunes {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
