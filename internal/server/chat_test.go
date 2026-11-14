package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"doubao2api/internal/doubao"
	"doubao2api/internal/registry"
)

// 本文件用一个「假上游」跑通 /v1/chat/completions 的完整编排：
// 请求归一化 → 选号/CSRF → 上游 SSE → 增量收集 → OpenAI 响应。
//
// 假上游通过替换 s.Up.HTTP.Transport 实现（Client 的 BaseURL 是常量，
// 但传输层可注入），因此不需要真实网络，也能断言网关真正发出去的报文。

// --- 假上游 ---

type fakeDoubao struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	chatCalls     int
	csrfCalls     int
	lastChatBody  map[string]any
	lastChatQuery url.Values
	lastCookie    string
	// handler 决定第 n 次（从 1 开始）/chat/completion 调用的响应；nil 时返回成功流。
	handler func(w http.ResponseWriter, n int)
}

func newFakeDoubao(t *testing.T) *fakeDoubao {
	t.Helper()
	f := &fakeDoubao{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/passport/safe/csrf_token/", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.csrfCalls++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":"success","data":{"passport_csrf_token":"csrf-token"}}`)
	})
	mux.HandleFunc("/chat/completion", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)

		f.mu.Lock()
		f.chatCalls++
		n := f.chatCalls
		f.lastChatBody = body
		f.lastChatQuery = r.URL.Query()
		f.lastCookie = r.Header.Get("Cookie")
		h := f.handler
		f.mu.Unlock()

		if h != nil {
			h(w, n)
			return
		}
		writeSSE(w, successStream)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// install 把网关的上游请求劫持到假上游。
func (f *fakeDoubao) install(s *Server) {
	target, err := url.Parse(f.srv.URL)
	if err != nil {
		f.t.Fatalf("解析假上游地址失败: %v", err)
	}
	s.Up.HTTP = &http.Client{Transport: rewriteTransport{target: target, base: http.DefaultTransport}}
}

func (f *fakeDoubao) calls() (chat, csrf int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chatCalls, f.csrfCalls
}

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = t.target.Scheme
	req.URL.Host = t.target.Host
	return t.base.RoundTrip(req)
}

// --- SSE 构造 ---

func sseEvent(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

func writeSSE(w http.ResponseWriter, stream string) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	_, _ = io.WriteString(w, stream)
}

// successStream 覆盖网关需要聚合的全部增量类型：
// 会话 ID、思维链、正文、来源、产物、图片、音乐。
var successStream = strings.Join([]string{
	sseEvent("SSE_ACK", `{"ack_client_meta":{"conversation_id":"38447714412500738"}}`),
	sseEvent("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content_block":[{"block_type":10040,"content":{}}]}}]}`),
	sseEvent("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"先想"}}}]}}]}`),
	sseEvent("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content_block":[{"block_type":10040,"content":{}}]}}]}`),
	sseEvent("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"你好"}}}]}}]}`),
	sseEvent("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content_block":[{"block_type":10025,"content":{"search_query_result_block":{"summary":"找到 1 篇","results":[{"text_card":{"title":"官网","url":"https://a/1","snippet":"简介","site_name":"站点"}}]}}}]}}]}`),
	sseEvent("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content_block":[{"block_type":10030,"content":{"artifact_block":{"title":"报告","resource_id":"r1","resource_type":2}}}]}}]}`),
	sseEvent("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content_block":[{"block_type":2074,"content":{"creation_block":{"creations":[{"id":"c1","image":{"image_ori":{"url":"https://cdn/1.png","width":512,"height":512}}}]}}}]}}]}`),
	sseEvent("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content_block":[{"block_type":10075,"content":{"lyrics_to_songs_music_block":{"task_nums":1,"lyrics_to_songs_music_content":{"video_id":"v1","title":"夏天的风"}}}}]}}]}`),
}, "")

// --- SSE 解析（测试侧） ---

type sseFrame struct {
	comments []string
	data     string
}

func splitFrames(t *testing.T, body string) []sseFrame {
	t.Helper()
	var frames []sseFrame
	for _, block := range strings.Split(body, "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		fr := sseFrame{}
		var data []string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, ":"):
				fr.comments = append(fr.comments, strings.TrimSpace(strings.TrimPrefix(line, ":")))
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		fr.data = strings.Join(data, "\n")
		frames = append(frames, fr)
	}
	return frames
}

func frameJSON(t *testing.T, fr sseFrame) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(fr.data), &m); err != nil {
		t.Fatalf("SSE 帧不是 JSON: %v\n%s", err, fr.data)
	}
	return m
}

func deltaOf(t *testing.T, fr sseFrame) map[string]any {
	t.Helper()
	m := frameJSON(t, fr)
	choices, _ := m["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("帧缺少 choices: %s", fr.data)
	}
	choice, _ := choices[0].(map[string]any)
	d, _ := choice["delta"].(map[string]any)
	return d
}

func optionOf(t *testing.T, f *fakeDoubao) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastChatBody == nil {
		t.Fatal("假上游未收到请求体")
	}
	opt, _ := f.lastChatBody["option"].(map[string]any)
	if opt == nil {
		t.Fatalf("请求体缺少 option: %+v", f.lastChatBody)
	}
	return opt
}

// lastText 取请求体里 text block 的文本（blocks 中最后一个文本块）。
func (f *fakeDoubao) lastText(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs, _ := f.lastChatBody["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("请求体缺少 messages")
	}
	msg, _ := msgs[0].(map[string]any)
	blocks, _ := msg["content_block"].([]any)
	for i := len(blocks) - 1; i >= 0; i-- {
		b, _ := blocks[i].(map[string]any)
		content, _ := b["content"].(map[string]any)
		tb, _ := content["text_block"].(map[string]any)
		if s, ok := tb["text"].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func chatBody(t *testing.T, stream bool, model string, content string) string {
	t.Helper()
	req := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": "be nice"},
			map[string]any{"role": "user", "content": content},
		},
		"stream": stream,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// --- 阻塞式编排 ---

func TestChatBlockingAggregates(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	fake := newFakeDoubao(t)
	fake.install(s)

	rec := serve(s, newReq("POST", "/v1/chat/completions", chatBody(t, false, "doubao-think", "hi"), loopbackAddr))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	resp := decode(t, rec)

	choices, _ := resp["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %+v", resp["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	if msg["content"] != "你好" {
		t.Errorf("content = %v, want 你好", msg["content"])
	}
	if msg["reasoning_content"] != "先想" {
		t.Errorf("reasoning_content = %v, want 先想", msg["reasoning_content"])
	}
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v", choice["finish_reason"])
	}
	if resp["conversation_id"] != "38447714412500738" {
		t.Errorf("conversation_id = %v", resp["conversation_id"])
	}

	sources, _ := resp["sources"].([]any)
	if len(sources) != 1 || sources[0].(map[string]any)["url"] != "https://a/1" {
		t.Errorf("sources = %+v", resp["sources"])
	}
	artifacts, _ := resp["artifacts"].([]any)
	if len(artifacts) != 1 || artifacts[0].(map[string]any)["resource_id"] != "r1" {
		t.Errorf("artifacts = %+v", resp["artifacts"])
	}
	images, _ := resp["images"].([]any)
	if len(images) != 1 || images[0] != "https://cdn/1.png" {
		t.Errorf("images = %+v", resp["images"])
	}
	music, _ := resp["music"].([]any)
	if len(music) != 1 || music[0].(map[string]any)["title"] != "夏天的风" {
		t.Errorf("music = %+v", resp["music"])
	}

	usage, _ := resp["usage"].(map[string]any)
	total, _ := usage["total_tokens"].(float64)
	if total <= 0 {
		t.Errorf("usage.total_tokens = %v, 应大于 0", usage["total_tokens"])
	}

	// 上游报文：历史压平 + 思考档位 + 公参与 Cookie。
	// 只有一条 user 消息时保持原文（不加 "user: " 前缀），system 单独前置。
	if got, want := fake.lastText(t), "be nice\n\nhi"; got != want {
		t.Errorf("上游 text = %q, want %q", got, want)
	}
	opt := optionOf(t, fake)
	if opt["need_deep_think"] != float64(doubao.ThinkOn) {
		t.Errorf("need_deep_think = %v, want %d", opt["need_deep_think"], doubao.ThinkOn)
	}
	fake.mu.Lock()
	query, cookie := fake.lastChatQuery, fake.lastCookie
	fake.mu.Unlock()
	if query.Get("fp") != "verify_dev-a1" || query.Get("device_id") != "dev-a1" {
		t.Errorf("公参未按账号下发: %v", query)
	}
	if !strings.Contains(cookie, "sessionid=s1") {
		t.Errorf("Cookie = %q", cookie)
	}
	if _, csrf := fake.calls(); csrf == 0 {
		t.Error("账号未预置 CSRF token 时应先刷新一次")
	}
}

// 阻塞式路径的日志与统计必须落到实际使用的账号上。
func TestChatBlockingRecordsLog(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	newFakeDoubao(t).install(s)

	if rec := serve(s, newReq("POST", "/v1/chat/completions", chatBody(t, false, "doubao", "hi"), loopbackAddr)); rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
	}
	logs := s.Store.Logs(0)
	if len(logs) != 1 {
		t.Fatalf("日志条数 = %d, want 1: %+v", len(logs), logs)
	}
	e := logs[0]
	if e.Status != http.StatusOK || e.Stream || e.Model != "doubao" || e.Account != "a1" {
		t.Errorf("日志内容不符: %+v", e)
	}
	if e.OutputLen <= 0 || e.PromptLen <= 0 {
		t.Errorf("日志长度未记录: %+v", e)
	}
	if st := s.Store.Stats(); st.TotalRequests != 1 || st.FailedRequests != 0 {
		t.Errorf("统计 = %+v", st)
	}
}

// --- 流式编排 ---

func TestChatStreamFrames(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	newFakeDoubao(t).install(s)

	rec := serve(s, newReq("POST", "/v1/chat/completions", chatBody(t, true, "doubao-think", "hi"), loopbackAddr))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}

	frames := splitFrames(t, rec.Body.String())
	if len(frames) < 4 {
		t.Fatalf("SSE 帧过少: %d", len(frames))
	}

	// 首帧必须是 role，末两帧必须是 finish_reason=stop 与 [DONE]。
	if role, _ := deltaOf(t, frames[0])["role"].(string); role != "assistant" {
		t.Errorf("首帧 role = %q, want assistant", role)
	}
	last := frames[len(frames)-1]
	if last.data != "[DONE]" {
		t.Errorf("末帧 = %q, want [DONE]", last.data)
	}
	finishFrame := frameJSON(t, frames[len(frames)-2])
	fchoices, _ := finishFrame["choices"].([]any)
	fc, _ := fchoices[0].(map[string]any)
	if fc["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v, want stop", fc["finish_reason"])
	}

	var (
		gotReasoning, gotContent string
		gotImages, gotSources    int
		gotArtifacts, gotMusic   int
		convComment              string
	)
	for _, fr := range frames {
		for _, c := range fr.comments {
			if strings.HasPrefix(c, "conversation_id=") {
				convComment = strings.TrimPrefix(c, "conversation_id=")
			}
		}
		if fr.data == "" || fr.data == "[DONE]" {
			continue
		}
		d := deltaOf(t, fr)
		if v, ok := d["reasoning_content"].(string); ok {
			gotReasoning += v
		}
		if v, ok := d["content"].(string); ok {
			gotContent += v
		}
		if imgs, ok := d["images"].([]any); ok {
			gotImages += len(imgs)
		}
		if srcs, ok := d["sources"].([]any); ok {
			gotSources += len(srcs)
		}
		if arts, ok := d["artifacts"].([]any); ok {
			gotArtifacts += len(arts)
		}
		if _, ok := d["music"].(map[string]any); ok {
			gotMusic++
		}
	}

	if gotReasoning != "先想" || gotContent != "你好" {
		t.Errorf("流式文本 = reasoning:%q content:%q", gotReasoning, gotContent)
	}
	if gotImages != 1 || gotSources != 1 || gotArtifacts != 1 || gotMusic != 1 {
		t.Errorf("结构化产物帧计数 = images:%d sources:%d artifacts:%d music:%d", gotImages, gotSources, gotArtifacts, gotMusic)
	}
	if convComment != "38447714412500738" {
		t.Errorf("会话 ID 注释 = %q", convComment)
	}

	logs := s.Store.Logs(0)
	if len(logs) != 1 || !logs[0].Stream || logs[0].Status != http.StatusOK {
		t.Errorf("流式日志不符: %+v", logs)
	}
}

// --- 上游错误 ---

func TestChatBlockingUpstreamBusinessError(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	addTestAccount(t, s, "a2", "s2")
	fake := newFakeDoubao(t)
	fake.handler = func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":2,"msg":"campaign not found"}`)
	}
	fake.install(s)

	rec := serve(s, newReq("POST", "/v1/chat/completions", chatBody(t, false, "doubao", "hi"), loopbackAddr))
	// 业务 / 配置类错误不是网关故障：返回 422 与 business_error，
	// 调用方据此判断「重试无意义」，而不是把 502 当成上游抖动。
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("状态码 = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "campaign not found") {
		t.Errorf("错误体 = %s", body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "business_error") {
		t.Errorf("错误类型应为 business_error: %s", body)
	}
	if chat, _ := fake.calls(); chat != 1 {
		t.Errorf("业务错误不应换号重试，上游调用次数 = %d", chat)
	}
	for _, a := range s.Store.Accounts() {
		if a.Status != "ok" || a.Failures != 0 {
			t.Errorf("业务错误不该标红账号: %s status=%s failures=%d", a.ID, a.Status, a.Failures)
		}
	}
	// 日志要能区分业务错误与真实故障。
	if logs := s.Store.Logs(0); len(logs) != 1 || logs[0].Kind != "business" {
		t.Errorf("日志分类不符: %+v", logs)
	}
	stats := s.Store.Stats()
	if stats.FailedRequests != 0 || stats.BusinessErrors != 1 {
		t.Errorf("业务错误不应计入 FailedRequests: %+v", stats)
	}
}

// 上游在提交响应头之前就返回业务错误：应返回普通 JSON（真实状态码），
// 而不是「200 + 一个多数 SDK 不解析的 error 帧」。
func TestChatStreamPreflightErrorReturnsJSON(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	fake := newFakeDoubao(t)
	fake.handler = func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":2,"msg":"campaign not found"}`)
	}
	fake.install(s)

	rec := serve(s, newReq("POST", "/v1/chat/completions", chatBody(t, true, "doubao", "hi"), loopbackAddr))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("提交前错误应返回 422，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		t.Errorf("未提交时不应回退成 SSE: %s", ct)
	}
}

// 上游已经产出正文之后才报错：响应头已提交，只能下发结构化 error 帧
// （finish_reason=error + 顶层 error 字段），不能把错误文案当成正文，
// 否则 OpenAI SDK 会当正常回复收下。
func TestChatStreamUpstreamErrorIsStructured(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	fake := newFakeDoubao(t)
	fake.handler = func(w http.ResponseWriter, _ int) {
		writeSSE(w, sseEvent("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"半句"}}}]}}]}`))
	}
	fake.install(s)

	rec := serve(s, newReq("POST", "/v1/chat/completions", chatBody(t, true, "doubao", "hi"), loopbackAddr))
	if rec.Code != http.StatusOK {
		t.Fatalf("流式已提交响应头后应仍为 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "[错误]") {
		t.Errorf("错误不应再当正文下发: %s", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]"+"\n\n") {
		t.Errorf("流式错误后仍应以 [DONE] 收尾: %s", body)
	}
	if !strings.Contains(body, `"content":"半句"`) {
		t.Errorf("已产出的正文应保留: %s", body)
	}
}

// 401 登录失效属于账号级错误：应换号重试并只标记坏账号。
func TestChatRetriesAnotherAccountOnAuthError(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	addTestAccount(t, s, "a2", "s2")
	fake := newFakeDoubao(t)
	fake.handler = func(w http.ResponseWriter, n int) {
		// 前两次属于 a1（ChatStream 内部会因鉴权错误刷新 CSRF 再试一次）。
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":1,"msg":"登录已失效，请重新登录"}`)
			return
		}
		writeSSE(w, successStream)
	}
	fake.install(s)

	rec := serve(s, newReq("POST", "/v1/chat/completions", chatBody(t, false, "doubao", "hi"), loopbackAddr))
	if rec.Code != http.StatusOK {
		t.Fatalf("换号后应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if chat, _ := fake.calls(); chat < 3 {
		t.Errorf("上游调用次数 = %d，应至少 3 次（a1 重试 + a2）", chat)
	}

	bad := s.Store.Account("a1")
	if bad == nil || bad.Status != "error" || bad.Failures == 0 {
		t.Errorf("a1 应被标记失败: %+v", bad)
	}
	good := s.Store.Account("a2")
	if good == nil || good.Status != "ok" || good.Failures != 0 {
		t.Errorf("a2 应保持健康: %+v", good)
	}
}

// --- 请求校验 ---

func TestChatRequestValidation(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	newFakeDoubao(t).install(s)

	cases := []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{"GET 不允许", "GET", "", http.StatusMethodNotAllowed},
		{"非法 JSON", "POST", `{`, http.StatusBadRequest},
		{"空 messages", "POST", `{"model":"doubao","messages":[]}`, http.StatusBadRequest},
		{"消息内容为空", "POST", `{"model":"doubao","messages":[{"role":"user","content":"  "}]}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(s, newReq(tc.method, "/v1/chat/completions", tc.body, loopbackAddr))
			if rec.Code != tc.want {
				t.Fatalf("状态码 = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// --- 请求归一化 ---

func TestChatPayloadMapping(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		check func(t *testing.T, f *fakeDoubao, opt map[string]any)
	}{
		{
			name: "模型自带技能会包裹提示词",
			// 不带 system 消息：技能模板包裹的是整段压平后的文本，
			// 混入 system 会把模板套在系统提示上（见扫描报告）。
			body: `{"model":"doubao-draw","messages":[{"role":"user","content":"一只猫"}]}`,
			check: func(t *testing.T, f *fakeDoubao, opt map[string]any) {
				if opt["action_bar_skill_id"] != float64(doubao.SkillImageGen) {
					t.Errorf("action_bar_skill_id = %v", opt["action_bar_skill_id"])
				}
				if got := f.lastText(t); got != "生成一张图片: 一只猫" {
					t.Errorf("技能提示词 = %q", got)
				}
			},
		},
		{
			name: "reasoning_effort 覆盖思考档位",
			body: `{"model":"doubao","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
			check: func(t *testing.T, _ *fakeDoubao, opt map[string]any) {
				if opt["need_deep_think"] != float64(doubao.ThinkExpert) {
					t.Errorf("need_deep_think = %v, want %d", opt["need_deep_think"], doubao.ThinkExpert)
				}
			},
		},
		{
			name: "工作模式模型下发 model_item_key 与 agent_mode",
			body: chatBody(t, false, "deepseek-v4-flash", "hi"),
			check: func(t *testing.T, f *fakeDoubao, opt map[string]any) {
				if opt["agent_mode"] != float64(1) || opt["conversation_mode"] == nil {
					t.Errorf("工作模式字段缺失: %v", opt)
				}
				initExt, _ := opt["conversation_init_ext"].(map[string]any)
				if initExt["model_item_key"] != "person-deepseek-v4-1-flash-261017" {
					t.Errorf("conversation_init_ext = %+v", initExt)
				}
				f.mu.Lock()
				ext, _ := f.lastChatBody["ext"].(map[string]any)
				f.mu.Unlock()
				if ext["use_deep_think"] == nil {
					t.Errorf("ext.use_deep_think 缺失: %+v", ext)
				}
			},
		},
		{
			name: "doubao 命名空间参数合并",
			body: `{"model":"doubao","messages":[{"role":"user","content":"hi"}],"doubao":{"mode_id":"m-1","selection_mode":true,"selected_skills":["image"]}}`,
			check: func(t *testing.T, f *fakeDoubao, opt map[string]any) {
				initExt, _ := opt["conversation_init_ext"].(map[string]any)
				if initExt["mode_id"] != "m-1" || initExt["selection_mode"] != true {
					t.Errorf("conversation_init_ext = %+v", initExt)
				}
				f.mu.Lock()
				ext, _ := f.lastChatBody["ext"].(map[string]any)
				f.mu.Unlock()
				skills, _ := ext["mention_skill_list"].([]any)
				if len(skills) != 1 || skills[0] != "image" {
					t.Errorf("mention_skill_list = %+v", ext["mention_skill_list"])
				}
			},
		},
		{
			name: "显式 skill 名称 + input_skill 透传",
			body: `{"model":"doubao","messages":[{"role":"user","content":"hi"}],"skill":"translate","input_skill":"{\"a\":1}"}`,
			check: func(t *testing.T, f *fakeDoubao, opt map[string]any) {
				if opt["action_bar_skill_id"] != float64(doubao.SkillTranslate) {
					t.Errorf("action_bar_skill_id = %v", opt["action_bar_skill_id"])
				}
				f.mu.Lock()
				ext, _ := f.lastChatBody["ext"].(map[string]any)
				f.mu.Unlock()
				if ext["input_skill"] != `{"a":1}` {
					t.Errorf("ext.input_skill = %v", ext["input_skill"])
				}
			},
		},
		{
			name: "请求侧 model_item_key 在模型未自带时生效",
			body: `{"model":"doubao","messages":[{"role":"user","content":"hi"}],"model_item_key":"custom-key"}`,
			check: func(t *testing.T, _ *fakeDoubao, opt map[string]any) {
				initExt, _ := opt["conversation_init_ext"].(map[string]any)
				if initExt["model_item_key"] != "custom-key" {
					t.Errorf("conversation_init_ext = %+v", initExt)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			addTestAccount(t, s, "a1", "s1")
			fake := newFakeDoubao(t)
			fake.install(s)

			if rec := serve(s, newReq("POST", "/v1/chat/completions", tc.body, loopbackAddr)); rec.Code != http.StatusOK {
				t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
			}
			tc.check(t, fake, optionOf(t, fake))
		})
	}
}

// 请求里的 agent_mode 目前被合并进 extras 但从未下发：
// 只有 registry 模型自带的 AgentMode 会生效。此用例锁定当前行为，
// 待确认「是否允许客户端强制工作模式」后再改（见扫描报告发现 1）。
// 工作模式优先级：模型自带的 AgentMode 优先，请求字段只对普通模型生效。
func TestChatAgentModeResolution(t *testing.T) {
	t.Run("普通模型可由请求开启", func(t *testing.T) {
		s := newTestServer(t)
		addTestAccount(t, s, "a1", "s1")
		fake := newFakeDoubao(t)
		fake.install(s)

		body := `{"model":"doubao","messages":[{"role":"user","content":"hi"}],"agent_mode":2}`
		if rec := serve(s, newReq("POST", "/v1/chat/completions", body, loopbackAddr)); rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
		}
		opt := optionOf(t, fake)
		if opt["agent_mode"] != float64(2) || opt["conversation_mode"] == nil {
			t.Errorf("请求 agent_mode 未生效: %+v", opt)
		}
	})

	t.Run("doubao 命名空间同样生效", func(t *testing.T) {
		s := newTestServer(t)
		addTestAccount(t, s, "a1", "s1")
		fake := newFakeDoubao(t)
		fake.install(s)

		body := `{"model":"doubao","messages":[{"role":"user","content":"hi"}],"doubao":{"agent_mode":1}}`
		if rec := serve(s, newReq("POST", "/v1/chat/completions", body, loopbackAddr)); rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
		}
		if opt := optionOf(t, fake); opt["agent_mode"] != float64(1) {
			t.Errorf("doubao.agent_mode 未生效: %+v", opt)
		}
	})

	// 工作模式模型必须保持 agent_mode=1，不能被请求里的 2 覆盖。
	t.Run("模型自带值优先", func(t *testing.T) {
		s := newTestServer(t)
		addTestAccount(t, s, "a1", "s1")
		fake := newFakeDoubao(t)
		fake.install(s)

		body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"agent_mode":2}`
		if rec := serve(s, newReq("POST", "/v1/chat/completions", body, loopbackAddr)); rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
		}
		if opt := optionOf(t, fake); opt["agent_mode"] != float64(1) {
			t.Errorf("工作模式模型的 agent_mode 被请求覆盖: %+v", opt)
		}
	})
}

// --- 附件 ---

// 已有 uri 的附件直接透传；没有字节也没有 uri 的附件被跳过。
// 这两种情况都不应触发任何上传请求。
func TestWithUploadedAttachmentsSkipsReadyAndEmpty(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	fake := newFakeDoubao(t)
	fake.install(s)

	acct := s.Store.Account("a1")
	req := doubao.ChatRequest{
		Images: []doubao.ImageAttachment{
			{Name: "a.png", URI: "uri://ready"},
			{Name: "b.png"}, // 无 Data / CDNURL / URI
		},
		Files: []doubao.FileAttachment{
			{Name: "a.pdf", URI: "uri://file"},
			{Name: "b.pdf"}, // 无 Data
		},
	}
	got, err := s.withUploadedAttachments(t.Context(), acct, req)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got.Images[0].URI != "uri://ready" || got.Files[0].URI != "uri://file" {
		t.Errorf("已就绪附件被改动: %+v %+v", got.Images, got.Files)
	}
	if chat, csrf := fake.calls(); chat != 0 || csrf != 0 {
		t.Errorf("不应访问上游: chat=%d csrf=%d", chat, csrf)
	}
}

func TestFileAttachmentFromPart(t *testing.T) {
	pdf := "data:application/pdf;base64," + "JVBERi0xLjQ="

	cases := []struct {
		name     string
		part     map[string]any
		wantOK   bool
		wantName string
	}{
		{
			name:     "file 子对象 + data URL",
			part:     map[string]any{"type": "file", "file": map[string]any{"filename": "a.pdf", "file_data": pdf}},
			wantOK:   true,
			wantName: "a.pdf",
		},
		{
			name:     "input_file 平铺写法",
			part:     map[string]any{"type": "input_file", "filename": "b.pdf", "file_data": pdf},
			wantOK:   true,
			wantName: "b.pdf",
		},
		{
			name:     "裸 base64",
			part:     map[string]any{"type": "file", "file": map[string]any{"filename": "c.bin", "file_data": "JVBERi0xLjQ="}},
			wantOK:   true,
			wantName: "c.bin",
		},
		{
			name:     "缺文件名回退默认值",
			part:     map[string]any{"type": "file", "file": map[string]any{"file_data": pdf}},
			wantOK:   true,
			wantName: "file.bin",
		},
		{
			name:   "没有数据",
			part:   map[string]any{"type": "file", "file": map[string]any{"filename": "a.pdf"}},
			wantOK: false,
		},
		{
			name:   "非法 base64",
			part:   map[string]any{"type": "file", "file": map[string]any{"file_data": "!!!"}},
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := fileAttachmentFromPart(tc.part)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			if got.Size <= 0 || len(got.Data) == 0 {
				t.Errorf("数据未解出: %+v", got)
			}
		})
	}
}

// extractContent 的 file / input_file 分片要能同时与文本、图片共存。
func TestExtractContentWithFileParts(t *testing.T) {
	text, imgs, files := extractContent([]any{
		map[string]any{"type": "text", "text": "看一下"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
		map[string]any{"type": "file", "file": map[string]any{"filename": "a.pdf", "file_data": "data:application/pdf;base64,JVBERi0xLjQ="}},
	})
	if text != "看一下" {
		t.Errorf("text = %q", text)
	}
	if len(imgs) != 1 || imgs[0].Format != "png" || len(imgs[0].Data) == 0 {
		t.Errorf("images = %+v", imgs)
	}
	if len(files) != 1 || files[0].Name != "a.pdf" || len(files[0].Data) == 0 {
		t.Errorf("files = %+v", files)
	}
}

// 其他角色（如 tool）不能被丢掉，应作为带前缀的文本进入提示词。
func TestFlattenMessagesToolRole(t *testing.T) {
	text, _, _, err := flattenMessages([]chatMessage{
		{Role: "developer", Content: "dev"},
		{Role: "user", Content: "q"},
		{Role: "tool", Content: "result"},
		{Role: "assistant", Content: "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "dev\n\nuser: q\ntool: result\nassistant: a"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

// 流式响应没有 Flusher 时必须显式报错，而不是写出半截 SSE。
type noFlushWriter struct {
	header http.Header
	status int
	body   strings.Builder
}

func (w *noFlushWriter) Header() http.Header         { return w.header }
func (w *noFlushWriter) WriteHeader(code int)        { w.status = code }
func (w *noFlushWriter) Write(b []byte) (int, error) { return w.body.Write(b) }

func TestStreamChatRequiresFlusher(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	newFakeDoubao(t).install(s)

	w := &noFlushWriter{header: http.Header{}}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody(t, true, "doubao", "hi")))
	r.RemoteAddr = loopbackAddr
	s.streamChat(w, r, doubao.ChatRequest{Text: "hi"}, registry.Model{ID: "doubao"}, "", s.requestTimeout(), time.Now())

	if w.status != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500", w.status)
	}
	if !strings.Contains(w.body.String(), "流式") {
		t.Errorf("错误体 = %s", w.body.String())
	}
}

// probeAccount 用一个最小请求探活：上游业务错误必须转成 error。
func TestProbeAccount(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		s := newTestServer(t)
		addTestAccount(t, s, "a1", "s1")
		newFakeDoubao(t).install(s)

		if err := s.probeAccount(t.Context(), s.Store.Account("a1")); err != nil {
			t.Fatalf("探活应成功: %v", err)
		}
	})

	t.Run("错误码", func(t *testing.T) {
		s := newTestServer(t)
		addTestAccount(t, s, "a1", "s1")
		fake := newFakeDoubao(t)
		fake.handler = func(w http.ResponseWriter, _ int) {
			writeSSE(w, sseEvent("STREAM_ERROR", `{"error_code":710022004,"error_msg":"rate limited"}`))
		}
		fake.install(s)

		err := s.probeAccount(t.Context(), s.Store.Account("a1"))
		if err == nil || !strings.Contains(err.Error(), "710022004") {
			t.Fatalf("探活应返回上游错误: %v", err)
		}
	})
}

// 附件上传失败时，请求应在调用对话接口之前中止并返回明确错误。
func TestChatImageUploadFailure(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	fake := newFakeDoubao(t)
	fake.handler = func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":1001,"msg":"denied"}`)
	}
	fake.install(s)

	body := `{"model":"doubao","messages":[{"role":"user","content":[
		{"type":"text","text":"这是什么"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}
	]}]}`
	rec := serve(s, newReq("POST", "/v1/chat/completions", body, loopbackAddr))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "图片上传失败") {
		t.Errorf("错误体 = %s", rec.Body.String())
	}
	fake.mu.Lock()
	chatCalls := fake.chatCalls
	fake.mu.Unlock()
	if chatCalls != 0 {
		t.Errorf("上传失败后不应再调用对话接口，实际 %d 次", chatCalls)
	}
}

// 非 JSON 事件被跳过时必须留下计数，运维可通过 /health 发现协议漂移。
func TestHealthReportsNonJSONEvents(t *testing.T) {
	s := newTestServer(t)
	addTestAccount(t, s, "a1", "s1")
	fake := newFakeDoubao(t)
	fake.handler = func(w http.ResponseWriter, _ int) {
		writeSSE(w,
			sseEvent("STREAM_CHUNK", "not-json")+
				sseEvent("STREAM_CHUNK", `{"patch_op":[{"patch_value":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"hi"}}}]}}]}`))
	}
	fake.install(s)

	if rec := serve(s, newReq("POST", "/v1/chat/completions", chatBody(t, false, "doubao", "hi"), loopbackAddr)); rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
	}

	body := decode(t, serve(s, newReq("GET", "/health", "", loopbackAddr)))
	if body["non_json_events"] != float64(1) {
		t.Errorf("non_json_events = %v, want 1", body["non_json_events"])
	}
}
