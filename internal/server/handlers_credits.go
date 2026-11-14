package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"doubao2api/internal/doubao"
)

// 本文件是积分 / 签到 / 任务奖励端点。
//
// 所有端点都通过 runUpstream 走统一的选号、超时、日志与错误响应，
// 因此每个 handler 只剩「读参数 → 调上游 → 组装响应」三段。

// handleCredits 返回积分余额与可选任务。
//
//	GET /v1/credits?tasks=true
func (s *Server) handleCredits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET", "invalid_request_error")
		return
	}
	needTasks := strings.ToLower(r.URL.Query().Get("tasks")) != "false"

	var summary *doubao.CreditSummary
	acct, ok := s.runUpstream(w, r, upstreamCall{
		Label:     "credits",
		AccountID: r.URL.Query().Get("account_id"),
	}, func(ctx context.Context, a *doubao.Account) (int, error) {
		out, err := s.Up.CreditSummary(ctx, a, needTasks)
		if err != nil {
			return 0, err
		}
		summary = out
		return 1, nil
	})
	if !ok {
		return
	}

	resp := map[string]any{
		"object": "credit",
		"credit": summary.Credit,
	}
	if needTasks {
		resp["optional_tasks"] = summary.OptionalTask
	}
	if summary.RuleLink != "" {
		resp["credit_rule_link"] = map[string]any{
			"schema": summary.RuleLink,
			"text":   summary.RuleLinkText,
		}
	}
	resp["account_last_checkin"] = lastCheckinInfo(acct)
	writeJSON(w, http.StatusOK, resp)
}

// handleCreditHistory 返回积分流水。
//
//	GET /v1/credits/history?size=20&cursor=0&type=
func (s *Server) handleCreditHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET", "invalid_request_error")
		return
	}
	q := r.URL.Query()
	cursor, _ := strconv.ParseInt(q.Get("cursor"), 10, 64)
	size, _ := strconv.Atoi(q.Get("size"))

	var history *doubao.CreditHistory
	if _, ok := s.runUpstream(w, r, upstreamCall{
		Label:     "credit-history",
		AccountID: q.Get("account_id"),
	}, func(ctx context.Context, a *doubao.Account) (int, error) {
		out, err := s.Up.CreditHistory(ctx, a, cursor, size, q.Get("type"))
		if err != nil {
			return 0, err
		}
		history = out
		return len(out.Items), nil
	}); !ok {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object":      "list",
		"data":        history.Items,
		"next_cursor": history.NextCursor,
		"has_more":    history.HasMore,
	})
}

// handleCheckin 执行每日签到。
//
//	POST /v1/checkin {"scene":"sidebar"}
func (s *Server) handleCheckin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	var body struct {
		Scene     string `json:"scene"`
		AccountID string `json:"account_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}

	var result *doubao.CheckinResult
	acct, ok := s.runUpstream(w, r, upstreamCall{
		Label:     "checkin",
		AccountID: body.AccountID,
	}, func(ctx context.Context, a *doubao.Account) (int, error) {
		out, err := s.Up.DailyCheckIn(ctx, a, strings.TrimSpace(body.Scene))
		if err != nil {
			return 0, err
		}
		result = out
		return 1, nil
	})
	if !ok {
		return
	}

	// 只有真正签到成功才写账号状态：上游可能返回「已受理待确认」
	// （pending），此时不能把日期写死，否则会误判为今天已签到。
	if acct != nil && result.CheckedIn && !result.Pending {
		date := result.CheckinDate
		if date == "" {
			date = todayString()
		}
		s.Store.MarkCheckin(acct.ID, date, result.RewardAmount)
		_ = s.Store.Save()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":  "checkin",
		"result":  result,
		"account": lastCheckinInfo(acct),
	})
}

// handleTasks 返回积分任务 / 里程碑。
//
//	GET /v1/tasks?campaign_group_code=pc_incentive_task_mvp_v3
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET", "invalid_request_error")
		return
	}
	group := strings.TrimSpace(r.URL.Query().Get("campaign_group_code"))

	var detail *doubao.CampaignDetail
	if _, ok := s.runUpstream(w, r, upstreamCall{
		Label:     "tasks",
		AccountID: r.URL.Query().Get("account_id"),
	}, func(ctx context.Context, a *doubao.Account) (int, error) {
		out, err := s.Up.CampaignDetail(ctx, a, group)
		if err != nil {
			return 0, err
		}
		detail = out
		return len(out.Tasks), nil
	}); !ok {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "task_list",
		"data":   detail,
	})
}

// handleTaskClaim 领取单个任务奖励。
//
//	POST /v1/tasks/claim
//	{"task_id":"...","campaign_group_code":"...","campaign_item_code":"..."}
func (s *Server) handleTaskClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	var body struct {
		TaskID            string `json:"task_id"`
		CampaignGroupCode string `json:"campaign_group_code"`
		CampaignItemCode  string `json:"campaign_item_code"`
		AccountID         string `json:"account_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	itemCode := strings.TrimSpace(body.CampaignItemCode)
	if itemCode == "" {
		writeErr(w, http.StatusBadRequest, "campaign_item_code 不能为空", "invalid_request_error")
		return
	}

	var raw map[string]any
	if _, ok := s.runUpstream(w, r, upstreamCall{
		Label:     "task-claim",
		AccountID: body.AccountID,
	}, func(ctx context.Context, a *doubao.Account) (int, error) {
		out, err := s.Up.ClaimCampaignReward(ctx, a,
			strings.TrimSpace(body.TaskID), strings.TrimSpace(body.CampaignGroupCode), itemCode)
		raw = out
		return 1, err
	}); !ok {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": raw})
}

// handleTaskClaimCompleted 自动领取「已完成 / 直接领取」的任务奖励。
//
//	POST /v1/tasks/claim-completed {"campaign_group_code":"pc_incentive_task_mvp_v3"}
func (s *Server) handleTaskClaimCompleted(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	var body struct {
		CampaignGroupCode string `json:"campaign_group_code"`
		AccountID         string `json:"account_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	group := strings.TrimSpace(body.CampaignGroupCode)
	if group == "" {
		group = incentivesGroup
	}

	var (
		results []doubao.ClaimResult
		detail  *doubao.CampaignDetail
	)
	if _, ok := s.runUpstream(w, r, upstreamCall{
		Label:     "task-claim",
		AccountID: body.AccountID,
	}, func(ctx context.Context, a *doubao.Account) (int, error) {
		out, d, err := s.Up.ClaimCompletedCampaignRewards(ctx, a, group)
		results, detail = out, d
		return countClaimed(out), err
	}); !ok {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"claimed_count": countClaimed(results),
		"pending_count": countByStatus(results, "pending"),
		"failed_count":  countByStatus(results, "failed"),
		"results":       results,
		"detail":        detail,
	})
}

// handleTaskRun 尝试自动执行积分任务并领取奖励。
//
//	POST /v1/tasks/run {"max_tasks":3,"campaign_group_code":"pc_incentive_task_mvp_v3"}
func (s *Server) handleTaskRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	var body struct {
		MaxTasks          int    `json:"max_tasks"`
		CampaignGroupCode string `json:"campaign_group_code"`
		AccountID         string `json:"account_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	group := strings.TrimSpace(body.CampaignGroupCode)
	if group == "" {
		group = incentivesGroup
	}
	maxTasks := body.MaxTasks
	if maxTasks <= 0 {
		maxTasks = s.Store.Settings().AutoTaskLimit
	}
	if maxTasks <= 0 {
		maxTasks = 3
	}

	var results []doubao.TaskRunResult
	acct, ok := s.runUpstream(w, r, upstreamCall{
		Label:      "task-run",
		AccountID:  body.AccountID,
		MinTimeout: 15 * time.Minute, // 批量做任务的耗时远高于普通端点
	}, func(ctx context.Context, a *doubao.Account) (int, error) {
		out, err := s.Up.RunDailyTasks(ctx, a, group, maxTasks, nil)
		results = out
		return countClaimedTasks(out), err
	})
	// 无论成功失败都清理任务临时文件（上游会在本地留下载产物）。
	cleaned, _ := doubao.CleanupTaskTempFiles()
	if !ok {
		return
	}

	if acct != nil {
		s.Store.MarkTaskRun(acct.ID, todayString(), len(results))
		_ = s.Store.Save()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"claimed_count":      countClaimedTasks(results),
		"results":            results,
		"cleaned_temp_files": cleaned,
		"account":            lastCheckinInfo(acct),
	})
}

// countClaimed 统计已领取的奖励数。
func countClaimed(results []doubao.ClaimResult) int {
	n := 0
	for _, r := range results {
		if r.Status == "claimed" {
			n++
		}
	}
	return n
}

// countByStatus 统计指定状态的领取结果数（pending / failed / skipped）。
func countByStatus(results []doubao.ClaimResult, status string) int {
	n := 0
	for _, r := range results {
		if r.Status == status {
			n++
		}
	}
	return n
}

// countClaimedTasks 统计任务执行后已领取的奖励数。
func countClaimedTasks(results []doubao.TaskRunResult) int {
	n := 0
	for _, r := range results {
		if r.Status == "claimed" {
			n++
		}
	}
	return n
}
