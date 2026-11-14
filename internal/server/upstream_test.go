package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"doubao2api/internal/doubao"
)

// withAccountRetry 的错误归属：只有账号级错误才记失败并换号，业务错误立即返回。

func TestWithAccountRetryBusinessErrorNoRetryNoMark(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	addTestAccount(t, s, "a2", "s2")

	calls := 0
	_, err := s.withAccountRetry(context.Background(), time.Second, func(context.Context, *doubao.Account) error {
		calls++
		return &doubao.Error{Code: 2, Msg: "campaign not found"}
	})
	if err == nil {
		t.Fatal("业务错误应向上传递")
	}
	if calls != 1 {
		t.Errorf("业务错误不应换号重试，实际调用 %d 次", calls)
	}
	for _, a := range s.Store.Accounts() {
		if a.Failures != 0 || a.Status != "ok" {
			t.Errorf("业务错误不该把账号标红: %s failures=%d status=%s", a.ID, a.Failures, a.Status)
		}
	}
	// /health 依赖 status；这里再确认 ready 不会被业务错误吃掉。
	if got := countReady(s); got != 2 {
		t.Errorf("ready = %d, want 2", got)
	}
}

func TestWithAccountRetryAccountErrorMarksAndRetries(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	addTestAccount(t, s, "a2", "s2")

	calls := 0
	_, err := s.withAccountRetry(context.Background(), time.Second, func(context.Context, *doubao.Account) error {
		calls++
		return &doubao.Error{Code: 1, Msg: "登录已失效，请重新登录"}
	})
	if err == nil {
		t.Fatal("应返回最后一个错误")
	}
	if calls < 2 {
		t.Errorf("账号级错误应换号重试，实际调用 %d 次", calls)
	}
	marked := 0
	for _, a := range s.Store.Accounts() {
		if a.Failures > 0 && a.Status == "error" {
			marked++
		}
	}
	if marked == 0 {
		t.Error("账号级错误应把账号标记为失败")
	}
	if got := countReady(s); got != 0 {
		t.Errorf("ready = %d, want 0", got)
	}
}

func TestWithAccountRetrySuccessMarksOK(t *testing.T) {
	s := newTestServer(t)
	a := addTestAccount(t, s, "a1", "s1")
	s.Store.MarkAccountResult(a.ID, false, "登录已失效") // 先弄脏

	got, err := s.withAccountRetry(context.Background(), time.Second, func(context.Context, *doubao.Account) error {
		return nil
	})
	if err != nil || got == nil {
		t.Fatalf("应成功: got=%v err=%v", got, err)
	}
	if fresh := s.Store.Account(a.ID); fresh.Failures != 0 || fresh.Status != "ok" {
		t.Errorf("成功后应重置账号状态: %+v", fresh)
	}
}

func TestWithAccountBusinessErrorKeepsStatus(t *testing.T) {
	s := newTestServer(t)
	a := addTestAccount(t, s, "a1", "s1")

	_, err := s.withAccount(context.Background(), a.ID, time.Second, func(context.Context, *doubao.Account) error {
		return &doubao.Error{Code: 2, Msg: "campaign not found"}
	})
	if err == nil {
		t.Fatal("业务错误应向上传递")
	}
	if fresh := s.Store.Account(a.ID); fresh.Failures != 0 || fresh.Status != "ok" {
		t.Errorf("业务错误不该改动账号状态: failures=%d status=%s", fresh.Failures, fresh.Status)
	}
}

func TestWithAccountNetworkErrorNotMarked(t *testing.T) {
	s := newTestServer(t)
	a := addTestAccount(t, s, "a1", "s1")

	_, err := s.withAccount(context.Background(), a.ID, time.Second, func(context.Context, *doubao.Account) error {
		return errors.New("请求上游失败: connection reset")
	})
	if err == nil {
		t.Fatal("网络错误应向上传递")
	}
	if fresh := s.Store.Account(a.ID); fresh.Failures != 0 {
		t.Errorf("网络错误不是账号问题，不应记失败: failures=%d", fresh.Failures)
	}
}

// countReady 复刻 /health 的 ready 计数口径。
func countReady(s *Server) int {
	n := 0
	for _, a := range s.Store.Accounts() {
		if a.Enabled && a.Status == "ok" {
			n++
		}
	}
	return n
}
