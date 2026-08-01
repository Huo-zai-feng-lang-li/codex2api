package auth

import (
	"sync"
	"testing"
	"time"
)

func TestActiveRequestSnapshotsRoundTrip(t *testing.T) {
	store := NewStore(nil, nil, nil)
	startedAt := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	now := startedAt.Add(2500 * time.Millisecond)

	id := store.BeginActiveRequest(ActiveRequestMeta{
		AccountID:        42,
		AccountName:      "Operator Main",
		AccountEmail:     "operator@example.com",
		Endpoint:         "/v1/responses",
		UpstreamEndpoint: "/v1/responses",
		Model:            "gpt-5.1-codex",
		EffectiveModel:   "gpt-5.1-codex",
		APIKeyID:         7,
		APIKeyName:       "team-key",
		APIKeyMasked:     "sk-...abcd",
		Stream:           true,
		StartedAt:        startedAt,
	})
	if id == 0 {
		t.Fatal("BeginActiveRequest() returned zero id")
	}

	snapshots := store.ActiveRequestSnapshots(now)
	if len(snapshots) != 1 {
		t.Fatalf("len(snapshots) = %d, want 1", len(snapshots))
	}
	got := snapshots[0]
	if got.ID != id || got.AccountID != 42 || got.AccountName != "Operator Main" || got.AccountEmail != "operator@example.com" {
		t.Fatalf("snapshot identity = id:%d account:%d name:%q email:%q", got.ID, got.AccountID, got.AccountName, got.AccountEmail)
	}
	if got.Model != "gpt-5.1-codex" || got.APIKeyName != "team-key" || got.APIKeyMasked != "sk-...abcd" {
		t.Fatalf("snapshot metadata = model:%q api_key:%q masked:%q", got.Model, got.APIKeyName, got.APIKeyMasked)
	}
	if got.DurationMs != 2500 {
		t.Fatalf("DurationMs = %d, want 2500", got.DurationMs)
	}

	store.EndActiveRequest(id)
	if got := store.ActiveRequestSnapshots(now); len(got) != 0 {
		t.Fatalf("snapshots after EndActiveRequest = %d, want 0", len(got))
	}
}

func TestActiveRequestChangesNotifySubscribers(t *testing.T) {
	store := NewStore(nil, nil, nil)
	changes, unsubscribe := store.SubscribeActiveRequestChanges()
	defer unsubscribe()

	id := store.BeginActiveRequest(ActiveRequestMeta{AccountID: 42})
	requireActiveRequestChange(t, changes)

	store.EndActiveRequest(id)
	requireActiveRequestChange(t, changes)
}

func TestActiveRequestChangesCoalesceWithoutBlocking(t *testing.T) {
	store := NewStore(nil, nil, nil)
	changes, unsubscribe := store.SubscribeActiveRequestChanges()
	defer unsubscribe()

	store.BeginActiveRequest(ActiveRequestMeta{AccountID: 1})
	store.BeginActiveRequest(ActiveRequestMeta{AccountID: 2})
	requireActiveRequestChange(t, changes)

	select {
	case <-changes:
		t.Fatal("received duplicate queued change; want coalesced notification")
	default:
	}

	if got := len(store.ActiveRequestSnapshots(time.Now())); got != 2 {
		t.Fatalf("active snapshots = %d, want 2", got)
	}
}

func TestActiveRequestChangesStopAfterUnsubscribe(t *testing.T) {
	store := NewStore(nil, nil, nil)
	changes, unsubscribe := store.SubscribeActiveRequestChanges()
	unsubscribe()
	unsubscribe()

	store.BeginActiveRequest(ActiveRequestMeta{AccountID: 42})
	select {
	case <-changes:
		t.Fatal("received change after unsubscribe")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestActiveRequestChangesNotifySubscribersIndependently(t *testing.T) {
	store := NewStore(nil, nil, nil)
	first, unsubscribeFirst := store.SubscribeActiveRequestChanges()
	second, unsubscribeSecond := store.SubscribeActiveRequestChanges()
	defer unsubscribeSecond()

	id := store.BeginActiveRequest(ActiveRequestMeta{AccountID: 42})
	requireActiveRequestChange(t, first)
	requireActiveRequestChange(t, second)

	unsubscribeFirst()
	store.EndActiveRequest(id)
	requireActiveRequestChange(t, second)
	select {
	case <-first:
		t.Fatal("first subscriber received a change after unsubscribe")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestActiveRequestChangesIgnoreDuplicateEnd(t *testing.T) {
	store := NewStore(nil, nil, nil)
	changes, unsubscribe := store.SubscribeActiveRequestChanges()
	defer unsubscribe()

	id := store.BeginActiveRequest(ActiveRequestMeta{AccountID: 42})
	requireActiveRequestChange(t, changes)
	store.EndActiveRequest(id)
	requireActiveRequestChange(t, changes)

	store.EndActiveRequest(id)
	select {
	case <-changes:
		t.Fatal("duplicate EndActiveRequest emitted a change")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestActiveRequestChangesConcurrentNotifyAndUnsubscribe(t *testing.T) {
	store := NewStore(nil, nil, nil)

	for i := 0; i < 100; i++ {
		_, unsubscribe := store.SubscribeActiveRequestChanges()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			unsubscribe()
		}()
		go func(accountID int64) {
			defer wg.Done()
			id := store.BeginActiveRequest(ActiveRequestMeta{AccountID: accountID})
			store.EndActiveRequest(id)
		}(int64(i + 1))
		wg.Wait()
	}

	if got := len(store.ActiveRequestSnapshots(time.Now())); got != 0 {
		t.Fatalf("active snapshots = %d, want 0", got)
	}
}

func requireActiveRequestChange(t *testing.T, changes <-chan struct{}) {
	t.Helper()
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active request change")
	}
}
