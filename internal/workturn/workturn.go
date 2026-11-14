// Package workturn 把「工作模式对话 + 工具通道 + 本地沙箱执行」串成一个完整回合。
//
// 时序（实测约束，见 docs/recon.md §6.12）：
//
//	POST /chat/completion（agent_mode=1）
//	  ├─ SSE: STREAM_MSG_NOTIFY 首帧 → agent_id
//	  ├─ 并行：sandbox_info_report 注册本机沙箱
//	  ├─ 并行：msg/fetch_agent_miss 长轮询 → 拿到工具调用
//	  │        → 本地沙箱执行 → upload_tool_call_result → msg/complete
//	  └─ SSE 继续输出正文，直到 SSE_REPLY_END
//
// 关键点：agent_id 只在会话存活期内有效——**必须与 SSE 流并发**轮询，
// 流结束后再拉只会得到 expired。因此 Run 会先启动流，拿到 agent_id 立刻起轮询。
package workturn

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"doubao2api/internal/agent"
	"doubao2api/internal/doubao"
)

// ChatStreamer 是对话流接口（生产环境为 *doubao.Client）。
type ChatStreamer interface {
	ChatStream(ctx context.Context, a *doubao.Account, req doubao.ChatRequest, onChunk func(doubao.CompletionChunk) error) error
}

// ToolChannel 是工具通道接口（生产环境同为 *doubao.Client）。
type ToolChannel interface {
	SandboxInfoReport(ctx context.Context, a *doubao.Account, agentID string, info map[string]any) error
	FetchAgentMiss(ctx context.Context, a *doubao.Account, agentID, agentType string, beginSeq int64, limit int) (*doubao.AgentMissPage, error)
	AckAgentMsgs(ctx context.Context, a *doubao.Account, msgIDs []string) error
	CompleteAgentMsg(ctx context.Context, a *doubao.Account, msgID string) error
	UploadToolCallResult(ctx context.Context, a *doubao.Account, sandboxID string, result any) error
}

// Options 是一次工作模式回合的参数。
type Options struct {
	Text            string
	ThinkLevel      int
	ModelItemKey    string
	ModeID          string
	ReasoningEffort string
	ConversationID  string
	BotID           string

	// PollInterval 是工具通道的轮询间隔。
	PollInterval time.Duration
	// PollWindow 是流结束后继续收尾的时间（让晚到的工具调用仍被执行）。
	PollWindow time.Duration
	// MaxToolRounds 限制单次回合执行的工具数量（防失控）。
	MaxToolRounds int

	// OnChunk 可选：透传正文增量（生产环境用于 SSE 转发）。
	OnChunk func(doubao.CompletionChunk) error
	// OnTool 可选：工具执行回调（日志 / 观测）。
	OnTool func(ToolEvent)
	// OnRawMsg 可选：原始 agent 消息（校准报文形状用）。
	OnRawMsg func(map[string]any)
	// OnPoll 可选：每次轮询的结果（排障用：expired / 消息数）。
	OnPoll func(expired bool, count int, err error)
}

// ToolEvent 记录一次工具执行。
type ToolEvent struct {
	Name   string
	Input  map[string]any
	Result map[string]any
	Err    error
}

// Result 是一次回合的产出。
type Result struct {
	Text           string
	Thinking       string
	ConversationID string
	AgentID        string
	Tools          []ToolEvent
	SandboxID      string
}

// 默认参数。
const (
	defaultPollInterval  = 1200 * time.Millisecond
	defaultPollWindow    = 4 * time.Second
	defaultMaxToolRounds = 8
)

// Run 执行一次完整回合：流式对话 + 工具轮询 + 本地执行 + 结果回传。
//
// box 必须已经初始化（工作区、限额、是否允许命令都在那里控制）。
func Run(ctx context.Context, chat ChatStreamer, tc ToolChannel, a *doubao.Account, box *agent.Sandbox, opt Options) (*Result, error) {
	if chat == nil || tc == nil || a == nil || box == nil {
		return nil, errors.New("workturn: 参数不完整")
	}
	if opt.PollInterval <= 0 {
		opt.PollInterval = defaultPollInterval
	}
	if opt.PollWindow < 0 {
		opt.PollWindow = 0
	} else if opt.PollWindow == 0 {
		opt.PollWindow = defaultPollWindow
	}
	if opt.MaxToolRounds <= 0 {
		opt.MaxToolRounds = defaultMaxToolRounds
	}

	res := &Result{SandboxID: box.SandboxID}
	var mu sync.Mutex

	loopCtx, cancelLoop := context.WithCancel(ctx)
	defer cancelLoop()

	// busy 标记工具是否正在执行：流结束后不能立刻取消，否则会把执行中的命令掐断。
	var busy atomic.Bool

	var (
		once    sync.Once
		loopWG  sync.WaitGroup
		loopErr error
	)
	startLoop := func(agentID string) {
		once.Do(func() {
			mu.Lock()
			res.AgentID = agentID
			mu.Unlock()
			loopWG.Add(1)
			go func() {
				defer loopWG.Done()
				if err := runToolLoop(loopCtx, tc, a, box, agentID, opt, res, &mu, &busy); err != nil && !errors.Is(err, context.Canceled) {
					mu.Lock()
					loopErr = err
					mu.Unlock()
				}
			}()
		})
	}

	upReq := doubao.ChatRequest{
		Text:            opt.Text,
		ThinkLevel:      opt.ThinkLevel,
		BotID:           opt.BotID,
		ConversationID:  opt.ConversationID,
		AgentMode:       1, // 工作模式
		ModelItemKey:    opt.ModelItemKey,
		ModeID:          opt.ModeID,
		ReasoningEffort: opt.ReasoningEffort,
	}

	streamErr := chat.ChatStream(ctx, a, upReq, func(ch doubao.CompletionChunk) error {
		if ch.AgentID != "" {
			startLoop(ch.AgentID)
		}
		mu.Lock()
		res.Text += ch.Text
		res.Thinking += ch.Thinking
		if ch.ConvID != "" {
			res.ConversationID = ch.ConvID
		}
		mu.Unlock()
		if opt.OnChunk != nil {
			return opt.OnChunk(ch)
		}
		return nil
	})

	// 流结束：给工具通道留一个收尾窗口（晚到的调用仍可执行），
	// 并且必须等正在执行的工具跑完——否则 cancelLoop 会把命令掐断。
	if res.AgentID != "" {
		drainUntil := time.Now().Add(opt.PollWindow)
		hardCap := time.Now().Add(opt.PollWindow + 2*time.Minute)
		for {
			if ctx.Err() != nil {
				break
			}
			if time.Now().After(drainUntil) && !busy.Load() {
				break
			}
			if time.Now().After(hardCap) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancelLoop()
	loopWG.Wait()

	mu.Lock()
	defer mu.Unlock()
	if streamErr != nil {
		return res, streamErr
	}
	return res, loopErr
}

// runToolLoop 轮询工具通道、执行工具并回传结果，直到会话过期或 ctx 取消。
func runToolLoop(ctx context.Context, tc ToolChannel, a *doubao.Account, box *agent.Sandbox,
	agentID string, opt Options, res *Result, mu *sync.Mutex, busy *atomic.Bool) error {

	// info 的字段取自客户端 init_env 成功时的上报（entry-moa @2139038）：
	//   {agent_workspace, project_directory, local_skill_paths, available}
	// 少这几个字段时上游不会把本机当成"可用沙箱"，也就不会下发工具调用（实测：
	// 模型会直接声称已完成，而工作区里什么都没有）。
	info := map[string]any{
		"agent_workspace":   box.Root,
		"project_directory": box.Root,
		"local_skill_paths": []string{},
		"available":         true,
	}
	if err := tc.SandboxInfoReport(ctx, a, agentID, info); err != nil {
		return fmt.Errorf("沙箱信息上报失败: %w", err)
	}

	ticker := time.NewTicker(opt.PollInterval)
	defer ticker.Stop()

	seen := map[string]bool{}
	rounds := 0
	beginSeq := int64(0)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		page, err := tc.FetchAgentMiss(ctx, a, agentID, doubao.ToolAgentType, beginSeq, 100)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if opt.OnPoll != nil {
				opt.OnPoll(false, 0, err)
			}
			log.Printf("workturn: 拉取工具消息失败（稍后重试）: %v", err)
			continue
		}
		if opt.OnPoll != nil {
			if page != nil {
				opt.OnPoll(page.Expired, len(page.Messages), nil)
			} else {
				opt.OnPoll(false, 0, nil)
			}
		}
		if page == nil || page.Expired {
			return nil // 会话已结束
		}
		if len(page.Messages) == 0 {
			continue
		}

		ids := make([]string, 0, len(page.Messages))
		for _, m := range page.Messages {
			if m.MsgID != "" {
				ids = append(ids, m.MsgID)
			}
			if opt.OnRawMsg != nil {
				opt.OnRawMsg(m.Raw)
			}
		}
		_ = tc.AckAgentMsgs(ctx, a, ids)

		for _, m := range page.Messages {
			if m.Seq >= beginSeq {
				beginSeq = m.Seq + 1
			}
			if m.MsgID != "" {
				if seen[m.MsgID] {
					continue
				}
				seen[m.MsgID] = true
			}
			call, ok := doubao.ParseToolCall(m.Raw)
			if !ok {
				// 非工具消息：确认收到即可，避免重复投递。
				_ = tc.CompleteAgentMsg(ctx, a, m.MsgID)
				continue
			}
			if rounds >= opt.MaxToolRounds {
				log.Printf("workturn: 工具调用次数达到上限 %d，停止执行", opt.MaxToolRounds)
				return nil
			}
			rounds++

			busy.Store(true)
			result, execErr := box.Exec(ctx, call.Name, call.Input)
			busy.Store(false)
			if result == nil {
				result = map[string]any{}
			}
			if execErr != nil {
				result["ok"] = false
				result["error"] = execErr.Error()
			}
			payload := map[string]any{
				"ok":           result["ok"],
				"tool_name":    call.Name,
				"tool_call_id": firstNonEmpty(call.CallID, call.MsgID),
				"result":       result,
			}
			if execErr != nil {
				payload["error"] = execErr.Error()
			}

			ev := ToolEvent{Name: call.Name, Input: call.Input, Result: result, Err: execErr}
			mu.Lock()
			res.Tools = append(res.Tools, ev)
			mu.Unlock()
			if opt.OnTool != nil {
				opt.OnTool(ev)
			}

			if err := tc.UploadToolCallResult(ctx, a, box.SandboxID, payload); err != nil {
				log.Printf("workturn: 回传工具结果失败: %v", err)
			}
			_ = tc.CompleteAgentMsg(ctx, a, m.MsgID)
		}
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
