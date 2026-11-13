package doubao

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// 积分 / 签到 / 激励任务相关常量。
const (
	// IncentiveCampaignGroupV3 是桌面端「积分任务」默认活动组（client bundle 常量）。
	IncentiveCampaignGroupV3 = "pc_incentive_task_mvp_v3"
	// IncentiveCampaignGroupV2 是旧版活动组。
	IncentiveCampaignGroupV2 = "pc_incentive_task_mvp_v2"

	// 任务状态（client Dq.D1）
	TaskStatusUnknown    = 0
	TaskStatusCreated    = 1
	TaskStatusCompleted  = 2
	TaskStatusRewarded   = 3
	TaskStatusInProgress = 4

	// 领取模式（client Dq.BH）
	RewardModeCompleteThenClaim = 1
	RewardModeDirectClaim       = 2
)

// CreditInfo 是积分余额信息。
type CreditInfo struct {
	TotalCreditNum int64  `json:"total_credit_num"`
	CreditText     string `json:"credit_text,omitempty"`
	CreditDescText string `json:"credit_desc_text,omitempty"`
	IconURL        string `json:"icon_url,omitempty"`
	DarkModeIcon   string `json:"dark_mode_icon_url,omitempty"`
}

// OptionalTask 是「每日登录」等可选任务。
type OptionalTask struct {
	TaskType     int    `json:"task_type"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
	ButtonText   string `json:"button_text,omitempty"`
	ButtonStatus int    `json:"button_status,omitempty"`
	ButtonSchema string `json:"button_schema,omitempty"`
	IconURL      string `json:"icon_url,omitempty"`
	DarkModeIcon string `json:"dark_mode_icon_url,omitempty"`
}

// CreditSummary 是积分面板数据。
type CreditSummary struct {
	Credit       CreditInfo     `json:"credit"`
	OptionalTask []OptionalTask `json:"optional_tasks"`
	RuleLink     string         `json:"credit_rule_link,omitempty"`
	RuleLinkText string         `json:"credit_rule_link_text,omitempty"`
}

// CreditHistory 是积分流水。
type CreditHistory struct {
	Items      []map[string]any `json:"items"`
	NextCursor int64            `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

// CheckinResult 是每日签到结果。
type CheckinResult struct {
	CheckedIn      bool   `json:"checked_in"`
	CheckinDate    string `json:"checkin_date,omitempty"`
	ContinuousDays int    `json:"continuous_days,omitempty"`
	Idempotent     bool   `json:"idempotent"`
	RewardAmount   int64  `json:"reward_amount,omitempty"`
	RewardCode     string `json:"reward_code,omitempty"`
	RewardType     string `json:"reward_type,omitempty"`
	RewardStatus   int    `json:"reward_status,omitempty"`
}

// CampaignReward 是一项任务奖励。
type CampaignReward struct {
	Amount int64  `json:"reward_amount"`
	Type   string `json:"reward_type"`
}

// CampaignTask 是激励任务 / 里程碑。
type CampaignTask struct {
	CampaignCode      string           `json:"campaign_code"`
	CampaignGroupCode string           `json:"campaign_group_code"`
	CampaignType      string           `json:"campaign_type,omitempty"`
	TaskKind          string           `json:"task_kind,omitempty"`
	Title             string           `json:"title,omitempty"`
	Subtitle          string           `json:"subtitle,omitempty"`
	Description       string           `json:"description,omitempty"`
	Status            int              `json:"task_status"`
	StatusName        string           `json:"status_name"`
	RewardMode        int              `json:"reward_mode,omitempty"`
	InstanceTaskID    string           `json:"instance_task_id,omitempty"`
	MilestoneAt       int64            `json:"milestone_threshold,omitempty"`
	Rewards           []CampaignReward `json:"rewards,omitempty"`
	Progress          map[string]any   `json:"progress,omitempty"`
	RecommendPrompts  []string         `json:"recommend_prompts,omitempty"`
	Claimable         bool             `json:"claimable"`
}

// CampaignDetail 是活动详情。
type CampaignDetail struct {
	CampaignCode string         `json:"campaign_code"`
	Tasks        []CampaignTask `json:"tasks"`
	Milestones   []CampaignTask `json:"milestones"`
	TotalAmount  int64          `json:"total_amount"`
}

// ClaimResult 是一次奖励领取结果。
type ClaimResult struct {
	CampaignCode string `json:"campaign_code"`
	TaskID       string `json:"task_id,omitempty"`
	Status       string `json:"status"` // claimed / skipped / failed
	Reason       string `json:"reason,omitempty"`
	Raw          any    `json:"raw,omitempty"`
}

// CreditSummary 拉取积分余额与可选任务。
func (c *Client) CreditSummary(ctx context.Context, a *Account, needTasks bool) (*CreditSummary, error) {
	raw, err := c.postJSON(ctx, a, "/commerce/benefit_supply/credit/get_credit_num_optional_tasks",
		map[string]any{"need_tasks": needTasks})
	if err != nil {
		return nil, err
	}
	obj, ok := decodeObj(string(raw))
	if !ok {
		return nil, fmt.Errorf("积分响应解析失败: %s", truncate(string(raw), 200))
	}
	if code := intOf(obj["code"]); code != 0 {
		return nil, fmt.Errorf("积分获取失败 (code=%d): %s", code, truncate(firstNonEmpty(str(obj["message"]), str(obj["msg"]), string(raw)), 200))
	}
	data, _ := obj["data"].(map[string]any)
	out := &CreditSummary{OptionalTask: []OptionalTask{}}
	if ci, ok := data["credit_info"].(map[string]any); ok {
		out.Credit = CreditInfo{
			TotalCreditNum: int64(intOf(ci["total_credit_num"])),
			CreditText:     str(ci["credit_text"]),
			CreditDescText: str(ci["credit_desc_text"]),
			IconURL:        str(ci["icon_url"]),
			DarkModeIcon:   str(ci["dark_mode_icon_url"]),
		}
	}
	for _, it := range asSlice(data["optional_tasks"]) {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		out.OptionalTask = append(out.OptionalTask, OptionalTask{
			TaskType:     intOf(m["task_type"]),
			Title:        str(m["title"]),
			Description:  str(m["description"]),
			ButtonText:   str(m["button_text"]),
			ButtonStatus: intOf(m["button_status"]),
			ButtonSchema: str(m["button_schema"]),
			IconURL:      str(m["icon_url"]),
			DarkModeIcon: str(m["dark_mode_icon_url"]),
		})
	}
	if rl, ok := data["credit_rule_link"].(map[string]any); ok {
		out.RuleLink = str(rl["credit_rule_link_schema"])
		out.RuleLinkText = str(rl["credit_rule_link_text"])
	}
	return out, nil
}

// CreditHistory 拉取积分流水。
func (c *Client) CreditHistory(ctx context.Context, a *Account, cursor int64, size int, transactionType string) (*CreditHistory, error) {
	if size <= 0 || size > 100 {
		size = 20
	}
	body := map[string]any{"size": size}
	// cursor=0 会被上游判为 common invalid param；首屏必须不带 cursor。
	if cursor > 0 {
		body["cursor"] = cursor
	}
	if transactionType != "" {
		body["transaction_type"] = transactionType
	}
	raw, err := c.postJSON(ctx, a, "/commerce/benefit_supply/credit/get_credit_usage_history", body)
	if err != nil {
		return nil, err
	}
	obj, ok := decodeObj(string(raw))
	if !ok {
		return nil, fmt.Errorf("积分流水解析失败: %s", truncate(string(raw), 200))
	}
	if code := intOf(obj["code"]); code != 0 {
		return nil, fmt.Errorf("积分流水获取失败 (code=%d): %s", code, truncate(firstNonEmpty(str(obj["message"]), str(obj["msg"])), 200))
	}
	data, _ := obj["data"].(map[string]any)
	out := &CreditHistory{
		Items:      []map[string]any{},
		NextCursor: int64(intOf(data["next_cursor"])),
		HasMore:    truthy(data["has_more"]),
	}
	for _, it := range asSlice(data["items"]) {
		if m, ok := it.(map[string]any); ok {
			out.Items = append(out.Items, m)
		}
	}
	return out, nil
}

// DailyCheckIn 执行每日签到（上游按天幂等）。
func (c *Client) DailyCheckIn(ctx context.Context, a *Account, scene string) (*CheckinResult, error) {
	body := map[string]any{}
	if scene != "" {
		body["scene"] = scene
	}
	raw, err := c.postJSON(ctx, a, "/alice/ug/campaign/landing/checkin", body)
	if err != nil {
		return nil, err
	}
	obj, ok := decodeObj(string(raw))
	if !ok {
		return nil, fmt.Errorf("签到响应解析失败: %s", truncate(string(raw), 200))
	}
	if code := intOf(obj["code"]); code != 0 {
		return nil, fmt.Errorf("签到失败 (code=%d): %s", code, truncate(firstNonEmpty(str(obj["message"]), str(obj["msg"])), 200))
	}
	data, _ := obj["data"].(map[string]any)
	return &CheckinResult{
		CheckedIn:      truthy(data["checked_in"]),
		CheckinDate:    str(data["checkin_date"]),
		ContinuousDays: intOf(data["continuous_days"]),
		Idempotent:     truthy(data["idempotent"]),
		RewardAmount:   int64(intOf(data["reward_amount"])),
		RewardCode:     str(data["reward_code"]),
		RewardType:     str(data["reward_type"]),
		RewardStatus:   intOf(data["reward_status"]),
	}, nil
}

// CampaignDetail 拉取激励任务详情。
//
// groupCode 为空时使用桌面端默认活动组 pc_incentive_task_mvp_v3。
func (c *Client) CampaignDetail(ctx context.Context, a *Account, groupCode string) (*CampaignDetail, error) {
	if strings.TrimSpace(groupCode) == "" {
		groupCode = IncentiveCampaignGroupV3
	}
	raw, err := c.postJSON(ctx, a, "/alice/ug/campaign/v3/detail/",
		map[string]any{"campaign_group_code": groupCode})
	if err != nil {
		return nil, err
	}
	obj, ok := decodeObj(string(raw))
	if !ok {
		return nil, fmt.Errorf("任务详情解析失败: %s", truncate(string(raw), 200))
	}
	if code := intOf(obj["code"]); code != 0 {
		return nil, fmt.Errorf("任务详情获取失败 (code=%d): %s", code, truncate(firstNonEmpty(str(obj["message"]), str(obj["msg"])), 200))
	}
	data, _ := obj["data"].(map[string]any)
	if data == nil {
		return nil, fmt.Errorf("任务详情为空")
	}
	out := &CampaignDetail{
		CampaignCode: firstNonEmpty(str(data["campaign_code"]), groupCode),
		TotalAmount:  int64(intOf(nested(data, "progress", "total_amount"))),
	}
	out.Tasks = parseCampaignTasks(asSlice(data["tasks"]))
	if out.Tasks == nil {
		out.Tasks = []CampaignTask{}
	}
	out.Milestones = parseCampaignTasks(asSlice(data["milestones"]))
	if out.Milestones == nil {
		out.Milestones = []CampaignTask{}
	}
	return out, nil
}

// ClaimCampaignReward 领取单个任务奖励。
func (c *Client) ClaimCampaignReward(ctx context.Context, a *Account, taskID, groupCode, itemCode string) (map[string]any, error) {
	if strings.TrimSpace(itemCode) == "" {
		return nil, fmt.Errorf("campaign_item_code 不能为空")
	}
	if strings.TrimSpace(groupCode) == "" {
		groupCode = IncentiveCampaignGroupV3
	}
	body := map[string]any{
		"task_id":             taskID,
		"campaign_group_code": groupCode,
		"campaign_item_code":  itemCode,
	}
	raw, err := c.postJSON(ctx, a, "/alice/ug/campaign/item/reward/claim/", body)
	if err != nil {
		return nil, err
	}
	obj, ok := decodeObj(string(raw))
	if !ok {
		return nil, fmt.Errorf("领取响应解析失败: %s", truncate(string(raw), 200))
	}
	if code := intOf(obj["code"]); code != 0 {
		return obj, fmt.Errorf("领取失败 (code=%d): %s", code, truncate(firstNonEmpty(str(obj["message"]), str(obj["msg"])), 200))
	}
	// 上游业务错误放在 data.biz_error；客户端把 0/4/6 视为成功，其余为失败。
	if data, ok := obj["data"].(map[string]any); ok {
		if biz, ok := data["biz_error"].(map[string]any); ok {
			code := intOf(biz["code"])
			if code != 0 && code != 4 && code != 6 {
				msg := firstNonEmpty(str(biz["message"]), str(data["toast"]), str(obj["message"]))
				return obj, fmt.Errorf("领取失败 (biz_code=%d): %s", code, truncate(msg, 200))
			}
		}
	}
	return obj, nil
}

// ClaimCompletedCampaignRewards 自动领取「已完成」或「直接领取」的任务奖励。
//
// 只读取任务状态后领取，不伪造任务完成行为；每次领取之间留出间隔。
func (c *Client) ClaimCompletedCampaignRewards(ctx context.Context, a *Account, groupCode string) ([]ClaimResult, *CampaignDetail, error) {
	detail, err := c.CampaignDetail(ctx, a, groupCode)
	if err != nil {
		return nil, nil, err
	}
	results := make([]ClaimResult, 0)
	for _, t := range append(append([]CampaignTask{}, detail.Tasks...), detail.Milestones...) {
		if t.Status == TaskStatusRewarded {
			continue
		}
		// 未完成的里程碑需要累计额度达到阈值；已完成的不再重复判断。
		if t.MilestoneAt > 0 && t.Status != TaskStatusCompleted && detail.TotalAmount < t.MilestoneAt {
			continue
		}
		claimable := t.Status == TaskStatusCompleted || t.RewardMode == RewardModeDirectClaim
		if !claimable || !t.Claimable {
			continue
		}
		taskID := t.InstanceTaskID
		if taskID == "" && t.RewardMode != RewardModeDirectClaim {
			results = append(results, ClaimResult{
				CampaignCode: t.CampaignCode,
				Status:       "skipped",
				Reason:       "completed task has no instance_task_id",
			})
			continue
		}
		raw, err := c.ClaimCampaignReward(ctx, a, taskID, firstNonEmpty(t.CampaignGroupCode, groupCode), t.CampaignCode)
		if err != nil {
			results = append(results, ClaimResult{
				CampaignCode: t.CampaignCode,
				TaskID:       taskID,
				Status:       "failed",
				Reason:       err.Error(),
				Raw:          raw,
			})
		} else {
			results = append(results, ClaimResult{
				CampaignCode: t.CampaignCode,
				TaskID:       taskID,
				Status:       "claimed",
				Raw:          raw,
			})
		}
		if err := sleepCtx(ctx, 350*time.Millisecond); err != nil {
			return results, detail, err
		}
	}
	return results, detail, nil
}

// parseCampaignTasks 解析任务 / 里程碑数组。
func parseCampaignTasks(items []any) []CampaignTask {
	var out []CampaignTask
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		t := CampaignTask{
			CampaignCode:      str(m["campaign_code"]),
			CampaignGroupCode: str(m["campaign_group_code"]),
			CampaignType:      str(m["campaign_type"]),
			TaskKind:          str(m["task_kind"]),
			Title:             str(m["title"]),
			Subtitle:          str(m["subtitle"]),
			Description:       str(m["description"]),
			Status:            intOf(m["task_status"]),
			RewardMode:        intOf(m["reward_mode"]),
			InstanceTaskID:    firstNonEmpty(str(m["instance_task_id"]), str(m["instanceTaskId"]), str(m["task_id"])),
			MilestoneAt:       int64(intOf(m["milestone_threshold"])),
			Progress:          mapOf(m["progress"]),
		}
		t.StatusName = TaskStatusName(t.Status)
		for _, r := range asSlice(m["reward_summaries"]) {
			if rm, ok := r.(map[string]any); ok {
				t.Rewards = append(t.Rewards, CampaignReward{
					Amount: int64(intOf(rm["reward_amount"])),
					Type:   str(rm["reward_type"]),
				})
			}
		}
		for _, p := range asSlice(m["recommend_prompts"]) {
			if s, ok := p.(string); ok && s != "" {
				t.RecommendPrompts = append(t.RecommendPrompts, s)
			}
		}
		t.Claimable = t.Status == TaskStatusCompleted || t.RewardMode == RewardModeDirectClaim
		if t.Status == TaskStatusRewarded {
			t.Claimable = false
		}
		out = append(out, t)
	}
	return out
}

// TaskStatusName 把任务状态码转为可读名称。
func TaskStatusName(status int) string {
	switch status {
	case TaskStatusCreated:
		return "created"
	case TaskStatusCompleted:
		return "completed"
	case TaskStatusRewarded:
		return "rewarded"
	case TaskStatusInProgress:
		return "in_progress"
	}
	return "unknown"
}

// nested 读取 map 的多级字段。
func nested(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

// mapOf 安全取出 map。
func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// TaskRunResult 是一次自动任务执行结果。
type TaskRunResult struct {
	CampaignCode   string `json:"campaign_code"`
	Title          string `json:"title,omitempty"`
	Status         string `json:"status"` // claimed / completed / not_completed / skipped / failed
	Reason         string `json:"reason,omitempty"`
	RewardAmount   int64  `json:"reward_amount,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// autoTaskSkill 把活动任务类型映射到网关技能 ID。
func autoTaskSkill(campaignType string) int {
	switch campaignType {
	case "GenWord", "gen_word":
		return SkillWrite
	case "GenExcel", "gen_excel":
		return SkillCodeInterpreter
	case "GenPPT":
		return SkillAiPPT
	case "ScheduleSkill":
		return SkillWrite
	}
	return 0
}

// autoTaskPrompt 从任务推荐提示词中选择一条可发送的 prompt。
func autoTaskPrompt(t CampaignTask) (string, bool) {
	for _, raw := range t.RecommendPrompts {
		if obj, ok := decodeObj(raw); ok {
			if q := firstNonEmpty(str(obj["sendQuery"]), str(obj["send_query"]), str(obj["prompt"])); q != "" {
				return q, true
			}
		}
		if strings.TrimSpace(raw) != "" && !strings.HasPrefix(strings.TrimSpace(raw), "{") {
			return raw, true
		}
	}
	if t.CampaignCode == "pc_task_complete_work_task_daily" {
		return "帮我完成一项工作任务：整理今日待办清单，按优先级排序并给出执行顺序。", true
	}
	return "", false
}

// shouldAutoRunTask 判断任务是否适合由网关尝试执行。
func shouldAutoRunTask(t CampaignTask) bool {
	if t.Status == TaskStatusRewarded {
		return false
	}
	switch t.CampaignCode {
	case "select_work_fields", "daily_signin":
		return false
	}
	if t.TaskKind == "points_milestone" || t.MilestoneAt > 0 {
		return false
	}
	return true
}

// StartCampaignTask 标记任务开始（创建任务实例）。
//
// 对齐客户端：EventReport(scene=campaign_task_click, params.campaign_group_code)。
func (c *Client) StartCampaignTask(ctx context.Context, a *Account, group, itemCode string) (string, error) {
	if itemCode == "" {
		return "", fmt.Errorf("campaign_item_code 不能为空")
	}
	if group == "" {
		group = IncentiveCampaignGroupV3
	}
	raw, err := c.postJSON(ctx, a, "/alice/ug/event/report", map[string]any{
		"scene":              "campaign_task_click",
		"campaign_item_code": itemCode,
		"params":             map[string]any{"campaign_group_code": group},
	})
	if err != nil {
		return "", err
	}
	obj, ok := decodeObj(string(raw))
	if !ok {
		return "", fmt.Errorf("任务开始响应解析失败: %s", truncate(string(raw), 200))
	}
	if code := intOf(obj["code"]); code != 0 {
		return "", fmt.Errorf("任务开始失败 (code=%d): %s", code, truncate(firstNonEmpty(str(obj["message"]), str(obj["msg"])), 200))
	}
	data, _ := obj["data"].(map[string]any)
	return firstNonEmpty(str(data["task_id"]), str(data["taskId"])), nil
}

// RunDailyTasks 尝试自动执行积分任务并领取奖励。
//
// 流程：读取任务 -> 发送推荐提示词（WorkTask 模式）-> 轮询任务状态 ->
// 领取奖励 -> 清理会话。只执行任务本身允许的推荐提示词，不伪造任务完成。
func (c *Client) RunDailyTasks(ctx context.Context, a *Account, group string, maxTasks int, onProgress func(TaskRunResult)) ([]TaskRunResult, error) {
	if group == "" {
		group = IncentiveCampaignGroupV3
	}
	if maxTasks <= 0 {
		maxTasks = 3
	}
	if maxTasks > 10 {
		maxTasks = 10
	}
	detail, err := c.CampaignDetail(ctx, a, group)
	if err != nil {
		return nil, err
	}
	results := make([]TaskRunResult, 0)
	attempted := 0
	for _, t := range detail.Tasks {
		if attempted >= maxTasks {
			break
		}
		if !shouldAutoRunTask(t) {
			continue
		}
		res := TaskRunResult{CampaignCode: t.CampaignCode, Title: t.Title}
		if len(t.Rewards) > 0 {
			res.RewardAmount = t.Rewards[0].Amount
		}

		// 已完成的任务直接领取。
		if t.Status == TaskStatusCompleted {
			if err := c.claimTask(ctx, a, group, t); err != nil {
				res.Status = "failed"
				res.Reason = err.Error()
			} else {
				res.Status = "claimed"
			}
			results = append(results, res)
			attempted++
			if onProgress != nil {
				onProgress(res)
			}
			continue
		}

		// 先向服务端登记任务开始，拿到 instance task id（对齐客户端 campaign_task_click）。
		instanceTaskID, _ := c.StartCampaignTask(ctx, a, group, t.CampaignCode)

		prompt, ok := autoTaskPrompt(t)
		if !ok {
			res.Status = "skipped"
			res.Reason = "no recommend prompt"
			results = append(results, res)
			continue
		}
		skill := autoTaskSkill(t.CampaignType)
		var convID string
		err := c.ChatStream(ctx, a, ChatRequest{
			Text:       prompt,
			ThinkLevel: ThinkOff,
			SkillID:    skill,
			AgentMode:  4, // WorkTask 模式（桌面端 d7D.F）
		}, func(ch CompletionChunk) error {
			if ch.ConvID != "" {
				convID = ch.ConvID
			}
			if ch.ErrorCode != 0 {
				return fmt.Errorf("上游错误 %d: %s", ch.ErrorCode, ch.ErrorMsg)
			}
			return nil
		})
		res.ConversationID = convID
		if err != nil {
			res.Status = "failed"
			res.Reason = err.Error()
		} else {
			// 给上游一点时间结算任务状态。
			if c.waitTaskDone(ctx, a, group, t.CampaignCode, 3*time.Minute) {
				if err := c.claimTaskWithID(ctx, a, group, t, instanceTaskID); err != nil {
					res.Status = "failed"
					res.Reason = err.Error()
				} else {
					res.Status = "claimed"
				}
			} else {
				res.Status = "not_completed"
				res.Reason = "task did not reach completed state"
			}
		}
		// 清理会话：先清空消息，再归档。
		if convID != "" {
			_ = c.ConversationAction(ctx, a, "clear", convID, "")
			_ = c.ConversationAction(ctx, a, "archive", convID, "")
		}
		results = append(results, res)
		attempted++
		if onProgress != nil {
			onProgress(res)
		}
		if err := sleepCtx(ctx, time.Second); err != nil {
			return results, err
		}
	}
	return results, nil
}

// claimTask 领取指定任务的奖励。
func (c *Client) claimTask(ctx context.Context, a *Account, group string, t CampaignTask) error {
	return c.claimTaskWithID(ctx, a, group, t, t.InstanceTaskID)
}

// claimTaskWithID 用指定实例 ID 领取奖励（为空时按 DirectClaim 处理）。
func (c *Client) claimTaskWithID(ctx context.Context, a *Account, group string, t CampaignTask, instanceTaskID string) error {
	taskID := firstNonEmpty(instanceTaskID, t.InstanceTaskID)
	if taskID == "" && t.RewardMode != RewardModeDirectClaim {
		return fmt.Errorf("任务缺少 instance_task_id，无法领取")
	}
	_, err := c.ClaimCampaignReward(ctx, a, taskID, firstNonEmpty(t.CampaignGroupCode, group), t.CampaignCode)
	return err
}

// waitTaskDone 轮询任务状态，直到 completed / rewarded 或超时。
func (c *Client) waitTaskDone(ctx context.Context, a *Account, group, code string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		detail, err := c.CampaignDetail(ctx, a, group)
		if err == nil {
			for _, t := range detail.Tasks {
				if t.CampaignCode != code {
					continue
				}
				if t.Status == TaskStatusCompleted || t.Status == TaskStatusRewarded {
					return true
				}
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		if err := sleepCtx(ctx, 5*time.Second); err != nil {
			return false
		}
	}
}

// CleanupTaskTempFiles 清理网关任务产生的临时文件（默认 1 小时前的 doubao2api-*）。
func CleanupTaskTempFiles() (int, error) {
	dir := os.TempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "doubao2api-") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(dir + string(os.PathSeparator) + e.Name()); err == nil {
			removed++
		}
	}
	return removed, nil
}
