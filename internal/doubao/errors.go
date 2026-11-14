package doubao

import (
	"errors"
	"strings"
)

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
// 这属于业务/配置类错误 —— 换任何账号都一样，把它记成账号失败会让整池账号变红，
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
	return !IsBusinessError(err)
}
