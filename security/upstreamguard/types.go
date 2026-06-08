package upstreamguard

import "time"

const (
	ModeOff       = "off"
	ModeWarn      = "warn"
	ModeHighBlock = "high_block"
	ModeStrict    = "strict"

	DirectionRequest  = "request"
	DirectionResponse = "response"
	DirectionSource   = "source"

	RiskNone     RiskLevel = "none"
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"

	SourceOfficial   SourceType = "official"
	SourceThirdParty SourceType = "third_party"
	SourceUnknown    SourceType = "unknown"

	RuleDLPToken           = "dlp_token"
	RuleDLPPrivateKey      = "dlp_private_key"
	RuleDLPDatabaseURL     = "dlp_database_url"
	RuleDLPEnvBulk         = "dlp_env_bulk"
	RuleResponseInjection  = "response_injection"
	RuleToolCall           = "tool_call"
	RuleUnknownField       = "unknown_field"
	RuleSourceOfficial     = "source_official"
	RuleSourceThirdParty   = "source_third_party"
	RuleSourceUnknown      = "source_unknown"
	RuleSourceInsecureHTTP = "source_insecure_http"

	HintExampleSecret    = "example_secret"
	HintDocumentation    = "documentation_template"
	HintCodeBlock        = "code_block"
	HintSecurityAnalysis = "security_analysis"
	HintSuppressed       = "suppressed"

	SuppressDowngrade = "downgrade"
)

type RiskLevel string

type SourceType string

type Config struct {
	Enabled            bool
	Mode               string
	RequestDLPEnabled  bool
	ResponseFirewall   bool
	ToolCallWarning    bool
	ScanTimeout        time.Duration
	MaxScanBytes       int
	MaxPreviewChars    int
	Suppressions       []SuppressionRule
	OfficialHostSuffix []string
}

type SuppressionRule struct {
	RuleID    string `json:"rule_id"`
	Endpoint  string `json:"endpoint"`
	AccountID int64  `json:"account_id"`
	BaseURL   string `json:"base_url"`
	Action    string `json:"action"`
}

type ScanContext struct {
	Endpoint    string
	Model       string
	AccountID   int64
	AccountName string
	BaseURL     string
	Source      SourceType
	Stream      bool
}

type Evidence struct {
	RuleID  string `json:"rule_id"`
	Snippet string `json:"snippet"`
}

type Verdict struct {
	Enabled            bool       `json:"enabled"`
	Direction          string     `json:"direction"`
	Action             string     `json:"action"`
	RiskScore          int        `json:"risk_score"`
	RiskLevel          RiskLevel  `json:"risk_level"`
	Confidence         int        `json:"confidence"`
	RuleIDs            []string   `json:"rule_ids"`
	Evidence           []Evidence `json:"evidence"`
	Reason             string     `json:"reason"`
	FalsePositiveHints []string   `json:"false_positive_hints"`
	Preview            string     `json:"preview"`
	Source             SourceType `json:"source_type"`
	ToolCall           bool       `json:"tool_call"`
	ScannerError       string     `json:"scanner_error,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:            true,
		Mode:               ModeWarn,
		RequestDLPEnabled:  true,
		ResponseFirewall:   true,
		ToolCallWarning:    true,
		ScanTimeout:        1500 * time.Millisecond,
		MaxScanBytes:       128 * 1024,
		MaxPreviewChars:    500,
		OfficialHostSuffix: []string{"api.openai.com", "api.anthropic.com"},
	}
}
