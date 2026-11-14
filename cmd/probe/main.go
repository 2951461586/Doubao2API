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
	"sort"
	"strings"
	"time"

	"doubao2api/internal/agent"
	"doubao2api/internal/doubao"
	"doubao2api/internal/registry"
	"doubao2api/internal/store"
	"doubao2api/internal/workturn"
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

// runClientModels 读取桌面端缓存里的模型清单并与 registry 比对。
//
// 返回 0 表示一致（或只有「客户端未列出」的本地模型），1 表示存在差异。
//
// 注意：同一模型在不同 profile 的缓存里可能给出不同上下文（服务端按实验下发），
// 因此按 model_item_key 聚合后比对：名称必须完全一致，上下文命中任一
// 客户端取值即可（registry 只能存一个值）。
func runClientModels(dir, only string) int {
	lists, err := doubao.ReadAllClientModels(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取客户端模型清单失败:", err)
		return 1
	}

	regByKey := make(map[string]registry.Model)
	for _, m := range registry.Builtin {
		if m.ModelItemKey != "" {
			regByKey[m.ModelItemKey] = m
		}
	}

	type agg struct {
		name    string
		windows map[int]bool
	}
	union := make(map[string]*agg)
	var order []string

	for _, l := range lists {
		if only != "" && l.Profile != only {
			continue
		}
		captured := "未知"
		if !l.CapturedAt.IsZero() {
			captured = l.CapturedAt.Format("2006-01-02 15:04:05")
		}
		fmt.Println()
		fmt.Printf("== %s  缓存时间 %s  默认模型 %s", l.Profile, captured, l.DefaultModelKey)
		fmt.Println()
		fmt.Printf("   %-28s %-42s %-7s %s", "客户端名称", "model_item_key", "上下文", "状态")
		fmt.Println()
		for _, m := range l.Models {
			a := union[m.ModelItemKey]
			if a == nil {
				a = &agg{name: m.Name, windows: map[int]bool{}}
				union[m.ModelItemKey] = a
				order = append(order, m.ModelItemKey)
			}
			if m.TotalWindow > 0 {
				a.windows[m.TotalWindow] = true
			}
			status := "OK"
			if _, ok := regByKey[m.ModelItemKey]; !ok {
				status = "registry 缺失"
			} else if regByKey[m.ModelItemKey].Name != m.Name {
				status = "名称不一致"
			}
			fmt.Printf("   %-28s %-42s %-7d %s", m.Name, m.ModelItemKey, m.TotalWindow, status)
			fmt.Println()
		}
	}

	drift := 0
	for _, key := range order {
		a := union[key]
		reg, ok := regByKey[key]
		if !ok {
			drift++
			fmt.Printf("✗ %s：registry 缺少该 model_item_key", key)
			fmt.Println()
			continue
		}
		if reg.Name != a.name {
			drift++
			fmt.Printf("✗ %s：名称 registry=%q 客户端=%q", key, reg.Name, a.name)
			fmt.Println()
		}
		if len(a.windows) > 0 && !a.windows[reg.Context] {
			drift++
			fmt.Printf("✗ %s：上下文 registry=%d 客户端=%v", key, reg.Context, sortedInts(a.windows))
			fmt.Println()
		}
	}

	// registry 里带 model_item_key、但客户端清单没有的模型：可能已下线。
	var stale []string
	for key, m := range regByKey {
		if _, ok := union[key]; !ok {
			stale = append(stale, fmt.Sprintf("%s(%s)", m.ID, key))
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		fmt.Println()
		fmt.Println("客户端未列出（可能已下线）：" + strings.Join(stale, ", "))
	}
	if drift > 0 {
		fmt.Println()
		fmt.Printf("存在 %d 处差异：更新 internal/registry/registry.go 后重跑本命令。", drift)
		fmt.Println()
		return 1
	}
	fmt.Println()
	fmt.Println("模型清单已对齐。")
	return 0
}

// sortedInts 返回升序的整数切片（用于稳定输出）。
func sortedInts(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
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
	lazy := flag.Bool("lazy", false, "option.support_lazy_fetch_stream=true（A/B 用）")
	dump := flag.Bool("dump", false, "只打印将要发送的请求体（不请求上游），便于对照协议")
	turnWS := flag.String("workturn", "", "工作模式全链路：传工作区目录，跑「流+轮询+工具执行」一次回合")
	mkey := flag.String("mkey", "person-deepseek-v4-1-flash-261017", "工作模式模型 model_item_key")
	rawMsg := flag.Bool("raw", false, "打印工具通道的原始消息（校准报文形状用）")
	shell := flag.Bool("shell", false, "允许工具执行命令（默认关闭，仅白名单）")
	allow := flag.String("allow", "go,git", "命令白名单（逗号分隔，配合 -shell）")
	nosec := flag.Bool("nosec", false, "不附加豆包公参（passport 等接口用）")
	maxOut := flag.Int("max", 200000, "JSON 打印上限（字节，0 = 不截断）")
	listModels := flag.Bool("models", false, "打印模型清单（ID / 名称 / 上游 model_item_key / 模式）后退出")
	clientModels := flag.Bool("client-models", false, "读取本机客户端缓存里的模型清单，并与 registry 比对后退出")
	clientDir := flag.String("client-dir", "", "DoubaoWork User Data 目录（留空自动探测）")
	clientProfile := flag.String("client-profile", "", "只比对指定 profile（留空比对全部）")
	flag.Parse()

	if *listModels {
		fmt.Printf("%-22s %-24s %-40s %s\n", "ID", "名称", "上游 model_item_key", "模式")
		for _, m := range registry.List(nil) {
			mode := "普通对话"
			if m.AgentMode == 1 {
				mode = "工作模式"
			}
			fmt.Printf("%-22s %-24s %-40s %s\n", m.ID, m.Name, m.ModelItemKey, mode)
		}
		return
	}

	if *clientModels {
		os.Exit(runClientModels(*clientDir, *clientProfile))
	}

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

	if *turnWS != "" {
		box, err := agent.New(*turnWS, "")
		if err != nil {
			fmt.Println("沙箱初始化失败:", err)
			os.Exit(1)
		}
		box.AllowShell = *shell
		if *shell {
			box.ShellAllow = strings.Split(*allow, ",")
		}
		if *dump {
			req := doubao.ChatRequest{Text: *chat, ThinkLevel: *think, AgentMode: 1,
				ModelItemKey: *mkey, ModeID: "3", ReasoningEffort: "4"}
			payload := doubao.BuildPayload(a, req)
			out, _ := json.MarshalIndent(payload, "", "  ")
			os.Stdout.Write(out)
			fmt.Println()
			return
		}

		c := doubao.NewClient()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		tools := 0
		res, err := workturn.Run(ctx, c, c, a, box, workturn.Options{
			Text:            *chat,
			ThinkLevel:      *think,
			ModelItemKey:    *mkey,
			ModeID:          "3",
			ReasoningEffort: "4",
			PollInterval:    1200 * time.Millisecond,
			PollWindow:      8 * time.Second,
			OnTool: func(ev workturn.ToolEvent) {
				tools++
				status := "ok"
				if ev.Err != nil {
					status = "err: " + ev.Err.Error()
				}
				inputJSON, _ := json.Marshal(ev.Input)
				fmt.Printf("TOOL %-14s %s input=%s\n", ev.Name, status, string(inputJSON))
			},
			OnPoll: func(expired bool, count int, err error) {
				if *rawMsg {
					if err != nil {
						fmt.Println("POLL err:", err)
					} else {
						fmt.Printf("POLL expired=%v msgs=%d\n", expired, count)
					}
				}
			},
			OnRawMsg: func(m map[string]any) {
				if !*rawMsg {
					return
				}
				raw, _ := json.MarshalIndent(m, "", "  ")
				fmt.Println("RAWMSG", string(raw))
			},
		})
		fmt.Printf("\nagent_id=%s conv=%s sandbox=%s tools=%d\n", res.AgentID, res.ConversationID, res.SandboxID, tools)
		fmt.Printf("REPLY: %s\n", res.Text)
		if err != nil {
			fmt.Println("err:", err)
		}
		if res.AgentID == "" {
			fmt.Println("提示: 未拿到 agent_id（可能该模型不支持工作模式或本轮没有 agent 壳）")
		}
		return
	}

	var rd io.Reader
	if *cs {
		c := doubao.NewClient()
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		n, ncr := 0, 0
		var txt strings.Builder
		req := doubao.ChatRequest{Text: *chat, ThinkLevel: *think, SkillID: *skill, SupportLazyFetchStream: *lazy}
		if *dump {
			payload := doubao.BuildPayload(a, req)
			out, _ := json.MarshalIndent(payload, "", "  ")
			os.Stdout.Write(out)
			fmt.Println()
			return
		}
		err := c.ChatStream(ctx, a, req, func(ch doubao.CompletionChunk) error {
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
		hreq.Header.Set("User-Agent", a.UAValue())
		hreq.Header.Set("Origin", doubao.UpstreamBase)
		hreq.Header.Set("Referer", doubao.UpstreamBase+"/chat/")
		hreq.Header.Set("Agw-Js-Conv", "str")
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
	req.Header.Set("User-Agent", a.UAValue())
	req.Header.Set("Origin", doubao.UpstreamBase)
	req.Header.Set("Referer", doubao.UpstreamBase+"/chat/")
	req.Header.Set("Agw-Js-Conv", "str")
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
		// 截断时只输出完整行，并把提示写到 stderr：
		// 早先直接按字节截断会把 "\uXXXX" 转义切开，输出变成非法 JSON，
		// 管道里的解析器会误判成「上游返回格式异常」。
		if *maxOut > 0 && len(out) > *maxOut {
			cut := *maxOut
			if i := bytes.LastIndexByte(out[:cut], '\n'); i > 0 {
				cut = i
			}
			os.Stdout.Write(out[:cut])
			fmt.Printf("\n…已截断（%d/%d 字节）：用 -max 0 打印完整响应\n", cut, len(out))
			return
		}
		os.Stdout.Write(out)
		fmt.Println()
		return
	}
	os.Stdout.Write(raw)
	fmt.Println()
}
