// 临时上游探测工具（逆向用，不属于网关功能）。
//
// 用法:
//
//	go run ./cmd/probe -path /samantha/pages/upload_image -body '{"file_type":"png","data":"..."}'
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"doubao2api/internal/doubao"
	"doubao2api/internal/store"
)

func main() {
	data := flag.String("data", "doubao2api-data.json", "状态文件")
	path := flag.String("path", "", "上游路径")
	body := flag.String("body", "", "JSON 请求体，或 @文件")
	method := flag.String("method", "POST", "HTTP 方法")
	acctIdx := flag.Int("acct", 0, "账号索引")
	upload := flag.String("upload", "", "上传本地图片并打印 uri（逆向验证用）")
	flag.Parse()

	st, err := store.Open(*data)
	if err != nil {
		panic(err)
	}
	accts := st.Accounts()
	if len(accts) == 0 {
		panic("无账号")
	}
	if *acctIdx >= len(accts) {
		panic("账号索引越界")
	}
	a := accts[*acctIdx]

	if *upload != "" {
		data, err := os.ReadFile(*upload)
		if err != nil {
			panic(err)
		}
		name := filepath.Base(*upload)
		format := strings.TrimPrefix(filepath.Ext(name), ".")
		c := doubao.NewClient()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		uri, err := c.UploadImage(ctx, a, doubao.ImageAttachment{Name: name, Format: format, Data: data})
		if err != nil {
			fmt.Println("上传失败:", err)
			os.Exit(1)
		}
		fmt.Println("uri =", uri)
		return
	}

	var rd io.Reader
	if *body != "" {
		b := []byte(*body)
		if b[0] == '@' {
			b, err = os.ReadFile(string(b[1:]))
			if err != nil {
				panic(err)
			}
		}
		rd = bytes.NewReader(b)
	}

	u := doubao.UpstreamBase + *path + "?" + doubao.SecurityParams(a).Encode()
	req, err := http.NewRequest(*method, u, rd)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", doubao.UserAgent)
	req.Header.Set("Origin", doubao.UpstreamBase)
	req.Header.Set("Referer", doubao.UpstreamBase+"/chat/")
	if t := a.CSRFToken(); t != "" {
		req.Header.Set("x-tt-passport-csrf-token", t)
	}
	req.Header.Set("Cookie", doubao.CookieHeader(a.Cookies))

	c := &http.Client{Timeout: 90 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	fmt.Printf("HTTP %d  %s\n", resp.StatusCode, resp.Header.Get("Content-Type"))
	fmt.Printf("bytes=%d\n", len(raw))
	var v any
	if json.Unmarshal(raw, &v) == nil {
		out, _ := json.MarshalIndent(v, "", "  ")
		if len(out) > 12000 {
			out = out[:12000]
		}
		os.Stdout.Write(out)
		fmt.Println()
		return
	}
	os.Stdout.Write(raw)
	fmt.Println()
}
