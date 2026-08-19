package proxy

import (
	"context"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"github.com/codex2api/auth"
)

type retryAccountExclusions struct {
	hard               map[int64]bool
	soft               map[int64]bool
	firstTokenTimeouts int
}

func newRetryAccountExclusions() *retryAccountExclusions {
	return &retryAccountExclusions{
		hard: make(map[int64]bool),
		soft: make(map[int64]bool),
	}
}

func (r *retryAccountExclusions) MarkHard(accountID int64) {
	if r == nil || accountID == 0 {
		return
	}
	r.hard[accountID] = true
	delete(r.soft, accountID)
}

func (r *retryAccountExclusions) MarkSoftFirstTokenTimeout(accountID int64) bool {
	if r == nil || accountID == 0 {
		return false
	}
	if r.firstTokenTimeouts >= maxFirstTokenTimeoutAccountAttempts() {
		return false
	}
	if r.hard[accountID] {
		return false
	}
	r.soft[accountID] = true
	r.firstTokenTimeouts++
	return true
}

func (r *retryAccountExclusions) ResetSoft() bool {
	if r == nil || len(r.soft) == 0 {
		return false
	}
	r.soft = make(map[int64]bool)
	return true
}

func (r *retryAccountExclusions) ForSelection() map[int64]bool {
	if r == nil || (len(r.hard) == 0 && len(r.soft) == 0) {
		return nil
	}
	exclude := make(map[int64]bool, len(r.hard)+len(r.soft))
	for id := range r.hard {
		exclude[id] = true
	}
	for id := range r.soft {
		exclude[id] = true
	}
	return exclude
}

func (r *retryAccountExclusions) ShouldResetSoftForPool(poolTotal int) bool {
	if r == nil || poolTotal > 2 || poolTotal <= 0 {
		return false
	}
	return len(r.soft) >= poolTotal
}

func (r *retryAccountExclusions) FirstTokenTimeoutAttempts() int {
	if r == nil {
		return 0
	}
	return r.firstTokenTimeouts
}

func maxFirstTokenTimeoutAccountAttempts() int {
	return 1
}

func (h *Handler) recheckAccountsAfterExhaustion(ctx context.Context, models ...string) bool {
	if h == nil || h.store == nil {
		return false
	}
	recovered := h.store.ProbeAllAccounts(ctx)
	for accountID := range recovered {
		if account := h.store.FindByID(accountID); account != nil {
			for _, model := range models {
				h.store.ClearModelCooldown(account, model)
			}
		}
	}
	if len(recovered) > 0 {
		log.Printf("请求账号池耗尽后全量复测成功，恢复账号数=%d", len(recovered))
	}
	return len(recovered) > 0
}

type retryAccountPick struct {
	account      *auth.Account
	proxyURL     string
	poolSnapshot auth.DispatchPoolSnapshot
	queueWait    time.Duration
	queueFull    bool
}

func (p retryAccountPick) QueueWaitMs() int64 {
	return int64(math.Round(float64(p.queueWait.Milliseconds())))
}

func (h *Handler) nextRetryAccountForSession(ctx context.Context, affinityKey string, apiKeyID int64, exclusions *retryAccountExclusions, filter auth.AccountFilter) (*auth.Account, string) {
	pick := h.nextRetryAccountPickForSession(ctx, affinityKey, apiKeyID, exclusions, filter)
	return pick.account, pick.proxyURL
}

func (h *Handler) nextRetryAccountPickForSession(ctx context.Context, affinityKey string, apiKeyID int64, exclusions *retryAccountExclusions, filter auth.AccountFilter) retryAccountPick {
	if h == nil || h.store == nil {
		return retryAccountPick{}
	}
	for {
		exclude := exclusions.ForSelection()
		snapshot := h.store.DispatchPoolSnapshotWithFilter(apiKeyID, exclude, filter)
		if exclusions.ShouldResetSoftForPool(snapshot.Total) {
			exclusions.ResetSoft()
			exclude = exclusions.ForSelection()
			snapshot = h.store.DispatchPoolSnapshotWithFilter(apiKeyID, exclude, filter)
			log.Printf("小账号池首字超时软排除已试完，清空本次请求软排除: pool_total=%d pool_excluded=%d", snapshot.Total, snapshot.Excluded)
		}
		account, stickyProxyURL := h.store.NextForStrictSessionExcludingWithFilter(affinityKey, apiKeyID, exclude, filter)
		if account != nil {
			return retryAccountPick{account: account, proxyURL: stickyProxyURL, poolSnapshot: snapshot}
		}
		releaseQueue, ok := h.store.TryEnterDispatchQueue(snapshot.Total)
		if !ok {
			return retryAccountPick{poolSnapshot: snapshot, queueFull: true}
		}
		waitStart := time.Now()
		account, stickyProxyURL = h.store.WaitForStrictSessionAvailableExcludingWithFilter(ctx, affinityKey, 30*time.Second, apiKeyID, exclude, filter)
		queueWait := time.Since(waitStart)
		releaseQueue()
		if account != nil {
			return retryAccountPick{account: account, proxyURL: stickyProxyURL, poolSnapshot: snapshot, queueWait: queueWait}
		}
		if !exclusions.ResetSoft() {
			return retryAccountPick{poolSnapshot: snapshot, queueWait: queueWait}
		}
		log.Printf("首字超时账号池已试完，清空本次请求软排除并进入下一轮重试")
	}
}

func isFirstTokenTimeoutOutcome(outcome streamOutcome) bool {
	return outcome.failureKind == "timeout"
}

func websocketFallbackHTTPEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEX_WS_FALLBACK_HTTP"))) {
	case "0", "false", "no", "n", "off":
		return false
	default:
		return true
	}
}

func isWebsocketMissingTerminalOutcome(outcome streamOutcome) bool {
	return outcome.failureKind == "websocket_missing_terminal"
}
