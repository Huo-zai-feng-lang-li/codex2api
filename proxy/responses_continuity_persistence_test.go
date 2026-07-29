package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/database"
)

type blockingResponsesContinuityPersistence struct{}

type controlledCleanupResponsesContinuityPersistence struct {
	trimCalls      atomic.Int32
	blockNext      atomic.Bool
	failNext       atomic.Bool
	cleanupStarted chan struct{}
	cleanupRelease chan struct{}
	cleanupDone    chan struct{}
	startedOnce    sync.Once
	doneOnce       sync.Once
}

func newControlledCleanupResponsesContinuityPersistence() *controlledCleanupResponsesContinuityPersistence {
	return &controlledCleanupResponsesContinuityPersistence{
		cleanupStarted: make(chan struct{}),
		cleanupRelease: make(chan struct{}),
		cleanupDone:    make(chan struct{}),
	}
}

func (blockingResponsesContinuityPersistence) UpsertResponsesContinuation(ctx context.Context, _ *database.ResponsesContinuationRow) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingResponsesContinuityPersistence) GetResponsesContinuation(context.Context, string) (database.ResponsesContinuationRow, bool, error) {
	return database.ResponsesContinuationRow{}, false, nil
}

func (blockingResponsesContinuityPersistence) GetLatestResponseBySessionID(context.Context, string) (database.ResponsesContinuationRow, bool, error) {
	return database.ResponsesContinuationRow{}, false, nil
}

func (blockingResponsesContinuityPersistence) TouchResponsesContinuations(context.Context, []string, time.Time) error {
	return nil
}

func (blockingResponsesContinuityPersistence) PruneResponsesContinuations(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (blockingResponsesContinuityPersistence) TrimResponsesContinuations(context.Context, int, int) (int64, error) {
	return 0, nil
}

func (*controlledCleanupResponsesContinuityPersistence) UpsertResponsesContinuation(context.Context, *database.ResponsesContinuationRow) error {
	return nil
}

func (*controlledCleanupResponsesContinuityPersistence) GetResponsesContinuation(context.Context, string) (database.ResponsesContinuationRow, bool, error) {
	return database.ResponsesContinuationRow{}, false, nil
}

func (*controlledCleanupResponsesContinuityPersistence) GetLatestResponseBySessionID(context.Context, string) (database.ResponsesContinuationRow, bool, error) {
	return database.ResponsesContinuationRow{}, false, nil
}

func (*controlledCleanupResponsesContinuityPersistence) TouchResponsesContinuations(context.Context, []string, time.Time) error {
	return nil
}

func (*controlledCleanupResponsesContinuityPersistence) PruneResponsesContinuations(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (persistence *controlledCleanupResponsesContinuityPersistence) TrimResponsesContinuations(ctx context.Context, _ int, _ int) (int64, error) {
	persistence.trimCalls.Add(1)
	if persistence.blockNext.CompareAndSwap(true, false) {
		persistence.startedOnce.Do(func() { close(persistence.cleanupStarted) })
		select {
		case <-persistence.cleanupRelease:
		case <-ctx.Done():
			persistence.doneOnce.Do(func() { close(persistence.cleanupDone) })
			return 0, ctx.Err()
		}
		persistence.doneOnce.Do(func() { close(persistence.cleanupDone) })
	}
	if persistence.failNext.CompareAndSwap(true, false) {
		return 0, errors.New("forced cleanup failure")
	}
	return 0, nil
}

func TestOpenAIResponsesContinuityRestoresFromPersistence(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "codex2api.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	defer db.Close()

	limits := openAIResponsesContinuityLimits{
		ttl: time.Hour, maxEntries: 20, maxItems: 20,
		maxItemBytes: 1 << 20, maxBytes: 2 << 20,
	}
	first := newOpenAIResponsesContinuityRegistry(limits)
	if err := first.setPersistence(ctx, db); err != nil {
		t.Fatalf("setPersistence(first): %v", err)
	}
	first.store("resp_root", "", "", openAIResponsesContinuation{
		accountID: 7,
		baseURL:   "https://relay.example.com",
		input: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"remember ALPHA"}`),
		},
		output: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ALPHA"}]}`),
		},
	})

	second := newOpenAIResponsesContinuityRegistry(limits)
	if err := second.setPersistence(ctx, db); err != nil {
		t.Fatalf("setPersistence(second): %v", err)
	}
	history, ok := second.materialize("resp_root")
	if !ok || len(history) != 2 {
		t.Fatalf("restored history = %s, ok=%v", mustMarshalRawMessages(history), ok)
	}
	entry, ok := second.get("resp_root")
	if !ok || entry.accountID != 7 || entry.baseURL != "https://relay.example.com" {
		t.Fatalf("restored owner = %+v, ok=%v", entry, ok)
	}
}

func TestOpenAIResponsesContinuityIgnoresCorruptPersistedPayload(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "codex2api.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	defer db.Close()
	now := time.Now().UTC()
	if err := db.UpsertResponsesContinuation(ctx, &database.ResponsesContinuationRow{
		ResponseID: "resp_corrupt", InputJSON: []byte(`not-json`), OutputJSON: []byte(`[]`),
		Replayable: true, CreatedAt: now, AccessedAt: now, SizeBytes: 8,
	}); err != nil {
		t.Fatalf("seed corrupt row: %v", err)
	}

	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl: time.Hour, maxEntries: 20, maxItems: 20,
		maxItemBytes: 1 << 20, maxBytes: 2 << 20,
	})
	if err := registry.setPersistence(ctx, db); err != nil {
		t.Fatalf("setPersistence: %v", err)
	}
	if _, ok := registry.get("resp_corrupt"); ok {
		t.Fatal("corrupt persisted row was restored")
	}
}

func TestOpenAIResponsesContinuityPersistenceTimeoutFailsOpen(t *testing.T) {
	previousTimeout := openAIResponsesContinuityDBTimeout
	openAIResponsesContinuityDBTimeout = 20 * time.Millisecond
	t.Cleanup(func() { openAIResponsesContinuityDBTimeout = previousTimeout })

	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl: time.Hour, maxEntries: 20, maxItems: 20,
		maxItemBytes: 1 << 20, maxBytes: 2 << 20,
	})
	if err := registry.setPersistence(context.Background(), blockingResponsesContinuityPersistence{}); err != nil {
		t.Fatalf("setPersistence: %v", err)
	}
	started := time.Now()
	registry.store("resp_timeout", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
	})
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("store blocked for %s", elapsed)
	}
	if _, ok := registry.get("resp_timeout"); !ok {
		t.Fatal("memory continuity was lost after persistence timeout")
	}
	if failures := registry.stats().PersistenceFailures; failures != 1 {
		t.Fatalf("persistence failures = %d, want 1", failures)
	}
}

func TestOpenAIResponsesContinuityRunsDiskCleanupOffRequestPath(t *testing.T) {
	previousTimeout := openAIResponsesContinuityDBTimeout
	openAIResponsesContinuityDBTimeout = 500 * time.Millisecond
	t.Cleanup(func() { openAIResponsesContinuityDBTimeout = previousTimeout })

	persistence := newControlledCleanupResponsesContinuityPersistence()
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl: time.Hour, maxEntries: 20, maxItems: 20,
		maxItemBytes: 1 << 20, maxBytes: 2 << 20,
	})
	if err := registry.setPersistence(context.Background(), persistence); err != nil {
		t.Fatalf("setPersistence: %v", err)
	}
	persistence.trimCalls.Store(0)
	persistence.blockNext.Store(true)
	forceOpenAIResponsesContinuityCleanup(registry)

	started := time.Now()
	registry.store("resp_limit", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
	})
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("store waited for disk cleanup for %s", elapsed)
	}
	select {
	case <-persistence.cleanupStarted:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("background disk cleanup did not start")
	}
	close(persistence.cleanupRelease)
	select {
	case <-persistence.cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("background disk cleanup did not finish")
	}
}

func TestOpenAIResponsesContinuityCoalescesBurstDiskCleanup(t *testing.T) {
	persistence := newControlledCleanupResponsesContinuityPersistence()
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl: time.Hour, maxEntries: 1, maxItems: 20,
		maxItemBytes: 1 << 20, maxBytes: 2 << 20,
	})
	if err := registry.setPersistence(context.Background(), persistence); err != nil {
		t.Fatalf("setPersistence: %v", err)
	}
	persistence.trimCalls.Store(0)
	persistence.blockNext.Store(true)
	forceOpenAIResponsesContinuityCleanup(registry)

	registry.store("resp_0", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
	})
	select {
	case <-persistence.cleanupStarted:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("background disk cleanup did not start")
	}
	for i := 1; i <= 32; i++ {
		registry.store("resp_"+strconv.Itoa(i), "", "", openAIResponsesContinuation{
			input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
		})
	}
	close(persistence.cleanupRelease)
	waitForOpenAIResponsesContinuityCondition(t, func() bool {
		return persistence.trimCalls.Load() >= 2
	})
	time.Sleep(50 * time.Millisecond)
	if calls := persistence.trimCalls.Load(); calls != 2 {
		t.Fatalf("trim calls after burst = %d, want 2 coalesced runs", calls)
	}
}

func TestOpenAIResponsesContinuitySkipsDiskCleanupBeforeInterval(t *testing.T) {
	persistence := newControlledCleanupResponsesContinuityPersistence()
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl: time.Hour, maxEntries: 20, maxItems: 20,
		maxItemBytes: 1 << 20, maxBytes: 2 << 20,
	})
	if err := registry.setPersistence(context.Background(), persistence); err != nil {
		t.Fatalf("setPersistence: %v", err)
	}
	persistence.trimCalls.Store(0)

	registry.store("resp_limit", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
	})
	time.Sleep(20 * time.Millisecond)
	if calls := persistence.trimCalls.Load(); calls != 0 {
		t.Fatalf("trim calls before cleanup interval = %d, want 0", calls)
	}
}

func TestOpenAIResponsesContinuityRetriesDiskCleanupNextInterval(t *testing.T) {
	persistence := newControlledCleanupResponsesContinuityPersistence()
	base := time.Now().UTC()
	current := base
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl: time.Hour, maxEntries: 20, maxItems: 20,
		maxItemBytes: 1 << 20, maxBytes: 2 << 20,
	})
	registry.now = func() time.Time { return current }
	if err := registry.setPersistence(context.Background(), persistence); err != nil {
		t.Fatalf("setPersistence: %v", err)
	}
	persistence.trimCalls.Store(0)
	persistence.failNext.Store(true)

	current = base.Add(2 * openAIResponsesContinuityCleanupInterval)
	registry.store("resp_fail", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
	})
	waitForOpenAIResponsesContinuityCondition(t, func() bool {
		return registry.stats().PersistenceFailures == 1
	})

	current = base.Add(4 * openAIResponsesContinuityCleanupInterval)
	registry.store("resp_retry", "", "", openAIResponsesContinuation{
		input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello again"}`)},
	})
	waitForOpenAIResponsesContinuityCondition(t, func() bool {
		return persistence.trimCalls.Load() == 2
	})
	if failures := registry.stats().PersistenceFailures; failures != 1 {
		t.Fatalf("persistence failures after successful retry = %d, want 1", failures)
	}
}

func forceOpenAIResponsesContinuityCleanup(registry *openAIResponsesContinuityRegistry) {
	registry.mu.Lock()
	registry.lastCleanup = time.Time{}
	registry.mu.Unlock()
}

func waitForOpenAIResponsesContinuityCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for Responses continuity condition")
}

func TestOpenAIResponsesContinuityPersistsAncestorAccessAcrossRestart(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "codex2api.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	defer db.Close()

	base := time.Now().UTC().Truncate(time.Second)
	current := base
	limits := openAIResponsesContinuityLimits{
		ttl: time.Hour, maxEntries: 20, maxItems: 20,
		maxItemBytes: 1 << 20, maxBytes: 2 << 20,
	}
	first := newOpenAIResponsesContinuityRegistry(limits)
	first.now = func() time.Time { return current }
	if err := first.setPersistence(ctx, db); err != nil {
		t.Fatalf("setPersistence(first): %v", err)
	}
	first.store("resp_root", "", "", openAIResponsesContinuation{
		input:  []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"remember"}`)},
		output: []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"ALPHA"}`)},
	})

	current = base.Add(59 * time.Minute)
	first.store("resp_child", "resp_root", "", openAIResponsesContinuation{
		input: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"repeat"}`)},
	})

	current = base.Add(70 * time.Minute)
	second := newOpenAIResponsesContinuityRegistry(limits)
	second.now = func() time.Time { return current }
	if err := second.setPersistence(ctx, db); err != nil {
		t.Fatalf("setPersistence(second): %v", err)
	}
	history, ok := second.materialize("resp_child")
	if !ok || len(history) != 3 {
		t.Fatalf("history after restart = %s, ok=%v", mustMarshalRawMessages(history), ok)
	}
}

func mustMarshalRawMessages(items []json.RawMessage) string {
	data, _ := json.Marshal(items)
	return string(data)
}

func BenchmarkOpenAIResponsesContinuityStoreMemory(b *testing.B) {
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl: time.Hour, maxEntries: 20, maxItems: 20,
		maxItemBytes: 1 << 20, maxBytes: 2 << 20,
	})
	entry := openAIResponsesContinuation{
		input:  []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
		output: []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"world"}`)},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		registry.store("resp_bench", "", "", entry)
	}
}

func BenchmarkOpenAIResponsesContinuityStoreSQLite(b *testing.B) {
	db, err := database.New("sqlite", filepath.Join(b.TempDir(), "codex2api.db"))
	if err != nil {
		b.Fatalf("database.New: %v", err)
	}
	defer db.Close()
	registry := newOpenAIResponsesContinuityRegistry(openAIResponsesContinuityLimits{
		ttl: time.Hour, maxEntries: 20, maxItems: 20,
		maxItemBytes: 1 << 20, maxBytes: 2 << 20,
	})
	if err := registry.setPersistence(context.Background(), db); err != nil {
		b.Fatalf("setPersistence: %v", err)
	}
	entry := openAIResponsesContinuation{
		input:  []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
		output: []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"world"}`)},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.store("resp_bench", "", "", entry)
	}
}
