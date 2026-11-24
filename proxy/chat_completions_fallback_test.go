package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// TestChatCompletionsImage400FallsBackToChatUpstream 验证：
// 当下游是 chat/completions、上游 Responses 端点因图片内容块返回 400 时，
// 网关应保留原始 chat 请求体，改打同一账号的 /v1/chat/completions，并原样回传。
func TestChatCompletionsImage400FallsBackToChatUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var responsesCalls int32
	var chatCalls int32
	chatBodyCh := make(chan []byte, 1)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			atomic.AddInt32(&responsesCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"OpenAIException - 29 validation errors for ChatCompletionRequest: input_value=[{'type': 'text', 'text': '...'}, {'type': 'unknown'}], image content block is not supported"}}`))
		case "/v1/chat/completions":
			atomic.AddInt32(&chatCalls, 1)
			body, _ := io.ReadAll(r.Body)
			chatBodyCh <- append([]byte(nil), body...)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","model":"agnes-3.0-flash","choices":[{"index":0,"message":{"role":"assistant","content":"红色"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	settings := &database.SystemSettings{
		MaxConcurrency:  1,
		TestConcurrency: 1,
		TestModel:       "agnes-3.0-flash",
		MaxRetries:      0,
		AffinityMode:    auth.AffinityModeOff,
	}
	store := auth.NewStore(nil, nil, settings)
	store.AddAccount(&auth.Account{
		DBID:         1,
		Name:         "agnes-test",
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "sk-agnes-test",
		Models:       []string{"agnes-3.0-flash"},
	})

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	reqBody := `{"model":"agnes-3.0-flash","messages":[{"role":"user","content":[{"type":"text","text":"这张图是什么颜色？"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA==","detail":"auto"}}]}],"stream":false}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post chat/completions: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "红色") {
		t.Fatalf("fallback response did not reach downstream: %s", body)
	}
	if got := atomic.LoadInt32(&responsesCalls); got != 1 {
		t.Fatalf("responses calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&chatCalls); got != 1 {
		t.Fatalf("chat calls = %d, want 1", got)
	}

	var chatBody []byte
	select {
	case chatBody = <-chatBodyCh:
	default:
		t.Fatal("chat upstream did not receive a request body")
	}
	if !gjson.GetBytes(chatBody, "messages").Exists() {
		t.Fatalf("fallback should send the original chat body, got: %s", chatBody)
	}
	if gjson.GetBytes(chatBody, "input").Exists() {
		t.Fatalf("fallback must not send a Responses input array, got: %s", chatBody)
	}
	if got := gjson.GetBytes(chatBody, "messages.0.content.1.type").String(); got != "image_url" {
		t.Fatalf("fallback lost image part, type = %q; body=%s", got, chatBody)
	}
}

// TestChatCompletionsImage400FallbackRelaysStream 流式场景必须原样透传 chat SSE，
// 不能把 Agnes 的 chat chunk 再按 Responses 事件解析。
func TestChatCompletionsImage400FallbackRelaysStream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var responsesCalls int32
	var chatCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			atomic.AddInt32(&responsesCalls, 1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"image content block type 'unknown' is not supported"}}`))
		case "/v1/chat/completions":
			atomic.AddInt32(&chatCalls, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"红\"},\"finish_reason\":null}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"色\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:  1,
		TestConcurrency: 1,
		TestModel:       "agnes-3.0-flash",
		MaxRetries:      0,
		AffinityMode:    auth.AffinityModeOff,
	})
	store.AddAccount(&auth.Account{
		DBID:         1,
		Name:         "agnes-stream",
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "sk-agnes-test",
		Models:       []string{"agnes-3.0-flash"},
	})

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	reqBody := `{"model":"agnes-3.0-flash","messages":[{"role":"user","content":[{"type":"text","text":"这张图是什么颜色？"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],"stream":true}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post chat/completions: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}
	if !strings.Contains(string(body), "\"content\":\"红\"") || !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("chat SSE was not relayed verbatim: %s", body)
	}
	if got := atomic.LoadInt32(&responsesCalls); got != 1 {
		t.Fatalf("responses calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&chatCalls); got != 1 {
		t.Fatalf("chat calls = %d, want 1", got)
	}
}

// TestChatCompletionsText400DoesNotFallback 没有图片的 400 不应触发 chat 回退，
// 否则普通参数错误会被悄悄改协议重试。
func TestChatCompletionsText400DoesNotFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var chatCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"image is mentioned but the request has no image"}}`))
		case "/v1/chat/completions":
			atomic.AddInt32(&chatCalls, 1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:  1,
		TestConcurrency: 1,
		TestModel:       "agnes-3.0-flash",
		MaxRetries:      0,
		AffinityMode:    auth.AffinityModeOff,
	})
	store.AddAccount(&auth.Account{
		DBID:         1,
		Name:         "agnes-text",
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      upstream.URL,
		APIKey:       "sk-agnes-test",
		Models:       []string{"agnes-3.0-flash"},
	})

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	reqBody := `{"model":"agnes-3.0-flash","messages":[{"role":"user","content":"hello"}],"stream":false}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post chat/completions: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", resp.StatusCode, body)
	}
	if got := atomic.LoadInt32(&chatCalls); got != 0 {
		t.Fatalf("chat calls = %d, want 0 for text-only request", got)
	}
}
