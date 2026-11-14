package server

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"

	"doubao2api/internal/doubao"
	"doubao2api/internal/ids"
)

// 本文件提供两条互补的排障通道：
//
//  1. 进程日志（stdout/stderr，Docker 自动轮转）——每个请求一行，含状态码、
//     耗时、账号、模型与错误分类；
//  2. 状态文件里的请求日志（上限 500 条）——给控制台看的摘要。
//
// 此前只有第 2 条，容器重建后 docker logs 里只剩启动行（issues.md #13）。

type reqInfoKey struct{}

// reqInfo 是一次请求的日志上下文。
type reqInfo struct {
	id     string
	ip     string
	logged bool // record 已输出过这一行，中间件不再重复
}

// withRequestInfo 注入请求级日志上下文。
func withRequestInfo(r *http.Request, info *reqInfo) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), reqInfoKey{}, info))
}

// requestInfoFrom 取出请求级日志上下文（可能为 nil）。
func requestInfoFrom(r *http.Request) *reqInfo {
	if r == nil {
		return nil
	}
	v, _ := r.Context().Value(reqInfoKey{}).(*reqInfo)
	return v
}

// withAccessLog 为每个请求生成一条访问日志。
//
// 对已经调用 record 的请求（/v1/*）不重复输出；其余端点（控制台、健康检查、
// /v1/models 等）由这里兜底，保证容器日志里能看到请求级线索。
func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &reqInfo{id: ids.Hex(6), ip: s.clientIPString(r)}
		r = withRequestInfo(r, info)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if info.logged {
			return
		}
		// 控制台轮询（state / logs / stats）会淹没日志，默认降为 debug 级：
		// 只在设置开启详细日志时输出。
		if s.Store.Settings().VerboseAccessLog || !isPollingPath(r.URL.Path) {
			log.Printf("access id=%s ip=%s method=%s path=%s status=%d ms=%d bytes=%d",
				info.id, info.ip, r.Method, r.URL.Path, sw.status,
				time.Since(start).Milliseconds(), sw.written)
		}
	})
}

// isPollingPath 判断是否为控制台的周期性轮询端点。
func isPollingPath(p string) bool {
	switch p {
	case "/admin/api/state", "/admin/api/logs", "/admin/api/stats", "/health", "/healthz", "/ping":
		return true
	}
	return false
}

// statusWriter 记录状态码与字节数，并透传 Flush（SSE 必需）。
type statusWriter struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, err
}

// Flush 透传底层 Flusher，避免 SSE 被中间件缓冲。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap 让 http.ResponseController 能找到底层 ResponseWriter。
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// writeRecordLog 输出一条请求级日志。
//
// 错误经过脱敏：上游响应体里可能带 token / cookie 片段，不能原样进日志。
func writeRequestLog(info *reqInfo, r *http.Request, model string, stream bool, kind doubao.Kind,
	start time.Time, key, account string, inLen, outLen int, err error) {
	id, ip := "-", "-"
	if info != nil {
		id, ip = info.id, info.ip
		info.logged = true
	}
	msg := "-"
	if err != nil {
		msg = redact(err.Error())
	}
	log.Printf("request id=%s ip=%s method=%s path=%s model=%s stream=%t status=%d kind=%s ms=%d key=%s acct=%s in=%d out=%d err=%q",
		id, ip, r.Method, r.URL.Path, model, stream, kind.HTTPStatus(), string(kind),
		time.Since(start).Milliseconds(), mask(key), account, inLen, outLen, msg)
}

// redact 抹掉日志里可能出现的凭据片段。
//
// 只做保守替换：命中已知的 cookie / token 名就截断其值，不做正则回溯，
// 保证在热路径上是常数开销。
func redact(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	rest := s
	for {
		name, idx := findSensitiveKey(rest)
		if idx < 0 {
			b.WriteString(rest)
			break
		}
		// 保留 "name=" 之前的内容与键名本身，值用 *** 代替。
		end := idx + len(name) + 1 // 跳过 '='
		b.WriteString(rest[:end])
		b.WriteString("***")
		rest = rest[end:]
		if headerLikeKey(name) {
			// Authorization / Cookie 这类头的值本身可能含空格与逗号
			// （"Bearer sk-..."），必须吃到行尾，否则会把剩余部分留在日志里。
			if i := strings.IndexAny(rest, "\n\r"); i >= 0 {
				rest = rest[i:]
				continue
			}
			rest = ""
			continue
		}
		if i := strings.IndexAny(rest, ";, \t&\n\""); i >= 0 {
			rest = rest[i:]
		} else {
			rest = ""
		}
	}
	out := b.String()
	if len(out) > 512 {
		out = out[:512] + "…"
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || !unicode.IsPrint(r) && r != ' ' {
			return ' '
		}
		return r
	}, out)
}

// sensitiveKeys 是需要脱敏的字段名（小写）。
var sensitiveKeys = []string{
	"sessionid", "sessionid_ss", "sid_guard", "sid_tt", "uid_tt", "uid_tt_ss",
	"odin_tt", "ttwid", "passport_csrf_token", "passport_csrf_token_default",
	"lark_suite_access_token", "lark_suite_dpop", "authorization", "x-api-key",
	"x-admin-password", "token", "ticket", "ms_token", "cookie",
}

// headerLikeKeys 的取值可能包含空格（如 "Bearer sk-..."），需要整行脱敏。
var headerLikeKeys = map[string]bool{
	"authorization": true, "x-api-key": true, "x-admin-password": true,
	"cookie": true, "set-cookie": true,
}

// headerLikeKey 报告该键是否需要整行脱敏。
func headerLikeKey(name string) bool { return headerLikeKeys[name] }

// findSensitiveKey 返回第一个命中的敏感键名及其位置。
func findSensitiveKey(s string) (string, int) {
	lower := strings.ToLower(s)
	bestIdx, bestName := -1, ""
	for _, k := range sensitiveKeys {
		// 只匹配 "key=" 形式，避免把普通单词误伤成脱敏。
		for off := 0; ; {
			i := strings.Index(lower[off:], k+"=")
			if i < 0 {
				break
			}
			pos := off + i
			// 前面必须是分隔符或串首，否则可能是 "mysessionid="。
			if pos == 0 || !isWordByte(s[pos-1]) {
				if bestIdx < 0 || pos < bestIdx {
					bestIdx, bestName = pos, k
				}
				break
			}
			off = pos + len(k)
		}
	}
	return bestName, bestIdx
}

func isWordByte(c byte) bool {
	return c == '_' || c == '-' ||
		(c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z')
}
