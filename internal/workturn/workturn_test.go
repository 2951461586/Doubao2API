package workturn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"doubao2api/internal/agent"
	"doubao2api/internal/doubao"
)

// 编排的核心是「时序」：流里拿到 agent_id 才起轮询，工具执行完必须回传并 complete。
// 这里用 fake 通道把时序固定下来，不依赖上游。

type fakeChat struct {
	chunks []doubao.CompletionChunk
	err    error
	gotReq *doubao.ChatRequest
}

func (f *fakeChat) ChatStream(_ context.Context, _ *doubao.Account, req doubao.ChatRequest,
	onChunk func(doubao.CompletionChunk) error) error {
	f.gotReq = &req
	for _, c := range f.chunks {
		if err := onChunk(c); err != nil {
			return err
		}
	}
	return f.err
}

type fakeTools struct {
	mu        sync.Mutex
	reported  bool
	reportErr error
	pages     []*doubao.AgentMissPage
	fetchErr  error
	uploads   []map[string]any
	acks      [][]string
	completed []string
}

func (f *fakeTools) SandboxInfoReport(_ context.Context, _ *doubao.Account, agentID string, info map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reported = agentID != "" && info != nil
	return f.reportErr
}

func (f *fakeTools) FetchAgentMiss(context.Context, *doubao.Account, string, string, int64, int) (*doubao.AgentMissPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	if len(f.pages) == 0 {
		return &doubao.AgentMissPage{Expired: true}, nil
	}
	p := f.pages[0]
	f.pages = f.pages[1:]
	return p, nil
}

func (f *fakeTools) AckAgentMsgs(_ context.Context, _ *doubao.Account, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, ids)
	return nil
}

func (f *fakeTools) CompleteAgentMsg(_ context.Context, _ *doubao.Account, msgID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, msgID)
	return nil
}

func (f *fakeTools) UploadToolCallResult(_ context.Context, _ *doubao.Account, sandboxID string, result any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, _ := result.(map[string]any)
	f.uploads = append(f.uploads, map[string]any{"sandbox_id": sandboxID, "payload": m})
	return nil
}

func newBox(t *testing.T) *agent.Sandbox {
	t.Helper()
	box, err := agent.New(t.TempDir(), "sandbox-test")
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	box.MaxBytes = 4096
	return box
}

func toolMsg(id, name string, input map[string]any) map[string]any {
	return map[string]any{
		"msg_id":     id,
		"type":       "tool_call",
		"tool_name":  name,
		"tool_input": input,
	}
}

func fastOpt(text string) Options {
	return Options{
		Text:           text,
		ModelItemKey:   "person-deepseek-v4-1-flash-261017",
		PollInterval:   time.Millisecond,
		PollWindow:     80 * time.Millisecond, // 给轮询留出执行窗口，避免时序抖动
		ConversationID: "0",
	}
}

// TestRunExecutesToolAndUploads 走通一次工具调用：执行 → 回传 → complete。
func TestRunExecutesToolAndUploads(t *testing.T) {
	box := newBox(t)
	chat := &fakeChat{chunks: []doubao.CompletionChunk{
		{AgentID: "agent-1"},
		{Text: "好的，"},
		{Text: "我来创建文件"},
		{ConvID: "conv-1"},
	}}
	tools := &fakeTools{pages: []*doubao.AgentMissPage{
		{Messages: []doubao.AgentMessage{
			{MsgID: "m1", Raw: toolMsg("m1", "write_file", map[string]any{"path": "hello.txt", "content": "OK"})},
		}},
	}}

	res, err := Run(context.Background(), chat, tools, &doubao.Account{DeviceID: "dev"}, box, fastOpt("创建 hello.txt"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 文件真的落盘
	data, err := os.ReadFile(filepath.Join(box.Root, "hello.txt"))
	if err != nil || string(data) != "OK" {
		t.Fatalf("hello.txt = %q, err=%v", data, err)
	}

	if !tools.reported {
		t.Error("应先上报沙箱信息再轮询")
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "write_file" || res.Tools[0].Err != nil {
		t.Fatalf("工具执行记录异常: %+v", res.Tools)
	}
	if len(tools.uploads) != 1 {
		t.Fatalf("应回传 1 次结果，实际 %d", len(tools.uploads))
	}
	payload := tools.uploads[0]["payload"].(map[string]any)
	if payload["ok"] != true || payload["tool_name"] != "write_file" {
		t.Errorf("回传内容异常: %+v", payload)
	}
	if tools.uploads[0]["sandbox_id"] != box.SandboxID {
		t.Errorf("sandbox_id = %v, want %s", tools.uploads[0]["sandbox_id"], box.SandboxID)
	}
	if len(tools.completed) != 1 || tools.completed[0] != "m1" {
		t.Errorf("应 complete m1，实际 %v", tools.completed)
	}
	if res.Text != "好的，我来创建文件" || res.ConversationID != "conv-1" || res.AgentID != "agent-1" {
		t.Errorf("结果汇总异常: %+v", res)
	}
	// 工作模式参数必须下发
	if chat.gotReq == nil || chat.gotReq.AgentMode != 1 || chat.gotReq.ModelItemKey == "" {
		t.Errorf("请求未按工作模式构造: %+v", chat.gotReq)
	}
}

// TestRunShellDisabledByDefault 命令类工具在默认配置下必须失败并如实回传。
func TestRunShellDisabledByDefault(t *testing.T) {
	box := newBox(t) // AllowShell 默认 false
	chat := &fakeChat{chunks: []doubao.CompletionChunk{{AgentID: "a"}}}
	tools := &fakeTools{pages: []*doubao.AgentMissPage{
		{Messages: []doubao.AgentMessage{
			{MsgID: "m1", Raw: toolMsg("m1", "run_command", map[string]any{"command": "go version"})},
		}},
	}}

	res, err := Run(context.Background(), chat, tools, &doubao.Account{DeviceID: "dev"}, box, fastOpt("跑个命令"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Err == nil {
		t.Fatalf("run_command 在 AllowShell=false 时应报错: %+v", res.Tools)
	}
	payload := tools.uploads[0]["payload"].(map[string]any)
	if payload["ok"] == true || payload["error"] == nil {
		t.Errorf("失败结果应如实回传: %+v", payload)
	}
}

// TestRunPathEscapeRejected 越权路径不会落盘，错误会回传给模型。
func TestRunPathEscapeRejected(t *testing.T) {
	box := newBox(t)
	chat := &fakeChat{chunks: []doubao.CompletionChunk{{AgentID: "a"}}}
	tools := &fakeTools{pages: []*doubao.AgentMissPage{
		{Messages: []doubao.AgentMessage{
			{MsgID: "m1", Raw: toolMsg("m1", "write_file", map[string]any{"path": "../../escape.txt", "content": "x"})},
		}},
	}}
	if _, err := Run(context.Background(), chat, tools, &doubao.Account{}, box, fastOpt("越权")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(box.Root), "escape.txt")); err == nil {
		t.Fatal("越权写入竟然落盘")
	}
	payload := tools.uploads[0]["payload"].(map[string]any)
	if payload["ok"] == true {
		t.Error("越权应回传失败")
	}
}

// TestRunNoAgentIDSkipsTools 没有 agent_id 时不启动工具通道。
func TestRunNoAgentIDSkipsTools(t *testing.T) {
	box := newBox(t)
	chat := &fakeChat{chunks: []doubao.CompletionChunk{{Text: "hi"}}}
	tools := &fakeTools{}
	res, err := Run(context.Background(), chat, tools, &doubao.Account{}, box, fastOpt("hi"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tools.reported {
		t.Error("没有 agent_id 不应上报沙箱")
	}
	if res.Text != "hi" || len(res.Tools) != 0 {
		t.Errorf("结果异常: %+v", res)
	}
}

// TestRunExpiredStopsLoop 会话过期立即结束轮询。
func TestRunExpiredStopsLoop(t *testing.T) {
	box := newBox(t)
	chat := &fakeChat{chunks: []doubao.CompletionChunk{{AgentID: "a"}}}
	tools := &fakeTools{} // 空 = 第一次 fetch 返回 expired
	start := time.Now()
	if _, err := Run(context.Background(), chat, tools, &doubao.Account{}, box, fastOpt("hi")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("过期后应立即返回，实际耗时 %s", elapsed)
	}
}

// TestRunMaxToolRounds 工具次数上限生效（防失控）。
func TestRunMaxToolRounds(t *testing.T) {
	box := newBox(t)
	chat := &fakeChat{chunks: []doubao.CompletionChunk{{AgentID: "a"}}}
	msgs := make([]doubao.AgentMessage, 0, 5)
	for _, id := range []string{"m1", "m2", "m3", "m4"} {
		msgs = append(msgs, doubao.AgentMessage{
			MsgID: id,
			Raw:   toolMsg(id, "write_file", map[string]any{"path": id + ".txt", "content": "x"}),
		})
	}
	tools := &fakeTools{pages: []*doubao.AgentMissPage{{Messages: msgs}}}
	opt := fastOpt("多轮")
	opt.MaxToolRounds = 2

	res, err := Run(context.Background(), chat, tools, &doubao.Account{}, box, opt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Tools) > 2 {
		t.Errorf("工具执行次数超过上限: %d", len(res.Tools))
	}
}

// TestRunReportFailureSurfaces 沙箱上报失败时把错误带回给调用方。
func TestRunReportFailureSurfaces(t *testing.T) {
	box := newBox(t)
	chat := &fakeChat{chunks: []doubao.CompletionChunk{{AgentID: "a"}}}
	tools := &fakeTools{reportErr: errors.New("boom")}
	res, err := Run(context.Background(), chat, tools, &doubao.Account{}, box, fastOpt("hi"))
	if err == nil || !strings.Contains(err.Error(), "沙箱信息上报失败") {
		t.Fatalf("应返回上报失败，实际 err=%v", err)
	}
	if res == nil {
		t.Fatal("即使失败也应返回结果结构")
	}
}
