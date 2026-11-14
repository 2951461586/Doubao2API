package server

import (
	_ "embed"
	"net/http"
)

// consoleHTML 是内嵌的控制台单页（单文件、无外部依赖、可离线使用）。
//
//go:embed web/index.html
var consoleHTML []byte

// faviconICO 是控制台图标，取自豆包客户端图标。
//
// 内含 16/32/48/64/128/256 六个原生尺寸：只给到 48 时，高分屏标签页与
// 任务栏会拿 48px 拉伸，看起来糊且偏小。
//
//go:embed web/favicon.ico
var faviconICO []byte

// appleTouchIconPNG 是 iOS 主屏图标（180×180 PNG，iOS 不认 ICO）。
//
//go:embed web/apple-touch-icon.png
var appleTouchIconPNG []byte

// serveConsole 输出控制台单页。
//
// 响应头刻意收紧：禁止缓存、禁止 MIME 嗅探、不发送 Referer，
// 控制台不引用任何外部资源，因此也不需要放宽 CSP。
func serveConsole(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(consoleHTML)
}

// serveAppleTouchIcon 输出 iOS 主屏图标。
func serveAppleTouchIcon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(appleTouchIconPNG)
}

// serveFavicon 输出控制台图标。
//
// 图标是构建时内嵌的字节，可以放心长期缓存（公开、不含敏感信息）；
// 只有浏览器会请求它，因此不参与鉴权。
func serveFavicon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/x-icon")
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(faviconICO)
}
