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

func readArg(s string) string {
	if strings.HasPrefix(s, "@") {
		if b, err := os.ReadFile(s[1:]); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return s
}

func mimeByExt(ext string) string {
	switch strings.ToLower(ext) {
	case "pdf":
		return "application/pdf"
	case "txt", "md":
		return "text/plain"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "doc":
		return "application/msword"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case "csv":
		return "text/csv"
	case "json":
		return "application/json"
	}
	return "application/octet-stream"
}

func main() {
	data := flag.String("data", "doubao2api-data.json", "状态文件")
	path := flag.String("path", "", "上游路径")
	body := flag.String("body", "", "JSON 请求体，或 @文件")
	method := flag.String("method", "POST", "HTTP 方法")
	acctIdx := flag.Int("acct", 0, "账号索引")
	upload := flag.String("upload", "", "上传本地文件并打印 uri（逆向验证用）")
	resType := flag.Int("resource-type", doubao.ResourceTypeImage, "上传资源类型：1=File 2=Image")
	chat := flag.String("chat", "", "发送一条对话并打印原始 SSE（逆向验证用）")
	think := flag.Int("think", 0, "思考档位（配合 -chat）")
	skill := flag.Int("skill", 0, "action_bar_skill_id（配合 -chat）：3=图像 9=音乐 17=视频")
	cs := flag.Bool("cs", false, "改用 ChatStream（与网关同一路径）发送 -chat 并打印增量")
	conv := flag.String("conv", "", "conversation_id（配合 -chat，用于多轮）")
	inSkill := flag.String("inputskill", "", "ext.input_skill（技能入参 JSON，配合 -chat）")
	base := flag.String("base", doubao.UpstreamBase, "上游 host（如 https://accounts.doubao.com）")
	nosec := flag.Bool("nosec", false, "不附加豆包公参（passport 等接口用）")
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
		uri, err := c.UploadResource(ctx, a, *resType, name, mimeByExt(format), data)
		if err != nil {
			fmt.Println("上传失败:", err)
			os.Exit(1)
		}
		fmt.Println("uri =", uri)
		return
	}

	var rd io.Reader
	if *cs {
		c := doubao.NewClient()
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		n, ncr := 0, 0
		var txt strings.Builder
		err := c.ChatStream(ctx, a, doubao.ChatRequest{Text: *chat, ThinkLevel: *think, SkillID: *skill}, func(ch doubao.CompletionChunk) error {
			n++
			txt.WriteString(ch.Text)
			if ch.ErrorCode != 0 {
				fmt.Println("ERRCODE:", ch.ErrorCode, ch.ErrorMsg)
			}
			for _, cr := range ch.Creations {
				ncr++
				u := cr.URL
				if len(u) > 90 {
					u = u[:90]
				}
				fmt.Printf("CREATION id=%s type=%d task=%d url=%s\n", cr.ID, cr.Type, cr.TaskType, u)
			}
			return nil
		})
		fmt.Println("chunks:", n, "creations:", ncr, "err:", err)
		fmt.Println("REPLY:", txt.String())
		return
	}

	if *chat != "" {
		chatText := readArg(*chat)
		inSkillVal := readArg(*inSkill)
		payload := doubao.BuildPayload(a, doubao.ChatRequest{Text: chatText, ThinkLevel: *think, SkillID: *skill, ConversationID: *conv, InputSkill: inSkillVal})
		body, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		u := doubao.UpstreamBase + "/chat/completion?" + doubao.SecurityParams(a).Encode()
		hreq, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
		if err != nil {
			panic(err)
		}
		hreq.Header.Set("Content-Type", "application/json")
		hreq.Header.Set("Accept", "text/event-stream")
		hreq.Header.Set("User-Agent", doubao.UserAgent)
		hreq.Header.Set("Origin", doubao.UpstreamBase)
		hreq.Header.Set("Referer", doubao.UpstreamBase+"/chat/")
		if t := a.CSRFToken(); t != "" {
			hreq.Header.Set("x-tt-passport-csrf-token", t)
		}
		hreq.Header.Set("Cookie", doubao.CookieHeader(a.Cookies))
		c := &http.Client{Timeout: 180 * time.Second}
		resp, err := c.Do(hreq)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()
		fmt.Printf("HTTP %d  %s\n", resp.StatusCode, resp.Header.Get("Content-Type"))
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
		os.Stdout.Write(raw)
		fmt.Println()
		return
	}

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

	u := *base + *path
	if !*nosec {
		u += "?" + doubao.SecurityParams(a).Encode()
	}
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
