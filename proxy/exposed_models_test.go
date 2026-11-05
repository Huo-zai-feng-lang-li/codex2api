package proxy

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/codex2api/auth"
)

func TestSetExposedModelsEmptyMeansNoFilter(t *testing.T) {
	original := CurrentExposedModels()
	t.Cleanup(func() {
		data, _ := json.Marshal(original)
		_ = SetExposedModels(string(data))
	})

	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"blank", "   \n\t  "},
		{"separators only", " , , "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := SetExposedModels(c.raw); err != nil {
				t.Fatalf("SetExposedModels(%q) returned error: %v", c.raw, err)
			}
			if got := CurrentExposedModels(); len(got) != 0 {
				t.Fatalf("config %q should disable filtering, got %v", c.raw, got)
			}
		})
	}

	t.Run("newline list", func(t *testing.T) {
		if err := SetExposedModels("gpt-6-astra\nagnes-2.5-flash"); err != nil {
			t.Fatal(err)
		}
		got := CurrentExposedModels()
		if len(got) != 2 || got[0] != "gpt-6-astra" || got[1] != "agnes-2.5-flash" {
			t.Fatalf("explicit config = %v", got)
		}
	})

	t.Run("json list", func(t *testing.T) {
		if err := SetExposedModels(`["gpt-6-sol","gpt-5.6-sol"]`); err != nil {
			t.Fatal(err)
		}
		got := CurrentExposedModels()
		if len(got) != 2 || got[0] != "gpt-6-sol" || got[1] != "gpt-5.6-sol" {
			t.Fatalf("json config = %v", got)
		}
	})

	t.Run("case-insensitive dedupe", func(t *testing.T) {
		if err := SetExposedModels("gpt-6-sol\nGPT-6-SOL\nagnes-2.5-flash"); err != nil {
			t.Fatal(err)
		}
		if got := CurrentExposedModels(); len(got) != 2 {
			t.Fatalf("duplicate config = %v, want 2 unique entries", got)
		}
	})
}

func TestBurstClassifierSeparatedFromStrongSemantics(t *testing.T) {
	now := time.Now()

	burstBody := []byte(`{"error":{"message":"Rate limit reached for requests per min"}}`)
	if _, matched := matchGenericBurstRateLimit(burstBody, now); !matched {
		t.Fatal("burst classifier should match RPM error")
	}
	if _, matched := matchGenericUpstreamRateLimit(burstBody, now); matched {
		t.Fatal("generic classifier should NOT match RPM after refactor")
	}

	dailyBody := []byte(`{"error":{"message":"daily usage limit exceeded"}}`)
	if _, matched := matchGenericUpstreamRateLimit(dailyBody, now); !matched {
		t.Fatal("generic classifier should still match daily limit")
	}
	if _, matched := matchGenericBurstRateLimit(dailyBody, now); matched {
		t.Fatal("burst classifier should NOT match daily limit")
	}
}

// TestCooldownModelForUsesForwardedModel 验证模型级冷却 key 与实际转发模型一致。
// responses_api 账号转发前会替换为 mappedModel，冷却必须以 mappedModel 为 key，
// 否则调度过滤（查 mappedModel）永远命中不到冷却记录。
func TestCooldownModelForUsesForwardedModel(t *testing.T) {
	responsesAcc := &auth.Account{
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      "https://relay.example.com",
		APIKey:       "sk-test",
	}
	if !responsesAcc.IsOpenAIResponsesAPI() {
		t.Fatal("test fixture should be a responses_api account")
	}
	if got := cooldownModelFor(responsesAcc, "gpt-5.2", "free/glm-5.3-flash"); got != "free/glm-5.3-flash" {
		t.Fatalf("responses_api account should use mappedModel, got %q", got)
	}
	if got := cooldownModelFor(responsesAcc, "gpt-5.2", ""); got != "gpt-5.2" {
		t.Fatalf("empty mappedModel should fall back to effectiveModel, got %q", got)
	}

	codexAcc := &auth.Account{AccessToken: "codex-token"}
	if codexAcc.IsOpenAIResponsesAPI() {
		t.Fatal("test fixture should NOT be a responses_api account")
	}
	if got := cooldownModelFor(codexAcc, "gpt-5.2", "free/glm-5.3-flash"); got != "gpt-5.2" {
		t.Fatalf("codex account must keep effectiveModel, got %q", got)
	}

	if got := cooldownModelFor(nil, "gpt-5.2", "x"); got != "gpt-5.2" {
		t.Fatalf("nil account should fall back to effectiveModel, got %q", got)
	}
}

// TestModelScopedQuotaFailureExcludesAccountLevel 验证账号级欠费语义不再走模型级冷却。
// insufficient_quota / insufficient quota / exceeded your current quota 属于账号级，
// 若误判为模型级，会让账号其它模型继续被调度并反复撞墙。
func TestModelScopedQuotaFailureExcludesAccountLevel(t *testing.T) {
	accountLevel := []string{
		`{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota"}}`,
		`{"error":{"code":"insufficient_quota","message":"quota exhausted"}}`,
		`{"error":{"message":"insufficient quota for this key"}}`,
	}
	for _, body := range accountLevel {
		if modelScopedQuotaFailure([]byte(body)) {
			t.Fatalf("account-level quota error must NOT be model-scoped: %s", body)
		}
	}

	modelLevel := []string{
		`{"error":{"message":"insufficient user quota for group paid"}}`,
		`{"error":{"code":"insufficient_user_quota","message":"group quota exhausted"}}`,
		`{"error":{"message":"model not enabled for this group"}}`,
		`{"error":{"message":"billing hard limit reached"}}`,
	}
	for _, body := range modelLevel {
		if !modelScopedQuotaFailure([]byte(body)) {
			t.Fatalf("group-scoped quota error SHOULD be model-scoped: %s", body)
		}
	}

	if modelScopedQuotaFailure(nil) {
		t.Fatal("nil body must not match")
	}
	if modelScopedQuotaFailure([]byte(`{}`)) {
		t.Fatal("empty body must not match")
	}
}

// TestSupportedModelIDsCacheInvalidation 验证缓存命中与主动失效。
func TestSupportedModelIDsCacheInvalidation(t *testing.T) {
	InvalidateSupportedModelIDsCache()
	t.Cleanup(InvalidateSupportedModelIDsCache)

	first := SupportedModelIDs(context.Background(), nil)
	if len(first) == 0 {
		t.Fatal("builtin catalog should return non-empty models")
	}
	second := SupportedModelIDs(context.Background(), nil)
	if &first[0] != &second[0] {
		t.Fatal("second call should hit cache and return the same backing array")
	}

	InvalidateSupportedModelIDsCache()
	third := SupportedModelIDs(context.Background(), nil)
	if &first[0] == &third[0] {
		t.Fatal("after invalidation the cache should be rebuilt")
	}
}

// TestSupportedModelIDsCacheNotCorruptedByAppend 验证 supportedModelIDs 的 append
// 不会污染 SupportedModelIDs 的缓存底层数组。
// enabledModelIDs 用 make([]string, 0, len(items)) 分配，存在 cap > len 的余量；
// 若调用方直接 append，会写入缓存空闲槽位，并发下产生数据竞争并互相串写模型。
func TestSupportedModelIDsCacheNotCorruptedByAppend(t *testing.T) {
	InvalidateSupportedModelIDsCache()
	t.Cleanup(InvalidateSupportedModelIDsCache)

	cached := SupportedModelIDs(context.Background(), nil)
	if len(cached) == 0 {
		t.Fatal("builtin catalog should return non-empty models")
	}
	snapshot := append([]string(nil), cached...)

	h := &Handler{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.supportedModelIDs(context.Background())
		}()
	}
	wg.Wait()

	after := SupportedModelIDs(context.Background(), nil)
	if len(after) != len(snapshot) {
		t.Fatalf("cache length changed: %d -> %d", len(snapshot), len(after))
	}
	for i := range snapshot {
		if after[i] != snapshot[i] {
			t.Fatalf("cache[%d] changed: %q -> %q", i, snapshot[i], after[i])
		}
	}
}
