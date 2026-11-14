package doubao

import (
	"context"
	"fmt"
)

// 本文件封装客户端「本地工具通道」（/alice/office/tool_local/*）。
//
// 协议事实（2026-11 从 entry-moa/main.js 取证 + 实测，见 docs/recon.md §6.12）：
//   - 工具调用**不走 SSE**，而是服务端把它排进 agent 的消息队列，
//     客户端用 msg/fetch_agent_miss 长轮询取；取到后 msg/ack，执行完
//     upload_tool_call_result 回传，再 msg/complete。
//   - agent_id 来自 SSE 的 STREAM_MSG_NOTIFY 首帧；会话存在时效，
//     过期后 fetch 返回 {"expired": true, "msgs": []}。
//   - agent_type 必须传**字符串**（传数字会被判 InvalidRequest）。
//   - 沙箱实例 id 由客户端本地生成（nativeInstanceId），通过
//     sandbox_info_report 注册给服务端后，模型才认为"这台机器有工具可用"。

// ToolAgentType 是 fetch_agent_miss 的 agent_type 取值。
//
// 上游只校验它是字符串；实测 "1" / "agent" / "local" 都能返回 code 0。
const ToolAgentType = "1"

// ToolReportProtocolVersion 是 sandbox_info_report 的协议版本。
const ToolReportProtocolVersion = "v1.0.0"

// AgentMessage 是工具通道里的一条消息。
//
// 字段按上游实际下发归一化；未知字段留在 Raw 里，避免丢信息。
type AgentMessage struct {
	MsgID    string
	Seq      int64
	Type     string
	ToolName string
	ToolCall string // 原始工具入参（JSON 字符串）
	Raw      map[string]any
}

// AgentMissPage 是 fetch_agent_miss 的一页结果。
type AgentMissPage struct {
	Expired  bool
	Messages []AgentMessage
}

// SandboxInfoReport 把本机沙箱/工具可用性注册给服务端（按 agent 维度）。
//
// info 由调用方决定字段（至少含 available）；实测空对象也能返回 code 0，
// 但模型只有在得到"工具可用"的信号后才会发起工具调用。
func (c *Client) SandboxInfoReport(ctx context.Context, a *Account, agentID string, info map[string]any) error {
	if agentID == "" {
		return fmt.Errorf("agent_id 不能为空")
	}
	if info == nil {
		info = map[string]any{}
	}
	raw, err := c.postJSON(ctx, a, "/alice/office/tool_local/sandbox_info_report", map[string]any{
		"protocol_version": ToolReportProtocolVersion,
		"agent_id":         agentID,
		"info":             info,
	})
	if err != nil {
		return err
	}
	obj, ok := decodeObj(string(raw))
	if !ok {
		return fmt.Errorf("沙箱信息上报响应解析失败: %s", truncate(string(raw), 200))
	}
	if code := intOf(obj["code"]); code != 0 {
		return fmt.Errorf("沙箱信息上报失败 (code=%d): %s", code, truncate(firstNonEmpty(str(obj["message"]), str(obj["msg"])), 200))
	}
	return nil
}

// FetchAgentMiss 拉取 agent 尚未投递的消息（工具调用从这里来）。
func (c *Client) FetchAgentMiss(ctx context.Context, a *Account, agentID, agentType string, beginSeq int64, limit int) (*AgentMissPage, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id 不能为空")
	}
	if agentType == "" {
		agentType = ToolAgentType
	}
	// 客户端固定用 100；begin_seq 未设置时**整字段省略**（不是传 0）。
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	body := map[string]any{
		"agent_id":   agentID,
		"agent_type": agentType, // 必须是字符串
		"limit":      limit,
	}
	if beginSeq > 0 {
		body["begin_seq"] = beginSeq
	}
	raw, err := c.postJSON(ctx, a, "/alice/office/tool_local/msg/fetch_agent_miss", body)
	if err != nil {
		return nil, err
	}
	obj, ok := decodeObj(string(raw))
	if !ok {
		return nil, fmt.Errorf("拉取 agent 消息响应解析失败: %s", truncate(string(raw), 200))
	}
	if code := intOf(obj["code"]); code != 0 {
		return nil, fmt.Errorf("拉取 agent 消息失败 (code=%d): %s", code, truncate(firstNonEmpty(str(obj["message"]), str(obj["msg"])), 200))
	}
	data, _ := obj["data"].(map[string]any)
	page := &AgentMissPage{Expired: truthy(data["expired"])}
	for _, it := range asSlice(data["msgs"]) {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		page.Messages = append(page.Messages, AgentMessage{
			MsgID:    firstNonEmpty(str(m["msg_id"]), str(m["id"])),
			Seq:      int64(intOf(firstNonEmptyAny(m["seq"], m["begin_seq"]))),
			Type:     firstNonEmpty(str(m["msg_type"]), str(m["type"])),
			ToolName: firstNonEmpty(str(m["tool_name"]), str(m["name"])),
			ToolCall: firstNonEmpty(str(m["tool_call"]), str(m["input"]), str(m["tool_input"])),
			Raw:      m,
		})
	}
	return page, nil
}

// AckAgentMsgs 告知服务端这些消息已接收（避免重复投递）。
func (c *Client) AckAgentMsgs(ctx context.Context, a *Account, msgIDs []string) error {
	if len(msgIDs) == 0 {
		return nil
	}
	_, err := c.postJSON(ctx, a, "/alice/office/tool_local/msg/ack", map[string]any{
		"msg_ids": msgIDs,
	})
	return err
}

// CompleteAgentMsg 标记某条消息处理完毕。
func (c *Client) CompleteAgentMsg(ctx context.Context, a *Account, msgID string) error {
	if msgID == "" {
		return nil
	}
	_, err := c.postJSON(ctx, a, "/alice/office/tool_local/msg/complete", map[string]any{
		"msg_id": msgID,
	})
	return err
}

// UploadToolCallResult 回传工具执行结果（v2 优先，失败时回退 v1）。
func (c *Client) UploadToolCallResult(ctx context.Context, a *Account, sandboxID string, result any) error {
	body := map[string]any{
		"sandbox_id":  sandboxID,
		"tool_result": result,
		"device_id":   a.DeviceID,
	}
	if _, err := c.postJSON(ctx, a, "/alice/office/tool_local/upload_tool_call_result_v2", body); err == nil {
		return nil
	}
	// v2 不可用时回退 v1（两者字段一致，仅路径不同）。
	_, err := c.postJSON(ctx, a, "/alice/office/tool_local/upload_tool_call_result", body)
	return err
}

// firstNonEmptyAny 返回第一个能转成 int 的值（上游 seq 有时是数字、有时是字符串）。
func firstNonEmptyAny(vals ...any) any {
	for _, v := range vals {
		if v == nil {
			continue
		}
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		return v
	}
	return nil
}
