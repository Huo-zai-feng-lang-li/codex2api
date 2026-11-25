package proxy

import (
	"net/http"
	"testing"
	"time"

	"github.com/codex2api/auth"
)

func TestUpstreamCooldownRuleProducesTransientCooldown(t *testing.T) {
	if err := SetUpstreamCooldownRules(`[{"domain_contains":"agnes-ai.cn","cooldown_seconds":50}]`); err != nil {
		t.Fatalf("设置上游冷却规则失败: %v", err)
	}
	t.Cleanup(func() {
		_ = SetUpstreamCooldownRules("")
	})

	account := &auth.Account{BaseURL: "https://apihub.agnes-ai.cn/v1", Status: auth.StatusReady, AccessToken: "token"}
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}

	decision := classify429RateLimit(account, nil, resp, time.Now(), "agnes-3.0-flash")

	if decision.Scope != rateLimitScopeAccount {
		t.Fatalf("Scope = %q, want %q", decision.Scope, rateLimitScopeAccount)
	}
	if decision.Reason != auth.CooldownReasonTransientRateLimit {
		t.Fatalf("Reason = %q, want %q（配置的冷却秒数必须能到点自动恢复）", decision.Reason, auth.CooldownReasonTransientRateLimit)
	}
	if decision.Cooldown != 50*time.Second {
		t.Fatalf("Cooldown = %v, want 50s", decision.Cooldown)
	}
}
