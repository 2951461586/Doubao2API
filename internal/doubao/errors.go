package doubao

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Kind 是一次上游调用的结果分类。
//
// 为什么需要它：此前所有失败都落成 `502 upstream_error`，导致
//   - 客户端主动断开（长回答被取消）被算成上游故障，失败率虚高；
//   - `campaign not found` 这类业务错误也报 502，调用方无法区分「重试有用」；
//   - 日志里只能看到字符串，排障要靠人读错误文案。
//
// 分类后：写响应、记统计、打日志、决定是否换号，全部由 Kind 驱动。
type Kind string

const (
	// KindOK 表示成功。
	KindOK Kind = "ok"
	// KindBusiness 是上游明确返回的业务 / 配置类错误（重试无效）。
	KindBusiness Kind = "business"
	// KindAccount 是账号级错误（会话 / 额度 / 风控），值得换号。
	KindAccount Kind = "account"
	// KindTimeout 是超时（单次尝试或总预算）。
	KindTimeout Kind = "timeout"
	// KindCanceled 是调用方主动断开（客户端超时、用户取消）。
	KindCanceled Kind = "canceled"
	// KindTransport 是网络 / 解析 / 非预期响应，可重试但不记账号。
	KindTransport Kind = "transport"
)

// Classify 把错误归入五类失败之一。
func Classify(err error) Kind {
	if err == nil {
		return KindOK
	}
	if errors.Is(err, context.Canceled) || IsCanceled(err) {
		return KindCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return KindTimeout
	}
	if IsBusinessError(err) {
		return KindBusiness
	}
	if IsAccountScopedError(err) {
		return KindAccount
	}
	return KindTransport
}

// HTTPStatus 返回该分类对应的对外状态码。
//
// 499 是 Nginx 的「客户端已关闭请求」约定值，没有标准状态码；
// 这里只在日志 / 统计里使用，真正写响应时连接通常已经不在了。
func (k Kind) HTTPStatus() int {
	switch k {
	case KindOK:
		return http.StatusOK
	case KindBusiness:
		// 语义是「请求本身不被上游接受」，不是网关故障。
		return http.StatusUnprocessableEntity
	case KindAccount:
		return http.StatusBadGateway
	case KindTimeout:
		return http.StatusGatewayTimeout
	case KindCanceled:
		return 499
	default:
		return http.StatusBadGateway
	}
}

// ErrorType 返回 OpenAI 风格的 error.type。
func (k Kind) ErrorType() string {
	switch k {
	case KindBusiness:
		return "business_error"
	case KindAccount:
		return "account_error"
	case KindTimeout:
		return "timeout_error"
	case KindCanceled:
		return "client_closed_request"
	default:
		return "upstream_error"
	}
}

// Retryable 报告该分类是否值得换号重试。
func (k Kind) Retryable() bool {
	return k == KindAccount || k == KindTransport || k == KindTimeout
}

// canceledError 标记「调用方已断开」。
//
// 上游此时返回的往往是 `context canceled`，但它既不代表上游故障，
// 也不该记到账号头上；必须在上层显式带上这个标记。
type canceledError struct{ err error }

func (e *canceledError) Error() string {
	if e.err == nil {
		return "客户端已断开"
	}
	return "客户端已断开: " + e.err.Error()
}

func (e *canceledError) Unwrap() error { return e.err }

// NewCanceled 把错误标记为客户端断开。
func NewCanceled(err error) error {
	if err == nil {
		err = context.Canceled
	}
	return &canceledError{err: err}
}

// IsCanceled 报告错误是否由调用方断开导致。
func IsCanceled(err error) bool {
	var ce *canceledError
	return errors.As(err, &ce)
}

// accountScopedMarkers 命中即认为错误与该账号本身有关（认证 / 会话 / 额度 / 风控）。
//
// 只有这类错误值得换号重试，也只有这类错误应该把账号标成异常。
var accountScopedMarkers = []string{
	// 认证 / 会话
	"login", "session", "csrf", "passport", "unauthorized", "forbidden",
	"登录", "会话", "未授权", "重新登录",
	// 额度 / 频率限制 / 风控
	"rate limit", "ratelimit", "too many requests", "quota", "710022004",
	"限流", "额度", "积分不足", "风控",
	// HTTP 层拒绝
	"http 401", "http 403",
}

// IsAccountScopedError 判断上游错误是否应记到具体账号头上（并值得换号重试）。
//
// 反例（线上实测）：积分活动下线后 `/v1/tasks` 回 `code=2 campaign not found`，
// 这属于业务/配置类错误 —— 换任何账号都一样，把它记成账号失败会让整池账号变红、
// `/health` 也会误报 `ready: 0`。
func IsAccountScopedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range accountScopedMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// IsBusinessError 判断是否是上游明确返回的业务 / 配置类错误。
//
// 判定：错误由上游结构化返回（*Error 或流式路径包装的同类型），且不属于账号级问题。
// 这类错误重试无效，应立即返回；网络错误、超时、解析失败不在此列（值得重试）。
func IsBusinessError(err error) bool {
	if err == nil {
		return false
	}
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return !IsAccountScopedError(err)
}

// ShouldRetryWithAnotherAccount 判断是否值得换号重试。
func ShouldRetryWithAnotherAccount(err error) bool {
	if err == nil {
		return false
	}
	return Classify(err).Retryable()
}

// IsAuthError 是 looksLikeAuthError 的导出版本，供其它包判断是否需要刷新凭据。
func IsAuthError(msg string) bool {
	m := strings.ToLower(msg)
	for _, k := range []string{"csrf", "x-tt-passport", "登录", "login", "session", "passport", "unauthorized", "forbidden"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}
