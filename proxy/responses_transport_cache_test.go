package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

type runtimeCacheSetCall struct {
	namespace string
	key       string
	value     json.RawMessage
	ttl       time.Duration
}

type affinityDeleteCall struct {
	key       string
	accountID int64
}

type recordingRuntimeCache struct {
	cache.TokenCache

	mu      sync.Mutex
	getErr  error
	setErr  error
	gets    []string
	sets    []runtimeCacheSetCall
	deletes []affinityDeleteCall
}

func newRecordingRuntimeCache(t *testing.T) *recordingRuntimeCache {
	t.Helper()
	base := cache.NewMemory(1)
	t.Cleanup(func() { _ = base.Close() })
	return &recordingRuntimeCache{TokenCache: base}
}

func (c *recordingRuntimeCache) GetRuntime(ctx context.Context, namespace, key string) (json.RawMessage, bool, error) {
	c.mu.Lock()
	c.gets = append(c.gets, namespace)
	err := c.getErr
	c.mu.Unlock()
	if err != nil {
		return nil, false, err
	}
	return c.TokenCache.GetRuntime(ctx, namespace, key)
}

func (c *recordingRuntimeCache) getNamespaces() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.gets))
	copy(out, c.gets)
	return out
}

func (c *recordingRuntimeCache) SetRuntime(ctx context.Context, namespace, key string, value json.RawMessage, ttl time.Duration) error {
	c.mu.Lock()
	c.sets = append(c.sets, runtimeCacheSetCall{
		namespace: namespace,
		key:       key,
		value:     append(json.RawMessage(nil), value...),
		ttl:       ttl,
	})
	err := c.setErr
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.TokenCache.SetRuntime(ctx, namespace, key, value, ttl)
}

func (c *recordingRuntimeCache) DeleteSessionAffinity(ctx context.Context, key string, accountID int64) error {
	c.mu.Lock()
	c.deletes = append(c.deletes, affinityDeleteCall{key: key, accountID: accountID})
	c.mu.Unlock()
	return c.TokenCache.DeleteSessionAffinity(ctx, key, accountID)
}

func (c *recordingRuntimeCache) setCalls() []runtimeCacheSetCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]runtimeCacheSetCall, len(c.sets))
	copy(out, c.sets)
	return out
}

func (c *recordingRuntimeCache) affinityDeleteCalls() []affinityDeleteCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]affinityDeleteCall, len(c.deletes))
	copy(out, c.deletes)
	return out
}

func newDirectResponsesWebSocketClient(t *testing.T, upstreamURL string, maxRetries int, runtimeCache cache.TokenCache) *websocket.Conn {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previousMode := openAIResponsesContinuityMode
	openAIResponsesContinuityMode = openAIResponsesContinuityModeUpstream
	t.Cleanup(func() { openAIResponsesContinuityMode = previousMode })
	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency: 2,
		TestModel:      "gpt-5.4",
		MaxRetries:     maxRetries,
	})
	store.AddAccount(&auth.Account{
		DBID:         1,
		Name:         "direct-openai-responses",
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstreamURL,
		APIKey:       "upstream-secret-token",
		Models:       []string{"gpt-5.4"},
	})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	handler.SetRuntimeCache(runtimeCache)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial downstream websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial downstream websocket failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func writeResponsesWebSocketTurn(t *testing.T, conn *websocket.Conn, payload string) []byte {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatalf("write downstream request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for range 12 {
		_, event, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read downstream event: %v", err)
		}
		switch gjson.GetBytes(event, "type").String() {
		case "response.completed", "response.failed", "error":
			return event
		}
	}
	t.Fatal("downstream turn did not emit a terminal event")
	return nil
}

func seedResponsesTransportFallbackHistory(t *testing.T) {
	t.Helper()
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	t.Cleanup(resetResponseCacheForTest)
	t.Cleanup(resetOpenAIResponsesContinuityForTest)
	openAIResponsesContinuity.store("resp_prev", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"call a tool"}`),
		},
		output: []json.RawMessage{
			json.RawMessage(`{"type":"function_call","id":"fc_123","call_id":"call_abc","name":"lookup","arguments":"{}"}`),
		},
	})
	cacheCompletedResponse(
		[]byte(`[{"type":"message","role":"user","content":"call a tool"}]`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_prev","output":[{"type":"function_call","id":"fc_123","call_id":"call_abc","name":"lookup","arguments":"{}"}]}}`),
	)
}

func TestResponsesWebSocketFallbackWithoutContinuationHistoryFailsExplicitly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	resetOpenAIResponsesContinuityForTest()
	t.Cleanup(resetResponseCacheForTest)
	t.Cleanup(resetOpenAIResponsesContinuityForTest)

	runtimeCache := newRecordingRuntimeCache(t)
	var httpCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		httpCalls.Add(1)
		writeCompletedResponsesSSE(w)
	}))
	defer upstream.Close()

	conn := newDirectResponsesWebSocketClient(t, upstream.URL, 0, runtimeCache)
	terminal := writeResponsesWebSocketTurn(t, conn, responsesTransportTestPayload)
	if typ := gjson.GetBytes(terminal, "type").String(); typ != "response.failed" {
		t.Fatalf("terminal type = %q body=%s, want response.failed", typ, terminal)
	}
	if code := gjson.GetBytes(terminal, "response.error.code").String(); code != "continuation_context_unavailable" {
		t.Fatalf("terminal error code = %q body=%s, want continuation_context_unavailable", code, terminal)
	}
	if got := httpCalls.Load(); got != 0 {
		t.Fatalf("HTTP fallback calls = %d, want 0 when continuation history is unavailable", got)
	}
}

const responsesTransportTestPayload = `{"model":"gpt-5.4","previous_response_id":"resp_prev","input":[{"type":"function_call_output","call_id":"call_abc","output":"ok"}]}`

func writeCompletedResponsesSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w,
		`data: {"type":"response.output_text.delta","delta":"ok"}`+"\n\n"+
			`data: {"type":"response.completed","response":{"id":"resp_done","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}`+"\n\n",
	)
}

func TestResponsesTransportCacheKeyNormalizesEndpointAndHidesSecrets(t *testing.T) {
	withSecrets := "wss://user:secret-token@EXAMPLE.com:443/v1/responses/?api_key=secret-token#fragment"
	canonical := "https://example.com/v1/responses"
	got := responsesTransportCacheKey(withSecrets)
	if want := responsesTransportCacheKey(canonical); got != want {
		t.Fatalf("normalized cache keys differ: got=%q want=%q", got, want)
	}
	for _, secret := range []string{"secret-token", "example.com", "api_key"} {
		if strings.Contains(got, secret) {
			t.Fatalf("cache key %q contains %q", got, secret)
		}
	}
}

func TestOpenAIResponsesWebSocketUnsupportedClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "upgrade required", status: http.StatusUpgradeRequired, want: true},
		{name: "not found", status: http.StatusNotFound, want: true},
		{name: "blank bad request", status: http.StatusBadRequest, body: " \n\t", want: true},
		{name: "explicit unsupported", status: http.StatusBadRequest, body: `{"error":{"message":"websocket is not supported"}}`, want: true},
		{name: "valid bad request", status: http.StatusBadRequest, body: `{"error":{"message":"invalid previous_response_id"}}`, want: false},
		{name: "server failure", status: http.StatusBadGateway, body: "websocket unavailable", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isOpenAIResponsesWebSocketUnsupported(test.status, []byte(test.body)); got != test.want {
				t.Fatalf("classification = %v, want %v", got, test.want)
			}
		})
	}
}

func TestResponsesWebSocketEmpty400FallsBackCachesAndExpires(t *testing.T) {
	seedResponsesTransportFallbackHistory(t)
	runtimeCache := newRecordingRuntimeCache(t)

	var wsCalls atomic.Int32
	var httpCalls atomic.Int32
	var bodiesMu sync.Mutex
	var httpBodies [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			wsCalls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, " \n\t")
			return
		}
		httpCalls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read fallback body: %v", err)
			return
		}
		bodiesMu.Lock()
		httpBodies = append(httpBodies, append([]byte(nil), body...))
		bodiesMu.Unlock()
		writeCompletedResponsesSSE(w)
	}))
	defer upstream.Close()

	conn := newDirectResponsesWebSocketClient(t, upstream.URL, 0, runtimeCache)
	for turn := 1; turn <= 2; turn++ {
		terminal := writeResponsesWebSocketTurn(t, conn, responsesTransportTestPayload)
		if typ := gjson.GetBytes(terminal, "type").String(); typ != "response.completed" {
			t.Fatalf("turn %d terminal type = %q body=%s", turn, typ, terminal)
		}
	}
	if got := wsCalls.Load(); got != 1 {
		t.Fatalf("websocket attempts after cached second turn = %d, want 1", got)
	}
	if got := httpCalls.Load(); got != 2 {
		t.Fatalf("HTTP calls after cached second turn = %d, want 2", got)
	}

	sets := runtimeCache.setCalls()
	if len(sets) != 1 {
		t.Fatalf("runtime cache SetRuntime calls = %d, want 1", len(sets))
	}
	if sets[0].ttl != 30*time.Minute {
		t.Fatalf("protocol fallback TTL = %s, want 30m", sets[0].ttl)
	}
	if strings.Contains(sets[0].key, "upstream-secret-token") || strings.Contains(sets[0].key, upstream.URL) {
		t.Fatalf("runtime cache key leaks endpoint/token: %q", sets[0].key)
	}
	if err := runtimeCache.DeleteRuntime(context.Background(), sets[0].namespace, sets[0].key); err != nil {
		t.Fatalf("expire cached preference: %v", err)
	}
	terminal := writeResponsesWebSocketTurn(t, conn, responsesTransportTestPayload)
	if typ := gjson.GetBytes(terminal, "type").String(); typ != "response.completed" {
		t.Fatalf("terminal after cache expiry = %q body=%s", typ, terminal)
	}
	if got := wsCalls.Load(); got != 2 {
		t.Fatalf("websocket attempts after cache expiry = %d, want 2", got)
	}

	bodiesMu.Lock()
	defer bodiesMu.Unlock()
	if len(httpBodies) != 3 {
		t.Fatalf("fallback HTTP bodies = %d, want 3", len(httpBodies))
	}
	for i, body := range httpBodies {
		if gjson.GetBytes(body, "previous_response_id").Exists() {
			t.Fatalf("fallback body %d retained previous_response_id: %s", i, body)
		}
		input := gjson.GetBytes(body, "input").Array()
		if len(input) != 3 || input[1].Get("type").String() != "function_call" {
			t.Fatalf("fallback body %d did not expand cached history: %s", i, body)
		}
	}
}

func TestResponsesWebSocketValid400DoesNotFallback(t *testing.T) {
	seedResponsesTransportFallbackHistory(t)
	runtimeCache := newRecordingRuntimeCache(t)
	var httpCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"invalid previous_response_id","type":"invalid_request_error"}}`)
			return
		}
		httpCalls.Add(1)
		writeCompletedResponsesSSE(w)
	}))
	defer upstream.Close()

	conn := newDirectResponsesWebSocketClient(t, upstream.URL, 0, runtimeCache)
	terminal := writeResponsesWebSocketTurn(t, conn, responsesTransportTestPayload)
	if typ := gjson.GetBytes(terminal, "type").String(); typ != "response.failed" {
		t.Fatalf("terminal type = %q body=%s, want response.failed", typ, terminal)
	}
	if got := httpCalls.Load(); got != 0 {
		t.Fatalf("HTTP fallback calls = %d, want 0", got)
	}
	if got := len(runtimeCache.setCalls()); got != 0 {
		t.Fatalf("runtime cache writes = %d, want 0", got)
	}
}

func TestResponsesWebSocketFallbackFailurePreservesHTTPError(t *testing.T) {
	seedResponsesTransportFallbackHistory(t)
	runtimeCache := newRecordingRuntimeCache(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"HTTP fallback upstream exploded","type":"upstream_error"}}`)
	}))
	defer upstream.Close()

	conn := newDirectResponsesWebSocketClient(t, upstream.URL, 0, runtimeCache)
	terminal := writeResponsesWebSocketTurn(t, conn, responsesTransportTestPayload)
	if typ := gjson.GetBytes(terminal, "type").String(); typ != "response.failed" {
		t.Fatalf("terminal type = %q body=%s, want response.failed", typ, terminal)
	}
	if !strings.Contains(string(terminal), "HTTP fallback upstream exploded") {
		t.Fatalf("terminal did not preserve fallback error: %s", terminal)
	}
	if got := len(runtimeCache.setCalls()); got != 0 {
		t.Fatalf("runtime cache writes after failed fallback = %d, want 0", got)
	}
}

func TestResponsesWebSocketFallbackRequestErrorPreservesTransportFailureWithoutAccountRetry(t *testing.T) {
	seedResponsesTransportFallbackHistory(t)
	runtimeCache := newRecordingRuntimeCache(t)
	var wsCalls atomic.Int32
	var httpCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			wsCalls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		httpCalls.Add(1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server does not support hijacking")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack fallback connection: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()

	conn := newDirectResponsesWebSocketClient(t, upstream.URL, 3, runtimeCache)
	terminal := writeResponsesWebSocketTurn(t, conn, responsesTransportTestPayload)
	if typ := gjson.GetBytes(terminal, "type").String(); typ != "response.failed" {
		t.Fatalf("terminal type = %q body=%s, want response.failed", typ, terminal)
	}
	if status := gjson.GetBytes(terminal, "response.status_code").Int(); status != http.StatusBadGateway {
		t.Fatalf("terminal status = %d body=%s, want 502", status, terminal)
	}
	message := gjson.GetBytes(terminal, "response.error.message").String()
	if !strings.Contains(message, "请求 OpenAI Responses API 失败") || strings.Contains(message, "无可用账号") {
		t.Fatalf("terminal did not preserve fallback transport error: %s", terminal)
	}
	if got := wsCalls.Load(); got != 1 {
		t.Fatalf("websocket attempts = %d, want 1", got)
	}
	if got := httpCalls.Load(); got != 1 {
		t.Fatalf("HTTP fallback attempts = %d, want 1", got)
	}
	if got := len(runtimeCache.setCalls()); got != 0 {
		t.Fatalf("runtime cache writes after failed fallback = %d, want 0", got)
	}
}

func TestResponsesWebSocketFallbackRequestErrorPreservesSessionAffinity(t *testing.T) {
	seedResponsesTransportFallbackHistory(t)
	previousMode := openAIResponsesContinuityMode
	openAIResponsesContinuityMode = openAIResponsesContinuityModeUpstream
	t.Cleanup(func() { openAIResponsesContinuityMode = previousMode })
	runtimeCache := newRecordingRuntimeCache(t)
	const sessionID = "fallback-http-request-error"
	affinityKey := sessionAffinityKey(sessionID, 0)

	var firstWSCalls atomic.Int32
	var firstHTTPCalls atomic.Int32
	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			firstWSCalls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		firstHTTPCalls.Add(1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server does not support hijacking")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack fallback connection: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer firstUpstream.Close()

	var secondCalls atomic.Int32
	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		writeCompletedResponsesSSE(w)
	}))
	defer secondUpstream.Close()

	settings := &database.SystemSettings{MaxConcurrency: 2, TestModel: "gpt-5.4", MaxRetries: 3}
	store := auth.NewStore(nil, runtimeCache, settings)
	firstAccount := &auth.Account{
		DBID:         1,
		Name:         "first-direct-account",
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      firstUpstream.URL,
		APIKey:       "first-key",
		Models:       []string{"gpt-5.4"},
	}
	secondAccount := &auth.Account{
		DBID:         2,
		Name:         "second-direct-account",
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      secondUpstream.URL,
		APIKey:       "second-key",
		Models:       []string{"gpt-5.4"},
	}
	store.AddAccount(firstAccount)
	store.AddAccount(secondAccount)
	store.SetAffinityMode(auth.AffinityModeStrict)
	store.BindSessionAffinity(affinityKey, firstAccount, "")

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	handler.SetRuntimeCache(runtimeCache)
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial downstream websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial downstream websocket failed: %v", err)
	}
	defer conn.Close()

	payload := `{"model":"gpt-5.4","prompt_cache_key":"` + sessionID + `","previous_response_id":"resp_prev","input":[{"type":"function_call_output","call_id":"call_abc","output":"ok"}]}`
	terminal := writeResponsesWebSocketTurn(t, conn, payload)
	if status := gjson.GetBytes(terminal, "response.status_code").Int(); status != http.StatusBadGateway {
		t.Fatalf("terminal status = %d body=%s, want 502", status, terminal)
	}
	if got := firstWSCalls.Load(); got != 1 {
		t.Fatalf("first-account WSS calls = %d, want 1", got)
	}
	if got := firstHTTPCalls.Load(); got != 1 {
		t.Fatalf("first-account HTTP calls = %d, want 1", got)
	}
	if got := secondCalls.Load(); got != 0 {
		t.Fatalf("second account calls = %d, want 0", got)
	}
	if deletes := runtimeCache.affinityDeleteCalls(); len(deletes) != 0 {
		t.Fatalf("affinity deletes = %+v, want no unbind for a transport error", deletes)
	}
}

func TestResponsesWebSocketRuntimeCacheWriteFailureDoesNotFailCompletedHTTPFallback(t *testing.T) {
	seedResponsesTransportFallbackHistory(t)
	runtimeCache := newRecordingRuntimeCache(t)
	runtimeCache.setErr = errors.New("runtime cache write unavailable")

	var wsCalls atomic.Int32
	var httpCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			wsCalls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		httpCalls.Add(1)
		writeCompletedResponsesSSE(w)
	}))
	defer upstream.Close()

	conn := newDirectResponsesWebSocketClient(t, upstream.URL, 0, runtimeCache)
	for turn := 1; turn <= 2; turn++ {
		terminal := writeResponsesWebSocketTurn(t, conn, responsesTransportTestPayload)
		if typ := gjson.GetBytes(terminal, "type").String(); typ != "response.completed" {
			t.Fatalf("turn %d terminal type = %q body=%s", turn, typ, terminal)
		}
	}
	if got := wsCalls.Load(); got != 2 {
		t.Fatalf("WSS probes after cache write failures = %d, want 2", got)
	}
	if got := httpCalls.Load(); got != 2 {
		t.Fatalf("successful HTTP fallbacks = %d, want 2", got)
	}
	deadline := time.Now().Add(time.Second)
	for len(runtimeCache.setCalls()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(runtimeCache.setCalls()); got != 2 {
		t.Fatalf("runtime cache write attempts = %d, want 2", got)
	}
}

func TestResponsesWebSocketRuntimeCacheReadFailureFailsOpenToWSS(t *testing.T) {
	seedResponsesTransportFallbackHistory(t)
	runtimeCache := newRecordingRuntimeCache(t)
	runtimeCache.getErr = errors.New("runtime cache unavailable")

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var wsCalls atomic.Int32
	var httpCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !websocket.IsWebSocketUpgrade(r) {
			httpCalls.Add(1)
			writeCompletedResponsesSSE(w)
			return
		}
		wsCalls.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade upstream websocket: %v", err)
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Errorf("read upstream request: %v", err)
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_done","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
	}))
	defer upstream.Close()

	conn := newDirectResponsesWebSocketClient(t, upstream.URL, 0, runtimeCache)
	terminal := writeResponsesWebSocketTurn(t, conn, responsesTransportTestPayload)
	if typ := gjson.GetBytes(terminal, "type").String(); typ != "response.completed" {
		t.Fatalf("terminal type = %q body=%s", typ, terminal)
	}
	if got := wsCalls.Load(); got != 1 {
		t.Fatalf("websocket attempts = %d, want 1 on cache read failure", got)
	}
	if got := httpCalls.Load(); got != 0 {
		t.Fatalf("HTTP calls = %d, want 0", got)
	}
	capabilityRead := false
	for _, namespace := range runtimeCache.getNamespaces() {
		if namespace != apiKeyCountCacheNamespace {
			capabilityRead = true
			break
		}
	}
	if !capabilityRead {
		t.Fatalf("runtime capability cache was not read; namespaces=%v", runtimeCache.getNamespaces())
	}
}

func TestResponsesWebSocketCloseBeforeFirstEventUsesShortHTTPPreference(t *testing.T) {
	for _, closeCode := range []int{websocket.ClosePolicyViolation, websocket.CloseTryAgainLater} {
		t.Run(http.StatusText(closeCode), func(t *testing.T) {
			seedResponsesTransportFallbackHistory(t)
			runtimeCache := newRecordingRuntimeCache(t)
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			var wsCalls atomic.Int32
			var httpCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !websocket.IsWebSocketUpgrade(r) {
					httpCalls.Add(1)
					writeCompletedResponsesSSE(w)
					return
				}
				wsCalls.Add(1)
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("upgrade upstream websocket: %v", err)
					return
				}
				defer conn.Close()
				if _, _, err := conn.ReadMessage(); err != nil {
					t.Errorf("read upstream request: %v", err)
					return
				}
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(closeCode, "websocket unavailable"), time.Now().Add(time.Second))
			}))
			defer upstream.Close()

			conn := newDirectResponsesWebSocketClient(t, upstream.URL, 0, runtimeCache)
			for turn := 1; turn <= 2; turn++ {
				terminal := writeResponsesWebSocketTurn(t, conn, responsesTransportTestPayload)
				if typ := gjson.GetBytes(terminal, "type").String(); typ != "response.completed" {
					t.Fatalf("turn %d terminal type = %q body=%s", turn, typ, terminal)
				}
			}
			if got := wsCalls.Load(); got != 1 {
				t.Fatalf("websocket attempts = %d, want 1", got)
			}
			if got := httpCalls.Load(); got != 2 {
				t.Fatalf("HTTP calls = %d, want 2", got)
			}
			sets := runtimeCache.setCalls()
			if len(sets) != 1 || sets[0].ttl != 2*time.Minute {
				t.Fatalf("short preference cache calls = %+v, want one 2m entry", sets)
			}
		})
	}
}

func TestResponsesWebSocketOtherCloseAndEOFDoNotUseShortHTTPFallback(t *testing.T) {
	tests := []struct {
		name      string
		closeCode int
		rawEOF    bool
	}{
		{name: "normal close", closeCode: websocket.CloseNormalClosure},
		{name: "going away", closeCode: websocket.CloseGoingAway},
		{name: "protocol error", closeCode: websocket.CloseProtocolError},
		{name: "unexpected EOF", rawEOF: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seedResponsesTransportFallbackHistory(t)
			runtimeCache := newRecordingRuntimeCache(t)
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			var wsCalls atomic.Int32
			var httpCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !websocket.IsWebSocketUpgrade(r) {
					httpCalls.Add(1)
					writeCompletedResponsesSSE(w)
					return
				}
				wsCalls.Add(1)
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("upgrade upstream websocket: %v", err)
					return
				}
				if _, _, err := conn.ReadMessage(); err != nil {
					_ = conn.Close()
					t.Errorf("read upstream request: %v", err)
					return
				}
				if test.rawEOF {
					_ = conn.UnderlyingConn().Close()
					return
				}
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(test.closeCode, test.name), time.Now().Add(time.Second))
				_ = conn.Close()
			}))
			defer upstream.Close()

			conn := newDirectResponsesWebSocketClient(t, upstream.URL, 0, runtimeCache)
			terminal := writeResponsesWebSocketTurn(t, conn, responsesTransportTestPayload)
			if typ := gjson.GetBytes(terminal, "type").String(); typ != "response.failed" {
				t.Fatalf("terminal type = %q body=%s, want response.failed", typ, terminal)
			}
			if got := wsCalls.Load(); got != 1 {
				t.Fatalf("websocket attempts = %d, want 1", got)
			}
			if got := httpCalls.Load(); got != 0 {
				t.Fatalf("HTTP fallback attempts = %d, want 0", got)
			}
			if got := len(runtimeCache.setCalls()); got != 0 {
				t.Fatalf("runtime cache writes = %d, want 0", got)
			}
		})
	}
}
