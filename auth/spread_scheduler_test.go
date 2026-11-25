package auth

import (
	"testing"
	"time"
)

func newSpreadTestStore(t *testing.T, count int) *Store {
	t.Helper()
	store := NewStore(nil, nil, nil)
	store.SetAffinityMode(AffinityModeOff)
	for i := 1; i <= count; i++ {
		store.AddAccount(&Account{DBID: int64(i), AccessToken: "token", Status: StatusReady})
	}
	return store
}

func pickSequence(t *testing.T, store *Store, rounds int) []int64 {
	t.Helper()
	got := make([]int64, 0, rounds)
	for i := 0; i < rounds; i++ {
		acc := store.NextExcluding(0, nil)
		if acc == nil {
			t.Fatalf("第 %d 次挑号返回 nil", i)
		}
		got = append(got, acc.DBID)
		store.Release(acc)
	}
	return got
}

func assertSequence(t *testing.T, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("挑号次数 = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("挑号序列 = %v, want %v", got, want)
		}
	}
}

func TestAffinityOffRoundRobinSpreadsRequests(t *testing.T) {
	store := newSpreadTestStore(t, 3)
	store.SetSchedulerMode(SchedulerModeRoundRobin)

	got := pickSequence(t, store, 6)
	assertSequence(t, got, []int64{1, 2, 3, 1, 2, 3})
}

func TestAffinityOffRemainingQuotaSpreadsRequests(t *testing.T) {
	store := newSpreadTestStore(t, 3)
	store.SetSchedulerMode(SchedulerModeRemainingQuota)

	got := pickSequence(t, store, 6)
	assertSequence(t, got, []int64{1, 2, 3, 1, 2, 3})
}

func TestAffinityOffSkipsStickyRateLimitedAccount(t *testing.T) {
	store := newSpreadTestStore(t, 2)
	limited := store.FindByID(1)
	if limited == nil {
		t.Fatal("账号 1 不存在")
	}
	limited.SetCooldownUntil(time.Now().Add(-time.Minute), "rate_limited")

	for i := 0; i < 3; i++ {
		acc := store.NextExcluding(0, nil)
		if acc == nil {
			t.Fatal("应当挑到未限流的账号 2")
		}
		if acc.DBID != 2 {
			t.Fatalf("挑到账号 %d, want 2（sticky 限流账号不能复活）", acc.DBID)
		}
		store.Release(acc)
	}
}

func TestStrictSessionPickerIgnoresBindingWhenAffinityOff(t *testing.T) {
	store := newSpreadTestStore(t, 3)
	store.SetSchedulerMode(SchedulerModeRoundRobin)

	first := store.NextExcluding(0, nil)
	if first == nil {
		t.Fatal("首次挑号失败")
	}
	store.Release(first)
	// 模拟上一轮请求建立的会话绑定。
	store.BindSessionAffinity("sess-off", first, "")

	seen := map[int64]bool{}
	for i := 0; i < 3; i++ {
		acc, _ := store.NextForStrictSessionExcludingWithFilter("sess-off", 0, nil, nil)
		if acc == nil {
			t.Fatal("off 模式下 strict 会话挑号返回 nil")
		}
		seen[acc.DBID] = true
		store.Release(acc)
	}
	if len(seen) != 3 {
		t.Fatalf("off 模式下应当铺开到 3 个账号，实际 %v", seen)
	}
}

func TestTransientRateLimitCooldownAutoExpires(t *testing.T) {
	expired := &Account{
		AccessToken:    "token",
		Status:         StatusCooldown,
		CooldownReason: CooldownReasonTransientRateLimit,
		CooldownUtil:   time.Now().Add(-time.Second),
	}
	if !expired.IsAvailable() {
		t.Fatal("短时限流冷却到期后应当自动恢复可调度")
	}
	if got := expired.RuntimeStatus(); got != "active" {
		t.Fatalf("RuntimeStatus() = %q, want active", got)
	}

	active := &Account{
		AccessToken:    "token",
		Status:         StatusCooldown,
		CooldownReason: CooldownReasonTransientRateLimit,
		CooldownUtil:   time.Now().Add(time.Minute),
	}
	if active.IsAvailable() {
		t.Fatal("冷却期内的短时限流账号不应可调度")
	}
	if got := active.RuntimeStatus(); got != "rate_limited" {
		t.Fatalf("RuntimeStatus() = %q, want rate_limited", got)
	}
}
