package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/codex2api/database"
)

func TestFirstTokenTimeoutGuardCancelsUpstream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	guard := newFirstTokenTimeoutGuard(20*time.Millisecond, cancel)
	defer guard.Stop()

	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first token timeout guard did not cancel upstream context")
	}
	if !guard.TimedOut() {
		t.Fatal("guard TimedOut() = false, want true")
	}
}

func TestFirstTokenTimeoutGuardStopsOnFirstTokenEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard := newFirstTokenTimeoutGuard(30*time.Millisecond, cancel)
	defer guard.Stop()

	guard.MarkEvent("response.output_text.delta")

	select {
	case <-ctx.Done():
		t.Fatal("first token timeout guard canceled after first token event")
	case <-time.After(80 * time.Millisecond):
	}
	if guard.TimedOut() {
		t.Fatal("guard TimedOut() = true, want false")
	}
}

func TestNormalizeRuntimeSettingsFirstTokenTimeout(t *testing.T) {
	settings := NormalizeRuntimeSettings(RuntimeSettings{FirstTokenTimeoutSec: -1})
	if settings.FirstTokenTimeoutSec != defaultFirstTokenTimeoutSec {
		t.Fatalf("negative first token timeout normalized to %d, want default %d", settings.FirstTokenTimeoutSec, defaultFirstTokenTimeoutSec)
	}

	settings = NormalizeRuntimeSettings(RuntimeSettings{FirstTokenTimeoutSec: 601})
	if settings.FirstTokenTimeoutSec != 600 {
		t.Fatalf("oversized first token timeout normalized to %d, want 600", settings.FirstTokenTimeoutSec)
	}
}

func TestDefaultRuntimeSettingsFirstTokenTimeoutIsFifteenSeconds(t *testing.T) {
	if got := DefaultRuntimeSettings().FirstTokenTimeoutSec; got != 15 {
		t.Fatalf("default FirstTokenTimeoutSec = %d, want 15", got)
	}
}

func TestApplyRuntimeSettingsFromSystemFirstTokenTimeout(t *testing.T) {
	defer ApplyRuntimeSettings(DefaultRuntimeSettings())

	settings := ApplyRuntimeSettingsFromSystem(&database.SystemSettings{
		FirstTokenTimeoutSeconds: 42,
	})

	if settings.FirstTokenTimeoutSec != 42 {
		t.Fatalf("FirstTokenTimeoutSec = %d, want 42", settings.FirstTokenTimeoutSec)
	}
	if got := currentFirstTokenTimeout(); got != 42*time.Second {
		t.Fatalf("currentFirstTokenTimeout() = %s, want 42s", got)
	}
}

func TestFirstTokenTimeoutForReasoningEffortUsesLongerTimeoutForHighEffort(t *testing.T) {
	previous := CurrentRuntimeSettings()
	ApplyRuntimeSettings(RuntimeSettings{FirstTokenTimeoutSec: 15})
	t.Cleanup(func() { ApplyRuntimeSettings(previous) })

	if got := firstTokenTimeoutForReasoningEffort("low"); got != 15*time.Second {
		t.Fatalf("low effort timeout = %s, want 15s", got)
	}
	if got := firstTokenTimeoutForReasoningEffort("high"); got != 15*time.Second {
		t.Fatalf("high effort timeout = %s, want 15s", got)
	}
	if got := firstTokenTimeoutForReasoningEffort("xhigh"); got != 15*time.Second {
		t.Fatalf("xhigh effort timeout = %s, want 15s", got)
	}
}

func TestFirstTokenTimeoutForReasoningEffortHonorsDisabledAndUpperSetting(t *testing.T) {
	previous := CurrentRuntimeSettings()
	t.Cleanup(func() { ApplyRuntimeSettings(previous) })

	ApplyRuntimeSettings(RuntimeSettings{FirstTokenTimeoutSec: 0})
	if got := firstTokenTimeoutForReasoningEffort("xhigh"); got != 0 {
		t.Fatalf("disabled timeout = %s, want 0", got)
	}

	ApplyRuntimeSettings(RuntimeSettings{FirstTokenTimeoutSec: 20})
	if got := firstTokenTimeoutForReasoningEffort("xhigh"); got != 20*time.Second {
		t.Fatalf("configured high timeout = %s, want 20s", got)
	}
}
