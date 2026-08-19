package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

func TestShouldRetry429IgnoresGlobalRetryBudget(t *testing.T) {
	generalRetries := 0
	rateLimitRetries := 0

	for attempt := 0; attempt < 3; attempt++ {
		if !shouldRetryHTTPStatus(http.StatusTooManyRequests, &generalRetries, &rateLimitRetries, 0, 1) {
			t.Fatalf("429 attempt %d should continue until account selection is exhausted", attempt+1)
		}
	}
	if rateLimitRetries != 3 {
		t.Fatalf("rateLimitRetries = %d, want 3", rateLimitRetries)
	}
}

func TestResponses429RechecksPoolOnceAndResumes(t *testing.T) {
	var mu sync.Mutex
	calls := make(map[string]int)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		mu.Lock()
		calls[key]++
		attempt := calls[key]
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if key == "Bearer key-3" && attempt == 2 {
			_, _ = io.WriteString(w, `{"id":"resp-recovered","status":"completed","output":[]}`)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"Too many requests"}}`)
	}))
	defer upstream.Close()

	store := newRetryPoolTestStore(upstream.URL)
	var probeCalls atomic.Int32
	store.SetRecoveryProbeFunc(func(_ context.Context, account *auth.Account) error {
		probeCalls.Add(1)
		if account.APIKey == "key-3" {
			return nil
		}
		return errors.New("still unavailable")
	})

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	response := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"hello","store":false}`), nil)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if got := probeCalls.Load(); got != 3 {
		t.Fatalf("probeCalls = %d, want one full recheck of 3 accounts", got)
	}
	for _, key := range []string{"Bearer key-1", "Bearer key-2", "Bearer key-3"} {
		if calls[key] == 0 {
			t.Fatalf("account %s was not attempted before recheck: calls=%v", key, calls)
		}
	}
	if calls["Bearer key-3"] != 2 {
		t.Fatalf("recovered account calls = %d, want first attempt plus resumed request", calls["Bearer key-3"])
	}
}

func TestResponses429ReturnsAfterFullRecheckStillFails(t *testing.T) {
	var mu sync.Mutex
	calls := make(map[string]int)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		mu.Lock()
		calls[key]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"Too many requests"}}`)
	}))
	defer upstream.Close()

	store := newRetryPoolTestStore(upstream.URL)
	var probeCalls atomic.Int32
	store.SetRecoveryProbeFunc(func(_ context.Context, _ *auth.Account) error {
		probeCalls.Add(1)
		return errors.New("still rate limited")
	})

	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	response := performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","input":"hello","store":false}`), nil)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", response.Code, response.Body.String())
	}
	if got := probeCalls.Load(); got != 3 {
		t.Fatalf("probeCalls = %d, want one full recheck of 3 accounts", got)
	}
	for _, key := range []string{"Bearer key-1", "Bearer key-2", "Bearer key-3"} {
		if calls[key] != 1 {
			t.Fatalf("account %s calls = %d, want one first-pass attempt; calls=%v", key, calls[key], calls)
		}
	}
}

func TestResponsesWebSocket429ResponseFailedUsesNextAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousExec := WebsocketExecuteFunc
	t.Cleanup(func() { WebsocketExecuteFunc = previousExec })

	var attempts atomic.Int32
	var accountIDsMu sync.Mutex
	var accountIDs []int64
	WebsocketExecuteFunc = func(_ context.Context, account *auth.Account, _ []byte, _ string, _ string, _ string, _ *DeviceProfileConfig, _ http.Header) (*http.Response, error) {
		attempt := attempts.Add(1)
		accountIDsMu.Lock()
		accountIDs = append(accountIDs, account.ID())
		accountIDsMu.Unlock()
		if attempt == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`data: {"type":"response.failed","response":{"status_code":429,"error":{"type":"rate_limit_error","message":"Too many requests"}}}` + "\n\n",
				)),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n",
			)),
		}, nil
	}

	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency: 2,
		TestModel:      "gpt-5.4",
		MaxRetries:     0,
	})
	store.AddAccount(&auth.Account{DBID: 1, AccessToken: "first", PlanType: "plus", AccountID: "acct-1"})
	store.AddAccount(&auth.Account{DBID: 2, AccessToken: "second", PlanType: "plus", AccountID: "acct-2"})
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true, UseWebsocket: true}, nil)

	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial websocket failed: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial websocket failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"gpt-5.4","input":"hello"}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, event, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if eventType := gjson.GetBytes(event, "type").String(); eventType != "response.completed" {
		t.Fatalf("event type = %q body=%s, want response.completed", eventType, event)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	accountIDsMu.Lock()
	defer accountIDsMu.Unlock()
	if len(accountIDs) != 2 || accountIDs[0] == accountIDs[1] {
		t.Fatalf("account IDs = %v, want two distinct accounts", accountIDs)
	}
}

func newRetryPoolTestStore(upstreamURL string) *auth.Store {
	store := auth.NewStore(nil, nil, &database.SystemSettings{
		MaxConcurrency:      1,
		TestModel:           "gpt-5.4",
		MaxRetries:          0,
		MaxRateLimitRetries: 1,
		AffinityMode:        auth.AffinityModeOff,
	})
	for i := 1; i <= 3; i++ {
		store.AddAccount(&auth.Account{
			DBID:         int64(i),
			Name:         fmt.Sprintf("retry-account-%d", i),
			UpstreamType: auth.UpstreamOpenAIResponses,
			BaseURL:      upstreamURL,
			APIKey:       fmt.Sprintf("key-%d", i),
			Models:       []string{"gpt-5.4"},
		})
	}
	return store
}
