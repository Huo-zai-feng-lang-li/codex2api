package proxy

import "testing"

func TestRetryAccountExclusionsSoftResetPreservesHard(t *testing.T) {
	exclusions := newRetryAccountExclusions()
	exclusions.MarkSoftFirstTokenTimeout(1)
	exclusions.MarkHard(2)

	selection := exclusions.ForSelection()
	if !selection[1] || !selection[2] {
		t.Fatalf("selection excludes = %#v, want soft and hard accounts", selection)
	}

	if !exclusions.ResetSoft() {
		t.Fatal("ResetSoft() = false, want true")
	}
	selection = exclusions.ForSelection()
	if selection[1] {
		t.Fatalf("soft account still excluded after reset: %#v", selection)
	}
	if !selection[2] {
		t.Fatalf("hard account was cleared by soft reset: %#v", selection)
	}
}

func TestRetryAccountExclusionsHardOverridesSoft(t *testing.T) {
	exclusions := newRetryAccountExclusions()
	exclusions.MarkSoftFirstTokenTimeout(1)
	exclusions.MarkHard(1)

	if exclusions.ResetSoft() {
		t.Fatal("ResetSoft() cleared a hard-only account")
	}
	selection := exclusions.ForSelection()
	if !selection[1] {
		t.Fatalf("hard account missing from selection excludes: %#v", selection)
	}
}

func TestIsFirstTokenTimeoutOutcome(t *testing.T) {
	if !isFirstTokenTimeoutOutcome(firstTokenTimeoutOutcome(10)) {
		t.Fatal("first-token timeout outcome should be classified as timeout")
	}
	if isFirstTokenTimeoutOutcome(streamOutcome{failureKind: "transport"}) {
		t.Fatal("transport outcome should not be classified as first-token timeout")
	}
}

func TestWebsocketFallbackHTTPEnvDefaultsOn(t *testing.T) {
	t.Setenv("CODEX_WS_FALLBACK_HTTP", "")
	if !websocketFallbackHTTPEnabled() {
		t.Fatal("websocketFallbackHTTPEnabled() = false, want default true")
	}

	t.Setenv("CODEX_WS_FALLBACK_HTTP", "false")
	if websocketFallbackHTTPEnabled() {
		t.Fatal("websocketFallbackHTTPEnabled() = true, want false")
	}
}

func TestWebsocketMissingTerminalTransparentRetryHonorsFallbackEnv(t *testing.T) {
	outcome := streamOutcome{
		logStatusCode:  logStatusUpstreamStreamBreak,
		failureKind:    "websocket_missing_terminal",
		failureMessage: "websocket closed by server before response.completed",
		penalize:       true,
	}

	t.Setenv("CODEX_WS_FALLBACK_HTTP", "")
	if !shouldTransparentRetryStream(outcome, 0, 2, false, nil, nil) {
		t.Fatal("websocket missing terminal should transparently retry before downstream body")
	}

	t.Setenv("CODEX_WS_FALLBACK_HTTP", "false")
	if shouldTransparentRetryStream(outcome, 0, 2, false, nil, nil) {
		t.Fatal("websocket missing terminal should not retry when fallback is disabled")
	}
}

func TestRetryAccountExclusionsFirstTokenBudgetAllowsOneSwitchOnly(t *testing.T) {
	exclusions := newRetryAccountExclusions()

	if !exclusions.MarkSoftFirstTokenTimeout(1) {
		t.Fatal("first timeout should be accepted as the only switch")
	}
	if exclusions.MarkSoftFirstTokenTimeout(2) {
		t.Fatal("second timeout should exceed first-token retry budget")
	}
}

func TestRetryAccountExclusionsSmallPoolSoftResetAfterRound(t *testing.T) {
	exclusions := newRetryAccountExclusions()
	exclusions.MarkHard(9)
	exclusions.MarkSoftFirstTokenTimeout(1)

	if !exclusions.ShouldResetSoftForPool(1) {
		t.Fatal("small pool with exhausted soft set should reset soft excludes")
	}
	exclusions.ResetSoft()
	selection := exclusions.ForSelection()
	if selection[1] || selection[2] {
		t.Fatalf("soft excludes should be cleared: %#v", selection)
	}
	if !selection[9] {
		t.Fatalf("hard exclude should be preserved: %#v", selection)
	}
}
