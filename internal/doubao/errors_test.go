package doubao

import (
	"errors"
	"fmt"
	"testing"
)

// 上游错误的两条归属线：账号级（换号重试 + 记账号失败）与业务级（立刻返回、不动账号）。
//
// 线上教训：积分活动下线后 `/v1/tasks` 回 `code=2 campaign not found`，
// 旧逻辑把这种错误记到账号头上，导致整池账号变红、/health 误报 ready: 0。
func TestIsAccountScopedError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		account  bool // 应记账号失败
		business bool // 应立即返回、不重试
		retry    bool // 是否值得换号重试
	}{
		{name: "nil", err: nil},
		{name: "业务码 campaign not found", err: &Error{Code: 2, Msg: "campaign not found"}, business: true},
		{name: "参数错误", err: &Error{Code: 710020202, Msg: "common invalid param"}, business: true},
		{name: "限流", err: &Error{Code: 710022004, Msg: "rate limited"}, account: true, retry: true},
		{name: "登录失效", err: &Error{Code: 1, Msg: "登录已失效，请重新登录"}, account: true, retry: true},
		{name: "CSRF 失效", err: errors.New("上游错误 1: csrf token invalid"), account: true, retry: true},
		{name: "HTTP 401", err: errors.New("上游拒绝访问 (HTTP 401): forbidden"), account: true, retry: true},
		{name: "网络错误（可重试但不记账号）", err: errors.New("请求上游失败: connection reset"), retry: true},
		{name: "包装后的业务错误", err: fmt.Errorf("wrapped: %w", &Error{Code: 2, Msg: "campaign not found"}), business: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAccountScopedError(tc.err); got != tc.account {
				t.Errorf("IsAccountScopedError = %v, want %v", got, tc.account)
			}
			if got := IsBusinessError(tc.err); got != tc.business {
				t.Errorf("IsBusinessError = %v, want %v", got, tc.business)
			}
			if got := ShouldRetryWithAnotherAccount(tc.err); got != tc.retry {
				t.Errorf("ShouldRetryWithAnotherAccount = %v, want %v", got, tc.retry)
			}
		})
	}
}

// TestStreamErrorIsStructured 保证流式路径的错误是 *Error（否则分类退化成字符串匹配）。
func TestStreamErrorIsStructured(t *testing.T) {
	err := error(&Error{Code: 710022004, Msg: "rate limited"})
	var target *Error
	if !errors.As(err, &target) {
		t.Fatal("流式错误应可被 errors.As 识别为 *Error")
	}
	if !IsAccountScopedError(err) {
		t.Error("限流错误应判为账号级")
	}
}
