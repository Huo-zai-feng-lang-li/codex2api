package proxy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/api"
	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

func TestSupportedModelsIncludeLatestRequestedModels(t *testing.T) {
	for _, model := range []string{"gpt-5.5", "gpt-5.3-codex-spark", "gpt-5.2", "gpt-image-2", "gpt-image-2-2k", "gpt-image-2-4k"} {
		if !slices.Contains(SupportedModels, model) {
			t.Fatalf("SupportedModels missing %q", model)
		}
	}
}

func TestSupportedModelsExcludeBelowGPT52(t *testing.T) {
	for _, model := range []string{
		"gpt-5", "gpt-5-codex", "gpt-5-codex-mini",
		"gpt-5.1", "gpt-5.1-codex", "gpt-5.1-codex-mini", "gpt-5.1-codex-max",
		"gpt-5.2-codex",
	} {
		if slices.Contains(SupportedModels, model) {
			t.Fatalf("SupportedModels should not include %q", model)
		}
	}
}

func TestListModelsIncludesLatestRequestedModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	handler := &Handler{}

	handler.ListModels(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	ids := make([]string, 0, len(payload.Data))
	for _, model := range payload.Data {
		ids = append(ids, model.ID)
	}
	for _, model := range []string{"gpt-5.5", "gpt-5.3-codex-spark", "gpt-5.2", "gpt-image-2"} {
		if !slices.Contains(ids, model) {
			t.Fatalf("/v1/models missing %q in %v", model, ids)
		}
	}
	for _, model := range []string{"gpt-image-2-2k", "gpt-image-2-4k"} {
		if !slices.Contains(ids, model) {
			t.Fatalf("/v1/models missing image alias %q in %v", model, ids)
		}
	}

	for _, model := range []string{"gpt-5", "gpt-5.1", "gpt-5.2-codex"} {
		if slices.Contains(ids, model) {
			t.Fatalf("/v1/models should not include %q in %v", model, ids)
		}
	}
}

func TestImageModelIsImageEndpointOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	sendImageOnlyModelError(ctx, "gpt-image-2")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(recorder.Body.String(), "/v1/images/generations") {
		t.Fatalf("error body should point to images endpoints: %s", recorder.Body.String())
	}
}

func TestRegisterRoutesIncludesCodexDirectResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := &Handler{}

	handler.RegisterRoutes(router)

	postRoutes := make(map[string]bool)
	getRoutes := make(map[string]bool)
	for _, route := range router.Routes() {
		if route.Method == http.MethodPost {
			postRoutes[route.Path] = true
		}
		if route.Method == http.MethodGet {
			getRoutes[route.Path] = true
		}
	}

	for _, path := range []string{
		"/backend-api/codex/responses",
		"/backend-api/codex/responses/*subpath",
	} {
		if !postRoutes[path] {
			t.Fatalf("expected POST route %s to be registered; routes=%v", path, postRoutes)
		}
	}
	for _, path := range []string{
		"/v1/responses",
		"/responses",
		"/backend-api/codex/responses",
	} {
		if !getRoutes[path] {
			t.Fatalf("expected GET route %s to be registered; routes=%v", path, getRoutes)
		}
	}
}

func TestNormalizeResponsesWebSocketClientPayload(t *testing.T) {
	t.Run("defaults response create type", func(t *testing.T) {
		got, model, apiErr := normalizeResponsesWebSocketClientPayload([]byte(`{"model":"gpt-5.4","input":"hi"}`))
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if model != "gpt-5.4" {
			t.Fatalf("model = %q, want gpt-5.4", model)
		}
		if eventType := gjson.GetBytes(got, "type").String(); eventType != "response.create" {
			t.Fatalf("type = %q, want response.create; body=%s", eventType, got)
		}
	})

	t.Run("rejects append", func(t *testing.T) {
		_, _, apiErr := normalizeResponsesWebSocketClientPayload([]byte(`{"type":"response.append","model":"gpt-5.4"}`))
		if apiErr == nil || !strings.Contains(apiErr.Message, "response.append") {
			t.Fatalf("error = %#v, want response.append rejection", apiErr)
		}
	})

	t.Run("rejects message previous response id", func(t *testing.T) {
		_, _, apiErr := normalizeResponsesWebSocketClientPayload([]byte(`{"type":"response.create","model":"gpt-5.4","previous_response_id":"msg_123"}`))
		if apiErr == nil || !strings.Contains(apiErr.Message, "response.id") {
			t.Fatalf("error = %#v, want previous_response_id rejection", apiErr)
		}
	})
}

func TestResponsesWSFinalAccountErrorPreservesLastUpstreamFailure(t *testing.T) {
	status, apiErr := responsesWSFinalAccountError(
		http.StatusBadGateway,
		[]byte(`{"error":{"type":"server_error","message":"origin unavailable"}}`),
		"gpt-5.4",
	)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
	if apiErr == nil || !strings.Contains(apiErr.Message, "origin unavailable") {
		t.Fatalf("last upstream error was lost: %#v", apiErr)
	}
}

func TestResponsesWSFinalAccountErrorUsesNoAvailableBeforeAnyAttempt(t *testing.T) {
	status, apiErr := responsesWSFinalAccountError(0, nil, "gpt-5.4")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	if apiErr == nil || !strings.Contains(apiErr.Message, "无可用账号") {
		t.Fatalf("missing no-available error: %#v", apiErr)
	}
}

func TestBuildContinuationContextIncompleteEvent(t *testing.T) {
	event := buildContinuationContextIncompleteEvent()
	if eventType := gjson.GetBytes(event, "type").String(); eventType != "response.failed" {
		t.Fatalf("event type = %q, want response.failed; body=%s", eventType, event)
	}
	if status := gjson.GetBytes(event, "response.status_code").Int(); status != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", status, event)
	}
	if code := gjson.GetBytes(event, "response.error.code").String(); code != "continuation_context_incomplete" {
		t.Fatalf("error code = %q, want continuation_context_incomplete; body=%s", code, event)
	}
	if strings.Contains(string(event), "no_available_account") || strings.Contains(string(event), "无可用账号") {
		t.Fatalf("incomplete continuation was misreported as account exhaustion: %s", event)
	}
}

func TestResponsesWebSocketOwnerUnavailableReportsIncompleteContinuation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetOpenAIResponsesContinuityForTest()

	owner := &auth.Account{DBID: 81, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://owner.example", APIKey: "owner", Models: []string{"gpt-5.4"}}
	RegisterPendingOpenAIResponsesContinuation("resp_ws_owner_missing", "", "", owner)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 0, AffinityMode: auth.AffinityModeOff})
	store.AddAccount(&auth.Account{DBID: 82, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://fallback.example", APIKey: "fallback", Models: []string{"gpt-5.4"}})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_ws_owner_missing","input":[{"type":"custom_tool_call_output","call_id":"call_missing","output":"x"}]}`)
	if err := conn.WriteMessage(websocket.TextMessage, request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, event, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read failure event: %v", err)
	}
	if status := gjson.GetBytes(event, "response.status_code").Int(); status != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", status, event)
	}
	if code := gjson.GetBytes(event, "response.error.code").String(); code != "continuation_context_incomplete" {
		t.Fatalf("error code = %q, want continuation_context_incomplete; body=%s", code, event)
	}
}

func TestResponsesWebSocketForwardsResponsesEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})
	t.Setenv("CODEX_WS_FALLBACK_HTTP", "false")

	bodyCh := make(chan []byte, 2)
	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		bodyCh <- append([]byte(nil), requestBody...)
		sse := "" +
			`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "at", PlanType: "plus", AccountID: "acct-1"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true, UseWebsocket: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_prev","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	select {
	case gotBody := <-bodyCh:
		if gjson.GetBytes(gotBody, "type").String() != "response.create" {
			t.Fatalf("upstream type missing: %s", gotBody)
		}
		if prev := gjson.GetBytes(gotBody, "previous_response_id").String(); prev != "resp_prev" {
			t.Fatalf("previous_response_id = %q, want resp_prev; body=%s", prev, gotBody)
		}
		if store := gjson.GetBytes(gotBody, "store"); store.Exists() {
			t.Fatalf("websocket ingress should not force store=false, got %s; body=%s", store.Raw, gotBody)
		}
		if !gjson.GetBytes(gotBody, "stream").Bool() {
			t.Fatalf("upstream stream should be true: %s", gotBody)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upstream request")
	}

	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first event: %v", err)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first event type = %q body=%s", eventType, first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if eventType := gjson.GetBytes(second, "type").String(); eventType != "response.completed" {
		t.Fatalf("terminal event type = %q body=%s", eventType, second)
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.4","input":"again"}`)); err != nil {
		t.Fatalf("write second request: %v", err)
	}
	select {
	case <-bodyCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second upstream request")
	}
}

func TestResponsesWebSocketRetriesBeforeFirstTokenWithoutClientError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	var attempts int
	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			sse := `data: {"type":"response.created","response":{"id":"resp_retry"}}` + "\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       &errorAfterReader{Reader: strings.NewReader(sse), err: io.ErrUnexpectedEOF},
			}, nil
		}
		sse := "" +
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 2})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "bad", PlanType: "plus", AccountID: "acct-1"})
	store.AddAccount(&auth.Account{DBID: 2, AccessToken: "good", PlanType: "plus", AccountID: "acct-2"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true, UseWebsocket: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first downstream event: %v", err)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first downstream event = %q body=%s, want retry result delta", eventType, first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if eventType := gjson.GetBytes(second, "type").String(); eventType != "response.completed" {
		t.Fatalf("terminal event type = %q body=%s", eventType, second)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestResponsesWebSocketFallsBackToHTTPAfterUpstreamWebsocketMissingTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	var attempts int
	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		attempts++
		if attempts != 1 {
			t.Fatalf("WebsocketExecuteFunc called after fallback, attempt=%d", attempts)
		}
		sse := `data: {"type":"response.created","response":{"id":"resp_retry"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &errorAfterReader{Reader: strings.NewReader(sse), err: errors.New("stream disconnected before completion: websocket closed by server before response.completed")},
		}, nil
	}

	var httpCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
				`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n",
		))
	}))
	defer upstream.Close()

	SetResinConfig(&ResinConfig{BaseURL: upstream.URL, PlatformName: "codex2api"})
	t.Cleanup(func() { SetResinConfig(nil) })

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 2})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "at", PlanType: "plus", AccountID: "acct-1"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true, UseWebsocket: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first downstream event: %v", err)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first downstream event = %q body=%s, want fallback delta", eventType, first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if eventType := gjson.GetBytes(second, "type").String(); eventType != "response.completed" {
		t.Fatalf("terminal event type = %q body=%s", eventType, second)
	}
	if attempts != 1 {
		t.Fatalf("websocket attempts = %d, want 1", attempts)
	}
	if httpCalls.Load() != 1 {
		t.Fatalf("http fallback calls = %d, want 1", httpCalls.Load())
	}
}

func TestResponsesWebSocketFallbackToHTTPIgnoresMaxRetriesBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	var attempts int
	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		attempts++
		if attempts != 1 {
			t.Fatalf("WebsocketExecuteFunc called after fallback, attempt=%d", attempts)
		}
		sse := `data: {"type":"response.created","response":{"id":"resp_retry"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &errorAfterReader{Reader: strings.NewReader(sse), err: errors.New("stream disconnected before completion: websocket closed by server before response.completed")},
		}, nil
	}

	var httpCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
				`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n",
		))
	}))
	defer upstream.Close()

	SetResinConfig(&ResinConfig{BaseURL: upstream.URL, PlatformName: "codex2api"})
	t.Cleanup(func() { SetResinConfig(nil) })

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 0})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "at", PlanType: "plus", AccountID: "acct-1"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true, UseWebsocket: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first downstream event: %v", err)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first downstream event = %q body=%s, want fallback delta", eventType, first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if eventType := gjson.GetBytes(second, "type").String(); eventType != "response.completed" {
		t.Fatalf("terminal event type = %q body=%s", eventType, second)
	}
	if attempts != 1 {
		t.Fatalf("websocket attempts = %d, want 1", attempts)
	}
	if httpCalls.Load() != 1 {
		t.Fatalf("http fallback calls = %d, want 1", httpCalls.Load())
	}
}

func TestResponsesWebSocketSendsResponseFailedWhenStreamBreaksAfterFirstToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		sse := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &errorAfterReader{Reader: strings.NewReader(sse), err: io.ErrUnexpectedEOF},
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 2})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "at", PlanType: "plus", AccountID: "acct-1"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true, UseWebsocket: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first downstream event: %v", err)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first downstream event = %q body=%s", eventType, first)
	}

	_, terminal, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal failure event: %v", err)
	}
	if eventType := gjson.GetBytes(terminal, "type").String(); eventType != "response.failed" {
		t.Fatalf("terminal event type = %q body=%s, want response.failed", eventType, terminal)
	}
	if message := gjson.GetBytes(terminal, "response.error.message").String(); !strings.Contains(message, "上游流读取失败") {
		t.Fatalf("terminal error message = %q body=%s", message, terminal)
	}
}

func TestResponsesStreamSendsResponseFailedWhenStreamBreaksAfterFirstToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		sse := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &errorAfterReader{Reader: strings.NewReader(sse), err: io.ErrUnexpectedEOF},
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 2})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "at", PlanType: "plus", AccountID: "acct-1"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true, UseWebsocket: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	reqBody := strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":true}`)
	resp, err := http.Post(server.URL+"/v1/responses", "application/json", reqBody)
	if err != nil {
		t.Fatalf("post responses: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"type":"response.output_text.delta"`) {
		t.Fatalf("delta event missing: %s", body)
	}
	if !strings.Contains(string(body), `"type":"response.failed"`) {
		t.Fatalf("terminal response.failed missing: %s", body)
	}
	if !strings.Contains(string(body), "上游流读取失败") {
		t.Fatalf("failure message missing: %s", body)
	}
}

func TestResponsesStreamMasksHTTP2InternalStreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		sse := `data: {"type":"response.created","response":{"id":"resp_http2_rst"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &errorAfterReader{Reader: strings.NewReader(sse), err: errors.New("stream error: stream ID 17; INTERNAL_ERROR; received from peer")},
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 0})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "at", PlanType: "plus", AccountID: "acct-1"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true, UseWebsocket: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	reqBody := strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":true}`)
	resp, err := http.Post(server.URL+"/v1/responses", "application/json", reqBody)
	if err != nil {
		t.Fatalf("post responses: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"type":"response.failed"`) {
		t.Fatalf("terminal response.failed missing: %s", body)
	}
	if !strings.Contains(string(body), "上游 HTTP/2 流被对端重置") {
		t.Fatalf("normalized failure message missing: %s", body)
	}
	if strings.Contains(string(body), "stream ID 17") || strings.Contains(string(body), "INTERNAL_ERROR") {
		t.Fatalf("raw http2 stream error leaked to downstream: %s", body)
	}
}

func TestOpenAIResponsesStreamSingleAccountDoesNotApplyFirstTokenTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upstreamCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_timeout"}}` + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(1200 * time.Millisecond)
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"real"}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n"))
	}))
	defer upstream.Close()

	settings := &database.SystemSettings{
		MaxConcurrency:           2,
		TestConcurrency:          1,
		TestModel:                "gpt-5.4",
		MaxRetries:               2,
		FirstTokenTimeoutSeconds: 1,
	}
	store := auth.NewStore(nil, nil, settings)
	store.AddAccount(&auth.Account{
		DBID:         1,
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "sk-test",
		Models:       []string{"gpt-5.4"},
	})
	previousSettings := CurrentRuntimeSettings()
	ApplyRuntimeSettings(RuntimeSettings{FirstTokenTimeoutSec: 1})
	t.Cleanup(func() { ApplyRuntimeSettings(previousSettings) })

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	reqBody := strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":true}`)
	resp, err := http.Post(server.URL+"/v1/responses", "application/json", reqBody)
	if err != nil {
		t.Fatalf("post responses: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if calls := atomic.LoadInt32(&upstreamCalls); calls != 1 {
		t.Fatalf("upstream calls = %d, want 1; body=%s", calls, body)
	}
	if !strings.Contains(string(body), `"type":"response.output_text.delta"`) {
		t.Fatalf("delta event missing: %s", body)
	}
	if !strings.Contains(string(body), `"type":"response.completed"`) {
		t.Fatalf("completed event missing: %s", body)
	}
	if strings.Contains(string(body), `"type":"response.failed"`) || strings.Contains(string(body), "上游首字超时") {
		t.Fatalf("single account should not expose first-token timeout: %s", body)
	}
}

func TestOpenAIResponsesStreamClientCancelDoesNotRetryAsNoAvailableAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upstreamCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(6 * time.Second)
	}))
	defer upstream.Close()

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:  1,
		TestConcurrency: 1,
		TestModel:       "gpt-5.4",
		MaxRetries:      2,
	})
	store.AddAccount(&auth.Account{
		DBID:         1,
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "sk-test",
		Models:       []string{"gpt-5.4"},
	})

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":true}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("client request unexpectedly completed with status %d", resp.StatusCode)
	}

	time.Sleep(200 * time.Millisecond)
	if calls := atomic.LoadInt32(&upstreamCalls); calls != 1 {
		t.Fatalf("upstream calls = %d, want 1; client cancellation must not retry into no_available_account", calls)
	}
}

func TestOpenAIResponsesStreamFirstTokenTimeoutSwitchesOnlyOnceThenWaitsSecondAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upstreamCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&upstreamCalls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_timeout"}}` + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if call == 2 {
			time.Sleep(1200 * time.Millisecond)
			_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"late second account"}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3},"service_tier":"default"}}` + "\n\n"))
			return
		}
		time.Sleep(1500 * time.Millisecond)
	}))
	defer upstream.Close()

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:           1,
		TestConcurrency:          1,
		TestModel:                "gpt-5.4",
		MaxRetries:               5,
		FirstTokenTimeoutSeconds: 1,
	})
	store.AddAccount(&auth.Account{
		DBID:         1,
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "sk-test-1",
		Models:       []string{"gpt-5.4"},
	})
	store.AddAccount(&auth.Account{
		DBID:         2,
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "sk-test-2",
		Models:       []string{"gpt-5.4"},
	})
	previousSettings := CurrentRuntimeSettings()
	ApplyRuntimeSettings(RuntimeSettings{FirstTokenTimeoutSec: 1})
	t.Cleanup(func() { ApplyRuntimeSettings(previousSettings) })

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	reqBody := strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":true}`)
	resp, err := http.Post(server.URL+"/v1/responses", "application/json", reqBody)
	if err != nil {
		t.Fatalf("post responses: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if calls := atomic.LoadInt32(&upstreamCalls); calls != 2 {
		t.Fatalf("upstream calls = %d, want 2; body=%s", calls, body)
	}
	if !strings.Contains(string(body), `"type":"response.output_text.delta"`) || !strings.Contains(string(body), `"type":"response.completed"`) {
		t.Fatalf("second slow account should continue instead of failing: %s", body)
	}
}

func TestOpenAIResponsesStreamFirstTokenSwitchUsesSecondSlowAccountInsteadOfRetryingAgain(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upstreamCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&upstreamCalls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		switch authHeader {
		case "Bearer 1":
			_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_slow_1"}}` + "\n\n"))
		case "Bearer 2":
			_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_slow_2"}}` + "\n\n"))
		default:
			t.Fatalf("unexpected Authorization %q", authHeader)
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if call == 1 {
			time.Sleep(1500 * time.Millisecond)
			return
		}
		time.Sleep(1200 * time.Millisecond)
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"late but valid"}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3},"service_tier":"default"}}` + "\n\n"))
	}))
	defer upstream.Close()

	settings := &database.SystemSettings{
		MaxConcurrency:           1,
		TestConcurrency:          1,
		TestModel:                "gpt-5.4",
		MaxRetries:               5,
		FirstTokenTimeoutSeconds: 1,
	}
	store := auth.NewStore(nil, nil, settings)
	store.AddAccount(&auth.Account{
		DBID:         1,
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "1",
		Models:       []string{"gpt-5.4"},
	})
	store.AddAccount(&auth.Account{
		DBID:         2,
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "2",
		Models:       []string{"gpt-5.4"},
	})
	previousSettings := CurrentRuntimeSettings()
	ApplyRuntimeSettings(RuntimeSettings{FirstTokenTimeoutSec: 1})
	t.Cleanup(func() { ApplyRuntimeSettings(previousSettings) })

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	reqBody := strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":true}`)
	resp, err := http.Post(server.URL+"/v1/responses", "application/json", reqBody)
	if err != nil {
		t.Fatalf("post responses: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "无可用账号") {
		t.Fatalf("slow-account fallback should not emit no_available_account: %s", body)
	}
	if !strings.Contains(string(body), `"type":"response.output_text.delta"`) || !strings.Contains(string(body), `"type":"response.completed"`) {
		t.Fatalf("slow-account fallback should complete successfully: %s", body)
	}
	if calls := atomic.LoadInt32(&upstreamCalls); calls != 2 {
		t.Fatalf("upstream calls = %d, want exactly 2 because slow path should switch once only", calls)
	}
}

func TestResponsesWebSocketSendsResponseFailedWhenRetriesExhaustBeforeFirstMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		sse := `data: {"type":"response.created","response":{"id":"resp_retry"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &errorAfterReader{Reader: strings.NewReader(sse), err: errors.New("stream disconnected before completion: websocket closed by server before response.completed")},
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 0})
	store.SetMaxRetries(0)
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "at", PlanType: "plus", AccountID: "acct-1"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true, UseWebsocket: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, terminal, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal failure event: %v", err)
	}
	if eventType := gjson.GetBytes(terminal, "type").String(); eventType != "response.failed" {
		t.Fatalf("terminal event type = %q body=%s, want response.failed", eventType, terminal)
	}
	if message := gjson.GetBytes(terminal, "response.error.message").String(); !strings.Contains(message, "before response.completed") {
		t.Fatalf("terminal error message = %q body=%s", message, terminal)
	}
}

func TestResponsesWebSocketRetriesResponseFailedBeforeFirstToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	var attempts int
	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			sse := `data: {"type":"response.failed","response":{"status_code":402,"error":{"message":"Usage limit reached"}}}` + "\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(sse)),
			}, nil
		}
		sse := "" +
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 2})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "bad", PlanType: "plus", AccountID: "acct-1"})
	store.AddAccount(&auth.Account{DBID: 2, AccessToken: "good", PlanType: "plus", AccountID: "acct-2"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first downstream event: %v", err)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first downstream event = %q body=%s, want retry result delta", eventType, first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if eventType := gjson.GetBytes(second, "type").String(); eventType != "response.completed" {
		t.Fatalf("terminal event type = %q body=%s", eventType, second)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestResponsesWebSocketRetriesNoAvailableAccountBeforeFirstToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	var attempts int
	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			sse := `data: {"type":"response.failed","response":{"status_code":503,"error":{"code":"no_available_account","message":"无可用账号，请稍后重试"}}}` + "\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(sse)),
			}, nil
		}
		sse := "" +
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 2})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "first", PlanType: "plus", AccountID: "acct-1"})
	store.AddAccount(&auth.Account{DBID: 2, AccessToken: "second", PlanType: "plus", AccountID: "acct-2"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first downstream event: %v", err)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first downstream event = %q body=%s, want retry result delta", eventType, first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if eventType := gjson.GetBytes(second, "type").String(); eventType != "response.completed" {
		t.Fatalf("terminal event type = %q body=%s", eventType, second)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestResponsesWebSocketRetriesWrappedNoAvailableAccountStreamBreak(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	var attempts int
	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("stream disconnected before completion: 无可用账号，请稍后重试")
		}
		sse := "" +
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 2})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "first", PlanType: "plus", AccountID: "acct-1"})
	store.AddAccount(&auth.Account{DBID: 2, AccessToken: "second", PlanType: "plus", AccountID: "acct-2"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first downstream event: %v", err)
	}
	if body := string(first); strings.Contains(body, "无可用账号") || strings.Contains(body, "no_available_account") {
		t.Fatalf("wrapped no_available_account leaked downstream: %s", body)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first downstream event = %q body=%s, want retry result delta", eventType, first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if eventType := gjson.GetBytes(second, "type").String(); eventType != "response.completed" {
		t.Fatalf("terminal event type = %q body=%s", eventType, second)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestResponsesWebSocketUsesDirectOpenAIResponsesAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	upstreamRequests := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("upstream path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
			t.Fatalf("upstream Authorization = %q, want Bearer upstream-key", got)
		}
		if got := r.Header.Get("OpenAI-Beta"); got != openAIResponsesWebSocketBeta {
			t.Fatalf("upstream OpenAI-Beta = %q, want %q", got, openAIResponsesWebSocketBeta)
		}
		if !websocket.IsWebSocketUpgrade(r) {
			http.Error(w, "websocket required", http.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade upstream websocket: %v", err)
		}
		defer conn.Close()
		_, body, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read upstream websocket body: %v", err)
		}
		upstreamRequests <- append([]byte(nil), body...)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.output_text.delta","delta":"ok"}`)); err != nil {
			t.Fatalf("write upstream delta: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}`)); err != nil {
			t.Fatalf("write upstream completed: %v", err)
		}
	}))
	defer upstream.Close()

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency: 2,
		TestModel:      "gpt-5.4",
		MaxRetries:     2,
	})
	store.AddAccount(&auth.Account{
		DBID:         1,
		Name:         "direct-openai-responses",
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "upstream-key",
		Models:       []string{"gpt-5.4"},
	})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_prev","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first downstream event: %v", err)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first downstream event = %q body=%s", eventType, first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if eventType := gjson.GetBytes(second, "type").String(); eventType != "response.completed" {
		t.Fatalf("terminal event type = %q body=%s", eventType, second)
	}
	select {
	case body := <-upstreamRequests:
		if model := gjson.GetBytes(body, "model").String(); model != "gpt-5.4" {
			t.Fatalf("upstream model = %q, want gpt-5.4; body=%s", model, body)
		}
		if typ := gjson.GetBytes(body, "type").String(); typ != "response.create" {
			t.Fatalf("upstream websocket type = %q, want response.create; body=%s", typ, body)
		}
		if prev := gjson.GetBytes(body, "previous_response_id").String(); prev != "resp_prev" {
			t.Fatalf("upstream previous_response_id = %q, want resp_prev; body=%s", prev, body)
		}
	default:
		t.Fatal("direct OpenAI Responses account did not receive websocket turn")
	}
}

func TestResponsesWebSocketDirectOpenAIResponsesWithoutPreviousUsesHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamRequests := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("upstream path = %q, want /v1/responses", r.URL.Path)
		}
		if websocket.IsWebSocketUpgrade(r) {
			t.Fatal("direct OpenAI Responses first turn should use HTTP, not websocket")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
			t.Fatalf("upstream Authorization = %q, want Bearer upstream-key", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamRequests <- append([]byte(nil), body...)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
				`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n",
		))
	}))
	defer upstream.Close()

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency: 2,
		TestModel:      "gpt-5.4",
		MaxRetries:     2,
	})
	store.AddAccount(&auth.Account{
		DBID:         1,
		Name:         "direct-openai-responses",
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "upstream-key",
		Models:       []string{"gpt-5.4"},
	})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first downstream event: %v", err)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first downstream event = %q body=%s", eventType, first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if eventType := gjson.GetBytes(second, "type").String(); eventType != "response.completed" {
		t.Fatalf("terminal event type = %q body=%s", eventType, second)
	}
	select {
	case body := <-upstreamRequests:
		if model := gjson.GetBytes(body, "model").String(); model != "gpt-5.4" {
			t.Fatalf("upstream model = %q, want gpt-5.4; body=%s", model, body)
		}
		if gjson.GetBytes(body, "type").Exists() {
			t.Fatalf("direct OpenAI Responses HTTP body should not include websocket envelope type: %s", body)
		}
	default:
		t.Fatal("direct OpenAI Responses account did not receive HTTP turn")
	}
}

func TestResponsesWebSocketDirectOpenAIResponsesPreviousFallsBackToHTTPWhenWebSocketUnsupported(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponseCacheForTest()
	cacheCompletedResponse(
		[]byte(`[{"type":"message","role":"user","content":"call a tool"}]`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_prev","output":[{"type":"function_call","id":"fc_123","call_id":"call_abc","name":"lookup","arguments":"{}"}]}}`),
	)

	httpRequests := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("upstream path = %q, want /v1/responses", r.URL.Path)
		}
		if websocket.IsWebSocketUpgrade(r) {
			http.Error(w, `{"error":{"message":"WebSocket upgrade required (Upgrade: websocket)","type":"invalid_request_error"}}`, http.StatusUpgradeRequired)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read fallback request body: %v", err)
		}
		httpRequests <- append([]byte(nil), body...)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
				`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n",
		))
	}))
	defer upstream.Close()

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency: 2,
		TestModel:      "gpt-5.4",
		MaxRetries:     2,
	})
	store.AddAccount(&auth.Account{
		DBID:         1,
		Name:         "direct-openai-responses",
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "upstream-key",
		Models:       []string{"gpt-5.4"},
	})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_prev","input":[{"type":"function_call_output","call_id":"call_abc","output":"ok"}]}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first downstream event: %v", err)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first downstream event = %q body=%s", eventType, first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if eventType := gjson.GetBytes(second, "type").String(); eventType != "response.completed" {
		t.Fatalf("terminal event type = %q body=%s", eventType, second)
	}
	select {
	case body := <-httpRequests:
		if gjson.GetBytes(body, "type").Exists() {
			t.Fatalf("fallback HTTP body should not include websocket envelope type: %s", body)
		}
		if gjson.GetBytes(body, "previous_response_id").Exists() {
			t.Fatalf("fallback HTTP body should drop previous_response_id after WS unsupported: %s", body)
		}
		input := gjson.GetBytes(body, "input").Array()
		if len(input) != 3 {
			t.Fatalf("fallback HTTP body should inject cached tool context; input count=%d body=%s", len(input), body)
		}
		if typ := input[1].Get("type").String(); typ != "function_call" {
			t.Fatalf("fallback input[1].type = %q, want function_call; body=%s", typ, body)
		}
		if callID := input[2].Get("call_id").String(); callID != "call_abc" {
			t.Fatalf("fallback output call_id = %q, want call_abc; body=%s", callID, body)
		}
	default:
		t.Fatal("direct OpenAI Responses fallback did not receive HTTP turn")
	}
}

func TestShouldRetryResponseFailedBeforeFirstMessageKeepsClientErrorsVisible(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "payment required", body: `{"type":"response.failed","response":{"status_code":402}}`, want: true},
		{name: "rate limited", body: `{"type":"response.failed","response":{"status_code":429}}`, want: true},
		{name: "server error", body: `{"type":"response.failed","response":{"status_code":500}}`, want: true},
		{name: "bad request", body: `{"type":"response.failed","response":{"status_code":400}}`, want: false},
		{name: "unprocessable request", body: `{"type":"response.failed","response":{"status_code":422}}`, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldRetryResponseFailedBeforeFirstMessage([]byte(test.body)); got != test.want {
				t.Fatalf("shouldRetryResponseFailedBeforeFirstMessage() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestResponsesStreamRetriesResponseFailedBeforeFirstToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	var attempts int
	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			sse := `data: {"type":"response.failed","response":{"status_code":401,"error":{"message":"Your remaining $100.00 promotional credit can only be used in Claude Code."}}}` + "\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(sse)),
			}, nil
		}
		sse := "" +
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4", MaxRetries: 2})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "bad", PlanType: "plus", AccountID: "acct-1"})
	store.AddAccount(&auth.Account{DBID: 2, AccessToken: "good", PlanType: "plus", AccountID: "acct-2"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true, UseWebsocket: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	reqBody := strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":true}`)
	resp, err := http.Post(server.URL+"/v1/responses", "application/json", reqBody)
	if err != nil {
		t.Fatalf("post responses: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "response.failed") || strings.Contains(string(body), "promotional credit") {
		t.Fatalf("first account failure leaked to client: %s", body)
	}
	if !strings.Contains(string(body), "response.output_text.delta") || !strings.Contains(string(body), "response.completed") {
		t.Fatalf("retry success events missing: %s", body)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

type errorAfterReader struct {
	*strings.Reader
	err error
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		return n, r.err
	}
	return n, err
}

func (r *errorAfterReader) Close() error {
	return nil
}

func assertNoAvailableAccountResponse(t *testing.T, body []byte) {
	t.Helper()

	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, string(body))
	}
	if payload.Error.Message == "" {
		t.Fatalf("message is empty; body=%s", string(body))
	}
	if payload.Error.Type != ErrorTypeServerError {
		t.Fatalf("type = %q, want %q", payload.Error.Type, ErrorTypeServerError)
	}
	if payload.Error.Code != ErrorCodeNoAvailableAccount {
		t.Fatalf("code = %q, want %q", payload.Error.Code, ErrorCodeNoAvailableAccount)
	}
}

func TestUsageLogErrorMessageExtractsStructuredError(t *testing.T) {
	body := []byte(`{"error":{"code":"rate_limit_exceeded","type":"server_error","message":"Too many requests"}}`)

	got := usageLogErrorMessage(http.StatusTooManyRequests, body)

	if got != "rate_limit_exceeded · server_error · Too many requests" {
		t.Fatalf("usageLogErrorMessage() = %q", got)
	}
}

func TestResponsesEndpointsAllowCompactionInputType(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewHandler(auth.NewStore(nil, nil, nil), nil, nil, nil)
	body := []byte(`{
		"model":"gpt-5.4",
		"input":[
			{"type":"message","role":"user","content":"hello"},
			{"type":"compaction","summary":"previous context was compacted"}
		]
	}`)

	tests := []struct {
		name    string
		path    string
		handler gin.HandlerFunc
	}{
		{name: "responses", path: "/v1/responses", handler: handler.Responses},
		{name: "responses compact", path: "/v1/responses/compact", handler: handler.ResponsesCompact},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			req := httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(recorder)
			ginCtx.Request = req

			test.handler(ginCtx)

			if recorder.Code == http.StatusBadRequest && strings.Contains(recorder.Body.String(), "invalid_input_type") {
				t.Fatalf("compaction input type was rejected by local validation: %s", recorder.Body.String())
			}
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d after validation passes; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
			}
			assertNoAvailableAccountResponse(t, recorder.Body.Bytes())
		})
	}
}

func TestResponsesCompactUsesOpenAIResponsesAPIAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("upstream path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
			t.Fatalf("upstream Authorization = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		if gjson.GetBytes(body, "stream").Bool() {
			t.Fatalf("compact upstream request should be non-stream: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_compact",
			"object":"response",
			"model":"gpt-5.4-mini",
			"service_tier":"default",
			"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],
			"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}
		}`))
	}))
	defer upstream.Close()

	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4-mini"})
	store.AddAccount(&auth.Account{
		DBID:         1,
		Name:         "openai-responses",
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "upstream-key",
		Models:       []string{"gpt-5.4-mini"},
	})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5.4-mini","input":"hello","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = req

	handler.ResponsesCompact(ginCtx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if object := gjson.GetBytes(recorder.Body.Bytes(), "object").String(); object != "response" {
		t.Fatalf("object = %q, want response; body=%s", object, recorder.Body.String())
	}
	output := gjson.GetBytes(recorder.Body.Bytes(), "output")
	if !output.IsArray() || len(output.Array()) == 0 {
		t.Fatalf("output should be preserved for compact Responses clients; body=%s", recorder.Body.String())
	}
	if content := gjson.GetBytes(recorder.Body.Bytes(), "output.0.content.0.text").String(); content != "ok" {
		t.Fatalf("content = %q, want ok; body=%s", content, recorder.Body.String())
	}
	if total := gjson.GetBytes(recorder.Body.Bytes(), "usage.total_tokens").Int(); total != 3 {
		t.Fatalf("usage.total_tokens = %d, want 3; body=%s", total, recorder.Body.String())
	}
}

func TestOpenAIHandlersUseCachedRawBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewHandler(auth.NewStore(nil, nil, nil), nil, nil, nil)
	validBodies := map[string][]byte{
		"/v1/responses":         []byte(`{"model":"gpt-5.4","input":"hello"}`),
		"/v1/responses/compact": []byte(`{"model":"gpt-5.4","input":"hello"}`),
		"/v1/chat/completions":  []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}]}`),
		"/v1/messages":          []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}]}`),
	}
	tests := []struct {
		path    string
		handler gin.HandlerFunc
	}{
		{path: "/v1/responses", handler: handler.Responses},
		{path: "/v1/responses/compact", handler: handler.ResponsesCompact},
		{path: "/v1/chat/completions", handler: handler.ChatCompletions},
		{path: "/v1/messages", handler: handler.Messages},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`not-json`))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(recorder)
			ginCtx.Request = req
			ginCtx.Set("raw_body", validBodies[test.path])

			test.handler(ginCtx)

			if recorder.Code == http.StatusBadRequest {
				t.Fatalf("handler reread Request.Body instead of cached raw_body; body=%s", recorder.Body.String())
			}
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d after cached body validation; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
			}
		})
	}
}

func TestReadUpstreamErrorBodyLimitsLargeBodies(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxUpstreamErrorBodyBytes+1024))),
	}

	body, err := readUpstreamErrorBody(resp)

	if err != nil {
		t.Fatalf("readUpstreamErrorBody returned error: %v", err)
	}
	if len(body) != maxUpstreamErrorBodyBytes {
		t.Fatalf("len(body) = %d, want %d", len(body), maxUpstreamErrorBodyBytes)
	}
}

func TestSendUpstreamErrorMasksAndTruncatesBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	handler := &Handler{}
	body := []byte(`{"error":{"message":"secret sk-test1234567890` + strings.Repeat("x", 4096) + `"}}`)

	handler.sendUpstreamError(ctx, http.StatusInternalServerError, body)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if strings.Contains(recorder.Body.String(), "sk-test1234567890") {
		t.Fatalf("response leaked upstream secret: %s", recorder.Body.String())
	}
	if recorder.Body.Len() > 2600 {
		t.Fatalf("response body length = %d, want bounded error payload", recorder.Body.Len())
	}
}

func TestResponsesEndpointsAllowGPT55MaxOutputTokens128K(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewHandler(auth.NewStore(nil, nil, nil), nil, nil, nil)
	body := []byte(`{"model":"gpt-5.5","input":"hello","max_output_tokens":128000}`)

	tests := []struct {
		name    string
		path    string
		handler gin.HandlerFunc
	}{
		{name: "responses", path: "/v1/responses", handler: handler.Responses},
		{name: "responses compact", path: "/v1/responses/compact", handler: handler.ResponsesCompact},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			req := httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(recorder)
			ginCtx.Request = req

			test.handler(ginCtx)

			if recorder.Code == http.StatusBadRequest && strings.Contains(recorder.Body.String(), "max_output_tokens") {
				t.Fatalf("gpt-5.5 128k max_output_tokens was rejected by local validation: %s", recorder.Body.String())
			}
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d after validation passes; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
			}
			assertNoAvailableAccountResponse(t, recorder.Body.Bytes())
		})
	}
}

func TestResponsesNoAvailableAccountFailsFastWithoutCancelledContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewHandler(auth.NewStore(nil, nil, nil), nil, nil, nil)
	body := []byte(`{"model":"gpt-5.4","input":"hello"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = req

	start := time.Now()
	handler.Responses(ginCtx)
	elapsed := time.Since(start)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	assertNoAvailableAccountResponse(t, recorder.Body.Bytes())
	if elapsed > 150*time.Millisecond {
		t.Fatalf("Responses took %s with no dispatch candidates; want fast failure", elapsed)
	}
}

func TestExtractResponseImageGenerationOutputDedupes(t *testing.T) {
	event := []byte(`{"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","result":"` + tinyPNGBase64 + `","output_format":"png"}}`)
	seen := make(map[string]struct{})

	raw, ok := extractResponseImageGenerationOutput(event, seen)
	if !ok {
		t.Fatal("expected image_generation_call output to be extracted")
	}

	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatalf("decode extracted image item: %v", err)
	}
	if item["result"] != tinyPNGBase64 {
		t.Fatalf("result = %v, want tiny PNG", item["result"])
	}
	if item["bytes"] != float64(tinyPNGByteSize(t)) || item["width"] != float64(1) || item["height"] != float64(1) {
		t.Fatalf("image stats = bytes:%v width:%v height:%v", item["bytes"], item["width"], item["height"])
	}

	if _, ok := extractResponseImageGenerationOutput(event, seen); ok {
		t.Fatal("expected duplicate image_generation_call output to be ignored")
	}
}

func TestRestoreMissingResponseOutputsUsesOutputItemDone(t *testing.T) {
	response := []byte(`{"id":"resp_1","object":"response","output":[]}`)
	outputItems := []json.RawMessage{
		json.RawMessage(`{"id":"rs_1","type":"reasoning","encrypted_content":"opaque","summary":[]}`),
		json.RawMessage(`{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"{\"age\":30,\"name\":\"John\"}"}]}`),
	}

	got := restoreMissingResponseOutputs(response, outputItems)

	output := gjson.GetBytes(got, "output")
	if !output.IsArray() || len(output.Array()) != 2 {
		t.Fatalf("output count = %d, want 2; body=%s", len(output.Array()), got)
	}
	if typ := output.Array()[0].Get("type").String(); typ != "reasoning" {
		t.Fatalf("first output type = %q, want reasoning; body=%s", typ, got)
	}
	if text := output.Array()[1].Get("content.0.text").String(); text != `{"age":30,"name":"John"}` {
		t.Fatalf("message text = %q, want structured JSON; body=%s", text, got)
	}
}

func TestRestoreMissingResponseOutputsPreservesCompletedOutput(t *testing.T) {
	response := []byte(`{"id":"resp_1","object":"response","output":[{"id":"msg_existing","type":"message","content":[{"type":"output_text","text":"done"}]}]}`)
	outputItems := []json.RawMessage{
		json.RawMessage(`{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"fallback"}]}`),
	}

	got := restoreMissingResponseOutputs(response, outputItems)

	if string(got) != string(response) {
		t.Fatalf("non-empty completed output should be preserved, got %s", got)
	}
}

func TestAppendMissingResponseImageOutputsAddsOutputItemDone(t *testing.T) {
	response := []byte(`{"id":"resp_1"}`)
	imageOutputs := []json.RawMessage{
		json.RawMessage(`{"id":"ig_1","type":"image_generation_call","result":"` + tinyPNGBase64 + `","output_format":"png"}`),
	}

	got := appendMissingResponseImageOutputs(response, imageOutputs)

	var payload struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("decode merged response: %v", err)
	}
	if len(payload.Output) != 1 {
		t.Fatalf("output count = %d, want 1; body=%s", len(payload.Output), got)
	}
	if payload.Output[0]["type"] != "image_generation_call" || payload.Output[0]["result"] != tinyPNGBase64 {
		t.Fatalf("unexpected output item: %#v", payload.Output[0])
	}
	if payload.Output[0]["bytes"] != float64(tinyPNGByteSize(t)) || payload.Output[0]["width"] != float64(1) || payload.Output[0]["height"] != float64(1) {
		t.Fatalf("image stats = bytes:%v width:%v height:%v", payload.Output[0]["bytes"], payload.Output[0]["width"], payload.Output[0]["height"])
	}

	gotAgain := appendMissingResponseImageOutputs(got, imageOutputs)
	if err := json.Unmarshal(gotAgain, &payload); err != nil {
		t.Fatalf("decode merged response again: %v", err)
	}
	if len(payload.Output) != 1 {
		t.Fatalf("duplicate output count = %d, want 1; body=%s", len(payload.Output), gotAgain)
	}
}

func TestAppendMissingResponseImageOutputsAnnotatesExistingOutput(t *testing.T) {
	response := []byte(`{"id":"resp_1","output":[{"id":"ig_1","type":"image_generation_call","result":"` + tinyPNGBase64 + `","output_format":"png"}]}`)

	got := appendMissingResponseImageOutputs(response, nil)

	var payload struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("decode annotated response: %v", err)
	}
	if len(payload.Output) != 1 {
		t.Fatalf("output count = %d, want 1; body=%s", len(payload.Output), got)
	}
	if payload.Output[0]["bytes"] != float64(tinyPNGByteSize(t)) || payload.Output[0]["width"] != float64(1) || payload.Output[0]["height"] != float64(1) {
		t.Fatalf("image stats = bytes:%v width:%v height:%v", payload.Output[0]["bytes"], payload.Output[0]["width"], payload.Output[0]["height"])
	}
}

func TestAccountFilterForSparkRequiresPro(t *testing.T) {
	filter := accountFilterForModel("gpt-5.3-codex-spark")
	if filter == nil {
		t.Fatal("expected filter for spark model")
	}
	if !filter(&auth.Account{PlanType: "pro"}) {
		t.Fatal("spark filter should allow pro accounts")
	}
	if !filter(&auth.Account{PlanType: "prolite"}) {
		t.Fatal("spark filter should treat prolite as pro")
	}
	if filter(&auth.Account{PlanType: "plus"}) {
		t.Fatal("spark filter should reject non-pro accounts")
	}
	normalFilter := accountFilterForModel("gpt-5.3-codex")
	if normalFilter == nil || !normalFilter(&auth.Account{PlanType: "plus"}) {
		t.Fatal("non-spark model filter should allow available accounts")
	}
	directOpenAIAccount := &auth.Account{
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      "https://api.openai.com",
		APIKey:       "sk-test",
		Models:       []string{"gpt-4.1"},
	}
	if normalFilter(directOpenAIAccount) {
		t.Fatal("codex account filter should reject direct OpenAI Responses accounts")
	}
	responsesFilter := accountFilterForResponsesModel("gpt-4.1", false)
	if !responsesFilter(directOpenAIAccount) {
		t.Fatal("responses filter should allow direct OpenAI account for configured model")
	}
	if responsesFilter(&auth.Account{AccessToken: "codex-at", PlanType: "plus"}) {
		t.Fatal("responses filter should reject codex accounts for direct-only models")
	}
	if !accountFilterForResponsesModel("gpt-4.1", true)(&auth.Account{AccessToken: "codex-at", PlanType: "plus"}) {
		t.Fatal("responses filter should allow codex accounts when model is in Codex catalog")
	}
	if accountFilterForResponsesModel("gpt-4.2", false)(directOpenAIAccount) {
		t.Fatal("responses filter should reject direct OpenAI account for unconfigured model")
	}
	cooled := &auth.Account{PlanType: "pro"}
	cooled.SetModelCooldownUntil("gpt-5.3-codex-spark", "model_capacity", time.Now().Add(time.Minute))
	if filter(cooled) {
		t.Fatal("filter should reject model-cooled accounts")
	}
}

func TestSupportedModelIDsIncludesOpenAIResponsesAccountModels(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2})
	store.AddAccount(&auth.Account{
		DBID:         1,
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      "https://api.openai.com",
		APIKey:       "sk-test",
		Models:       []string{"gpt-4.1-direct"},
	})

	handler := &Handler{store: store}
	models := handler.supportedModelIDs(context.Background())
	for _, model := range models {
		if model == "gpt-4.1-direct" {
			return
		}
	}
	t.Fatalf("supported models missing direct OpenAI model: %v", models)
}

func TestResponsesRequestTriggersRecoveryProbeWhenNoDispatchableAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamRequests := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("upstream path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
			t.Fatalf("upstream Authorization = %q, want Bearer upstream-key", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamRequests <- append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_recovered",
			"object":"response",
			"model":"gpt-5.4",
			"status":"completed",
			"output":[{"type":"message","content":[{"type":"output_text","text":"recovered"}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`))
	}))
	defer upstream.Close()

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:               1,
		TestConcurrency:              1,
		TestModel:                    "gpt-5.4",
		RecoveryProbeIntervalMinutes: 30,
	})
	account := &auth.Account{
		DBID:          1,
		Name:          "recoverable-openai-responses",
		UpstreamType:  auth.UpstreamOpenAIResponses,
		BaseURL:       upstream.URL,
		APIKey:        "upstream-key",
		Models:        []string{"gpt-5.4"},
		Status:        auth.StatusError,
		HealthTier:    auth.HealthTierBanned,
		FailureStreak: 3,
	}
	atomic.StoreInt32(&account.Disabled, 1)
	store.AddAccount(account)

	recoveryProbes := make(chan int64, 1)
	store.SetUsageProbeFunc(func(_ context.Context, probed *auth.Account) error {
		recoveryProbes <- probed.DBID
		return nil
	})

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	router := gin.New()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if text := gjson.GetBytes(recorder.Body.Bytes(), "output.0.content.0.text").String(); text != "recovered" {
		t.Fatalf("response text = %q, want recovered; body=%s", text, recorder.Body.String())
	}
	select {
	case id := <-recoveryProbes:
		if id != account.DBID {
			t.Fatalf("recovery probe account id = %d, want %d", id, account.DBID)
		}
	default:
		t.Fatal("request should trigger recovery probe before returning no_available_account")
	}
	select {
	case body := <-upstreamRequests:
		if model := gjson.GetBytes(body, "model").String(); model != "gpt-5.4" {
			t.Fatalf("upstream model = %q, want gpt-5.4; body=%s", model, body)
		}
	default:
		t.Fatal("recovered account should receive the upstream /v1/responses request")
	}
	if atomic.LoadInt32(&account.Disabled) != 0 {
		t.Fatal("successful recovery probe should clear Disabled before dispatch")
	}
	account.Mu().RLock()
	status := account.Status
	healthTier := account.HealthTier
	failureStreak := account.FailureStreak
	account.Mu().RUnlock()
	if status != auth.StatusReady {
		t.Fatalf("account status = %v, want ready", status)
	}
	if healthTier == auth.HealthTierBanned {
		t.Fatal("successful recovery probe should move account out of banned tier")
	}
	if failureStreak != 0 {
		t.Fatalf("FailureStreak = %d, want 0", failureStreak)
	}
}

func TestResponsesRetriesWhenPickedAccountLacksDispatchCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})

	var attempts int32
	var staleAttempts int32
	WebsocketExecuteFunc = nil

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:  1,
		TestConcurrency: 1,
		TestModel:       "gpt-5.4",
		MaxRetries:      1,
	})
	store.AddAccount(&auth.Account{
		DBID:        1,
		Name:        "stale-empty-token",
		PlanType:    "plus",
		AccountID:   "acct-1",
		AccessToken: "stale-token",
	})
	store.AddAccount(&auth.Account{
		DBID:        2,
		Name:        "healthy-token",
		PlanType:    "plus",
		AccountID:   "acct-2",
		AccessToken: "healthy-token",
	})

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	originalExecute := ExecuteRequest
	ExecuteRequest = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header, useWebsocket ...bool) (*http.Response, error) {
		atomic.AddInt32(&attempts, 1)
		if account.ID() == 1 {
			atomic.AddInt32(&staleAttempts, 1)
			return nil, ErrNoAvailableAccount()
		}
		sse := "" +
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}
	t.Cleanup(func() {
		ExecuteRequest = originalExecute
	})

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":true}`))
	if err != nil {
		t.Fatalf("post responses: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "无可用账号") {
		t.Fatalf("picked-account credential miss leaked no_available_account: %s", body)
	}
	if !strings.Contains(string(body), `"type":"response.completed"`) {
		t.Fatalf("retry success events missing: %s", body)
	}
	if got := atomic.LoadInt32(&staleAttempts); got > 1 {
		t.Fatalf("stale account attempts = %d, want at most 1", got)
	}
	if got := atomic.LoadInt32(&attempts); got < 1 || got > 2 {
		t.Fatalf("attempts = %d, want 1 or 2 depending scheduler order", got)
	}
}

func TestResponsesWebSocketRetriesWhenPickedAccountLacksDispatchCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CODEX_WS_FALLBACK_HTTP", "false")

	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() {
		WebsocketExecuteFunc = previousExec
	})
	WebsocketExecuteFunc = nil

	var attempts int32
	var staleAttempts int32
	originalExecute := ExecuteRequest
	ExecuteRequest = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header, useWebsocket ...bool) (*http.Response, error) {
		atomic.AddInt32(&attempts, 1)
		if account.ID() == 1 {
			atomic.AddInt32(&staleAttempts, 1)
			return nil, ErrNoAvailableAccount()
		}
		sse := "" +
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"service_tier":"default"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}
	t.Cleanup(func() {
		ExecuteRequest = originalExecute
	})

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:  1,
		TestConcurrency: 1,
		TestModel:       "gpt-5.4",
		MaxRetries:      1,
	})
	store.AddAccount(&auth.Account{
		DBID:        1,
		Name:        "stale-token",
		PlanType:    "plus",
		AccountID:   "acct-1",
		AccessToken: "stale-token",
	})
	store.AddAccount(&auth.Account{
		DBID:        2,
		Name:        "healthy-token",
		PlanType:    "plus",
		AccountID:   "acct-2",
		AccessToken: "healthy-token",
	})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true, CodexUpstreamTransport: "http"}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first downstream event: %v", err)
	}
	if eventType := gjson.GetBytes(first, "type").String(); eventType != "response.output_text.delta" {
		t.Fatalf("first downstream event = %q body=%s, want retry result delta", eventType, first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if eventType := gjson.GetBytes(second, "type").String(); eventType != "response.completed" {
		t.Fatalf("terminal event type = %q body=%s", eventType, second)
	}
	if got := atomic.LoadInt32(&staleAttempts); got > 1 {
		t.Fatalf("stale account attempts = %d, want at most 1", got)
	}
	if got := atomic.LoadInt32(&attempts); got < 1 || got > 2 {
		t.Fatalf("attempts = %d, want 1 or 2 depending scheduler order", got)
	}
}

func TestClassify429UsageLimitExactResetUsesAccountCooldown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	decision := classify429RateLimit(&auth.Account{PlanType: "team"}, []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":120}}`), nil, now, "gpt-5.4")
	if decision.Scope != rateLimitScopeAccount || decision.Reason != "usage_limit" {
		t.Fatalf("decision = %#v, want account usage_limit", decision)
	}
	if decision.Cooldown != 120*time.Second {
		t.Fatalf("Cooldown = %v, want 120s", decision.Cooldown)
	}
}

func TestClassify429CapacityUsesModelCooldown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"error":{"message":"Selected model is at capacity. Please try a different model."}}`)
	decision := classify429RateLimit(&auth.Account{PlanType: "team"}, body, nil, now, "gpt-5.4")
	if decision.Scope != rateLimitScopeModel || decision.Reason != "model_capacity" {
		t.Fatalf("decision = %#v, want model capacity cooldown", decision)
	}
	if decision.Cooldown != 5*time.Minute {
		t.Fatalf("Cooldown = %v, want 5m", decision.Cooldown)
	}
}

func TestClassify429Header7dUsesAccountCooldown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("x-codex-secondary-used-percent", "100")
	resp.Header.Set("x-codex-secondary-window-minutes", "10080")
	resp.Header.Set("x-codex-secondary-reset-after-seconds", "3600")
	decision := classify429RateLimit(&auth.Account{PlanType: "team"}, nil, resp, now, "gpt-5.4")
	if decision.Scope != rateLimitScopeAccount || decision.Reason != "rate_limited_7d" {
		t.Fatalf("decision = %#v, want 7d account cooldown", decision)
	}
	if decision.Cooldown != time.Hour {
		t.Fatalf("Cooldown = %v, want 1h", decision.Cooldown)
	}
}

func TestShouldRetryHTTPStatusSplitsRateLimitBudget(t *testing.T) {
	generalRetries := 0
	rateLimitRetries := 0
	if !shouldRetryHTTPStatus(http.StatusTooManyRequests, &generalRetries, &rateLimitRetries, 2, 1) {
		t.Fatal("first 429 should consume rate-limit retry budget")
	}
	if shouldRetryHTTPStatus(http.StatusTooManyRequests, &generalRetries, &rateLimitRetries, 2, 1) {
		t.Fatal("second 429 should be blocked by rate-limit retry budget")
	}
	if !shouldRetryHTTPStatus(http.StatusServiceUnavailable, &generalRetries, &rateLimitRetries, 2, 1) {
		t.Fatal("503 should still use general retry budget")
	}
	if !shouldRetryHTTPStatus(http.StatusGatewayTimeout, &generalRetries, &rateLimitRetries, 2, 1) {
		t.Fatal("504 gateway timeout before the first upstream event should use general retry budget")
	}
	if !shouldRetryHTTPStatus(http.StatusForbidden, &generalRetries, &rateLimitRetries, 2, 1) {
		t.Fatal("403 account-level upstream failure should switch accounts without exposing provider quota to client")
	}
	if !shouldRetryHTTPStatus(http.StatusPaymentRequired, &generalRetries, &rateLimitRetries, 2, 1) {
		t.Fatal("402 account-level upstream failure should retry without exposing provider quota to client")
	}
	if generalRetries != 2 || rateLimitRetries != 1 {
		t.Fatalf("budgets = general %d rate %d, want 2/1", generalRetries, rateLimitRetries)
	}
}

func TestDeactivatedWorkspace402MarksAccountError(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: 42, AccessToken: "at"}
	handler := &Handler{store: store}
	body := []byte(`{"detail":{"code":"deactivated_workspace"}}`)

	if !IsDeactivatedWorkspaceError(body) {
		t.Fatal("expected deactivated workspace body to be detected")
	}
	if got := upstreamErrorKind(http.StatusPaymentRequired, body, codex429Decision{}); got != "deactivated_workspace" {
		t.Fatalf("upstreamErrorKind = %q, want deactivated_workspace", got)
	}

	handler.applyCooldownForModel(account, http.StatusPaymentRequired, body, &http.Response{Header: make(http.Header)}, "gpt-5.4")

	if got := account.RuntimeStatus(); got != "error" {
		t.Fatalf("RuntimeStatus() = %q, want error", got)
	}
	account.Mu().RLock()
	errorMsg := account.ErrorMsg
	account.Mu().RUnlock()
	if !strings.Contains(errorMsg, "deactivated_workspace") {
		t.Fatalf("ErrorMsg = %q, want deactivated_workspace", errorMsg)
	}
}

func TestPaymentRequiredUsesLongRecoveryCooldown(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: 43, AccessToken: "at", Status: auth.StatusReady}
	startedAt := time.Now()

	if !ApplyUpstreamAccountFailure(store, account, http.StatusPaymentRequired, []byte(`{"error":"Insufficient balance"}`), nil, "gpt-5.4") {
		t.Fatal("ApplyUpstreamAccountFailure() = false, want true")
	}
	reason, until := account.GetCooldownSnapshot()
	if reason != "payment_required" {
		t.Fatalf("CooldownReason = %q, want payment_required", reason)
	}
	if until.Before(startedAt.Add(6*time.Hour - time.Minute)) {
		t.Fatalf("CooldownUntil = %s, want about 6 hours after %s", until, startedAt)
	}
}

func TestSendFinalUpstreamError_UsageLimitRewrites429(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	handler := &Handler{}
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"free","resets_at":1775317531,"resets_in_seconds":602705}}`)

	handler.sendFinalUpstreamError(ctx, http.StatusTooManyRequests, body)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if got := recorder.Header().Get("Retry-After"); got != "602705" {
		t.Fatalf("Retry-After = %q, want %q", got, "602705")
	}

	var payload struct {
		Error struct {
			Message         string `json:"message"`
			Type            string `json:"type"`
			Code            string `json:"code"`
			PlanType        string `json:"plan_type"`
			ResetsAt        int64  `json:"resets_at"`
			ResetsInSeconds int64  `json:"resets_in_seconds"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Type != "server_error" {
		t.Fatalf("type = %q, want %q", payload.Error.Type, "server_error")
	}
	if payload.Error.Code != "account_pool_usage_limit_reached" {
		t.Fatalf("code = %q, want %q", payload.Error.Code, "account_pool_usage_limit_reached")
	}
	if payload.Error.PlanType != "free" {
		t.Fatalf("plan_type = %q, want %q", payload.Error.PlanType, "free")
	}
	if payload.Error.ResetsAt != 1775317531 {
		t.Fatalf("resets_at = %d, want %d", payload.Error.ResetsAt, 1775317531)
	}
	if payload.Error.ResetsInSeconds != 602705 {
		t.Fatalf("resets_in_seconds = %d, want %d", payload.Error.ResetsInSeconds, 602705)
	}
	if payload.Error.Message == "" {
		t.Fatal("expected non-empty aggregated error message")
	}
}

func TestSendFinalUpstreamError_FallsBackForNonUsageLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	handler := &Handler{}
	body := []byte(`{"error":{"type":"rate_limit_error","message":"Too many requests"}}`)

	handler.sendFinalUpstreamError(ctx, http.StatusTooManyRequests, body)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want empty", got)
	}
}

func TestSendFinalUpstreamError_UsageLimitMissingTimeFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	handler := &Handler{}
	// usage_limit_reached 但不含 resets_at / resets_in_seconds
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"limit reached"}}`)

	handler.sendFinalUpstreamError(ctx, http.StatusTooManyRequests, body)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	// 无 resets_in_seconds 时不应设置 Retry-After
	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want empty (no resets_in_seconds)", got)
	}

	// 验证零值字段不出现在响应中
	var raw map[string]map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj := raw["error"]
	if _, exists := errObj["resets_at"]; exists {
		t.Fatal("resets_at should be omitted when 0")
	}
	if _, exists := errObj["resets_in_seconds"]; exists {
		t.Fatal("resets_in_seconds should be omitted when 0")
	}
	if _, exists := errObj["plan_type"]; exists {
		t.Fatal("plan_type should be omitted when empty")
	}
}

func TestSendFinalUpstreamError_Non429StatusPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	handler := &Handler{}
	body := []byte(`{"error":{"type":"server_error","message":"internal failure"}}`)

	handler.sendFinalUpstreamError(ctx, http.StatusInternalServerError, body)

	// 非 429 直接透传原状态码
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

func TestCompute429CooldownPlusUsesWindowHeaders(t *testing.T) {
	handler := &Handler{}
	account := &auth.Account{PlanType: "plus"}
	resp := &http.Response{
		Header: make(http.Header),
	}
	resp.Header.Set("x-codex-primary-used-percent", "100")
	resp.Header.Set("x-codex-primary-window-minutes", "300")
	resp.Header.Set("x-codex-secondary-used-percent", "20")
	resp.Header.Set("x-codex-secondary-window-minutes", "10080")

	got := handler.compute429Cooldown(account, []byte(`{"error":{"type":"usage_limit_reached"}}`), resp)
	want := 5 * time.Hour
	if got != want {
		t.Fatalf("cooldown = %v, want %v", got, want)
	}
}

func TestCompute429CooldownPlusPrefersExactResetTime(t *testing.T) {
	handler := &Handler{}
	account := &auth.Account{PlanType: "plus"}
	resp := &http.Response{
		Header: make(http.Header),
	}
	resp.Header.Set("x-codex-primary-used-percent", "100")
	resp.Header.Set("x-codex-primary-window-minutes", "10080")

	got := handler.compute429Cooldown(account, []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":1800}}`), resp)
	want := 30 * time.Minute
	if got != want {
		t.Fatalf("cooldown = %v, want %v", got, want)
	}
}

func TestApply429CooldownPremiumMarks5hRateLimitFromWindow(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: 101, PlanType: "plus"}
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("x-codex-primary-used-percent", "100")
	resp.Header.Set("x-codex-primary-window-minutes", "300")
	resp.Header.Set("x-codex-primary-reset-after-seconds", "900")

	decision := Apply429Cooldown(store, account, []byte(`{"error":{"type":"usage_limit_reached"}}`), resp, "gpt-5.4")

	if decision.Scope != rateLimitScopeAccount || decision.Reason != "rate_limited_5h" {
		t.Fatalf("decision = %#v, want premium 5h account decision", decision)
	}
	if !account.IsPremium5hRateLimited() {
		t.Fatal("expected account to enter premium 5h rate limited state")
	}
	pct5h, resetAt, ok := account.GetUsageSnapshot5h()
	if !ok {
		t.Fatal("expected 5h snapshot to be set")
	}
	if pct5h != 100 {
		t.Fatalf("usage_percent_5h = %v, want 100", pct5h)
	}
	if got := resetAt.Sub(time.Now()); got < 14*time.Minute || got > 16*time.Minute {
		t.Fatalf("resetAt delta = %v, want about 15m", got)
	}
}

func TestApply429CooldownUsageLimitUpdatesFreePlanMetadata(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("database.New 返回错误: %v", err)
	}
	defer db.Close()

	id, err := db.InsertAccountWithCredentials(ctx, "usage-limit-account", map[string]interface{}{
		"plan_type": "pro",
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials 返回错误: %v", err)
	}

	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: id, AccessToken: "at", PlanType: "pro"}
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"free","resets_in_seconds":3600}}`)

	decision := Apply429Cooldown(store, account, body, &http.Response{Header: make(http.Header)}, "gpt-5.4")

	if decision.Scope != rateLimitScopeAccount || decision.Reason != "usage_limit" {
		t.Fatalf("decision = %#v, want account usage_limit", decision)
	}
	if got := account.GetPlanType(); got != "free" {
		t.Fatalf("account plan_type = %q, want free", got)
	}
	pct, ok := account.GetUsagePercent7d()
	if !ok || pct != 100 {
		t.Fatalf("usage_percent_7d = %v ok=%v, want 100 true", pct, ok)
	}
	if got := account.RuntimeStatus(); got != "usage_exhausted" {
		t.Fatalf("RuntimeStatus() = %q, want usage_exhausted", got)
	}

	resetDelta := time.Until(account.GetReset7dAt())
	if resetDelta < 59*time.Minute || resetDelta > 61*time.Minute {
		t.Fatalf("reset_7d_at delta = %v, want about 1h", resetDelta)
	}

	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatalf("GetAccountByID 返回错误: %v", err)
	}
	if got := row.GetCredential("plan_type"); got != "free" {
		t.Fatalf("persisted plan_type = %q, want free", got)
	}
	if got := row.GetCredential("codex_7d_used_percent"); got != "100" {
		t.Fatalf("persisted codex_7d_used_percent = %q, want 100", got)
	}
	if got := row.GetCredential("codex_7d_reset_at"); got == "" {
		t.Fatal("persisted codex_7d_reset_at is empty")
	}
}

func TestSyncCodexUsageStateUpdatesPlanTypeFromHeader(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("database.New returned error: %v", err)
	}
	defer db.Close()

	id, err := db.InsertAccountWithCredentials(ctx, "plan-header-account", map[string]interface{}{
		"plan_type": "free",
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials returned error: %v", err)
	}

	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: id, AccessToken: "at", PlanType: "free"}
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("x-codex-plan-type", "Enterprise")
	resp.Header.Set("x-codex-primary-used-percent", "12")
	resp.Header.Set("x-codex-primary-window-minutes", "300")
	resp.Header.Set("x-codex-primary-reset-after-seconds", "1200")
	resp.Header.Set("x-codex-secondary-used-percent", "3")
	resp.Header.Set("x-codex-secondary-window-minutes", "10080")
	resp.Header.Set("x-codex-secondary-reset-after-seconds", "600000")

	result := SyncCodexUsageState(store, account, resp)

	if got := account.GetPlanType(); got != "enterprise" {
		t.Fatalf("account plan_type = %q, want enterprise", got)
	}
	if !result.Used5hHeaders || !result.HasUsage5h || !result.HasUsage7d {
		t.Fatalf("usage sync result = %#v, want 5h and 7d headers detected", result)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatalf("GetAccountByID returned error: %v", err)
	}
	if got := row.GetCredential("plan_type"); got != "enterprise" {
		t.Fatalf("persisted plan_type = %q, want enterprise", got)
	}
}

func TestApply429CooldownUnknown429UsesModelCooldown(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: 102, PlanType: "pro"}

	decision := Apply429Cooldown(store, account, []byte(`{"error":{"type":"rate_limit_error","message":"Too many requests"}}`), &http.Response{Header: make(http.Header)}, "gpt-5.4")

	if decision.Scope != rateLimitScopeModel {
		t.Fatalf("decision.Scope = %q, want model", decision.Scope)
	}
	if got := decision.ResetAt.Sub(time.Now()); got < 4*time.Minute || got > 6*time.Minute {
		t.Fatalf("resetAt delta = %v, want about 5m", got)
	}
	if !account.IsModelRateLimited("gpt-5.4") {
		t.Fatal("expected model cooldown")
	}
}

func TestSyncCodexUsageStateTriggersPremium5hLimitWith5hHeadersOnly(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: 103, PlanType: "team"}
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("x-codex-primary-used-percent", "100")
	resp.Header.Set("x-codex-primary-window-minutes", "300")
	resp.Header.Set("x-codex-primary-reset-after-seconds", "600")

	result := SyncCodexUsageState(store, account, resp)

	if !result.Used5hHeaders {
		t.Fatal("expected 5h headers to be detected")
	}
	if result.HasUsage7d {
		t.Fatal("expected no 7d usage snapshot")
	}
	if !result.HasUsage5h {
		t.Fatal("expected 5h usage snapshot")
	}
	if !result.Persisted5hOnly {
		t.Fatal("expected 5h-only persistence path to be selected")
	}
	if !result.Premium5hRateLimited {
		t.Fatal("expected premium 5h rate limit to trigger")
	}
	if !account.IsPremium5hRateLimited() {
		t.Fatal("expected account to be premium 5h rate limited")
	}
}

func TestAuthMiddlewareSetsAPIKeyContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("database.New 返回错误: %v", err)
	}
	defer db.Close()

	key := "sk-test-auth-1234567890"
	id, err := db.InsertAPIKey(context.Background(), "Team A", key)
	if err != nil {
		t.Fatalf("InsertAPIKey 返回错误: %v", err)
	}

	handler := NewHandler(nil, db, nil, nil)
	router := gin.New()
	router.Use(handler.authMiddleware())
	router.GET("/ok", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"id":     c.MustGet(contextAPIKeyID),
			"name":   c.MustGet(contextAPIKeyName),
			"masked": c.MustGet(contextAPIKeyMasked),
			"raw":    c.MustGet("apiKey"),
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var payload struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Masked string `json:"masked"`
		Raw    string `json:"raw"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal 返回错误: %v", err)
	}

	if payload.ID != id {
		t.Fatalf("id = %d, want %d", payload.ID, id)
	}
	if payload.Name != "Team A" {
		t.Fatalf("name = %q, want %q", payload.Name, "Team A")
	}
	if payload.Masked == "" || payload.Masked == key {
		t.Fatalf("masked = %q, want masked value", payload.Masked)
	}
	if payload.Raw != key {
		t.Fatalf("raw = %q, want %q", payload.Raw, key)
	}
}

func TestAuthMiddlewareRejectsExpiredAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("database.New 返回错误: %v", err)
	}
	defer db.Close()

	key := "sk-test-expired-1234567890"
	if _, err := db.InsertAPIKeyWithOptions(context.Background(), database.APIKeyInput{
		Name:      "Expired",
		Key:       key,
		ExpiresAt: sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("InsertAPIKeyWithOptions 返回错误: %v", err)
	}

	handler := NewHandler(nil, db, nil, nil)
	router := gin.New()
	router.Use(handler.authMiddleware())
	router.GET("/ok", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
	if got := gjson.GetBytes(recorder.Body.Bytes(), "error.code").String(); got != string(api.ErrCodeInvalidAuth) {
		t.Fatalf("error.code = %q, want %q", got, api.ErrCodeInvalidAuth)
	}
}

func TestAuthMiddlewareRejectsQuotaExhaustedAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("database.New 返回错误: %v", err)
	}
	defer db.Close()

	key := "sk-test-quota-1234567890"
	if _, err := db.InsertAPIKeyWithOptions(context.Background(), database.APIKeyInput{
		Name:       "Quota",
		Key:        key,
		QuotaLimit: 0.01,
		QuotaUsed:  0.01,
	}); err != nil {
		t.Fatalf("InsertAPIKeyWithOptions 返回错误: %v", err)
	}

	handler := NewHandler(nil, db, nil, nil)
	router := gin.New()
	router.Use(handler.authMiddleware())
	router.GET("/ok", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusTooManyRequests, recorder.Body.String())
	}
	if got := gjson.GetBytes(recorder.Body.Bytes(), "error.code").String(); got != string(api.ErrCodeRateLimitReached) {
		t.Fatalf("error.code = %q, want %q", got, api.ErrCodeRateLimitReached)
	}
}

func TestAuthMiddlewareUsesRuntimeAPIKeyCache(t *testing.T) {
	gin.SetMode(gin.TestMode)

	key := "sk-test-runtime-cache-1234567890"
	tc := cache.NewMemory(1)
	ctx := context.Background()
	keyPayload, _ := json.Marshal(apiKeyRuntimeRecord{
		ID:        42,
		Name:      "Cached Team",
		CreatedAt: time.Now(),
	})
	if err := tc.SetRuntime(ctx, apiKeyCacheNamespace, key, keyPayload, time.Minute); err != nil {
		t.Fatalf("SetRuntime api key: %v", err)
	}
	countPayload, _ := json.Marshal(apiKeyCountRuntimeRecord{Count: 1})
	if err := tc.SetRuntime(ctx, apiKeyCountCacheNamespace, "all", countPayload, time.Minute); err != nil {
		t.Fatalf("SetRuntime api key count: %v", err)
	}

	handler := NewHandler(nil, nil, nil, nil)
	handler.SetRuntimeCache(tc)
	router := gin.New()
	router.Use(handler.authMiddleware())
	router.GET("/ok", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"id":   c.MustGet(contextAPIKeyID),
			"name": c.MustGet(contextAPIKeyName),
			"raw":  c.MustGet("apiKey"),
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Raw  string `json:"raw"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal 返回错误: %v", err)
	}
	if payload.ID != 42 || payload.Name != "Cached Team" || payload.Raw != key {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestSessionAffinityKeySeparatesDifferentAPIKeys(t *testing.T) {
	key1 := sessionAffinityKey("session-1", 1)
	key2 := sessionAffinityKey("session-1", 2)

	if key1 == key2 {
		t.Fatalf("sessionAffinityKey should differ for different apiKeyID: %q", key1)
	}
	if got := sessionAffinityKey("session-1", 0); got != "session-1" {
		t.Fatalf("sessionAffinityKey() with apiKeyID=0 = %q, want session-1", got)
	}
}
