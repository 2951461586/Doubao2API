package server

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// consoleHTML 是内嵌的控制台单页（单文件、无外部依赖、可离线使用）。
//
//go:embed web/index.html
var consoleHTML []byte

// faviconICO 是控制台图标，取自豆包客户端图标。
//
// 内含 16/24/32/48/64/128/256 七个原生尺寸：只给到 48 时，高分屏标签页与
// 任务栏会拿 48px 拉伸，看起来糊且偏小。量化到 256 色调色板以控制体积
// （约 32 KB，与同类项目同一量级）。
//
//go:embed web/favicon.ico
var faviconICO []byte

// appleTouchIconPNG 是 iOS 主屏图标（180×180 PNG，iOS 不认 ICO）。
//
//go:embed web/apple-touch-icon.png
var appleTouchIconPNG []byte

// assetETags 缓存内嵌资源的内容指纹（资源编译期固定，算一次即可）。
var assetETags sync.Map // name -> string

// assetETag 返回资源的内容 ETag（形如 "abc…"）。
func assetETag(name string, raw []byte) string {
	if v, ok := assetETags.Load(name); ok {
		return v.(string)
	}
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	assetETags.Store(name, etag)
	return etag
}

// etagMatches 判断 If-None-Match 是否命中目标 ETag。
//
// 按 RFC 9110 允许逗号分隔多个值，并容忍弱校验前缀 W/ 与通配符 *。
func etagMatches(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == etag || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}

// serveConsole 输出控制台单页。
//
// 响应头刻意收紧：页面每次都重新校验（no-store），禁止 MIME 嗅探、不发送 Referer，
// 控制台不引用任何外部资源，因此也不需要放宽 CSP。
func serveConsole(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(consoleHTML)
}

// serveFavicon 输出控制台标签页图标。
func serveFavicon(w http.ResponseWriter, r *http.Request) {
	serveIcon(w, r, "favicon.ico", "image/x-icon", faviconICO)
}

// serveAppleTouchIcon 输出 iOS 主屏图标。
func serveAppleTouchIcon(w http.ResponseWriter, r *http.Request) {
	serveIcon(w, r, "apple-touch-icon.png", "image/png", appleTouchIconPNG)
}

// serveIcon 输出内嵌图标资源。
//
// 图标随二进制更新，所以用 ETag 重校验（Cache-Control: no-cache）而不是长 max-age：
// 升级后浏览器会带 If-None-Match 回来，未变化则 304（省流量），变化才重新下载
// （不会出现「升级完还是旧图标」）。
func serveIcon(w http.ResponseWriter, r *http.Request, name, contentType string, raw []byte) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")

	etag := assetETag(name, raw)
	h.Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(raw)
}
