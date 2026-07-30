package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestResponsesContinuityPersistsAndPrunesRows(t *testing.T) {
	ctx := context.Background()
	db, err := New("sqlite", filepath.Join(t.TempDir(), "codex2api.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()

	now := time.Now().UTC().Truncate(time.Second)
	rows := []ResponsesContinuationRow{
		{
			ResponseID: "resp_expired", AccountID: 1, BaseURL: "https://relay.example.com",
			InputJSON:  []byte(`[{"type":"message","role":"user","content":"old"}]`),
			OutputJSON: []byte(`[]`), Replayable: true, CreatedAt: now.Add(-2 * time.Hour),
			AccessedAt: now.Add(-2 * time.Hour), SizeBytes: 51,
		},
		{
			ResponseID: "resp_root", AccountID: 2, BaseURL: "https://relay.example.com",
			InputJSON:  []byte(`[{"type":"message","role":"user","content":"remember ALPHA"}]`),
			OutputJSON: []byte(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ALPHA"}]}]`),
			Replayable: true, CreatedAt: now, AccessedAt: now, SizeBytes: 153,
		},
		{
			ResponseID: "resp_child", ParentID: "resp_root", AccountID: 3,
			BaseURL:    "https://relay.example.com",
			InputJSON:  []byte(`[{"type":"message","role":"user","content":"repeat"}]`),
			OutputJSON: []byte(`[]`), Replayable: true, CreatedAt: now.Add(time.Second),
			AccessedAt: now.Add(time.Second), SizeBytes: 54,
		},
	}
	for i := range rows {
		if err := db.UpsertResponsesContinuation(ctx, &rows[i]); err != nil {
			t.Fatalf("UpsertResponsesContinuation(%s): %v", rows[i].ResponseID, err)
		}
	}

	deleted, err := db.PruneResponsesContinuations(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("PruneResponsesContinuations: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	if _, ok, err := db.GetResponsesContinuation(ctx, "resp_expired"); err != nil || ok {
		t.Fatalf("expired row: ok=%v err=%v", ok, err)
	}
	root, ok, err := db.GetResponsesContinuation(ctx, "resp_root")
	if err != nil || !ok {
		t.Fatalf("root row: ok=%v err=%v", ok, err)
	}
	if string(root.InputJSON) != string(rows[1].InputJSON) || !root.Replayable {
		t.Fatalf("root payload changed: %+v", root)
	}

	touchedAt := now.Add(2 * time.Minute)
	if err := db.TouchResponsesContinuations(ctx, []string{"resp_root", "resp_child"}, touchedAt); err != nil {
		t.Fatalf("TouchResponsesContinuations: %v", err)
	}
	for _, responseID := range []string{"resp_root", "resp_child"} {
		row, ok, err := db.GetResponsesContinuation(ctx, responseID)
		if err != nil || !ok {
			t.Fatalf("GetResponsesContinuation(%s): ok=%v err=%v", responseID, ok, err)
		}
		if !row.AccessedAt.Equal(touchedAt) {
			t.Fatalf("%s accessed_at = %s, want %s", row.ResponseID, row.AccessedAt, touchedAt)
		}
	}
}

func TestTrimResponsesContinuationsBoundsDiskRowsAndBytes(t *testing.T) {
	ctx := context.Background()
	db, err := New("sqlite", filepath.Join(t.TempDir(), "codex2api.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()

	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"resp_a", "resp_b", "resp_c"} {
		row := ResponsesContinuationRow{
			ResponseID: id, InputJSON: []byte(`[]`), OutputJSON: []byte(`[]`),
			Replayable: false, CreatedAt: now.Add(time.Duration(i) * time.Second),
			AccessedAt: now.Add(time.Duration(i) * time.Second), SizeBytes: 40,
		}
		if err := db.UpsertResponsesContinuation(ctx, &row); err != nil {
			t.Fatalf("UpsertResponsesContinuation(%s): %v", id, err)
		}
	}

	deleted, err := db.TrimResponsesContinuations(ctx, 2, 70)
	if err != nil {
		t.Fatalf("TrimResponsesContinuations: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}
	for _, responseID := range []string{"resp_a", "resp_b"} {
		if _, ok, err := db.GetResponsesContinuation(ctx, responseID); err != nil || ok {
			t.Fatalf("trimmed %s: ok=%v err=%v", responseID, ok, err)
		}
	}
	if _, ok, err := db.GetResponsesContinuation(ctx, "resp_c"); err != nil || !ok {
		t.Fatalf("retained resp_c: ok=%v err=%v", ok, err)
	}
}

func TestGetLatestReplayableResponseBySessionIDSkipsNewestPending(t *testing.T) {
	ctx := context.Background()
	db, err := New("sqlite", filepath.Join(t.TempDir(), "codex2api.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()

	now := time.Now().UTC().Truncate(time.Second)
	rows := []ResponsesContinuationRow{
		{
			ResponseID: "resp_replayable", SessionID: "session-1",
			InputJSON:  []byte(`[{"type":"message","role":"user","content":"keep"}]`),
			OutputJSON: []byte(`[]`), Replayable: true,
			CreatedAt: now, AccessedAt: now, SizeBytes: 55,
		},
		{
			ResponseID: "resp_pending", SessionID: "session-1",
			InputJSON: []byte(`null`), OutputJSON: []byte(`null`), Replayable: false,
			CreatedAt: now.Add(time.Second), AccessedAt: now.Add(time.Second),
		},
	}
	for i := range rows {
		if err := db.UpsertResponsesContinuation(ctx, &rows[i]); err != nil {
			t.Fatalf("UpsertResponsesContinuation(%s): %v", rows[i].ResponseID, err)
		}
	}

	row, ok, err := db.GetLatestReplayableResponseBySessionID(ctx, "session-1")
	if err != nil || !ok {
		t.Fatalf("GetLatestReplayableResponseBySessionID: ok=%v err=%v", ok, err)
	}
	if row.ResponseID != "resp_replayable" {
		t.Fatalf("response_id = %q, want resp_replayable", row.ResponseID)
	}
}
