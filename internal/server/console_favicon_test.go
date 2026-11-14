package server

import (
	"bytes"
	"net/http"
	"testing"
)

// 图标是浏览器自动请求的固定路径：这里守住「内容真的是图标」+「缓存语义正确」。
//
// 历史问题：图标只打包到 48px，高分屏要拉伸；且曾用长 max-age，升级后仍显示旧图标。
// 现在改成 ETag + no-cache：升级后重新校验，未变则 304。
func TestConsoleIconsServed(t *testing.T) {
	s := newTestServer(t)

	cases := []struct {
		path      string
		contentTy string
		magic     []byte
		minBytes  int
	}{
		{"/favicon.ico", "image/x-icon", []byte{0x00, 0x00, 0x01, 0x00}, 5000},
		{"/apple-touch-icon.png", "image/png", []byte("\x89PNG\r\n\x1a\n"), 2000},
	}

	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			rec := serve(s, newReq(http.MethodGet, c.path, "", loopbackAddr))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d，期望 200", c.path, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != c.contentTy {
				t.Errorf("Content-Type = %q，期望 %q", ct, c.contentTy)
			}
			body := rec.Body.Bytes()
			if !bytes.HasPrefix(body, c.magic) {
				t.Errorf("%s 不是合法的 %s（前 8 字节 %x）", c.path, c.contentTy, body[:min(8, len(body))])
			}
			if len(body) < c.minBytes {
				t.Errorf("图标只有 %d 字节，疑似被误替换成占位/空文件", len(body))
			}
			if xcto := rec.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
				t.Errorf("缺少 nosniff，实际 %q", xcto)
			}
			// 图标随二进制更新：必须可重校验，而不是长 max-age。
			if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
				t.Errorf("Cache-Control = %q，期望 no-cache", cc)
			}
			etag := rec.Header().Get("ETag")
			if etag == "" {
				t.Fatal("缺少 ETag")
			}

			// 条件请求应命中 304（不重复传输）。
			req := newReq(http.MethodGet, c.path, "", loopbackAddr)
			req.Header.Set("If-None-Match", etag)
			if rec2 := serve(s, req); rec2.Code != http.StatusNotModified {
				t.Errorf("带 If-None-Match 应回 304，实际 %d", rec2.Code)
			}

			// 非 GET/HEAD 一律拒绝。
			if rec3 := serve(s, newReq(http.MethodPost, c.path, "", loopbackAddr)); rec3.Code != http.StatusMethodNotAllowed {
				t.Errorf("POST %s = %d，期望 405", c.path, rec3.Code)
			}
		})
	}
}

// TestFaviconHasMultipleSizes 确认 ICO 里带了大尺寸帧（避免高分屏拉伸）。
func TestFaviconHasMultipleSizes(t *testing.T) {
	raw := faviconICO
	if len(raw) < 6 {
		t.Fatal("favicon 太短")
	}
	count := int(raw[4]) | int(raw[5])<<8
	if count < 4 {
		t.Fatalf("ICO 只有 %d 帧，至少要有 16/32/48/64 四档", count)
	}
	largest := 0
	for i := 0; i < count; i++ {
		off := 6 + 16*i
		if off+16 > len(raw) {
			t.Fatalf("ICO 目录越界（第 %d 项）", i)
		}
		w := int(raw[off])
		if w == 0 {
			w = 256 // ICO 用 0 表示 256
		}
		if w > largest {
			largest = w
		}
	}
	if largest < 256 {
		t.Errorf("最大帧只有 %dpx，高分屏会拉伸（期望 256）", largest)
	}
}

// TestETagMatches 覆盖 If-None-Match 的解析（逗号分隔 / 弱校验 / 通配符）。
func TestETagMatches(t *testing.T) {
	etag := `"abc123"`
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{etag, true},
		{`"other"`, false},
		{`"other", ` + etag, true},
		{`W/` + etag, true},
		{"*", true},
		{`  "other" ,  "abc123"  `, true},
	}
	for _, c := range cases {
		if got := etagMatches(c.header, etag); got != c.want {
			t.Errorf("etagMatches(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}
