package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

func TestResponsesAdditionalToolsInputReachesOpenAIResponsesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream, upstreamRequests := newFutureInputHTTPUpstream(t)
	handler := newFutureInputOpenAIResponsesHandler(upstream.URL)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(futureResponsesAdditionalToolsBody()))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.Responses(ctx)

	assertFutureInputWasNotRejectedLocally(t, recorder.Code, recorder.Body.Bytes())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	assertFutureAdditionalToolsPayload(t, receiveFutureUpstreamBody(t, upstreamRequests))
}

func TestResponsesAdditionalToolsInputPropagatesOpenAIResponsesUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream, upstreamRequests := newFutureInputHTTPUpstreamWithResponse(
		t,
		http.StatusBadRequest,
		`{"error":{"message":"upstream rejected additional_tools","type":"invalid_request_error","code":"unsupported_future_input"}}`,
	)
	handler := newFutureInputOpenAIResponsesHandler(upstream.URL)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(futureResponsesAdditionalToolsBody()))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.Responses(ctx)

	assertFutureAdditionalToolsPayload(t, receiveFutureUpstreamBody(t, upstreamRequests))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"upstream_400"`) {
		t.Fatalf("normalized upstream error code missing: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "upstream rejected additional_tools") {
		t.Fatalf("upstream error message missing: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "invalid_input_type") {
		t.Fatalf("local input type error replaced upstream error: %s", recorder.Body.String())
	}
}

func TestResponsesAdditionalToolsInputReachesCodexUpstreamPreparation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	requests := make(chan []byte, 1)
	previousExecuteRequest := ExecuteRequest
	ExecuteRequest = func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header, useWebsocket ...bool) (*http.Response, error) {
		requests <- append([]byte(nil), requestBody...)
		sse := "" +
			`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"resp_future_codex","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3},"service_tier":"default"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}
	t.Cleanup(func() {
		ExecuteRequest = previousExecuteRequest
	})

	handler := newFutureInputCodexHandler()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		bytes.NewReader(futureResponsesAdditionalToolsBody(`"stream":true`)),
	)
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.Responses(ctx)

	assertFutureInputWasNotRejectedLocally(t, recorder.Code, recorder.Body.Bytes())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	assertFutureAdditionalToolsPayload(t, receiveFutureUpstreamBody(t, requests))
}

func TestResponsesCompactAdditionalToolsInputReachesOpenAIResponsesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream, upstreamRequests := newFutureInputHTTPUpstream(t)
	handler := newFutureInputOpenAIResponsesHandler(upstream.URL)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(futureResponsesAdditionalToolsBody()))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	handler.ResponsesCompact(ctx)

	assertFutureInputWasNotRejectedLocally(t, recorder.Code, recorder.Body.Bytes())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	body := receiveFutureUpstreamBody(t, upstreamRequests)
	assertFutureAdditionalToolsPayload(t, body)
	if gjson.GetBytes(body, "stream").Bool() {
		t.Fatalf("compact request should not forward stream=true; body=%s", body)
	}
}

func TestResponsesWebSocketAdditionalToolsInputReachesOpenAIResponsesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream, upstreamRequests := newFutureInputWebSocketUpstream(t)
	handler := newFutureInputOpenAIResponsesHandler(upstream.URL)

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

	payload := futureResponsesAdditionalToolsBody(`"previous_response_id":"resp_future_prev"`)
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write websocket request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read downstream websocket event: %v", err)
	}
	if strings.Contains(string(first), "invalid_input_type") {
		t.Fatalf("additional_tools was rejected by local websocket validation: %s", first)
	}

	body := receiveFutureUpstreamBody(t, upstreamRequests)
	assertFutureAdditionalToolsPayload(t, body)
	if typ := gjson.GetBytes(body, "type").String(); typ != "response.create" {
		t.Fatalf("upstream websocket envelope type = %q, want response.create; body=%s", typ, body)
	}
	if prev := gjson.GetBytes(body, "previous_response_id").String(); prev != "resp_future_prev" {
		t.Fatalf("upstream previous_response_id = %q, want resp_future_prev; body=%s", prev, body)
	}
	if !gjson.GetBytes(body, "stream").Bool() {
		t.Fatalf("upstream websocket request should force stream=true; body=%s", body)
	}
}

func TestPrepareOpenAIResponsesHTTPBodyFromWebSocketPreservesAdditionalToolsInput(t *testing.T) {
	body, expandedInputRaw, ok := prepareOpenAIResponsesHTTPBodyFromWebSocket(
		futureResponsesAdditionalToolsBody(`"type":"response.create"`, `"stream":true`),
		false,
	)
	if !ok {
		t.Fatal("HTTP fallback body preparation failed")
	}

	assertFutureAdditionalToolsPayload(t, body)
	assertFutureAdditionalToolsPayload(t, []byte(`{"input":`+expandedInputRaw+`}`))
	if gjson.GetBytes(body, "type").Exists() {
		t.Fatalf("websocket envelope type should be stripped for HTTP fallback; body=%s", body)
	}
}

func newFutureInputOpenAIResponsesHandler(upstreamURL string) *Handler {
	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:  2,
		TestConcurrency: 1,
		TestModel:       "gpt-5.4",
		MaxRetries:      2,
	})
	store.AddAccount(&auth.Account{
		DBID:         1,
		Name:         "future-openai-responses",
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstreamURL,
		APIKey:       "upstream-key",
		Models:       []string{"gpt-5.4"},
	})
	return NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
}

func newFutureInputCodexHandler() *Handler {
	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:  2,
		TestConcurrency: 1,
		TestModel:       "gpt-5.4",
		MaxRetries:      0,
	})
	store.AddAccount(&auth.Account{
		DBID:        1,
		Name:        "future-codex",
		PlanType:    "plus",
		AccountID:   "acct-future-codex",
		AccessToken: "codex-access-token",
		Models:      []string{"gpt-5.4"},
	})
	return NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
}

func newFutureInputHTTPUpstream(t *testing.T) (*httptest.Server, <-chan []byte) {
	t.Helper()

	return newFutureInputHTTPUpstreamWithResponse(t, http.StatusOK, `{
		"id":"resp_future_input",
		"object":"response",
		"model":"gpt-5.4",
		"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],
		"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}
	}`)
}

func newFutureInputHTTPUpstreamWithResponse(t *testing.T, status int, responseBody string) (*httptest.Server, <-chan []byte) {
	t.Helper()

	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		requests <- append([]byte(nil), body...)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func newFutureInputWebSocketUpstream(t *testing.T) (*httptest.Server, <-chan []byte) {
	t.Helper()

	requests := make(chan []byte, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("upstream websocket path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
			t.Fatalf("upstream websocket Authorization = %q, want Bearer upstream-key", got)
		}
		if !websocket.IsWebSocketUpgrade(r) {
			t.Fatal("upstream expected websocket upgrade")
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
		requests <- append([]byte(nil), body...)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{
			"type":"response.output_text.delta",
			"delta":"ok"
		}`)); err != nil {
			t.Fatalf("write upstream websocket delta: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{
			"type":"response.completed",
			"response":{
				"id":"resp_future_ws",
				"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],
				"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}
			}
		}`)); err != nil {
			t.Fatalf("write upstream websocket completed: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func futureResponsesAdditionalToolsBody(extraFields ...string) []byte {
	extra := ""
	if len(extraFields) > 0 {
		extra = "," + strings.Join(extraFields, ",")
	}
	return []byte(`{
		"model":"gpt-5.4",
		"input":[
			{
				"type":"additional_tools",
				"tools":[
					{
						"type":"function",
						"name":"future_lookup",
						"description":"future contract tool",
						"parameters":{
							"type":"object",
							"properties":{"query":{"type":"string"}},
							"required":["query"]
						}
					}
				],
				"payload":{"nested":true,"labels":["alpha","beta"]}
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"hello future"}]
			}
		]` + extra + `
	}`)
}

func assertFutureInputWasNotRejectedLocally(t *testing.T, status int, body []byte) {
	t.Helper()
	if status == http.StatusBadRequest && strings.Contains(string(body), "invalid_input_type") {
		t.Fatalf("additional_tools was rejected by local validation: %s", body)
	}
}

func assertFutureAdditionalToolsPayload(t *testing.T, body []byte) {
	t.Helper()

	if typ := gjson.GetBytes(body, "input.0.type").String(); typ != "additional_tools" {
		t.Fatalf("input.0.type = %q, want additional_tools; body=%s", typ, body)
	}
	if name := gjson.GetBytes(body, "input.0.tools.0.name").String(); name != "future_lookup" {
		t.Fatalf("input.0.tools.0.name = %q, want future_lookup; body=%s", name, body)
	}
	if nested := gjson.GetBytes(body, "input.0.payload.nested").Bool(); !nested {
		t.Fatalf("input.0.payload.nested = false, want true; body=%s", body)
	}
	if label := gjson.GetBytes(body, "input.0.payload.labels.1").String(); label != "beta" {
		t.Fatalf("input.0.payload.labels.1 = %q, want beta; body=%s", label, body)
	}
	if text := gjson.GetBytes(body, "input.1.content.0.text").String(); text != "hello future" {
		t.Fatalf("input.1.content.0.text = %q, want hello future; body=%s", text, body)
	}
}

func receiveFutureUpstreamBody(t *testing.T, requests <-chan []byte) []byte {
	t.Helper()

	select {
	case body := <-requests:
		return body
	case <-time.After(2 * time.Second):
		t.Fatal("OpenAI Responses mock upstream did not receive request")
		return nil
	}
}
