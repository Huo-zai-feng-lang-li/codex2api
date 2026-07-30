package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponsesHTTPContinuationFallsBackToLocalHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	previousMode := openAIResponsesContinuityMode
	openAIResponsesContinuityMode = openAIResponsesContinuityModeUpstream
	t.Cleanup(func() { openAIResponsesContinuityMode = previousMode })

	var mu sync.Mutex
	requests := make([][]byte, 0, 5)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
			return
		}

		mu.Lock()
		requests = append(requests, append([]byte(nil), body...))
		attempt := len(requests)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch attempt {
		case 1:
			_, _ = w.Write([]byte(`{"id":"resp_root","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ALPHA"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		case 2:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"previous_response_id is only supported on Responses WebSocket v2"}}`))
		case 3:
			_, _ = w.Write([]byte(`{"id":"resp_next","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ALPHA"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`))
		case 4:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"previous_response_id is only supported on Responses WebSocket v2"}}`))
		case 5:
			_, _ = w.Write([]byte(`{"id":"resp_third","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ALPHA"}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}`))
		default:
			t.Errorf("unexpected upstream attempt %d", attempt)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(upstream.Close)

	handler := newFutureInputOpenAIResponsesHandler(upstream.URL)
	first := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"remember ALPHA","store":false}`), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200; body=%s", first.Code, first.Body.String())
	}

	second := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_root","input":"what token?","store":false}`), nil)
	if second.Code != http.StatusOK {
		t.Fatalf("continuation status = %d, want 200; body=%s", second.Code, second.Body.String())
	}
	if text := gjson.Get(second.Body.String(), "output.0.content.0.text").String(); text != "ALPHA" {
		t.Fatalf("continuation text = %q, want ALPHA; body=%s", text, second.Body.String())
	}

	third := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_next","input":"repeat it","store":false}`), nil)
	if third.Code != http.StatusOK {
		t.Fatalf("third status = %d, want 200; body=%s", third.Code, third.Body.String())
	}

	mu.Lock()
	got := append([][]byte(nil), requests...)
	mu.Unlock()
	if len(got) != 5 {
		t.Fatalf("upstream requests = %d, want 5", len(got))
	}
	fallback := got[2]
	if gjson.GetBytes(fallback, "previous_response_id").Exists() {
		t.Fatalf("fallback retained previous_response_id: %s", fallback)
	}
	input := gjson.GetBytes(fallback, "input").Array()
	if len(input) != 3 {
		t.Fatalf("fallback input count = %d, want 3; body=%s", len(input), fallback)
	}
	if text := input[0].Get("content").String(); text != "remember ALPHA" {
		t.Fatalf("root user input = %q, want remember ALPHA; body=%s", text, fallback)
	}
	if role := input[1].Get("role").String(); role != "assistant" {
		t.Fatalf("cached output role = %q, want assistant; body=%s", role, fallback)
	}
	if text := input[2].Get("content").String(); text != "what token?" {
		t.Fatalf("current user input = %q, want what token?; body=%s", text, fallback)
	}
	thirdFallback := got[4]
	thirdInput := gjson.GetBytes(thirdFallback, "input").Array()
	if len(thirdInput) != 5 {
		t.Fatalf("third fallback input count = %d, want 5; body=%s", len(thirdInput), thirdFallback)
	}
	if text := thirdInput[4].Get("content").String(); text != "repeat it" {
		t.Fatalf("third current input = %q, want repeat it; body=%s", text, thirdFallback)
	}
}

func TestOpenAIResponsesContinuationUsesResponseOwnerWhenAffinityIsOff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()

	var ownerMu sync.Mutex
	ownerRequests := make([][]byte, 0, 2)
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read owner body: %v", err)
			return
		}
		ownerMu.Lock()
		ownerRequests = append(ownerRequests, append([]byte(nil), body...))
		attempt := len(ownerRequests)
		ownerMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			_, _ = w.Write([]byte(`{"id":"resp_owned","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"BRAVO"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_owned_next","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"BRAVO"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(owner.Close)

	var otherCalls int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_fresh","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"FORGOT"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(other.Close)

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency: 2,
		TestModel:      "gpt-5.4",
		MaxRetries:     0,
		AffinityMode:   auth.AffinityModeOff,
	})
	store.AddAccount(&auth.Account{DBID: 1, Name: "owner", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: owner.URL, APIKey: "owner-key", Models: []string{"gpt-5.4"}})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

	headers := http.Header{"Session_id": []string{"thread-owner-test"}}
	first := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"remember BRAVO","store":true}`), headers)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200; body=%s", first.Code, first.Body.String())
	}
	// Make the second account strictly preferable to the ordinary scheduler.
	// Continuation ownership must still keep the request on the response owner.
	otherScoreBias := int64(10000)
	store.AddAccount(&auth.Account{
		DBID:              2,
		Name:              "other",
		UpstreamType:      auth.UpstreamOpenAIResponses,
		BaseURL:           other.URL,
		APIKey:            "other-key",
		Models:            []string{"gpt-5.4"},
		ScoreBiasOverride: &otherScoreBias,
	})
	second := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_owned","input":"what token?","store":true}`), headers)
	if second.Code != http.StatusOK {
		t.Fatalf("continuation status = %d, want 200; body=%s", second.Code, second.Body.String())
	}
	if text := gjson.Get(second.Body.String(), "output.0.content.0.text").String(); text != "BRAVO" {
		t.Fatalf("continuation text = %q, want BRAVO; body=%s", text, second.Body.String())
	}
	if otherCalls != 0 {
		t.Fatalf("non-owner upstream calls = %d, want 0", otherCalls)
	}

	ownerMu.Lock()
	ownerCount := len(ownerRequests)
	ownerMu.Unlock()
	if ownerCount != 2 {
		t.Fatalf("owner upstream calls = %d, want 2", ownerCount)
	}
}

func TestOpenAIResponsesCodexOwnerCanReplayLocalHistory(t *testing.T) {
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	owner := &auth.Account{DBID: 41, Name: "codex-owner"}
	cacheOpenAIResponsesContinuation(
		[]byte(`{"model":"gpt-5.4","input":"remember FOXTROT","store":false}`),
		[]byte(`{"id":"resp_codex_owner","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"FOXTROT"}]}]}`),
		owner,
		"session-codex-test",
	)

	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_codex_owner","input":"what token?","store":false}`)
	filter, bound := bindOpenAIResponsesContinuationOwner(request, "session-codex-test", nil)
	if !bound || !filter(owner) {
		t.Fatal("Codex response owner was not bound")
	}
	if filter(&auth.Account{DBID: 42, Name: "other"}) {
		t.Fatal("non-owner Codex account passed continuation filter")
	}
	fallback, ok := buildOpenAIResponsesContinuationFallback(request, "session-codex-test")
	if !ok {
		t.Fatal("Codex continuation history was not replayable")
	}
	if count := len(gjson.GetBytes(fallback, "input").Array()); count != 3 {
		t.Fatalf("fallback input count = %d, want 3; body=%s", count, fallback)
	}
}

func TestOpenAIResponsesStreamingContinuationFallsBackToLocalHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	previousMode := openAIResponsesContinuityMode
	openAIResponsesContinuityMode = openAIResponsesContinuityModeUpstream
	t.Cleanup(func() { openAIResponsesContinuityMode = previousMode })

	var mu sync.Mutex
	requests := make([][]byte, 0, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
			return
		}
		mu.Lock()
		requests = append(requests, append([]byte(nil), body...))
		attempt := len(requests)
		mu.Unlock()

		if attempt == 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"previous_response_id is only supported on Responses WebSocket v2"}}`))
			return
		}

		responseID := "resp_stream_root"
		if attempt == 3 {
			responseID = "resp_stream_next"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"" + responseID + "\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"CHARLIE\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	t.Cleanup(upstream.Close)

	handler := newFutureInputOpenAIResponsesHandler(upstream.URL)
	first := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"remember CHARLIE","stream":true,"store":false}`), nil)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "resp_stream_root") {
		t.Fatalf("first stream status/body = %d/%s", first.Code, first.Body.String())
	}
	second := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_stream_root","input":"what token?","stream":true,"store":false}`), nil)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "resp_stream_next") {
		t.Fatalf("continuation stream status/body = %d/%s", second.Code, second.Body.String())
	}

	mu.Lock()
	got := append([][]byte(nil), requests...)
	mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("upstream requests = %d, want 3", len(got))
	}
	if gjson.GetBytes(got[2], "previous_response_id").Exists() {
		t.Fatalf("stream fallback retained previous_response_id: %s", got[2])
	}
	if count := len(gjson.GetBytes(got[2], "input").Array()); count != 3 {
		t.Fatalf("stream fallback input count = %d, want 3; body=%s", count, got[2])
	}
}

func TestOpenAIResponsesContinuationReplaysHistoryAfterOwnerTransportFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()

	ownerCalls := 0
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerCalls++
		if ownerCalls == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_transport_owner","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"DELTA"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
			return
		}
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(owner.Close)

	var fallbackBody []byte
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_transport_next","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"DELTA"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`))
	}))
	t.Cleanup(fallback.Close)

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestModel: "gpt-5.4", MaxRetries: 2, AffinityMode: auth.AffinityModeOff})
	ownerScore := int64(10000)
	store.AddAccount(&auth.Account{DBID: 1, Name: "owner", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: owner.URL, APIKey: "owner-key", Models: []string{"gpt-5.4"}, ScoreBiasOverride: &ownerScore})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	first := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"remember DELTA","store":false}`), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200; body=%s", first.Code, first.Body.String())
	}
	store.AddAccount(&auth.Account{DBID: 2, Name: "fallback", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: fallback.URL, APIKey: "fallback-key", Models: []string{"gpt-5.4"}})
	second := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_transport_owner","input":"what token?","store":false}`), nil)
	if second.Code != http.StatusOK {
		t.Fatalf("continuation status = %d, want 200; body=%s", second.Code, second.Body.String())
	}
	if gjson.GetBytes(fallbackBody, "previous_response_id").Exists() {
		t.Fatalf("transport fallback retained previous_response_id: %s", fallbackBody)
	}
	if count := len(gjson.GetBytes(fallbackBody, "input").Array()); count != 3 {
		t.Fatalf("transport fallback input count = %d, want 3; body=%s", count, fallbackBody)
	}
}

func TestOpenAIResponsesContinuationReplaysHistoryAfterOwnerHTTPFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()

	ownerCalls := 0
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerCalls++
		w.Header().Set("Content-Type", "application/json")
		if ownerCalls == 1 {
			_, _ = w.Write([]byte(`{"id":"resp_http_owner","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ECHO"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":"Insufficient balance"}`))
	}))
	t.Cleanup(owner.Close)

	var fallbackBody []byte
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_http_next","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ECHO"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`))
	}))
	t.Cleanup(fallback.Close)

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestModel: "gpt-5.4", MaxRetries: 2, AffinityMode: auth.AffinityModeOff})
	ownerScore := int64(10000)
	store.AddAccount(&auth.Account{DBID: 1, Name: "owner", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: owner.URL, APIKey: "owner-key", Models: []string{"gpt-5.4"}, ScoreBiasOverride: &ownerScore})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	first := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"remember ECHO","store":false}`), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200; body=%s", first.Code, first.Body.String())
	}
	store.AddAccount(&auth.Account{DBID: 2, Name: "fallback", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: fallback.URL, APIKey: "fallback-key", Models: []string{"gpt-5.4"}})
	second := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_http_owner","input":"what token?","store":false}`), nil)
	if second.Code != http.StatusOK {
		t.Fatalf("continuation status = %d, want 200; body=%s", second.Code, second.Body.String())
	}
	if gjson.GetBytes(fallbackBody, "previous_response_id").Exists() {
		t.Fatalf("HTTP fallback retained previous_response_id: %s", fallbackBody)
	}
	if count := len(gjson.GetBytes(fallbackBody, "input").Array()); count != 3 {
		t.Fatalf("HTTP fallback input count = %d, want 3; body=%s", count, fallbackBody)
	}
}

func TestOpenAIResponsesOwnerFailureSwitchesAccountWithNormalizedToolHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	previousMode := openAIResponsesContinuityMode
	openAIResponsesContinuityMode = openAIResponsesContinuityModeUpstream
	t.Cleanup(func() { openAIResponsesContinuityMode = previousMode })

	ownerCalls := 0
	ownerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ownerCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"type":"server_error","message":"owner unavailable"}}`))
	}))
	t.Cleanup(ownerServer.Close)

	var fallbackBody []byte
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_switched","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	t.Cleanup(fallbackServer.Close)

	ownerScore := int64(10000)
	owner := &auth.Account{DBID: 41, Name: "owner", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: ownerServer.URL, APIKey: "owner", Models: []string{"gpt-5.4"}, ScoreBiasOverride: &ownerScore}
	fallback := &auth.Account{DBID: 42, Name: "fallback", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: fallbackServer.URL, APIKey: "fallback", Models: []string{"gpt-5.4"}}
	cacheOpenAIResponsesContinuation(
		[]byte(`{"model":"gpt-5.4","input":"run it","store":false}`),
		[]byte(`{"id":"resp_owner_tool","output":[{"type":"function_call","id":"fc_exec","call_id":"call_exec","name":"exec","arguments":"{}"}]}`),
		owner,
		"",
	)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestModel: "gpt-5.4", MaxRetries: 2, AffinityMode: auth.AffinityModeOff})
	store.AddAccount(owner)
	store.AddAccount(fallback)
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

	response := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_owner_tool","input":[{"type":"custom_tool_call_output","call_id":"call_exec","output":"ok"}],"store":false}`), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if ownerCalls != 1 {
		t.Fatalf("owner calls = %d, want exactly 1", ownerCalls)
	}
	if gjson.GetBytes(fallbackBody, "previous_response_id").Exists() {
		t.Fatalf("switched request retained previous_response_id: %s", fallbackBody)
	}
	input := gjson.GetBytes(fallbackBody, "input").Array()
	if len(input) != 3 || input[2].Get("type").String() != "function_call_output" {
		t.Fatalf("switched request did not contain normalized complete tool history: %s", fallbackBody)
	}
}

func TestOpenAIResponsesContinuationWithoutLocalHistoryDoesNotGuess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"previous_response_id is only supported on Responses WebSocket v2"}}`))
	}))
	t.Cleanup(upstream.Close)

	handler := newFutureInputOpenAIResponsesHandler(upstream.URL)
	response := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_missing","input":"continue","store":false}`), nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
}

func TestOpenAIResponsesContinuationFailsExplicitlyWhenFallbackIsSaturated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	previousMode := openAIResponsesContinuityMode
	openAIResponsesContinuityMode = openAIResponsesContinuityModeUpstream
	t.Cleanup(func() { openAIResponsesContinuityMode = previousMode })

	previousGovernor := defaultResponsesMemoryGovernor
	defaultResponsesMemoryGovernor = newResponsesMemoryGovernor(responsesMemoryLimits{
		maxInflightRequests: 4,
		maxInflightBytes:    1 << 20,
		maxFallbacks:        1,
	})
	t.Cleanup(func() { defaultResponsesMemoryGovernor = previousGovernor })

	occupied, ok := defaultResponsesMemoryGovernor.tryAcquireFallback()
	if !ok {
		t.Fatal("failed to occupy fallback capacity")
	}
	defer occupied.release()

	var mu sync.Mutex
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		attempt := attempts
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			_, _ = w.Write([]byte(`{"id":"resp_busy_root","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ALPHA"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"previous_response_id is only supported on Responses WebSocket v2"}}`))
	}))
	t.Cleanup(upstream.Close)

	handler := newFutureInputOpenAIResponsesHandler(upstream.URL)
	first := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"remember ALPHA","store":false}`), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("root status = %d, want 200; body=%s", first.Code, first.Body.String())
	}
	continuation := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_busy_root","input":"continue","store":false}`), nil)
	if continuation.Code != http.StatusServiceUnavailable {
		t.Fatalf("continuation status = %d, want 503; body=%s", continuation.Code, continuation.Body.String())
	}
	if code := gjson.Get(continuation.Body.String(), "error.code").String(); code != "local_continuation_busy" {
		t.Fatalf("error code = %q, want local_continuation_busy", code)
	}
	mu.Lock()
	gotAttempts := attempts
	mu.Unlock()
	if gotAttempts != 2 {
		t.Fatalf("upstream attempts = %d, want 2 without silent fallback", gotAttempts)
	}
}

func TestOpenAIResponsesThirdPartyContinuationReplaysLocallyBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	previousMode := openAIResponsesContinuityMode
	openAIResponsesContinuityMode = openAIResponsesContinuityModeAuto
	t.Cleanup(func() { openAIResponsesContinuityMode = previousMode })

	const token = "THIRD-PARTY-CONTINUITY"
	var mu sync.Mutex
	requests := make([][]byte, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
			return
		}
		mu.Lock()
		requests = append(requests, append([]byte(nil), body...))
		attempt := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			_, _ = w.Write([]byte(`{"id":"resp_third_party_root","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ACK"}]}]}`))
			return
		}
		if gjson.GetBytes(body, "previous_response_id").Exists() {
			_, _ = w.Write([]byte(`{"id":"resp_third_party_forgot","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I forgot"}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_third_party_next","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"THIRD-PARTY-CONTINUITY"}]}]}`))
	}))
	t.Cleanup(upstream.Close)

	handler := newFutureInputOpenAIResponsesHandler(upstream.URL)
	first := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"remember `+token+`","store":false}`), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("root status = %d, want 200; body=%s", first.Code, first.Body.String())
	}
	second := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_third_party_root","input":"repeat token","store":false}`), nil)
	if second.Code != http.StatusOK {
		t.Fatalf("continuation status = %d, want 200; body=%s", second.Code, second.Body.String())
	}
	if text := gjson.Get(second.Body.String(), "output.0.content.0.text").String(); text != token {
		t.Fatalf("continuation text = %q, want %q", text, token)
	}
	mu.Lock()
	got := append([][]byte(nil), requests...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("upstream requests = %d, want 2", len(got))
	}
	if gjson.GetBytes(got[1], "previous_response_id").Exists() {
		t.Fatalf("third-party continuation trusted upstream state: %s", got[1])
	}
	if count := len(gjson.GetBytes(got[1], "input").Array()); count != 3 {
		t.Fatalf("replayed input count = %d, want 3; body=%s", count, got[1])
	}
}

func performResponsesRequest(t *testing.T, handler *Handler, body []byte, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	ctx.Request = req
	handler.Responses(ctx)
	return recorder
}

func TestOpenAIResponsesSessionContinuationWithoutPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()

	var mu sync.Mutex
	requests := make([][]byte, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
			return
		}
		mu.Lock()
		requests = append(requests, append([]byte(nil), body...))
		attempt := len(requests)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			_, _ = w.Write([]byte(`{"id":"resp_sess_root","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SESSION_SECRET"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_sess_next","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SESSION_SECRET"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`))
	}))
	t.Cleanup(upstream.Close)

	handler := newFutureInputOpenAIResponsesHandler(upstream.URL)
	headers := http.Header{"Session_id": []string{"sess_123456"}}

	// 1st request: First turn with session_id
	first := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"my secret is SESSION_SECRET","store":false}`), headers)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200; body=%s", first.Code, first.Body.String())
	}

	// 2nd request: Second turn with SAME session_id but MISSING previous_response_id
	second := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"what was my secret?","store":false}`), headers)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200; body=%s", second.Code, second.Body.String())
	}

	mu.Lock()
	got := append([][]byte(nil), requests...)
	mu.Unlock()

	if len(got) != 2 {
		t.Fatalf("upstream requests count = %d, want 2", len(got))
	}

	// Verify that the second request payload expanded input from session continuity
	secondInput := gjson.GetBytes(got[1], "input").Array()
	if len(secondInput) != 3 {
		t.Fatalf("second request input count = %d, want 3 (replayed history + new input)", len(secondInput))
	}
	if text := secondInput[0].Get("content").String(); text != "my secret is SESSION_SECRET" {
		t.Fatalf("session replayed root text = %q, want my secret is SESSION_SECRET", text)
	}
}

func TestOpenAIResponsesPendingContinuationBindsOwnerDuringStream(t *testing.T) {
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	owner := &auth.Account{DBID: 99, Name: "stream-pending-owner"}

	// Register pending response during stream before response.completed arrives
	RegisterPendingOpenAIResponsesContinuation("resp_pending_123", "", "sess_pending_stream", owner)

	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_pending_123","input":"followup","store":false}`)
	filter, bound := bindOpenAIResponsesContinuationOwner(request, "sess_pending_stream", nil)
	if !bound {
		t.Fatal("Pending continuation owner was not bound")
	}
	if !filter(owner) {
		t.Fatal("Pending owner account did not pass filter")
	}
	if filter(&auth.Account{DBID: 100, Name: "other"}) {
		t.Fatal("Other account passed pending continuation filter")
	}
}

func TestOpenAIResponsesPendingFunctionCallCanBuildCompleteFallbackBeforeCompletion(t *testing.T) {
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	owner := &auth.Account{DBID: 101, Name: "interrupted-stream-owner"}
	requestBody := []byte(`{"model":"gpt-5.4","input":"look it up","stream":true,"store":false}`)

	RegisterPendingOpenAIResponsesContinuation("resp_interrupted", "", "sess_interrupted", owner)
	functionCall := gjson.Parse(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"alpha\"}"}`)
	if !AppendPendingOpenAIResponsesOutput("resp_interrupted", requestBody, functionCall) {
		t.Fatal("completed function_call should be appended to pending continuation")
	}
	if !AppendPendingOpenAIResponsesOutput("resp_interrupted", requestBody, functionCall) {
		t.Fatal("duplicate completed function_call should be accepted idempotently")
	}
	if AppendPendingOpenAIResponsesOutput("resp_interrupted", requestBody, gjson.Parse(`{"type":"function_call","call_id":"call_partial","name":"lookup"}`)) {
		t.Fatal("partial function_call must not be appended")
	}
	if AppendPendingOpenAIResponsesOutput("resp_interrupted", requestBody, gjson.Parse(`{"type":"function_call","call_id":"call_invalid","name":"lookup","arguments":"{"}`)) {
		t.Fatal("function_call with incomplete JSON arguments must not be appended")
	}

	history, ok := openAIResponsesContinuity.materialize("resp_interrupted")
	if !ok {
		t.Fatal("pending continuation should materialize after a complete output item")
	}
	if len(history) != 2 {
		t.Fatalf("materialized item count = %d, want 2; history=%s", len(history), history)
	}
	if callID := gjson.GetBytes(history[1], "call_id").String(); callID != "call_1" {
		t.Fatalf("materialized call_id = %q, want call_1; item=%s", callID, history[1])
	}

	fallback, ok := buildOpenAIResponsesContinuationFallback(
		[]byte(`{"model":"gpt-5.4","previous_response_id":"resp_interrupted","input":[{"type":"function_call_output","call_id":"call_1","output":"alpha"}],"store":false}`),
		"",
	)
	if !ok {
		t.Fatal("pending function call and current tool output should build a complete fallback")
	}
	input := gjson.GetBytes(fallback, "input").Array()
	if len(input) != 3 {
		t.Fatalf("fallback input count = %d, want 3; body=%s", len(input), fallback)
	}
	if input[1].Get("type").String() != "function_call" || input[2].Get("type").String() != "function_call_output" {
		t.Fatalf("fallback tool pair is incomplete or out of order: %s", fallback)
	}
}

func TestOpenAIResponsesFallbackUsesReplayableSessionHeadBehindEmptyPending(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	owner := &auth.Account{DBID: 787, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://owner.example", APIKey: "owner"}
	const sessionID = "session-replayable-head"

	openAIResponsesContinuity.store("resp_replayable_parent", "", sessionID, openAIResponsesContinuation{
		accountID: owner.ID(),
		baseURL:   "https://owner.example",
		input: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"run tool"}`),
		},
		output: []json.RawMessage{
			json.RawMessage(`{"type":"function_call","call_id":"call_replayable","name":"lookup","arguments":"{}"}`),
		},
	})
	RegisterPendingOpenAIResponsesContinuation("resp_empty_pending", "", sessionID, owner)

	fallback, ok := buildOpenAIResponsesContinuationFallback(
		[]byte(`{"model":"gpt-5.4","input":[{"type":"function_call_output","call_id":"call_replayable","output":"ok"}]}`),
		sessionID,
	)
	if !ok {
		t.Fatal("fallback should use the last replayable session head")
	}
	input := gjson.GetBytes(fallback, "input").Array()
	if len(input) != 3 {
		t.Fatalf("fallback input count = %d, want 3; body=%s", len(input), fallback)
	}
	if input[1].Get("call_id").String() != "call_replayable" {
		t.Fatalf("replayed call_id = %q, want call_replayable", input[1].Get("call_id").String())
	}
}

func TestOpenAIResponsesPendingOutputExtendsReplayableParentWithoutInput(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	owner := &auth.Account{DBID: 787, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://owner.example", APIKey: "owner"}
	const sessionID = "session-pending-output"

	openAIResponsesContinuity.store("resp_parent", "", sessionID, openAIResponsesContinuation{
		accountID: owner.ID(),
		baseURL:   "https://owner.example",
		input: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"continue"}`),
		},
		output: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ready"}]}`),
		},
	})
	RegisterPendingOpenAIResponsesContinuation("resp_pending_output", "resp_parent", sessionID, owner)

	item := gjson.Parse(`{"type":"function_call","call_id":"call_child","name":"lookup","arguments":"{}"}`)
	if !AppendPendingOpenAIResponsesOutput("resp_pending_output", []byte(`{"model":"gpt-5.4"}`), item) {
		t.Fatal("pending output without input should extend the replayable parent")
	}
	history, ok := openAIResponsesContinuity.materialize("resp_pending_output")
	if !ok {
		t.Fatal("pending output chain should materialize")
	}
	if got := gjson.GetBytes(history[len(history)-1], "call_id").String(); got != "call_child" {
		t.Fatalf("last call_id = %q, want call_child", got)
	}
}

func TestOpenAIResponsesUnavailableOwnerUsesReplayableSessionHead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	const sessionID = "session-owner-disabled"

	var fallbackBody []byte
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_switched","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	t.Cleanup(fallbackServer.Close)

	owner := &auth.Account{DBID: 787, Name: "owner", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://owner.example", APIKey: "owner", Models: []string{"gpt-5.4"}}
	fallback := &auth.Account{DBID: 788, Name: "fallback", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: fallbackServer.URL, APIKey: "fallback", Models: []string{"gpt-5.4"}}
	openAIResponsesContinuity.store("resp_replayable_owner", "", sessionID, openAIResponsesContinuation{
		accountID: owner.ID(),
		baseURL:   "https://owner.example",
		input: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"run"}`),
		},
		output: []json.RawMessage{
			json.RawMessage(`{"type":"function_call","call_id":"call_owner","name":"lookup","arguments":"{}"}`),
		},
	})
	RegisterPendingOpenAIResponsesContinuation("resp_empty_owner_pending", "", sessionID, owner)

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestModel: "gpt-5.4", MaxRetries: 0, AffinityMode: auth.AffinityModeOff})
	store.AddAccount(owner)
	store.AddAccount(fallback)
	atomic.StoreInt32(&owner.DispatchPaused, 1)
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	headers := http.Header{}
	headers.Set("session_id", sessionID)
	response := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":[{"type":"function_call_output","call_id":"call_owner","output":"ok"}],"store":false}`), headers)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if gjson.GetBytes(fallbackBody, "previous_response_id").Exists() {
		t.Fatalf("fallback retained previous_response_id: %s", fallbackBody)
	}
	input := gjson.GetBytes(fallbackBody, "input").Array()
	if len(input) != 3 || input[1].Get("call_id").String() != "call_owner" || input[2].Get("call_id").String() != "call_owner" {
		t.Fatalf("fallback did not receive complete replayed tool chain: %s", fallbackBody)
	}
	history, ok := openAIResponsesContinuity.materialize("resp_switched")
	if !ok {
		t.Fatal("switched response should remain replayable")
	}
	if len(history) != 4 {
		t.Fatalf("switched response history count = %d, want 4", len(history))
	}
	if gjson.GetBytes(history[1], "call_id").String() != "call_owner" || gjson.GetBytes(history[2], "call_id").String() != "call_owner" {
		t.Fatalf("switched response lost its replayed parent tool chain: %s", history)
	}
}

func TestOpenAIResponsesPendingToolCallsBuildCompleteFallback(t *testing.T) {
	tests := []struct {
		name           string
		call           string
		currentInput   string
		expectedTypes  []string
		expectedCallID string
	}{
		{
			name:           "function",
			call:           `{"type":"function_call","id":"fc_function","call_id":"call_function","name":"lookup","arguments":"{}"}`,
			currentInput:   `{"type":"function_call_output","call_id":"call_function","output":"ok"}`,
			expectedTypes:  []string{"message", "function_call", "function_call_output"},
			expectedCallID: "call_function",
		},
		{
			name:           "custom",
			call:           `{"type":"custom_tool_call","id":"ctc_custom","call_id":"call_custom","name":"exec","input":"echo ok","status":"completed"}`,
			currentInput:   `{"type":"custom_tool_call_output","call_id":"call_custom","output":"ok"}`,
			expectedTypes:  []string{"message", "custom_tool_call", "custom_tool_call_output"},
			expectedCallID: "call_custom",
		},
		{
			name:           "MCP server call",
			call:           `{"type":"mcp_call","id":"mcp_server","server_label":"files","name":"read","arguments":"{}","output":"ok","status":"completed"}`,
			currentInput:   `{"type":"message","role":"user","content":"continue"}`,
			expectedTypes:  []string{"message", "mcp_call", "message"},
			expectedCallID: "",
		},
		{
			name:           "MCP client output compatibility",
			call:           `{"type":"mcp_tool_call","id":"mcp_client","call_id":"call_mcp","name":"read","arguments":"{}","status":"completed"}`,
			currentInput:   `{"type":"mcp_tool_call_output","call_id":"call_mcp","output":"ok"}`,
			expectedTypes:  []string{"message", "mcp_tool_call", "mcp_tool_call_output"},
			expectedCallID: "call_mcp",
		},
		{
			name:           "tool search",
			call:           `{"type":"tool_search_call","id":"ts_search","call_id":"call_search","arguments":{"query":"docs"},"execution":"client","status":"completed"}`,
			currentInput:   `{"type":"tool_search_call_output","id":"tso_search","call_id":"call_search","output":[],"status":"completed"}`,
			expectedTypes:  []string{"message", "tool_search_call", "tool_search_call_output"},
			expectedCallID: "call_search",
		},
		{
			name:           "server tool search",
			call:           `{"type":"tool_search_call","id":"ts_server","call_id":"call_server_search","arguments":{"query":"docs"},"execution":"server","status":"completed"}`,
			currentInput:   `{"type":"message","role":"user","content":"continue"}`,
			expectedTypes:  []string{"message", "tool_search_call", "message"},
			expectedCallID: "",
		},
		{
			name:           "local shell",
			call:           `{"type":"local_shell_call","id":"lsc_shell","call_id":"call_shell","action":{"command":["echo","ok"]},"status":"completed"}`,
			currentInput:   `{"type":"local_shell_call_output","id":"call_shell","output":"{\"stdout\":\"ok\"}","status":"completed"}`,
			expectedTypes:  []string{"message", "local_shell_call", "local_shell_call_output"},
			expectedCallID: "call_shell",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetResponseCacheForTest()
			resetOpenAIResponsesContinuityForTest()
			responseID := "resp_" + strings.ReplaceAll(tt.name, " ", "_")
			requestBody := []byte(`{"model":"gpt-5.4","input":"run it","stream":true,"store":false}`)

			RegisterPendingOpenAIResponsesContinuation(responseID, "", "", &auth.Account{DBID: 101})
			if !AppendPendingOpenAIResponsesOutput(responseID, requestBody, gjson.Parse(tt.call)) {
				t.Fatalf("completed %s was not captured", tt.name)
			}
			fallback, ok := buildOpenAIResponsesContinuationFallback(
				[]byte(fmt.Sprintf(`{"model":"gpt-5.4","previous_response_id":%q,"input":[%s],"store":false}`, responseID, tt.currentInput)),
				"",
			)
			if !ok {
				t.Fatalf("%s pending history did not produce fallback", tt.name)
			}
			input := gjson.GetBytes(fallback, "input").Array()
			if len(input) != len(tt.expectedTypes) {
				t.Fatalf("input count = %d, want %d; body=%s", len(input), len(tt.expectedTypes), fallback)
			}
			for i, expectedType := range tt.expectedTypes {
				if actualType := input[i].Get("type").String(); actualType != expectedType {
					t.Fatalf("input[%d].type = %q, want %q; body=%s", i, actualType, expectedType, fallback)
				}
			}
			if tt.expectedCallID == "" {
				return
			}
			if callID := input[1].Get("call_id").String(); callID != tt.expectedCallID {
				t.Fatalf("call item id = %q, want %q; body=%s", callID, tt.expectedCallID, fallback)
			}
			outputID := input[2].Get("call_id").String()
			if outputID == "" {
				outputID = input[2].Get("id").String()
			}
			if outputID != tt.expectedCallID {
				t.Fatalf("output item id = %q, want %q; body=%s", outputID, tt.expectedCallID, fallback)
			}
		})
	}
}

func TestOpenAIResponsesInterruptedStreamReplaysAllToolCallTypes(t *testing.T) {
	tests := []struct {
		name         string
		call         string
		followup     string
		callType     string
		followupType string
		assertFields func(*testing.T, gjson.Result, gjson.Result)
	}{
		{
			name:         "function",
			call:         `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}`,
			followup:     `{"type":"function_call_output","call_id":"call_1","output":"ok"}`,
			callType:     "function_call",
			followupType: "function_call_output",
		},
		{
			name:         "custom",
			call:         `{"type":"custom_tool_call","id":"ct_1","call_id":"call_1","name":"exec","input":"echo ok","status":"completed"}`,
			followup:     `{"type":"custom_tool_call_output","call_id":"call_1","output":"ok"}`,
			callType:     "custom_tool_call",
			followupType: "custom_tool_call_output",
			assertFields: func(t *testing.T, call, _ gjson.Result) {
				t.Helper()
				if call.Get("input").String() != "echo ok" {
					t.Fatalf("custom input was not preserved: %s", call.Raw)
				}
			},
		},
		{
			name:         "tool search",
			call:         `{"type":"tool_search_call","id":"ts_1","call_id":"call_1","execution":"client","arguments":{"query":"weather"},"status":"completed"}`,
			followup:     `{"type":"tool_search_call_output","id":"tso_1","call_id":"call_1","output":[{"type":"function","name":"weather"}],"status":"completed"}`,
			callType:     "tool_search_call",
			followupType: "tool_search_call_output",
			assertFields: func(t *testing.T, call, output gjson.Result) {
				t.Helper()
				if call.Get("execution").String() != "client" || output.Get("output.0.name").String() != "weather" {
					t.Fatalf("tool-search fields were not preserved: call=%s output=%s", call.Raw, output.Raw)
				}
			},
		},
		{
			name:         "local shell",
			call:         `{"type":"local_shell_call","id":"ls_1","call_id":"call_1","action":{"type":"exec","command":["echo","ok"]},"status":"completed"}`,
			followup:     `{"type":"local_shell_call_output","id":"call_1","output":"{\"stdout\":\"ok\"}","status":"completed"}`,
			callType:     "local_shell_call",
			followupType: "local_shell_call_output",
			assertFields: func(t *testing.T, call, output gjson.Result) {
				t.Helper()
				if call.Get("action.command.0").String() != "echo" || output.Get("id").String() != "call_1" {
					t.Fatalf("local-shell correlation fields were not preserved: call=%s output=%s", call.Raw, output.Raw)
				}
			},
		},
		{
			name:         "MCP compatibility",
			call:         `{"type":"mcp_tool_call","id":"mcp_client","call_id":"call_1","name":"read_file","arguments":"{}","status":"completed"}`,
			followup:     `{"type":"mcp_tool_call_output","call_id":"call_1","output":"ok"}`,
			callType:     "mcp_tool_call",
			followupType: "mcp_tool_call_output",
		},
		{
			name:         "MCP",
			call:         `{"type":"mcp_call","id":"mcp_1","server_label":"filesystem","name":"read_file","arguments":"{\"path\":\"a.txt\"}","output":"ok","status":"completed"}`,
			followup:     `{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}`,
			callType:     "mcp_call",
			followupType: "message",
			assertFields: func(t *testing.T, call, _ gjson.Result) {
				t.Helper()
				if call.Get("id").String() != "mcp_1" || call.Get("output").String() != "ok" {
					t.Fatalf("MCP identity/output was not preserved: %s", call.Raw)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetResponseCacheForTest()
			resetOpenAIResponsesContinuityForTest()
			owner := &auth.Account{DBID: 101, Name: "interrupted-stream-owner"}
			requestBody := []byte(`{"model":"gpt-5.4","input":"run it","stream":true,"store":false}`)
			responseID := trackOpenAIResponsesContinuationSSEEvent(
				gjson.Parse(`{"type":"response.created","response":{"id":"resp_all_tools"}}`),
				requestBody,
				"sess_all_tools",
				owner,
				"",
			)
			trackOpenAIResponsesContinuationSSEEvent(
				gjson.Parse(fmt.Sprintf(`{"type":"response.output_item.done","item":%s}`, tt.call)),
				requestBody,
				"sess_all_tools",
				owner,
				responseID,
			)

			followup := []byte(fmt.Sprintf(`{"model":"gpt-5.4","previous_response_id":"resp_all_tools","input":[%s],"store":false}`, tt.followup))
			fallback, ok := buildOpenAIResponsesContinuationFallback(followup, "")
			if !ok {
				t.Fatal("completed output_item.done should remain replayable after interruption")
			}
			if gjson.GetBytes(fallback, "previous_response_id").Exists() {
				t.Fatalf("fallback retained previous_response_id: %s", fallback)
			}
			input := gjson.GetBytes(fallback, "input").Array()
			if len(input) != 3 {
				t.Fatalf("fallback input count = %d, want 3; body=%s", len(input), fallback)
			}
			if input[1].Get("type").String() != tt.callType || input[2].Get("type").String() != tt.followupType {
				t.Fatalf("fallback item order/types are wrong: %s", fallback)
			}
			if tt.assertFields != nil {
				tt.assertFields(t, input[1], input[2])
			}
		})
	}
}

func TestOpenAIResponsesInterruptedStreamReplaysPendingFunctionCallOnFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()

	var ownerMu sync.Mutex
	ownerCalls := 0
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerMu.Lock()
		ownerCalls++
		attempt := ownerCalls
		ownerMu.Unlock()
		if attempt > 1 {
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_pending_tool\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"fc_pending\",\"call_id\":\"call_pending\",\"name\":\"lookup\",\"arguments\":\"{}\"}}\n\n"))
	}))
	t.Cleanup(owner.Close)

	var fallbackBody []byte
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_after_pending","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	t.Cleanup(fallback.Close)

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestModel: "gpt-5.4", MaxRetries: 2, AffinityMode: auth.AffinityModeOff})
	ownerScore := int64(10000)
	store.AddAccount(&auth.Account{DBID: 1, Name: "owner", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: owner.URL, APIKey: "owner-key", Models: []string{"gpt-5.4"}, ScoreBiasOverride: &ownerScore})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	first := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"look it up","stream":true,"store":false}`), nil)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "call_pending") {
		t.Fatalf("interrupted stream status/body = %d/%s", first.Code, first.Body.String())
	}

	store.AddAccount(&auth.Account{DBID: 2, Name: "fallback", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: fallback.URL, APIKey: "fallback-key", Models: []string{"gpt-5.4"}})
	second := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_pending_tool","input":[{"type":"function_call_output","call_id":"call_pending","output":"ok"}],"store":false}`), nil)
	if second.Code != http.StatusOK {
		t.Fatalf("fallback status = %d, want 200; body=%s", second.Code, second.Body.String())
	}
	if gjson.GetBytes(fallbackBody, "previous_response_id").Exists() {
		t.Fatalf("fallback retained previous_response_id: %s", fallbackBody)
	}
	input := gjson.GetBytes(fallbackBody, "input").Array()
	if len(input) != 3 || input[1].Get("call_id").String() != "call_pending" || input[2].Get("call_id").String() != "call_pending" {
		t.Fatalf("fallback did not contain the complete pending tool pair: %s", fallbackBody)
	}
}

func TestOpenAIResponsesUnavailableOwnerWithIncompleteContextReturnsExplicitError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		output string
	}{
		{name: "function", output: `{"type":"function_call_output","call_id":"call_missing","output":"x"}`},
		{name: "custom", output: `{"type":"custom_tool_call_output","call_id":"call_missing","output":"x"}`},
		{name: "MCP compatibility", output: `{"type":"mcp_tool_call_output","call_id":"call_missing","output":"x"}`},
		{name: "tool search", output: `{"type":"tool_search_call_output","call_id":"call_missing","output":[]}`},
		{name: "local shell", output: `{"type":"local_shell_call_output","id":"call_missing","output":"{}"}`},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetResponseCacheForTest()
			resetOpenAIResponsesContinuityForTest()
			ownerServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			ownerURL := ownerServer.URL
			ownerServer.Close()
			owner := &auth.Account{DBID: int64(11 + i), Name: "unavailable-owner", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: ownerURL, APIKey: "owner-key", Models: []string{"gpt-5.4"}}
			responseID := fmt.Sprintf("resp_incomplete_%d", i)
			RegisterPendingOpenAIResponsesContinuation(responseID, "", "", owner)
			store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 0, AffinityMode: auth.AffinityModeOff})
			store.AddAccount(owner)
			handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

			body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","previous_response_id":%q,"input":[%s],"store":false}`, responseID, tt.output))
			response := performResponsesRequest(t, handler, body, nil)
			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%s", response.Code, response.Body.String())
			}
			if code := gjson.Get(response.Body.String(), "error.code").String(); code != "continuation_context_incomplete" {
				t.Fatalf("error.code = %q, want continuation_context_incomplete; body=%s", code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "no_available_account") {
				t.Fatalf("incomplete continuation was misreported as account exhaustion: %s", response.Body.String())
			}
		})
	}
}

func TestOpenAIResponsesCustomToolOutputErrorRetriesNormalizedFallbackOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	previousMode := openAIResponsesContinuityMode
	openAIResponsesContinuityMode = openAIResponsesContinuityModeUpstream
	t.Cleanup(func() { openAIResponsesContinuityMode = previousMode })

	var mu sync.Mutex
	requests := make([][]byte, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, append([]byte(nil), body...))
		attempt := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"No tool output found for custom tool call fc_retry"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_retry_ok","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	t.Cleanup(upstream.Close)

	account := &auth.Account{DBID: 21, Name: "retry-owner", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: upstream.URL, APIKey: "owner-key", Models: []string{"gpt-5.4"}}
	cacheOpenAIResponsesContinuation(
		[]byte(`{"model":"gpt-5.4","input":"look it up","store":false}`),
		[]byte(`{"id":"resp_retry_tool","output":[{"type":"function_call","id":"fc_retry","call_id":"call_retry","name":"lookup","arguments":"{}"}]}`),
		account,
		"",
	)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 0, AffinityMode: auth.AffinityModeOff})
	store.AddAccount(account)
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	response := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_retry_tool","input":[{"type":"custom_tool_call_output","call_id":"call_retry","output":"ok"}],"store":false}`), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	mu.Lock()
	got := append([][]byte(nil), requests...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("upstream requests = %d, want exactly 2", len(got))
	}
	if gjson.GetBytes(got[1], "previous_response_id").Exists() || len(gjson.GetBytes(got[1], "input").Array()) != 3 {
		t.Fatalf("retry did not use one complete local fallback: %s", got[1])
	}
	if typ := gjson.GetBytes(got[1], "input.2.type").String(); typ != "function_call_output" {
		t.Fatalf("retry output type = %q, want function_call_output; body=%s", typ, got[1])
	}
}

func TestOpenAIResponsesMissingToolOutputErrorKinds(t *testing.T) {
	tests := []struct {
		name    string
		message string
		status  int
		want    bool
	}{
		{name: "function", message: "No tool output found for function call call_1", status: http.StatusBadRequest, want: true},
		{name: "custom", message: "No tool output found for custom tool call fc_1", status: http.StatusBadRequest, want: true},
		{name: "MCP", message: "No tool output found for MCP tool call mcp_1", status: http.StatusBadRequest, want: true},
		{name: "tool search", message: "No tool output found for tool search call ts_1", status: http.StatusBadRequest, want: true},
		{name: "local shell", message: "No tool output found for local shell call ls_1", status: http.StatusBadRequest, want: true},
		{name: "unrelated validation", message: "No tool output field is allowed here", status: http.StatusBadRequest, want: false},
		{name: "wrong status", message: "No tool output found for custom tool call fc_1", status: http.StatusInternalServerError, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"error":{"message":%q}}`, tt.message))
			if got := isMissingOpenAIResponsesToolOutputError(tt.status, body); got != tt.want {
				t.Fatalf("matched = %v, want %v for %q", got, tt.want, tt.message)
			}
		})
	}
}

func TestOpenAIResponsesRetryExhaustionReturnsLastUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"type":"server_error","message":"origin unavailable"}}`))
	}))
	t.Cleanup(upstream.Close)

	account := &auth.Account{DBID: 31, Name: "only-account", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: upstream.URL, APIKey: "key", Models: []string{"gpt-5.4"}}
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 1, AffinityMode: auth.AffinityModeOff})
	store.AddAccount(account)
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

	response := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"hello","store":false}`), nil)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", response.Code, response.Body.String())
	}
	if message := gjson.Get(response.Body.String(), "error.message").String(); !strings.Contains(message, "origin unavailable") {
		t.Fatalf("last upstream error was lost: %s", response.Body.String())
	}
}

func TestOpenAIResponsesTransportRetryExhaustionPreservesFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	upstreamURL := closed.URL
	closed.Close()

	request := []byte(`{"model":"gpt-5.4","input":"hello","store":false}`)
	perform := func(maxRetries int) *httptest.ResponseRecorder {
		account := &auth.Account{DBID: int64(70 + maxRetries), Name: "closed-upstream", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: upstreamURL, APIKey: "key", Models: []string{"gpt-5.4"}}
		store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: maxRetries, AffinityMode: auth.AffinityModeOff})
		store.AddAccount(account)
		return performResponsesRequest(t, NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil), request, nil)
	}

	withoutRetry := perform(0)
	withRetry := perform(1)
	if withRetry.Code != withoutRetry.Code {
		t.Fatalf("transport status changed after retry exhaustion: without=%d with=%d; body=%s", withoutRetry.Code, withRetry.Code, withRetry.Body.String())
	}
	if code := gjson.Get(withRetry.Body.String(), "error.code").String(); code == "no_available_account" {
		t.Fatalf("transport failure was rewritten as no_available_account: %s", withRetry.Body.String())
	}
}

func TestOpenAIResponsesFallbackRejectsUnmatchedFunctionCallOutput(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	openAIResponsesContinuity.store("resp_without_call", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
	})
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_without_call","input":[{"type":"function_call_output","call_id":"call_missing","output":"x"}]}`)
	if fallback, ok := buildOpenAIResponsesContinuationFallback(request, ""); ok || !bytes.Equal(fallback, request) {
		t.Fatalf("unmatched tool output must not produce fallback: %s", fallback)
	}
}

func TestOpenAIResponsesFallbackAcceptsMatchedCodexToolOutputs(t *testing.T) {
	tests := []struct {
		name       string
		callType   string
		outputType string
	}{
		{name: "custom tool", callType: "custom_tool_call", outputType: "custom_tool_call_output"},
		{name: "MCP tool", callType: "mcp_tool_call", outputType: "mcp_tool_call_output"},
		{name: "tool search", callType: "tool_search_call", outputType: "tool_search_call_output"},
		{name: "local shell", callType: "local_shell_call", outputType: "local_shell_call_output"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetOpenAIResponsesContinuityForTest()
			openAIResponsesContinuity.store("resp_tool", "", "", openAIResponsesContinuation{
				input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"run it"}`)},
				output: []json.RawMessage{json.RawMessage(fmt.Sprintf(
					`{"type":%q,"call_id":"call_tool","name":"exec","arguments":"{}"}`,
					tt.callType,
				))},
			})
			request := []byte(fmt.Sprintf(
				`{"model":"gpt-5.4","previous_response_id":"resp_tool","input":[{"type":%q,"call_id":"call_tool","output":"ok"}]}`,
				tt.outputType,
			))

			fallback, ok := buildOpenAIResponsesContinuationFallback(request, "")
			if !ok {
				t.Fatalf("matched %s must produce fallback", tt.outputType)
			}
			if gjson.GetBytes(fallback, "previous_response_id").Exists() {
				t.Fatalf("fallback retained previous_response_id: %s", fallback)
			}
			if got := len(gjson.GetBytes(fallback, "input").Array()); got != 3 {
				t.Fatalf("fallback input items = %d, want 3; body=%s", got, fallback)
			}
		})
	}
}

func TestOpenAIResponsesFallbackNormalizesCodexOutputToStoredCallType(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	openAIResponsesContinuity.store("resp_exec", "", "", openAIResponsesContinuation{
		input:  []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"run it"}`)},
		output: []json.RawMessage{json.RawMessage(`{"type":"function_call","id":"fc_exec","call_id":"call_exec","name":"exec","arguments":"{}"}`)},
	})
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_exec","input":[{"type":"custom_tool_call_output","call_id":"call_exec","output":"ok"}]}`)

	fallback, ok := buildOpenAIResponsesContinuationFallback(request, "")
	if !ok {
		t.Fatal("Codex custom tool output paired with a stored function call must produce fallback")
	}
	input := gjson.GetBytes(fallback, "input").Array()
	if len(input) != 3 {
		t.Fatalf("fallback input items = %d, want 3; body=%s", len(input), fallback)
	}
	if got := input[2].Get("type").String(); got != "function_call_output" {
		t.Fatalf("normalized output type = %q, want function_call_output; body=%s", got, fallback)
	}
}

func TestOpenAIResponsesFallbackNormalizesLocalShellCorrelationField(t *testing.T) {
	tests := []struct {
		name           string
		call           string
		output         string
		expectedType   string
		expectedField  string
		forbiddenField string
	}{
		{
			name:           "local shell output to function output",
			call:           `{"type":"function_call","id":"fc_exec","call_id":"call_exec","name":"exec","arguments":"{}"}`,
			output:         `{"type":"local_shell_call_output","id":"call_exec","output":"{}"}`,
			expectedType:   "function_call_output",
			expectedField:  "call_id",
			forbiddenField: "id",
		},
		{
			name:           "function output to local shell output",
			call:           `{"type":"local_shell_call","id":"ls_exec","call_id":"call_exec","action":{"command":["echo","ok"]},"status":"completed"}`,
			output:         `{"type":"function_call_output","call_id":"call_exec","output":"{}"}`,
			expectedType:   "local_shell_call_output",
			expectedField:  "id",
			forbiddenField: "call_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetOpenAIResponsesContinuityForTest()
			openAIResponsesContinuity.store("resp_local_shell_fields", "", "", openAIResponsesContinuation{
				input:  []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"run it"}`)},
				output: []json.RawMessage{json.RawMessage(tt.call)},
			})
			request := []byte(fmt.Sprintf(`{"model":"gpt-5.4","previous_response_id":"resp_local_shell_fields","input":[%s]}`, tt.output))

			fallback, ok := buildOpenAIResponsesContinuationFallback(request, "")
			if !ok {
				t.Fatal("matched cross-type tool output must produce fallback")
			}
			output := gjson.GetBytes(fallback, "input.2")
			if got := output.Get("type").String(); got != tt.expectedType {
				t.Fatalf("output type = %q, want %q; body=%s", got, tt.expectedType, fallback)
			}
			if got := output.Get(tt.expectedField).String(); got != "call_exec" {
				t.Fatalf("%s = %q, want call_exec; body=%s", tt.expectedField, got, fallback)
			}
			if output.Get(tt.forbiddenField).Exists() {
				t.Fatalf("forbidden field %s remained after normalization: %s", tt.forbiddenField, fallback)
			}
		})
	}
}

func TestOpenAIResponsesFallbackRejectsMismatchedCodexToolOutput(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	openAIResponsesContinuity.store("resp_custom", "", "", openAIResponsesContinuation{
		input:  []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"run it"}`)},
		output: []json.RawMessage{json.RawMessage(`{"type":"custom_tool_call","call_id":"call_other","name":"exec","arguments":"{}"}`)},
	})
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_custom","input":[{"type":"custom_tool_call_output","call_id":"call_missing","output":"ok"}]}`)
	if fallback, ok := buildOpenAIResponsesContinuationFallback(request, ""); ok || !bytes.Equal(fallback, request) {
		t.Fatalf("unmatched custom tool output must not produce fallback: %s", fallback)
	}
}

func TestOpenAIResponsesFallbackRejectsPartialToolOutputSet(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	openAIResponsesAutoCompleteToolOutputs = false
	defer func() { openAIResponsesAutoCompleteToolOutputs = true }()

	openAIResponsesContinuity.store("resp_partial_tools", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"run both"}`)},
		output: []json.RawMessage{
			json.RawMessage(`{"type":"function_call","call_id":"call_one","name":"first","arguments":"{}"}`),
			json.RawMessage(`{"type":"function_call","call_id":"call_two","name":"second","arguments":"{}"}`),
		},
	})
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_partial_tools","input":[{"type":"function_call_output","call_id":"call_one","output":"ok"}]}`)

	if fallback, ok := buildOpenAIResponsesContinuationFallback(request, ""); ok || !bytes.Equal(fallback, request) {
		t.Fatalf("partial tool output set under strict mode must not produce fallback: %s", fallback)
	}
}

func TestOpenAIResponsesFallbackAutoCompletesPartialToolOutputSet(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	openAIResponsesAutoCompleteToolOutputs = true

	openAIResponsesContinuity.store("resp_partial_tools_auto", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"run both"}`)},
		output: []json.RawMessage{
			json.RawMessage(`{"type":"function_call","call_id":"call_one","name":"first","arguments":"{}"}`),
			json.RawMessage(`{"type":"function_call","call_id":"call_two","name":"second","arguments":"{}"}`),
		},
	})
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_partial_tools_auto","input":[{"type":"function_call_output","call_id":"call_one","output":"ok"}]}`)

	fallback, ok := buildOpenAIResponsesContinuationFallback(request, "")
	if !ok {
		t.Fatalf("partial tool output set under auto-complete mode should seamlessly produce fallback")
	}
	input := gjson.GetBytes(fallback, "input").Array()
	if len(input) != 5 {
		t.Fatalf("fallback input items count = %d, want 5; body=%s", len(input), fallback)
	}
	if input[3].Get("call_id").String() != "call_two" || input[3].Get("type").String() != "function_call_output" {
		t.Fatalf("missing tool call Output was not auto-completed with synthetic output: %s", fallback)
	}
}

func TestOpenAIResponsesFallbackStripsMissingPreviousID(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	openAIResponsesContinuity.store("resp_missing_in_store", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"init"}`)},
	})

	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_missing_in_store","input":[{"type":"message","role":"user","content":"hello"}]}`)

	if !canBuildOpenAIResponsesContinuationFallback(request, "") {
		t.Fatalf("request with missing previous_response_id must be fallback-buildable")
	}
	fallback, ok := buildOpenAIResponsesContinuationFallback(request, "")
	if !ok {
		t.Fatalf("buildOpenAIResponsesContinuationFallback failed for missing previous_response_id")
	}
	if gjson.GetBytes(fallback, "previous_response_id").Exists() {
		t.Fatalf("fallback must strip missing previous_response_id: %s", fallback)
	}
	input := gjson.GetBytes(fallback, "input").Array()
	if len(input) != 2 || input[1].Get("content").String() != "hello" {
		t.Fatalf("fallback input matches original user content: %s", fallback)
	}
}

func TestOpenAIResponsesFallbackRejectsDuplicateToolOutputs(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	openAIResponsesContinuity.store("resp_duplicate_output", "", "", openAIResponsesContinuation{
		input:  []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"run it"}`)},
		output: []json.RawMessage{json.RawMessage(`{"type":"custom_tool_call","call_id":"call_dup","name":"exec","input":"echo ok"}`)},
	})
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_duplicate_output","input":[{"type":"custom_tool_call_output","call_id":"call_dup","output":"first"},{"type":"custom_tool_call_output","call_id":"call_dup","output":"second"}]}`)

	if fallback, ok := buildOpenAIResponsesContinuationFallback(request, ""); ok || !bytes.Equal(fallback, request) {
		t.Fatalf("duplicate tool outputs must not produce fallback: %s", fallback)
	}
}

func TestPrepareOpenAIResponsesWebSocketContinuationReleasesOwnerAfterReplay(t *testing.T) {
	tests := []struct {
		name           string
		call           string
		followup       string
		expectedCall   string
		expectedOutput string
	}{
		{name: "function normalization", call: `{"type":"function_call","id":"fc_ws","call_id":"call_ws","name":"exec","arguments":"{}"}`, followup: `{"type":"custom_tool_call_output","call_id":"call_ws","output":"ok"}`, expectedCall: "function_call", expectedOutput: "function_call_output"},
		{name: "custom", call: `{"type":"custom_tool_call","id":"ct_ws","call_id":"call_ws","name":"exec","input":"echo ok","status":"completed"}`, followup: `{"type":"custom_tool_call_output","call_id":"call_ws","output":"ok"}`, expectedCall: "custom_tool_call", expectedOutput: "custom_tool_call_output"},
		{name: "tool search", call: `{"type":"tool_search_call","id":"ts_ws","call_id":"call_ws","execution":"client","arguments":{},"status":"completed"}`, followup: `{"type":"tool_search_call_output","call_id":"call_ws","output":[]}`, expectedCall: "tool_search_call", expectedOutput: "tool_search_call_output"},
		{name: "local shell", call: `{"type":"local_shell_call","id":"ls_ws","call_id":"call_ws","action":{"command":["echo","ok"]},"status":"completed"}`, followup: `{"type":"local_shell_call_output","id":"call_ws","output":"{}"}`, expectedCall: "local_shell_call", expectedOutput: "local_shell_call_output"},
		{name: "MCP", call: `{"type":"mcp_call","id":"mcp_ws","server_label":"files","name":"read","arguments":"{}","output":"ok","status":"completed"}`, followup: `{"type":"message","role":"user","content":"continue"}`, expectedCall: "mcp_call", expectedOutput: "message"},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetOpenAIResponsesContinuityForTest()
			owner := &auth.Account{DBID: int64(51 + i), UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://owner.example", APIKey: "owner"}
			fallback := &auth.Account{DBID: int64(61 + i), UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://fallback.example", APIKey: "fallback"}
			responseID := fmt.Sprintf("resp_ws_tool_%d", i)
			cacheOpenAIResponsesContinuation(
				[]byte(`{"model":"gpt-5.4","input":"run it"}`),
				[]byte(fmt.Sprintf(`{"id":%q,"output":[%s]}`, responseID, tt.call)),
				owner,
				"",
			)
			request := []byte(fmt.Sprintf(`{"model":"gpt-5.4","previous_response_id":%q,"input":[%s]}`, responseID, tt.followup))

			body, filter, replayed, ownerBound := prepareOpenAIResponsesWebSocketContinuation(request, "", func(*auth.Account) bool { return true })
			if !replayed {
				t.Fatal("complete WebSocket continuation should replay local history")
			}
			if ownerBound {
				t.Fatal("complete WebSocket replay must release owner binding")
			}
			if gjson.GetBytes(body, "previous_response_id").Exists() {
				t.Fatalf("replayed WebSocket body retained previous_response_id: %s", body)
			}
			if !filter(owner) || !filter(fallback) {
				t.Fatal("local WebSocket replay must release the owner-only account filter")
			}
			input := gjson.GetBytes(body, "input").Array()
			if len(input) != 3 || input[1].Get("type").String() != tt.expectedCall || input[2].Get("type").String() != tt.expectedOutput {
				t.Fatalf("WebSocket replay did not preserve complete tool history: %s", body)
			}
		})
	}
}

func TestPrepareOpenAIResponsesWebSocketContinuationKeepsOwnerWithoutReplay(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	owner := &auth.Account{DBID: 61, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://owner.example", APIKey: "owner"}
	fallback := &auth.Account{DBID: 62, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://fallback.example", APIKey: "fallback"}
	RegisterPendingOpenAIResponsesContinuation("resp_ws_incomplete", "", "", owner)
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_ws_incomplete","input":[{"type":"function_call_output","call_id":"call_missing","output":"x"}]}`)

	body, filter, replayed, ownerBound := prepareOpenAIResponsesWebSocketContinuation(request, "", func(*auth.Account) bool { return true })
	if replayed || !bytes.Equal(body, request) {
		t.Fatalf("incomplete WebSocket continuation must not be replayed: %s", body)
	}
	if !ownerBound {
		t.Fatal("incomplete WebSocket continuation must report owner binding")
	}
	if !filter(owner) || filter(fallback) {
		t.Fatal("incomplete WebSocket continuation must remain owner-bound")
	}
}

func TestPrepareOpenAIResponsesWebSocketContinuationReportsIncompleteToolContext(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	owner := &auth.Account{DBID: 71, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://owner.example", APIKey: "owner"}
	RegisterPendingOpenAIResponsesContinuation("resp_ws_missing_tool", "", "", owner)
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_ws_missing_tool","input":[{"type":"custom_tool_call_output","call_id":"call_missing","output":"x"}]}`)

	body, filter, replayed, ownerBound := prepareOpenAIResponsesWebSocketContinuation(request, "", func(*auth.Account) bool { return true })
	if replayed || !ownerBound {
		t.Fatalf("replayed = %v, ownerBound = %v; want owner-bound incomplete continuation", replayed, ownerBound)
	}
	if !bytes.Equal(body, request) || !filter(owner) {
		t.Fatalf("incomplete continuation changed request or owner filter: %s", body)
	}
}

func TestReplayableOpenAIResponsesItemRequiresEncryptedReasoningState(t *testing.T) {
	t.Run("preserves encrypted reasoning state", func(t *testing.T) {
		item := gjson.Parse(`{"id":"rs_1","type":"reasoning","encrypted_content":"opaque","summary":[]}`)
		got, ok := replayableOpenAIResponsesItem(item)
		if !ok {
			t.Fatal("encrypted reasoning item should remain replayable")
		}
		if encrypted := gjson.GetBytes(got, "encrypted_content").String(); encrypted != "opaque" {
			t.Fatalf("encrypted_content = %q, want opaque; item=%s", encrypted, got)
		}
		if id := gjson.GetBytes(got, "id").String(); id != "rs_1" {
			t.Fatalf("encrypted reasoning id = %q, want rs_1; item=%s", id, got)
		}
	})

	t.Run("drops reasoning without encrypted state", func(t *testing.T) {
		item := gjson.Parse(`{"id":"rs_2","type":"reasoning","summary":[]}`)
		if got, ok := replayableOpenAIResponsesItem(item); ok || got != nil {
			t.Fatalf("reasoning item without encrypted_content should not be replayed: %s", got)
		}
	})

	t.Run("drops reasoning with empty encrypted state", func(t *testing.T) {
		item := gjson.Parse(`{"id":"rs_3","type":"reasoning","encrypted_content":"  ","summary":[]}`)
		if got, ok := replayableOpenAIResponsesItem(item); ok || got != nil {
			t.Fatalf("reasoning item with empty encrypted_content should not be replayed: %s", got)
		}
	})
}

func TestOpenAIResponsesContinuationSanitizesLegacyReasoningHistory(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	openAIResponsesContinuity.store("resp_legacy_reasoning", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"remember ALPHA"}`),
		},
		output: []json.RawMessage{
			json.RawMessage(`{"id":"rs_legacy","type":"reasoning","summary":[]}`),
			json.RawMessage(`{"id":"msg_legacy","type":"message","role":"assistant","content":"ALPHA"}`),
		},
	})

	fallback, ok := buildOpenAIResponsesContinuationFallback(
		[]byte(`{"model":"gpt-5.4","previous_response_id":"resp_legacy_reasoning","input":"repeat it","store":false}`),
		"",
	)
	if !ok {
		t.Fatal("legacy continuation should remain replayable after sanitization")
	}
	input := gjson.GetBytes(fallback, "input").Array()
	if len(input) != 3 {
		t.Fatalf("fallback input count = %d, want 3; body=%s", len(input), fallback)
	}
	for index, item := range input {
		if item.Get("type").String() == "reasoning" {
			t.Fatalf("fallback input[%d] contains reasoning without encrypted_content: %s", index, fallback)
		}
	}
}

func TestOpenAIResponsesThreeTurnContinuationWithAccountSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()

	sessionID := "019fb196-1622-7df3-8c14-996e482832ec"

	// Turn 1: Owner returns a custom_tool_call
	var ownerMu sync.Mutex
	ownerCalls := 0
	ownerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerMu.Lock()
		ownerCalls++
		ownerMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_turn_1\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"custom_tool_call\",\"id\":\"ct_1\",\"call_id\":\"call_1785405835851080000_80000\",\"name\":\"read_file\",\"input\":\"doc.txt\"}}\n\n"))
	}))
	t.Cleanup(ownerServer.Close)

	// Turn 2 & 3: Fallback server
	var fallbackMu sync.Mutex
	fallbackRequests := make([][]byte, 0, 2)
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fallbackMu.Lock()
		fallbackRequests = append(fallbackRequests, append([]byte(nil), body...))
		callCount := len(fallbackRequests)
		fallbackMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			_, _ = w.Write([]byte(`{"id":"resp_turn_2","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"file content ok"}]}]}`))
		} else {
			_, _ = w.Write([]byte(`{"id":"resp_turn_3","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary ok"}]}]}`))
		}
	}))
	t.Cleanup(fallbackServer.Close)

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestModel: "gpt-5.4", MaxRetries: 0, AffinityMode: auth.AffinityModeOff})
	ownerScore := int64(10000)
	ownerAccount := &auth.Account{DBID: 1001, Name: "owner-acc", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: ownerServer.URL, APIKey: "owner-key", Models: []string{"gpt-5.4"}, ScoreBiasOverride: &ownerScore}
	fallbackAccount := &auth.Account{DBID: 1002, Name: "fallback-acc", UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: fallbackServer.URL, APIKey: "fallback-key", Models: []string{"gpt-5.4"}}

	store.AddAccount(ownerAccount)
	store.AddAccount(fallbackAccount)

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	headers := http.Header{}
	headers.Set("session_id", sessionID)

	// --- Turn 1 ---
	turn1Resp := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"read doc.txt","stream":true,"store":false}`), headers)
	if turn1Resp.Code != http.StatusOK {
		t.Fatalf("Turn 1 status = %d, want 200; body=%s", turn1Resp.Code, turn1Resp.Body.String())
	}
	if !strings.Contains(turn1Resp.Body.String(), "call_1785405835851080000_80000") {
		t.Fatalf("Turn 1 output missing call_id: %s", turn1Resp.Body.String())
	}

	// Disable owner account to simulate quota exhaustion / account lockout
	atomic.StoreInt32(&ownerAccount.DispatchPaused, 1)

	// --- Turn 2 ---
	// Client submits tool output referencing previous_response_id "resp_turn_1"
	turn2Body := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_turn_1","input":[{"type":"custom_tool_call_output","call_id":"call_1785405835851080000_80000","output":"hello world"}],"store":false}`)
	turn2Resp := performResponsesRequest(t, handler, turn2Body, headers)
	if turn2Resp.Code != http.StatusOK {
		t.Fatalf("Turn 2 status = %d, want 200; body=%s", turn2Resp.Code, turn2Resp.Body.String())
	}
	if code := gjson.Get(turn2Resp.Body.String(), "error.code").String(); code == "continuation_context_incomplete" {
		t.Fatalf("Turn 2 returned 409 continuation_context_incomplete: %s", turn2Resp.Body.String())
	}

	// Verify Fallback server received the replayed history in Turn 2
	fallbackMu.Lock()
	if len(fallbackRequests) < 1 {
		t.Fatalf("Fallback server received no requests in Turn 2")
	}
	req1 := fallbackRequests[0]
	fallbackMu.Unlock()
	if gjson.GetBytes(req1, "previous_response_id").Exists() {
		t.Fatalf("Turn 2 fallback request should remove previous_response_id: %s", req1)
	}
	items := gjson.GetBytes(req1, "input").Array()
	if len(items) != 3 {
		t.Fatalf("Turn 2 fallback input count = %d, want 3; req=%s", len(items), req1)
	}

	// --- Turn 3 ---
	// Client continues conversation with previous_response_id "resp_turn_2"
	turn3Body := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_turn_2","input":"summarize it","store":false}`)
	turn3Resp := performResponsesRequest(t, handler, turn3Body, headers)
	if turn3Resp.Code != http.StatusOK {
		t.Fatalf("Turn 3 status = %d, want 200; body=%s", turn3Resp.Code, turn3Resp.Body.String())
	}

	fallbackMu.Lock()
	if len(fallbackRequests) != 2 {
		t.Fatalf("Fallback server total requests = %d, want 2", len(fallbackRequests))
	}
	req2 := fallbackRequests[1]
	fallbackMu.Unlock()

	// Verify Turn 3 maintained continuity
	if gjson.GetBytes(req2, "previous_response_id").String() != "resp_turn_2" && len(gjson.GetBytes(req2, "input").Array()) != 5 {
		t.Fatalf("Turn 3 should keep previous_response_id resp_turn_2 or expand 5 items history; req=%s", req2)
	}
}
