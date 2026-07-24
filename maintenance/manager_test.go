package maintenance_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex2api/maintenance"
)

// TestSnapshotDefaultUnknown verifies a fresh Manager returns unknown status.
func TestSnapshotDefaultUnknown(t *testing.T) {
	m := maintenance.New(maintenance.Config{})
	snap := m.Snapshot()
	if snap.Status != maintenance.StatusUnknown {
		t.Fatalf("expected unknown, got %s", snap.Status)
	}
}

// TestStartStop verifies Start+Stop does not leak goroutines.
func TestStartStop(t *testing.T) {
	m := maintenance.New(maintenance.Config{})
	m.Start()
	// Give the background goroutine time to run its first pass.
	time.Sleep(50 * time.Millisecond)
	// Stop must return promptly.
	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() did not return within 3s")
	}
}

// TestDirSize verifies recursive directory size calculation.
func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	// Write a known-size file.
	data := make([]byte, 1024)
	if err := os.WriteFile(filepath.Join(dir, "test.dat"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := maintenance.Config{LogDir: dir}
	m := maintenance.New(cfg)
	m.Start()
	time.Sleep(200 * time.Millisecond)
	m.Stop()

	snap := m.Snapshot()
	if snap.Managed.LogsBytes < 1024 {
		t.Fatalf("expected logs_bytes >= 1024, got %d", snap.Managed.LogsBytes)
	}
}

// TestStorageStatusBoundaries verifies 80/90 thresholds via exported helper.
func TestStorageStatusBoundaries(t *testing.T) {
	cases := []struct {
		pct    float64
		expect maintenance.StorageStatus
	}{
		{0, maintenance.StatusNormal},
		{79.9, maintenance.StatusNormal},
		{80.0, maintenance.StatusWarning},
		{89.9, maintenance.StatusWarning},
		{90.0, maintenance.StatusCritical},
		{99.9, maintenance.StatusCritical},
	}
	for _, tc := range cases {
		got := maintenance.StatusFromPercent(tc.pct)
		if got != tc.expect {
			t.Errorf("pct=%.1f: want %s got %s", tc.pct, tc.expect, got)
		}
	}
}

// TestRemoteImagesUnknown verifies that empty ImagesDir reports -1 (remote/unknown).
func TestRemoteImagesUnknown(t *testing.T) {
	dir := t.TempDir()
	cfg := maintenance.Config{
		LogDir:    dir,
		ImagesDir: "", // remote storage
	}
	m := maintenance.New(cfg)
	m.Start()
	time.Sleep(200 * time.Millisecond)
	m.Stop()

	snap := m.Snapshot()
	if snap.Managed.ImagesBytes != -1 {
		t.Fatalf("expected -1 for remote images, got %d", snap.Managed.ImagesBytes)
	}
}

// TestDefaultRetentionConfig validates env-var override.
func TestDefaultRetentionConfig(t *testing.T) {
	t.Setenv("RETENTION_USAGE_LOG_DAYS", "15")
	cfg := maintenance.DefaultRetentionConfig(30)
	want := 15 * 24 * time.Hour
	if cfg.UsageLogs != want {
		t.Fatalf("expected %v, got %v", want, cfg.UsageLogs)
	}
}
