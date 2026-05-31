package admin

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

// ProbeUsageSnapshot 主动刷新账号用量。
//
// 优先尝试 /backend-api/wham/usage（零额度成本的结构化端点）；
// 只有拿到 HTTP 响应但 wham 不可用时，才回退到 /backend-api/codex/responses
// 最小探针。网络/代理层不可达时不回退，避免无收益的真实请求。
func (h *Handler) ProbeUsageSnapshot(ctx context.Context, account *auth.Account) error {
	if account == nil {
		return nil
	}
	if account.IsOpenAIResponsesAPI() {
		return h.probeOpenAIResponsesAPI(ctx, account)
	}

	account.Mu().RLock()
	hasToken := account.AccessToken != ""
	account.Mu().RUnlock()
	if !hasToken {
		return nil
	}

	// 1) 优先用 wham（零成本）
	if resp, err := h.probeUsageViaWham(ctx, account); err == nil {
		return nil
	} else {
		if !shouldFallbackUsageProbeAfterWhamFailure(resp, err) {
			return err
		}
		log.Printf("[账号 %d] wham 用量探测失败，回退到 /responses 探针: %v", account.DBID, err)
	}

	// 2) Fallback: 原有的 /responses 最小探针
	return h.probeUsageViaResponses(ctx, account)
}

func (h *Handler) probeOpenAIResponsesAPI(ctx context.Context, account *auth.Account) error {
	model, err := h.connectionTestModelForAccount(ctx, account, "")
	if err != nil {
		return err
	}
	payload := buildTestPayload(model)
	resp, err := proxy.ExecuteOpenAIResponsesRequest(ctx, account, payload, h.store.ResolveProxyForAccount(account), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		h.store.ReportRequestSuccess(account, 0)
		return nil
	case http.StatusUnauthorized:
		h.store.ReportRequestFailure(account, "client", 0)
		h.store.MarkCooldownWithError(account, 24*time.Hour, "unauthorized", fmt.Sprintf("OpenAI Responses 探针上游返回 %d: %s", resp.StatusCode, truncate(string(body), 300)))
		return nil
	case http.StatusTooManyRequests:
		h.store.ReportRequestFailure(account, "client", 0)
		proxy.Apply429Cooldown(h.store, account, body, resp, model)
		return nil
	default:
		if shouldMarkUsageProbeAccountError(resp.StatusCode, body) {
			h.store.MarkError(account, fmt.Sprintf("OpenAI Responses 探针上游返回 %d: %s", resp.StatusCode, truncate(string(body), 300)))
			return nil
		}
		if resp.StatusCode >= 500 {
			h.store.ReportRequestFailure(account, "server", 0)
		} else if resp.StatusCode >= 400 {
			h.store.ReportRequestFailure(account, "client", 0)
		}
		return fmt.Errorf("OpenAI Responses 探针返回状态 %d", resp.StatusCode)
	}
}

// probeUsageViaWham 通过 /backend-api/wham/usage 拉取用量，
// 不消耗任何 token 额度。
func (h *Handler) probeUsageViaWham(ctx context.Context, account *auth.Account) (*http.Response, error) {
	usage, resp, err := proxy.QueryWhamUsage(ctx, account, h.store.ResolveProxyForAccount(account))
	if resp != nil {
		// QueryWhamUsage 在非 200 时不会读 body；这里读取一小段用于账号错误详情。
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			h.store.ReportRequestFailure(account, "client", 0)
			h.store.MarkCooldownWithError(account, 24*time.Hour, "unauthorized", fmt.Sprintf("用量探针 wham 上游返回 %d: %s", resp.StatusCode, truncate(string(body), 300)))
		case http.StatusTooManyRequests:
			h.store.ReportRequestFailure(account, "client", 0)
		}
	}
	if err != nil {
		return resp, err
	}
	if usage == nil {
		return resp, fmt.Errorf("wham returned empty body")
	}

	state := proxy.ApplyWhamUsage(h.store, account, usage)
	h.store.ReportRequestSuccess(account, 0)
	// 用量未耗尽时重置冷却
	if !state.Premium5hRateLimited && (!state.HasUsage7d || state.UsagePct7d < 100) {
		h.store.ClearCooldown(account)
	}
	return resp, nil
}

func shouldFallbackUsageProbeAfterWhamFailure(resp *http.Response, err error) bool {
	return err != nil && resp != nil
}

// probeUsageViaResponses 原有探针：发送最小 /responses 请求，
// 通过响应头同步 Codex 用量状态。会真实消耗少量 token。
func (h *Handler) probeUsageViaResponses(ctx context.Context, account *auth.Account) error {
	payload := buildTestPayload(h.store.GetTestModel())
	resp, err := proxy.ExecuteRequest(ctx, account, payload, "", h.store.ResolveProxyForAccount(account), "", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	usageState := proxy.SyncCodexUsageState(h.store, account, resp)

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	switch resp.StatusCode {
	case http.StatusOK:
		h.store.ReportRequestSuccess(account, 0)
		// 只有用量未耗尽时才重置状态
		if !usageState.Premium5hRateLimited && (!usageState.HasUsage7d || usageState.UsagePct7d < 100) {
			h.store.ClearCooldown(account)
		}
		return nil
	case http.StatusUnauthorized:
		h.store.ReportRequestFailure(account, "client", 0)
		h.store.MarkCooldownWithError(account, 24*time.Hour, "unauthorized", fmt.Sprintf("用量探针上游返回 %d: %s", resp.StatusCode, truncate(string(body), 300)))
		return nil
	case http.StatusTooManyRequests:
		h.store.ReportRequestFailure(account, "client", 0)
		proxy.Apply429Cooldown(h.store, account, body, resp, h.store.GetTestModel())
		return nil
	default:
		if shouldMarkUsageProbeAccountError(resp.StatusCode, body) {
			h.store.MarkError(account, fmt.Sprintf("用量探针上游返回 %d: %s", resp.StatusCode, truncate(string(body), 300)))
			return nil
		}
		if resp.StatusCode >= 500 {
			h.store.ReportRequestFailure(account, "server", 0)
		} else if resp.StatusCode >= 400 {
			h.store.ReportRequestFailure(account, "client", 0)
		}
		return fmt.Errorf("探针返回状态 %d", resp.StatusCode)
	}
}

func shouldMarkUsageProbeAccountError(statusCode int, body []byte) bool {
	switch statusCode {
	case http.StatusPaymentRequired, http.StatusForbidden:
		return proxy.IsDeactivatedWorkspaceError(body)
	default:
		return false
	}
}

// ForceUsageProbe 主动触发一次"忽略缓存阈值"的全量用量探针，并立即返回。
// 真正的探针在后台并发执行（受 usage_probe_concurrency 限制）。
func (h *Handler) ForceUsageProbe(c *gin.Context) {
	if h.store.GetLazyMode() {
		c.JSON(http.StatusOK, gin.H{
			"triggered":   false,
			"reason":      "lazy_mode",
			"concurrency": h.store.GetUsageProbeConcurrency(),
		})
		return
	}
	h.store.TriggerUsageProbeForceAsync()
	c.JSON(http.StatusOK, gin.H{
		"triggered":   true,
		"concurrency": h.store.GetUsageProbeConcurrency(),
	})
}
