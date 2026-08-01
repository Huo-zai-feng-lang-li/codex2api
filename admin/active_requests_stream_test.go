package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/gin-gonic/gin"
)

func TestActiveRequestStreamRequiresAdminAuthentication(t *testing.T) {
	_, router := newActiveRequestStreamTestRouter(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/admin/runtime-status/active-requests/events", nil)

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
}

func TestActiveRequestStreamReturnsUnavailableWithoutStore(t *testing.T) {
	db := newTestAdminDB(t)
	tokenCache := cache.NewMemory(4)
	t.Cleanup(func() { tokenCache.Close() })
	handler := NewHandler(nil, db, tokenCache, nil, "admin-secret")
	router := gin.New()
	handler.RegisterRoutes(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/admin/runtime-status/active-requests/events", nil)
	request.Header.Set("X-Admin-Key", "admin-secret")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
}

func TestActiveRequestStreamPushesInitialAndChangedSnapshots(t *testing.T) {
	store, router := newActiveRequestStreamTestRouter(t)
	server := httptest.NewServer(router)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/admin/runtime-status/active-requests/events", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	request.Header.Set("X-Admin-Key", "admin-secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open active request stream: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}
	if got := response.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("cache-control = %q, want no-store", got)
	}
	if got := response.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}

	reader := bufio.NewReader(response.Body)
	initial := readActiveRequestSnapshotEvent(t, reader)
	if len(initial.ActiveRequestDetails) != 0 || initial.ActiveRequests != 0 {
		t.Fatalf("initial snapshot = %+v, want empty", initial)
	}

	id := store.BeginActiveRequest(auth.ActiveRequestMeta{
		AccountID:        42,
		AccountName:      "Current API",
		UpstreamEndpoint: "https://user:password@example.com/v1/responses?token=secret#fragment",
		Model:            "gpt-5-codex",
		EffectiveModel:   "gpt-5-codex",
		APIKeyName:       "desktop",
		StartedAt:        time.Now(),
	})
	started := readActiveRequestSnapshotEvent(t, reader)
	if started.ActiveRequests != 1 || len(started.ActiveRequestDetails) != 1 {
		t.Fatalf("started snapshot = %+v, want one request", started)
	}
	if got := started.ActiveRequestDetails[0]; got.ID != id || got.AccountID != 42 || got.APIKeyName != "desktop" {
		t.Fatalf("started request = %+v, want id=%d account=42 api_key=desktop", got, id)
	}
	if got := started.ActiveRequestDetails[0].UpstreamEndpoint; got != "https://example.com/v1/responses" {
		t.Fatalf("upstream endpoint = %q, want credentials and query redacted", got)
	}

	store.EndActiveRequest(id)
	ended := readActiveRequestSnapshotEvent(t, reader)
	if ended.ActiveRequests != 0 || len(ended.ActiveRequestDetails) != 0 {
		t.Fatalf("ended snapshot = %+v, want empty", ended)
	}
}

func newActiveRequestStreamTestRouter(t *testing.T) (*auth.Store, *gin.Engine) {
	t.Helper()
	db := newTestAdminDB(t)
	tokenCache := cache.NewMemory(4)
	t.Cleanup(func() { tokenCache.Close() })
	store := auth.NewStore(db, tokenCache, nil)
	handler := NewHandler(store, db, tokenCache, nil, "admin-secret")
	router := gin.New()
	handler.RegisterRoutes(router)
	return store, router
}

func readActiveRequestSnapshotEvent(t *testing.T, reader *bufio.Reader) activeRequestStreamSnapshot {
	t.Helper()
	eventName := ""
	dataLines := make([]string, 0, 1)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE event: %v", err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			break
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if eventName != "snapshot" {
		t.Fatalf("event = %q, want snapshot", eventName)
	}
	var snapshot activeRequestStreamSnapshot
	if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return snapshot
}
