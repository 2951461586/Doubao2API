package server

import (
	"context"
	"errors"
	"testing"

	"doubao2api/internal/account"
	"doubao2api/internal/doubao"
	"doubao2api/internal/store"
)

// 待确认签到重试队列的行为测试。
//
// 上游把签到奖励做成异步发放（biz_code=3），因此「确认中」必须能被后台复查：
// 跨天作废、次数用尽保留记录、确认后补写签到日期。

func TestPendingCheckinAction(t *testing.T) {
	today := "2026-11-14"
	cases := []struct {
		name string
		acct *account.Account
		want pendingAction
	}{
		{name: "nil 账号", acct: nil, want: pendingDropStale},
		{name: "当日待确认", acct: &account.Account{PendingCheckinDate: today}, want: pendingRetry},
		{name: "跨天作废", acct: &account.Account{PendingCheckinDate: "2026-11-13"}, want: pendingDropStale},
		{name: "次数用尽", acct: &account.Account{PendingCheckinDate: today, PendingCheckinTries: maxPendingCheckinTries}, want: pendingGiveUp},
		{name: "次数未用尽", acct: &account.Account{PendingCheckinDate: today, PendingCheckinTries: 1}, want: pendingRetry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pendingCheckinAction(tc.acct, today, maxPendingCheckinTries); got != tc.want {
				t.Errorf("pendingCheckinAction() = %v, want %v", got, tc.want)
			}
		})
	}
	// maxTries<=0 时回落到默认上限
	a := &account.Account{PendingCheckinDate: today, PendingCheckinTries: 1}
	if got := pendingCheckinAction(a, today, 0); got != pendingRetry {
		t.Errorf("maxTries=0 应回落到默认上限，实际 %v", got)
	}
}

// newPendingServer 构造开启重试队列、且有一个「确认中」账号的服务。
func newPendingServer(t *testing.T) (*Server, *account.Account) {
	t.Helper()
	s := newTestServer(t)
	s.Store.UpdateSettings(func(st *store.Settings) { st.RetryPendingCheckin = true })
	a := addTestAccount(t, s, "a1", "session-1")
	s.Store.MarkCheckinPending(a.ID, todayString())
	return s, a
}

func TestRetryPendingCheckinsPendingThenConfirmed(t *testing.T) {
	s, a := newPendingServer(t)

	// 第一次复查：上游仍确认中 → 只累加次数，不写签到日期。
	calls := 0
	s.retryPendingCheckinsWith(context.Background(), func(_ context.Context, acct *account.Account, _ string) (*doubao.CheckinResult, error) {
		calls++
		if acct.ID != a.ID {
			t.Errorf("传入账号 = %q, want %q", acct.ID, a.ID)
		}
		return &doubao.CheckinResult{Pending: true}, nil
	})
	if calls != 1 {
		t.Fatalf("应调用一次上游，实际 %d", calls)
	}
	got := s.Store.Account(a.ID)
	if got.PendingCheckinTries != 1 {
		t.Errorf("尝试次数 = %d, want 1", got.PendingCheckinTries)
	}
	if got.LastCheckinDate != "" {
		t.Errorf("确认中不应写入签到日期，实际 %q", got.LastCheckinDate)
	}

	// 第二次复查：上游已确认 → 写签到日期并清空队列项。
	s.retryPendingCheckinsWith(context.Background(), func(_ context.Context, _ *account.Account, _ string) (*doubao.CheckinResult, error) {
		return &doubao.CheckinResult{CheckedIn: true, CheckinDate: todayString(), RewardAmount: 200, ContinuousDays: 2}, nil
	})
	got = s.Store.Account(a.ID)
	if got.PendingCheckinDate != "" || got.PendingCheckinTries != 0 {
		t.Errorf("确认后应清空队列项: %+v", got)
	}
	if got.LastCheckinDate != todayString() || got.LastCheckinReward != 200 {
		t.Errorf("确认后应写入签到结果: date=%q reward=%d", got.LastCheckinDate, got.LastCheckinReward)
	}
}

func TestRetryPendingCheckinsDisabled(t *testing.T) {
	s, _ := newPendingServer(t)
	s.Store.UpdateSettings(func(st *store.Settings) { st.RetryPendingCheckin = false })

	calls := 0
	s.retryPendingCheckinsWith(context.Background(), func(context.Context, *account.Account, string) (*doubao.CheckinResult, error) {
		calls++
		return &doubao.CheckinResult{}, nil
	})
	if calls != 0 {
		t.Errorf("开关关闭时不应请求上游，实际 %d 次", calls)
	}
}

func TestRetryPendingCheckinsDropsStale(t *testing.T) {
	s, a := newPendingServer(t)
	// 把记录改成昨天
	s.Store.UpdateSettings(func(*store.Settings) {})
	past := s.Store.Account(a.ID)
	past.PendingCheckinDate = "2026-01-01"
	s.Store.UpsertAccount(past)

	calls := 0
	s.retryPendingCheckinsWith(context.Background(), func(context.Context, *account.Account, string) (*doubao.CheckinResult, error) {
		calls++
		return &doubao.CheckinResult{}, nil
	})
	if calls != 0 {
		t.Errorf("跨天记录不应请求上游，实际 %d 次", calls)
	}
	if got := s.Store.Account(a.ID); got.PendingCheckinDate != "" {
		t.Errorf("跨天记录应被清空，实际 %q", got.PendingCheckinDate)
	}
}

func TestRetryPendingCheckinsGivesUp(t *testing.T) {
	s, a := newPendingServer(t)
	acc := s.Store.Account(a.ID)
	acc.PendingCheckinTries = maxPendingCheckinTries
	s.Store.UpsertAccount(acc)

	calls := 0
	s.retryPendingCheckinsWith(context.Background(), func(context.Context, *account.Account, string) (*doubao.CheckinResult, error) {
		calls++
		return &doubao.CheckinResult{}, nil
	})
	if calls != 0 {
		t.Errorf("次数用尽后不应再请求上游，实际 %d 次", calls)
	}
	// 记录保留（便于控制台判断上游是否恢复），而不是悄悄丢掉。
	if got := s.Store.Account(a.ID); got.PendingCheckinTries != maxPendingCheckinTries {
		t.Errorf("用尽后应保留记录，实际 %+v", got)
	}
}

func TestRetryPendingCheckinsErrorKeepsQueue(t *testing.T) {
	s, a := newPendingServer(t)

	s.retryPendingCheckinsWith(context.Background(), func(context.Context, *account.Account, string) (*doubao.CheckinResult, error) {
		return nil, errors.New("network down")
	})
	got := s.Store.Account(a.ID)
	if got.PendingCheckinTries != 1 {
		t.Errorf("失败也应累加次数，实际 %d", got.PendingCheckinTries)
	}
	if got.PendingCheckinDate == "" {
		t.Error("失败后应保留队列项以便继续重试")
	}
}
