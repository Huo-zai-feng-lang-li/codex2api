package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	resetOpenAIResponsesContinuityForTest()
	owner := &auth.Account{DBID: 41, Name: "codex-owner"}
	cacheOpenAIResponsesContinuation(
		[]byte(`{"model":"gpt-5.4","input":"remember FOXTROT","store":false}`),
		[]byte(`{"id":"resp_codex_owner","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"FOXTROT"}]}]}`),
		owner,
	)

	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_codex_owner","input":"what token?","store":false}`)
	filter, bound := bindOpenAIResponsesContinuationOwner(request, nil)
	if !bound || !filter(owner) {
		t.Fatal("Codex response owner was not bound")
	}
	if filter(&auth.Account{DBID: 42, Name: "other"}) {
		t.Fatal("non-owner Codex account passed continuation filter")
	}
	fallback, ok := buildOpenAIResponsesContinuationFallback(request)
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

func TestOpenAIResponsesContinuationWithoutLocalHistoryDoesNotGuess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()

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
