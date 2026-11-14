package server

// maxBodyBytes 是所有 JSON 请求体的统一上限。
//
// 此前 chat 用 8MB、anthropic 用 16MB，同一类请求在不同端点行为不一致。
// 需要更大负载（内联图片 / 文件）时统一改这里。
const maxBodyBytes = 16 << 20

// 本文件集中放置跨文件复用的极小工具函数。
//
// 此前同一语义有三个实现（firstNonEmpty / firstNonEmptyStr / firstString），
// 分散在 login.go、autorewards.go、chat.go，读代码时容易误以为行为不同。

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// firstString 返回第一个非空字符串值（入参可能是 any）。
func firstString(vals ...any) string {
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// clip 截断字符串，超长时追加省略号。
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
