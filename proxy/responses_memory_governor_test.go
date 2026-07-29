package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestResponsesMemoryGovernorEnforcesRequestCountAndBytes(t *testing.T) {
	governor := newResponsesMemoryGovernor(responsesMemoryLimits{
		maxInflightRequests: 2,
		maxInflightBytes:    10,
		maxFallbacks:        1,
	})

	first, ok := governor.tryAcquireRequest(4)
	if !ok {
		t.Fatal("first request admission was rejected")
	}
	second, ok := governor.tryAcquireRequest(6)
	if !ok {
		t.Fatal("second request admission was rejected")
	}
	if _, ok := governor.tryAcquireRequest(1); ok {
		t.Fatal("request admission exceeded global limits")
	}

	first.release()
	if !second.resize(5) {
		t.Fatal("shrinking an active request failed")
	}
	third, ok := governor.tryAcquireRequest(5)
	if !ok {
		t.Fatal("released capacity was not reusable")
	}
	second.release()
	third.release()

	stats := governor.stats()
	if stats.InflightRequests != 0 || stats.InflightBytes != 0 {
		t.Fatalf("active usage leaked: %+v", stats)
	}
}

func TestResponsesMemoryGovernorBoundsFallbacks(t *testing.T) {
	governor := newResponsesMemoryGovernor(responsesMemoryLimits{
		maxInflightRequests: 1,
		maxInflightBytes:    1,
		maxFallbacks:        1,
	})

	first, ok := governor.tryAcquireFallback()
	if !ok {
		t.Fatal("first fallback admission was rejected")
	}
	if _, ok := governor.tryAcquireFallback(); ok {
		t.Fatal("fallback admission exceeded its limit")
	}
	first.release()
	second, ok := governor.tryAcquireFallback()
	if !ok {
		t.Fatal("released fallback capacity was not reusable")
	}
	second.release()
}

func TestResponsesMemoryLimitsReadEnvironment(t *testing.T) {
	values := map[string]string{
		"CODEX_RESPONSES_MAX_INFLIGHT_REQUESTS":   "12",
		"CODEX_RESPONSES_MAX_INFLIGHT_BYTES_MB":   "96",
		"CODEX_RESPONSES_MAX_FALLBACKS":           "3",
		"CODEX_RESPONSES_CONTINUITY_MAX_ENTRIES":  "800",
		"CODEX_RESPONSES_CONTINUITY_MAX_CHAIN_MB": "6",
		"CODEX_RESPONSES_CONTINUITY_MAX_BYTES_MB": "48",
	}
	getenv := func(key string) string { return values[key] }
	governor := responsesMemoryLimitsFromEnv(getenv)
	if governor.maxInflightRequests != 12 || governor.maxInflightBytes != 96<<20 || governor.maxFallbacks != 3 {
		t.Fatalf("governor limits = %+v", governor)
	}
	continuity := openAIResponsesContinuityLimitsFromEnv(getenv)
	if continuity.maxEntries != 800 || continuity.maxItemBytes != 6<<20 || continuity.maxBytes != 48<<20 {
		t.Fatalf("continuity limits = %+v", continuity)
	}
	if mode := openAIResponsesContinuityModeFromEnv(func(string) string { return "upstream" }); mode != openAIResponsesContinuityModeUpstream {
		t.Fatalf("continuity mode = %q, want upstream", mode)
	}
}

func TestResponsesMemoryAdmissionMiddlewareRejectsBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousGovernor := defaultResponsesMemoryGovernor
	defaultResponsesMemoryGovernor = newResponsesMemoryGovernor(responsesMemoryLimits{
		maxInflightRequests: 1,
		maxInflightBytes:    8,
		maxFallbacks:        1,
	})
	t.Cleanup(func() { defaultResponsesMemoryGovernor = previousGovernor })
	occupied, ok := defaultResponsesMemoryGovernor.tryAcquireRequest(8)
	if !ok {
		t.Fatal("failed to occupy request capacity")
	}
	defer occupied.release()

	recorder := httptest.NewRecorder()
	nextCalled := false
	router := gin.New()
	router.Use(ResponsesMemoryAdmissionMiddleware())
	router.POST("/v1/responses", func(c *gin.Context) {
		nextCalled = true
		c.Status(204)
	})
	router.ServeHTTP(recorder, httptest.NewRequest("POST", "/v1/responses", nil))
	if nextCalled {
		t.Fatal("handler chain ran while request admission was saturated")
	}
	if recorder.Code != 503 {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestResponsesMemoryStatsMarshalsGovernanceFields(t *testing.T) {
	encoded, err := json.Marshal(ResponsesMemoryStats())
	if err != nil {
		t.Fatalf("marshal stats: %v", err)
	}
	for _, key := range []string{
		"inflight_requests", "inflight_bytes", "active_fallbacks",
		"continuity_entries", "continuity_bytes", "continuity_persistent", "continuity_persistence_failures",
	} {
		if !json.Valid(encoded) || !containsJSONKey(encoded, key) {
			t.Fatalf("stats JSON missing %q: %s", key, encoded)
		}
	}
}

func containsJSONKey(data []byte, key string) bool {
	var values map[string]any
	if json.Unmarshal(data, &values) != nil {
		return false
	}
	_, ok := values[key]
	return ok
}

func TestContinuationRegistryStoresParentChainIncrementally(t *testing.T) {
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl:          time.Hour,
		maxEntries:   10,
		maxItems:     20,
		maxItemBytes: 1 << 20,
		maxBytes:     1 << 20,
	})

	rootInput := rawMessages(`{"type":"message","role":"user","content":"root"}`)
	rootOutput := rawMessages(`{"type":"message","role":"assistant","content":"ROOT"}`)
	childInput := rawMessages(`{"type":"message","role":"user","content":"child"}`)
	childOutput := rawMessages(`{"type":"message","role":"assistant","content":"CHILD"}`)
	grandInput := rawMessages(`{"type":"message","role":"user","content":"grand"}`)
	grandOutput := rawMessages(`{"type":"message","role":"assistant","content":"GRAND"}`)

	registry.store("resp_root", "", "", continuationDelta(rootInput, rootOutput))
	registry.store("resp_child", "resp_root", "", continuationDelta(childInput, childOutput))
	registry.store("resp_grand", "resp_child", "", continuationDelta(grandInput, grandOutput))

	wantBytes := rawMessagesSize(rootInput) + rawMessagesSize(rootOutput) +
		rawMessagesSize(childInput) + rawMessagesSize(childOutput) +
		rawMessagesSize(grandInput) + rawMessagesSize(grandOutput)
	stats := registry.stats()
	if stats.Bytes != wantBytes {
		t.Fatalf("stored bytes = %d, want unique delta bytes %d", stats.Bytes, wantBytes)
	}

	history, ok := registry.materialize("resp_grand")
	if !ok {
		t.Fatal("grandchild history was not replayable")
	}
	if got, want := rawMessageContents(history), []string{"root", "ROOT", "child", "CHILD", "grand", "GRAND"}; !equalStrings(got, want) {
		t.Fatalf("materialized history = %v, want %v", got, want)
	}
}

func TestContinuationRegistryBranchesShareParentWithoutCrossTalk(t *testing.T) {
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl:          time.Hour,
		maxEntries:   10,
		maxItems:     20,
		maxItemBytes: 1 << 20,
		maxBytes:     1 << 20,
	})
	rootInput := rawMessages(`{"type":"message","role":"user","content":"root"}`)
	rootOutput := rawMessages(`{"type":"message","role":"assistant","content":"ROOT"}`)
	registry.store("root", "", "", continuationDelta(rootInput, rootOutput))
	registry.store("branch_a", "root", "", continuationDelta(
		rawMessages(`{"type":"message","role":"user","content":"A"}`),
		rawMessages(`{"type":"message","role":"assistant","content":"A1"}`),
	))
	registry.store("branch_b", "root", "", continuationDelta(
		rawMessages(`{"type":"message","role":"user","content":"B"}`),
		rawMessages(`{"type":"message","role":"assistant","content":"B1"}`),
	))

	branchA, ok := registry.materialize("branch_a")
	if !ok {
		t.Fatal("branch A was not replayable")
	}
	branchB, ok := registry.materialize("branch_b")
	if !ok {
		t.Fatal("branch B was not replayable")
	}
	if got, want := rawMessageContents(branchA), []string{"root", "ROOT", "A", "A1"}; !equalStrings(got, want) {
		t.Fatalf("branch A = %v, want %v", got, want)
	}
	if got, want := rawMessageContents(branchB), []string{"root", "ROOT", "B", "B1"}; !equalStrings(got, want) {
		t.Fatalf("branch B = %v, want %v", got, want)
	}
}

func TestContinuationRegistryNeverReplaysAfterParentEviction(t *testing.T) {
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl:          time.Hour,
		maxEntries:   2,
		maxItems:     20,
		maxItemBytes: 1 << 20,
		maxBytes:     1 << 20,
	})
	registry.store("root", "", "", continuationDelta(rawMessages(`{"content":"root"}`), rawMessages(`{"content":"ROOT"}`)))
	registry.store("child", "root", "", continuationDelta(rawMessages(`{"content":"child"}`), rawMessages(`{"content":"CHILD"}`)))
	registry.store("other", "", "", continuationDelta(rawMessages(`{"content":"other"}`), rawMessages(`{"content":"OTHER"}`)))

	if _, ok := registry.materialize("child"); ok {
		t.Fatal("child replayed partial history after its parent chain was evicted")
	}
	stats := registry.stats()
	if stats.Entries > 2 || stats.Bytes > 1<<20 {
		t.Fatalf("registry exceeded limits: %+v", stats)
	}
}

func TestContinuationRegistryConcurrentStoresStayWithinLimits(t *testing.T) {
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl:          time.Hour,
		maxEntries:   20,
		maxItems:     20,
		maxItemBytes: 1 << 20,
		maxBytes:     2048,
	})
	var workers sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < 100; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			responseID := "response_" + strconv.Itoa(index)
			registry.store(responseID, "", "", continuationDelta(
				rawMessages(`{"content":"request"}`),
				rawMessages(`{"content":"response"}`),
			))
		}(index)
	}
	close(start)
	workers.Wait()
	stats := registry.stats()
	if stats.Entries > 20 || stats.Bytes > 2048 {
		t.Fatalf("concurrent stores exceeded limits: %+v", stats)
	}
}

func continuationDelta(input, output []json.RawMessage) openAIResponsesContinuation {
	return openAIResponsesContinuation{
		accountID: 1,
		input:     input,
		output:    output,
	}
}

func rawMessages(items ...string) []json.RawMessage {
	result := make([]json.RawMessage, len(items))
	for index, item := range items {
		result[index] = json.RawMessage(item)
	}
	return result
}

func rawMessageContents(items []json.RawMessage) []string {
	result := make([]string, len(items))
	for index, item := range items {
		var decoded struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(item, &decoded)
		result[index] = decoded.Content
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func BenchmarkResponsesMemoryGovernorRequestCycle(b *testing.B) {
	governor := newResponsesMemoryGovernor(responsesMemoryLimits{
		maxInflightRequests: 64,
		maxInflightBytes:    256 << 20,
		maxFallbacks:        4,
	})
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		lease, ok := governor.tryAcquireRequest(4096)
		if !ok {
			b.Fatal("request admission failed")
		}
		lease.release()
	}
}

func BenchmarkContinuationRegistryMaterializeTwentyTurns(b *testing.B) {
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl:          time.Hour,
		maxEntries:   100,
		maxItems:     100,
		maxItemBytes: 1 << 20,
		maxBytes:     1 << 20,
	})
	parentID := ""
	for index := 0; index < 20; index++ {
		responseID := "response_" + strconv.Itoa(index)
		registry.store(responseID, parentID, "", continuationDelta(
			rawMessages(`{"content":"request"}`),
			rawMessages(`{"content":"response"}`),
		))
		parentID = responseID
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, ok := registry.materialize(parentID); !ok {
			b.Fatal("materialize failed")
		}
	}
}
