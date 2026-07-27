package auth

import (
	"testing"
	"time"
)

func TestIsFullyAvailableRequiresVerifiedGlobalAndModelState(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		acc  *Account
		want bool
	}{
		{
			name: "ready account",
			acc:  &Account{AccessToken: "token", Status: StatusReady},
			want: true,
		},
		{
			name: "expired sticky rate limit",
			acc: &Account{
				AccessToken:    "token",
				Status:         StatusCooldown,
				CooldownReason: "rate_limited",
				CooldownUtil:   now.Add(-time.Minute),
			},
			want: false,
		},
		{
			name: "expired transient cooldown",
			acc: &Account{
				AccessToken:    "token",
				Status:         StatusCooldown,
				CooldownReason: "server_error",
				CooldownUtil:   now.Add(-time.Minute),
			},
			want: true,
		},
		{
			name: "active model cooldown",
			acc: &Account{
				AccessToken: "token",
				Status:      StatusReady,
				ModelCooldowns: map[string]ModelCooldown{
					"gpt-5.4": {Model: "gpt-5.4", ResetAt: now.Add(time.Minute)},
				},
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.acc.IsFullyAvailable(); got != test.want {
				t.Fatalf("IsFullyAvailable() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestAvailableCountUsesFullyAvailablePredicate(t *testing.T) {
	store := NewStore(nil, nil, nil)
	store.AddAccount(&Account{DBID: 1, AccessToken: "ready", Status: StatusReady})
	store.AddAccount(&Account{
		DBID:        2,
		AccessToken: "model-limited",
		Status:      StatusReady,
		ModelCooldowns: map[string]ModelCooldown{
			"gpt-5.4": {Model: "gpt-5.4", ResetAt: time.Now().Add(time.Minute)},
		},
	})

	if got := store.AvailableCount(); got != 1 {
		t.Fatalf("AvailableCount() = %d, want 1", got)
	}
}

func TestExpiredStickyCooldownIsNotSelectedByAnyScheduler(t *testing.T) {
	for _, reason := range []string{"rate_limited", "payment_required"} {
		for _, test := range []struct {
			name string
			set  func(*Store)
		}{
			{name: "standard"},
			{name: "fast", set: func(store *Store) { store.SetFastSchedulerEnabled(true) }},
			{name: "lazy", set: func(store *Store) { store.SetLazyMode(true) }},
		} {
			t.Run(reason+"/"+test.name, func(t *testing.T) {
				store := NewStore(nil, nil, nil)
				if test.set != nil {
					test.set(store)
				}
				store.AddAccount(&Account{
					DBID:           1,
					AccessToken:    "token",
					Status:         StatusCooldown,
					CooldownReason: reason,
					CooldownUtil:   time.Now().Add(-time.Minute),
				})

				if got := store.NextExcluding(0, nil); got != nil {
					store.Release(got)
					t.Fatalf("NextExcluding() selected account %d", got.DBID)
				}
			})
		}
	}
}
