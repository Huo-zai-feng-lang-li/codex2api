package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestExplicitPendingContinuationNeverFallsBackToOlderSessionHead(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	owner := &auth.Account{
		DBID:         901,
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      "https://relay.example",
		APIKey:       "owner",
	}
	const sessionID = "session-explicit-pending"

	openAIResponsesContinuity.store("resp_parent", "", sessionID, openAIResponsesContinuation{
		accountID: owner.ID(),
		baseURL:   "https://relay.example",
		input: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"turn one"}`),
		},
		output: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer one"}]}`),
		},
	})
	RegisterPendingOpenAIResponsesContinuation("resp_pending", "resp_parent", sessionID, owner)

	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_pending","input":"turn three"}`)
	if fallback, ok := buildOpenAIResponsesContinuationFallback(request, sessionID); ok {
		t.Fatalf("explicit pending parent silently fell back to an older session head: %s", fallback)
	}
}

func TestPendingOutputMaintainsContinuityByteAccounting(t *testing.T) {
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl:          time.Hour,
		maxEntries:   10,
		maxItems:     10,
		maxItemBytes: 1024,
		maxBytes:     4096,
	})
	registry.registerPending("resp_bytes", "", "", 1, "")
	input := []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"turn one"}`)}
	output := json.RawMessage(`{"type":"message","role":"assistant","content":"answer one"}`)
	if !registry.appendPendingOutput("resp_bytes", input, output) {
		t.Fatal("failed to append pending output")
	}
	wantBytes := rawMessagesSize(input) + rawMessagesSize([]json.RawMessage{output})
	if got := registry.stats().Bytes; got != wantBytes {
		t.Fatalf("pending bytes = %d, want %d", got, wantBytes)
	}
	registry.finalizeContinuation("resp_bytes", continuationStateCompleted, nil, nil)
	if got := registry.stats().Bytes; got != wantBytes {
		t.Fatalf("finalized bytes = %d, want %d", got, wantBytes)
	}
}

func TestPendingContinuationWaitsForReplayableSignal(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	owner := &auth.Account{DBID: 902, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://relay.example", APIKey: "owner"}
	const sessionID = "session-pending-wait"

	openAIResponsesContinuity.store("resp_parent", "", sessionID, openAIResponsesContinuation{
		accountID: owner.ID(),
		baseURL:   "https://relay.example",
		input:     []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"turn one"}`)},
		output:    []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer one"}]}`)},
	})
	RegisterPendingOpenAIResponsesContinuation("resp_pending", "resp_parent", sessionID, owner)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ready := make(chan bool, 1)
	go func() {
		ready <- openAIResponsesContinuity.waitUntilReplayable(ctx, "resp_pending")
	}()

	select {
	case <-ready:
		t.Fatal("pending continuation became ready before any replayable output was committed")
	case <-time.After(20 * time.Millisecond):
	}

	requestBody := []byte(`{"model":"gpt-5.4","input":"turn two"}`)
	output := gjson.Parse(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer two"}]}`)
	if !AppendPendingOpenAIResponsesOutput("resp_pending", requestBody, output) {
		t.Fatal("failed to save pending output snapshot")
	}
	select {
	case <-ready:
		t.Fatal("output_item.done advanced the continuation before response.completed")
	case <-time.After(20 * time.Millisecond):
	}
	openAIResponsesContinuity.finalizeContinuation("resp_pending", continuationStateCompleted, nil, nil)

	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("pending waiter woke without a replayable continuation")
		}
	case <-time.After(time.Second):
		t.Fatal("pending waiter was not notified")
	}

	fallback, ok := buildOpenAIResponsesContinuationFallback(
		[]byte(`{"model":"gpt-5.4","previous_response_id":"resp_pending","input":"turn three"}`),
		sessionID,
	)
	if !ok {
		t.Fatal("ready pending continuation did not build a fallback")
	}
	input := gjson.GetBytes(fallback, "input").Array()
	if len(input) != 5 {
		t.Fatalf("fallback item count = %d, want 5; body=%s", len(input), fallback)
	}
}

func TestLatePendingBranchCannotReplaceNewerSessionHead(t *testing.T) {
	resetOpenAIResponsesContinuityForTest()
	owner := &auth.Account{DBID: 903, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://relay.example", APIKey: "owner"}
	const sessionID = "session-branch-head"

	openAIResponsesContinuity.store("resp_parent", "", sessionID, openAIResponsesContinuation{
		accountID: owner.ID(),
		input:     []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"root"}`)},
		output:    []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"root answer"}]}`)},
	})
	RegisterPendingOpenAIResponsesContinuation("resp_old_branch", "resp_parent", sessionID, owner)
	RegisterPendingOpenAIResponsesContinuation("resp_new_branch", "resp_parent", sessionID, owner)

	requestBody := []byte(`{"model":"gpt-5.4","input":"branch"}`)
	oldOutput := gjson.Parse(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"old"}]}`)
	if !AppendPendingOpenAIResponsesOutput("resp_old_branch", requestBody, oldOutput) {
		t.Fatal("failed to save old branch snapshot")
	}
	openAIResponsesContinuity.finalizeContinuation("resp_old_branch", continuationStateCompleted, nil, nil)
	if got := openAIResponsesContinuity.getLatestReplayableResponseID(sessionID); got != "resp_parent" {
		t.Fatalf("late old branch replaced session head: got %q, want resp_parent", got)
	}

	newOutput := gjson.Parse(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"new"}]}`)
	if !AppendPendingOpenAIResponsesOutput("resp_new_branch", requestBody, newOutput) {
		t.Fatal("failed to save new branch snapshot")
	}
	openAIResponsesContinuity.finalizeContinuation("resp_new_branch", continuationStateCompleted, nil, nil)
	if got := openAIResponsesContinuity.getLatestReplayableResponseID(sessionID); got != "resp_new_branch" {
		t.Fatalf("new branch did not advance session head: got %q", got)
	}
}

func TestHTTPPendingContinuationWaitsBeforeThirdPartyReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetOpenAIResponsesContinuityForTest()

	received := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_final","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer three"}]}]}`))
	}))
	t.Cleanup(upstream.Close)

	owner := &auth.Account{DBID: 904, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: upstream.URL, APIKey: "owner", Models: []string{"gpt-5.4"}}
	seedPendingContinuation(t, owner, "resp_http_pending", "session-http-pending")
	handler := newPendingContinuationTestHandler(owner)

	responseDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseDone <- performResponsesRequest(t, handler, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_http_pending","input":"turn three","store":false}`), nil)
	}()
	waitForPendingContinuationWaiter(t, "resp_http_pending")
	select {
	case body := <-received:
		t.Fatalf("third-party upstream was called before pending history became replayable: %s", body)
	default:
	}
	commitPendingContinuation(t, "resp_http_pending")

	select {
	case response := <-responseDone:
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP continuation did not resume after pending history committed")
	}
	assertCompletePendingReplay(t, <-received)
}

func TestHTTPPendingContinuationTimeoutNeverCallsThirdParty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetOpenAIResponsesContinuityForTest()
	previousWait := openAIResponsesContinuationWait
	openAIResponsesContinuationWait = 20 * time.Millisecond
	t.Cleanup(func() { openAIResponsesContinuationWait = previousWait })

	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)
	owner := &auth.Account{DBID: 905, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: upstream.URL, APIKey: "owner", Models: []string{"gpt-5.4"}}
	seedPendingContinuation(t, owner, "resp_http_timeout", "session-http-timeout")

	response := performResponsesRequest(t, newPendingContinuationTestHandler(owner), []byte(`{"model":"gpt-5.4","previous_response_id":"resp_http_timeout","input":"turn three","store":false}`), nil)
	if response.Code == http.StatusConflict {
		t.Fatalf("unexpected 409 conflict when pending timeout: body=%s", response.Body.String())
	}
	if code := gjson.Get(response.Body.String(), "error.code").String(); code == "continuation_context_incomplete" {
		t.Fatalf("unexpected continuation_context_incomplete error code: body=%s", response.Body.String())
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("third-party upstream calls = %d, want 0", calls)
	}
}

func TestWebSocketPendingContinuationTimeoutNeverCallsThirdParty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetOpenAIResponsesContinuityForTest()
	previousWait := openAIResponsesContinuationWait
	openAIResponsesContinuationWait = 20 * time.Millisecond
	t.Cleanup(func() { openAIResponsesContinuationWait = previousWait })

	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)
	owner := &auth.Account{DBID: 907, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: upstream.URL, APIKey: "owner", Models: []string{"gpt-5.4"}}
	seedPendingContinuation(t, owner, "resp_ws_timeout", "session-ws-timeout")
	handler := newPendingContinuationTestHandler(owner)
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_ws_timeout","input":"turn three","store":false}`)
	if err := conn.WriteMessage(websocket.TextMessage, request); err != nil {
		t.Fatalf("write websocket request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, event, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket response: %v", err)
	}
	if statusCode := gjson.GetBytes(event, "response.status_code").Int(); statusCode != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d; body=%s", statusCode, http.StatusServiceUnavailable, event)
	}
	if code := gjson.GetBytes(event, "response.error.code").String(); code != "continuation_context_unavailable" {
		t.Fatalf("error code = %q, want continuation_context_unavailable; body=%s", code, event)
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("third-party upstream calls = %d, want 0", calls)
	}
}

func TestWebSocketPendingContinuationWaitsBeforeThirdPartyReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetOpenAIResponsesContinuityForTest()

	received := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ws_final\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer three\"}]}]}}\n\n"))
	}))
	t.Cleanup(upstream.Close)

	owner := &auth.Account{DBID: 906, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: upstream.URL, APIKey: "owner", Models: []string{"gpt-5.4"}}
	seedPendingContinuation(t, owner, "resp_ws_pending", "session-ws-pending")
	handler := newPendingContinuationTestHandler(owner)
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_ws_pending","input":"turn three","store":false}`)
	if err := conn.WriteMessage(websocket.TextMessage, request); err != nil {
		t.Fatalf("write websocket request: %v", err)
	}
	waitForPendingContinuationWaiter(t, "resp_ws_pending")
	select {
	case body := <-received:
		t.Fatalf("third-party upstream was called before WS pending history became replayable: %s", body)
	default:
	}
	commitPendingContinuation(t, "resp_ws_pending")

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, event, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket response: %v", err)
	}
	if eventType := gjson.GetBytes(event, "type").String(); eventType != "response.completed" {
		t.Fatalf("event type = %q, want response.completed; body=%s", eventType, event)
	}
	assertCompletePendingReplay(t, <-received)
}

func newPendingContinuationTestHandler(account *auth.Account) *Handler {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestModel: "gpt-5.4", MaxRetries: 0, AffinityMode: auth.AffinityModeOff})
	store.AddAccount(account)
	return NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
}

func seedPendingContinuation(t *testing.T, owner *auth.Account, responseID, sessionID string) {
	t.Helper()
	openAIResponsesContinuity.store("resp_pending_parent", "", sessionID, openAIResponsesContinuation{
		accountID: owner.ID(),
		baseURL:   normalizeContinuationBaseURL(owner.BaseURL),
		input:     []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"turn one"}`)},
		output:    []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer one"}]}`)},
	})
	RegisterPendingOpenAIResponsesContinuation(responseID, "resp_pending_parent", sessionID, owner)
}

func commitPendingContinuation(t *testing.T, responseID string) {
	t.Helper()
	if !AppendPendingOpenAIResponsesOutput(
		responseID,
		[]byte(`{"model":"gpt-5.4","input":"turn two"}`),
		gjson.Parse(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer two"}]}`),
	) {
		t.Fatal("failed to commit pending continuation")
	}
	openAIResponsesContinuity.finalizeContinuation(responseID, continuationStateCompleted, nil, nil)
}

func waitForPendingContinuationWaiter(t *testing.T, responseID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		openAIResponsesContinuity.mu.Lock()
		_, waiting := openAIResponsesContinuity.waiters[responseID]
		openAIResponsesContinuity.mu.Unlock()
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("continuation waiter for %s was not registered", responseID)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertCompletePendingReplay(t *testing.T, body []byte) {
	t.Helper()
	if gjson.GetBytes(body, "previous_response_id").Exists() {
		t.Fatalf("replayed request retained previous_response_id: %s", body)
	}
	input := gjson.GetBytes(body, "input").Array()
	if len(input) != 5 {
		t.Fatalf("replayed input count = %d, want 5; body=%s", len(input), body)
	}
	for index, want := range []string{"turn one", "answer one", "turn two", "answer two", "turn three"} {
		if !strings.Contains(input[index].Raw, want) {
			t.Fatalf("input[%d] does not contain %q: %s", index, want, body)
		}
	}
}
