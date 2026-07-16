package proxy

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

const (
	defaultResponsesMaxInflightRequests = 64
	defaultResponsesMaxInflightBytes    = 256 << 20
	defaultResponsesMaxFallbacks        = 4
	responsesRequestLeaseContextKey     = "responses_memory_request_lease"
)

type responsesMemoryLimits struct {
	maxInflightRequests int
	maxInflightBytes    int64
	maxFallbacks        int
}

type continuationFallbackActivation uint8

const (
	continuationFallbackUnavailable continuationFallbackActivation = iota
	continuationFallbackActivated
	continuationFallbackRejected
)

type responsesMemoryGovernor struct {
	mu               sync.Mutex
	limits           responsesMemoryLimits
	inflightRequests int
	inflightBytes    int64
	activeFallbacks  int
	rejectedRequests uint64
	rejectedFallback uint64
}

type responsesRequestLease struct {
	mu       sync.Mutex
	governor *responsesMemoryGovernor
	bytes    int64
	released bool
}

type responsesFallbackLease struct {
	mu       sync.Mutex
	governor *responsesMemoryGovernor
	released bool
}

type responsesGovernorStats struct {
	InflightRequests int    `json:"inflight_requests"`
	InflightBytes    int64  `json:"inflight_bytes"`
	ActiveFallbacks  int    `json:"active_fallbacks"`
	RejectedRequests uint64 `json:"rejected_requests"`
	RejectedFallback uint64 `json:"rejected_fallbacks"`
	MaxRequests      int    `json:"max_inflight_requests"`
	MaxBytes         int64  `json:"max_inflight_bytes"`
	MaxFallbacks     int    `json:"max_fallbacks"`
}

type ResponsesMemorySnapshot struct {
	responsesGovernorStats
	ContinuityEntries             int    `json:"continuity_entries"`
	ContinuityBytes               int    `json:"continuity_bytes"`
	ContinuityEvictions           uint64 `json:"continuity_evictions"`
	ContinuityPersistent          bool   `json:"continuity_persistent"`
	ContinuityPersistenceFailures uint64 `json:"continuity_persistence_failures"`
}

var defaultResponsesMemoryGovernor = newResponsesMemoryGovernor(responsesMemoryLimitsFromEnv(os.Getenv))

func newResponsesMemoryGovernor(limits responsesMemoryLimits) *responsesMemoryGovernor {
	if limits.maxInflightRequests <= 0 {
		limits.maxInflightRequests = defaultResponsesMaxInflightRequests
	}
	if limits.maxInflightBytes <= 0 {
		limits.maxInflightBytes = defaultResponsesMaxInflightBytes
	}
	if limits.maxFallbacks <= 0 {
		limits.maxFallbacks = defaultResponsesMaxFallbacks
	}
	return &responsesMemoryGovernor{limits: limits}
}

func responsesMemoryLimitsFromEnv(getenv func(string) string) responsesMemoryLimits {
	return responsesMemoryLimits{
		maxInflightRequests: positiveEnvInt(getenv, "CODEX_RESPONSES_MAX_INFLIGHT_REQUESTS", defaultResponsesMaxInflightRequests),
		maxInflightBytes:    int64(positiveEnvInt(getenv, "CODEX_RESPONSES_MAX_INFLIGHT_BYTES_MB", defaultResponsesMaxInflightBytes>>20)) << 20,
		maxFallbacks:        positiveEnvInt(getenv, "CODEX_RESPONSES_MAX_FALLBACKS", defaultResponsesMaxFallbacks),
	}
}

func positiveEnvInt(getenv func(string) string, key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func (governor *responsesMemoryGovernor) tryAcquireRequest(bytes int64) (*responsesRequestLease, bool) {
	if bytes < 0 {
		bytes = 0
	}
	governor.mu.Lock()
	defer governor.mu.Unlock()
	if governor.inflightRequests >= governor.limits.maxInflightRequests ||
		bytes > governor.limits.maxInflightBytes-governor.inflightBytes {
		governor.rejectedRequests++
		return nil, false
	}
	governor.inflightRequests++
	governor.inflightBytes += bytes
	return &responsesRequestLease{governor: governor, bytes: bytes}, true
}

func (lease *responsesRequestLease) resize(bytes int64) bool {
	if bytes < 0 {
		bytes = 0
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return false
	}
	delta := bytes - lease.bytes
	lease.governor.mu.Lock()
	defer lease.governor.mu.Unlock()
	if delta > lease.governor.limits.maxInflightBytes-lease.governor.inflightBytes {
		lease.governor.rejectedRequests++
		return false
	}
	lease.governor.inflightBytes += delta
	lease.bytes = bytes
	return true
}

func (lease *responsesRequestLease) release() {
	if lease == nil {
		return
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return
	}
	lease.governor.mu.Lock()
	lease.governor.inflightRequests--
	lease.governor.inflightBytes -= lease.bytes
	lease.governor.mu.Unlock()
	lease.released = true
}

func (governor *responsesMemoryGovernor) tryAcquireFallback() (*responsesFallbackLease, bool) {
	governor.mu.Lock()
	defer governor.mu.Unlock()
	if governor.activeFallbacks >= governor.limits.maxFallbacks {
		governor.rejectedFallback++
		return nil, false
	}
	governor.activeFallbacks++
	return &responsesFallbackLease{governor: governor}, true
}

func (lease *responsesFallbackLease) release() {
	if lease == nil {
		return
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return
	}
	lease.governor.mu.Lock()
	lease.governor.activeFallbacks--
	lease.governor.mu.Unlock()
	lease.released = true
}

func (governor *responsesMemoryGovernor) stats() responsesGovernorStats {
	governor.mu.Lock()
	defer governor.mu.Unlock()
	return responsesGovernorStats{
		InflightRequests: governor.inflightRequests,
		InflightBytes:    governor.inflightBytes,
		ActiveFallbacks:  governor.activeFallbacks,
		RejectedRequests: governor.rejectedRequests,
		RejectedFallback: governor.rejectedFallback,
		MaxRequests:      governor.limits.maxInflightRequests,
		MaxBytes:         governor.limits.maxInflightBytes,
		MaxFallbacks:     governor.limits.maxFallbacks,
	}
}

func ResponsesMemoryStats() ResponsesMemorySnapshot {
	continuity := openAIResponsesContinuity.stats()
	return ResponsesMemorySnapshot{
		responsesGovernorStats:        defaultResponsesMemoryGovernor.stats(),
		ContinuityEntries:             continuity.Entries,
		ContinuityBytes:               continuity.Bytes,
		ContinuityEvictions:           continuity.Evictions,
		ContinuityPersistent:          continuity.Persistent,
		ContinuityPersistenceFailures: continuity.PersistenceFailures,
	}
}

func ResponsesMemoryAdmissionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost || c.Request.URL.Path != "/v1/responses" {
			c.Next()
			return
		}
		estimatedBytes := c.Request.ContentLength
		if estimatedBytes < 0 {
			estimatedBytes = int64(security.MaxRequestBodySize)
		}
		lease, ok := defaultResponsesMemoryGovernor.tryAcquireRequest(estimatedBytes)
		if !ok {
			writeResponsesMemoryError(c, "local_memory_pressure", "服务请求内存已达上限，请稍后重试")
			c.Abort()
			return
		}
		c.Set(responsesRequestLeaseContextKey, lease)
		defer lease.release()
		c.Next()
	}
}

func ensureResponsesRequestAdmission(c *gin.Context, actualBytes int64) (func(), bool) {
	if value, exists := c.Get(responsesRequestLeaseContextKey); exists {
		lease, ok := value.(*responsesRequestLease)
		if !ok || !lease.resize(actualBytes) {
			writeResponsesMemoryError(c, "local_memory_pressure", "服务请求内存已达上限，请稍后重试")
			return nil, false
		}
		return func() {}, true
	}
	lease, ok := defaultResponsesMemoryGovernor.tryAcquireRequest(actualBytes)
	if !ok {
		writeResponsesMemoryError(c, "local_memory_pressure", "服务请求内存已达上限，请稍后重试")
		return nil, false
	}
	return lease.release, true
}

func writeResponsesMemoryError(c *gin.Context, code, message string) {
	c.Header("Retry-After", "1")
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error": gin.H{
			"message": message,
			"type":    ErrorTypeServerError,
			"code":    code,
		},
	})
}
