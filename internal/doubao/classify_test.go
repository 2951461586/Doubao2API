package doubao

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Kind
	}{
		{"nil", nil, KindOK},
		{"业务错误", &Error{Code: 2, Msg: "campaign not found"}, KindBusiness},
		{"账号会话失效", &Error{Code: 1001, Msg: "login required"}, KindAccount},
		{"限流", errors.New("710022004 rate limited"), KindAccount},
		{"超时", context.DeadlineExceeded, KindTimeout},
		{"客户端断开", NewCanceled(errors.New("context canceled")), KindCanceled},
		{"网络错误", errors.New("dial tcp: connection refused"), KindTransport},
		{"解析失败", errors.New("上游返回非流式响应"), KindTransport},
	}
	for _, c := range cases {
		if got := Classify(c.err); got != c.want {
			t.Errorf("%s: Classify() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestKindStatusAndRetry(t *testing.T) {
	statuses := map[Kind]int{
		KindOK:        200,
		KindBusiness:  422,
		KindAccount:   502,
		KindTimeout:   504,
		KindCanceled:  499,
		KindTransport: 502,
	}
	for k, want := range statuses {
		if got := k.HTTPStatus(); got != want {
			t.Errorf("%s.HTTPStatus() = %d, want %d", k, got, want)
		}
	}
	if KindBusiness.Retryable() {
		t.Error("业务错误不应重试")
	}
	if KindCanceled.Retryable() {
		t.Error("客户端断开不应重试")
	}
	if !KindAccount.Retryable() || !KindTransport.Retryable() || !KindTimeout.Retryable() {
		t.Error("账号 / 网络 / 超时应可重试")
	}
	for _, k := range []Kind{KindBusiness, KindAccount, KindTimeout, KindCanceled, KindTransport} {
		if k.ErrorType() == "" {
			t.Errorf("%s 缺少 error.type", k)
		}
	}
}

func TestCanceledMarker(t *testing.T) {
	base := errors.New("context canceled")
	err := NewCanceled(base)
	if !IsCanceled(err) {
		t.Error("NewCanceled 的返回值必须被 IsCanceled 识别")
	}
	if !errors.Is(err, base) {
		t.Error("canceledError 应保留原始错误链")
	}
	if !strings.Contains(err.Error(), "客户端已断开") {
		t.Errorf("错误文案应说明客户端断开: %v", err)
	}
	if IsCanceled(errors.New("whatever")) {
		t.Error("普通错误不应被识别为断开")
	}
}

func TestIsAccountScoped(t *testing.T) {
	if !IsAccountScopedError(&Error{Code: 1, Msg: "login invalid"}) {
		t.Error("login 应判定为账号级")
	}
	if IsAccountScopedError(&Error{Code: 2, Msg: "campaign not found"}) {
		t.Error("campaign not found 不应判定为账号级")
	}
	if IsBusinessError(errors.New("network down")) {
		t.Error("普通错误不是业务错误")
	}
}
