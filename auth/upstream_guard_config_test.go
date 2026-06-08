package auth

import (
	"testing"

	"github.com/codex2api/database"
	"github.com/codex2api/security/upstreamguard"
)

func TestStoreLoadsUpstreamGuardConfigFromSystemSettings(t *testing.T) {
	store := NewStore(nil, nil, &database.SystemSettings{
		UpstreamGuardMode: upstreamguard.ModeOff,
	})

	cfg := store.GetUpstreamGuardConfig()
	if cfg.Mode != upstreamguard.ModeOff {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, upstreamguard.ModeOff)
	}
}

func TestStoreDefaultsUpstreamGuardConfigToWarnMode(t *testing.T) {
	store := NewStore(nil, nil, nil)

	cfg := store.GetUpstreamGuardConfig()
	if cfg.Mode != upstreamguard.ModeWarn {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, upstreamguard.ModeWarn)
	}
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true for default warn mode")
	}
}

func TestStoreLoadsUpstreamGuardSuppressionsFromSystemSettings(t *testing.T) {
	store := NewStore(nil, nil, &database.SystemSettings{
		UpstreamGuardMode:         upstreamguard.ModeWarn,
		UpstreamGuardSuppressions: `[{"rule_id":"response_injection","endpoint":"/v1/responses","account_id":42,"base_url":"https://relay.example.com/v1","action":"downgrade"}]`,
	})

	cfg := store.GetUpstreamGuardConfig()
	if len(cfg.Suppressions) != 1 {
		t.Fatalf("Suppressions len = %d, want 1", len(cfg.Suppressions))
	}
	got := cfg.Suppressions[0]
	if got.RuleID != upstreamguard.RuleResponseInjection || got.Endpoint != "/v1/responses" || got.AccountID != 42 || got.BaseURL != "https://relay.example.com/v1" || got.Action != upstreamguard.SuppressDowngrade {
		t.Fatalf("unexpected suppression: %+v", got)
	}
}
