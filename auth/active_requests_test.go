package auth

import (
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
