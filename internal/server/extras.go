package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"doubao2api/internal/doubao"
	"doubao2api/internal/registry"
)

// handleSkills 返回上游技能清单（GET /v1/skills）。
//
// 数据来自 POST /samantha/skill/list；本地再补充别名、默认提示词与模型映射，
// 便于客户端发现「哪些技能可用 / 用哪个模型 ID 触发」。
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET", "invalid_request_error")
		return
	}
	start := time.Now()
	key := extractKey(r)
	settings := s.Store.Settings()
	timeout := time.Duration(settings.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}

	var list *doubao.SkillList
	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		l, err := s.Up.Skills(ctx, acct)
		if err != nil {
			return err
		}
		list = l
		return nil
	})
	if err != nil {
		s.record(key, acct, "skills", false, http.StatusBadGateway, start, 0, 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}

	// 上游返回的技能 -> 可读信息
	type upstreamSkill struct {
		doubao.Skill
	}
	up := map[int]upstreamSkill{}
	if list != nil {
		for _, sk := range list.Skills {
			up[sk.SkillType] = upstreamSkill{sk}
		}
	}

	modelBySkill := map[int]string{}
	for _, m := range registry.Builtin {
		if m.Skill != 0 {
			if _, ok := modelBySkill[m.Skill]; !ok {
				modelBySkill[m.Skill] = m.ID
			}
		}
	}

	data := make([]map[string]any, 0, len(doubao.SkillMetas)+len(up))
	seen := map[int]bool{}
	for _, meta := range doubao.SkillMetas {
		seen[meta.Type] = true
		item := map[string]any{
			"skill_type": meta.Type,
			"name":       meta.Name,
			"aliases":    meta.Aliases,
			"model":      modelBySkill[meta.Type],
		}
		if meta.Desc != "" {
			item["description"] = meta.Desc
		}
		if meta.DefaultPrompt != "" {
			item["default_prompt"] = meta.DefaultPrompt
		}
		if sk, ok := up[meta.Type]; ok {
			if sk.Name != "" {
				item["name"] = sk.Name
			}
			if sk.ShowNameShort != "" {
				item["short_name"] = sk.ShowNameShort
			}
			if sk.Description != "" {
				item["description"] = sk.Description
			}
			if sk.DefaultPrompt != "" {
				item["default_prompt"] = sk.DefaultPrompt
			}
		}
		data = append(data, item)
	}
	// 上游独有的技能也一并暴露
	for t, sk := range up {
		if seen[t] {
			continue
		}
		data = append(data, map[string]any{
			"skill_type":     t,
			"name":           sk.Name,
			"short_name":     sk.ShowNameShort,
			"description":    sk.Description,
			"default_prompt": sk.DefaultPrompt,
			"model":          modelBySkill[t],
		})
	}

	s.record(key, acct, "skills", false, http.StatusOK, start, 0, len(data), nil)
	resp := map[string]any{"object": "list", "data": data}
	if list != nil {
		resp["input_skill_types"] = list.Input
		resp["sidebar_skill_types"] = list.Sidebar
		resp["hide_in_lp"] = list.HideInLP
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleConversations 处理会话列表与单会话操作。
//
//	GET    /v1/conversations?limit=20&offset=0&types=3
//	PATCH  /v1/conversations/{id}          {"name":"..."}
//	DELETE /v1/conversations/{id}          归档
//	POST   /v1/conversations/{id}/pin|unpin|archive|clear
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/conversations")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET", "invalid_request_error")
			return
		}
		s.listConversations(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	id := strings.TrimSpace(parts[0])
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少 conversation id", "invalid_request_error")
		return
	}

	switch r.Method {
	case http.MethodPatch:
		action = "rename"
	case http.MethodDelete:
		action = "archive"
	case http.MethodPost:
		if action == "" {
			action = "archive"
		}
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法", "invalid_request_error")
		return
	}

	name := ""
	if action == "rename" {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体解析失败", "invalid_request_error")
			return
		}
		name = strings.TrimSpace(body.Name)
		if name == "" {
			writeErr(w, http.StatusBadRequest, "name 不能为空", "invalid_request_error")
			return
		}
	}

	start := time.Now()
	key := extractKey(r)
	settings := s.Store.Settings()
	timeout := time.Duration(settings.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		return s.Up.ConversationAction(ctx, acct, action, id, name)
	})
	if err != nil {
		s.record(key, acct, "conversations", false, http.StatusBadGateway, start, 0, 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	s.record(key, acct, "conversations", false, http.StatusOK, start, 0, 0, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": action, "id": id})
}

// listConversations 拉取一页会话。
func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	var types []int
	if raw := strings.TrimSpace(q.Get("types")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				types = append(types, n)
			}
		}
	}

	start := time.Now()
	key := extractKey(r)
	settings := s.Store.Settings()
	timeout := time.Duration(settings.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	var page *doubao.ConversationPage
	acct, err := s.withAccount(r.Context(), q.Get("account_id"), timeout, func(ctx context.Context, acct *doubao.Account) error {
		p, err := s.Up.Conversations(ctx, acct, offset, limit, types)
		if err != nil {
			return err
		}
		page = p
		return nil
	})
	if err != nil {
		s.record(key, acct, "conversations", false, http.StatusBadGateway, start, 0, 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	if page == nil {
		page = &doubao.ConversationPage{}
	}
	s.record(key, acct, "conversations", false, http.StatusOK, start, 0, len(page.Conversations), nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"object":   "list",
		"data":     page.Conversations,
		"has_more": page.HasMore,
		"offset":   offset,
		"limit":    limit,
	})
}

// lyricsRequest 是 /v1/music/lyrics 的请求体。
type lyricsRequest struct {
	Prompt string `json:"prompt"`
	Theme  string `json:"theme"`
	Genre  string `json:"genre"`
	Mood   string `json:"mood"`
	Lyric  string `json:"lyric"`
}

// handleMusicLyrics 是 POST /v1/music/lyrics 的入口。
//
// 上游：/alice/media/bigmusic/lyrics_web（带 genre/mood）或 /lyrics（仅 prompt）。
func (s *Server) handleMusicLyrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	var req lyricsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	prompt := strings.TrimSpace(firstNonEmpty(req.Prompt, req.Theme, req.Lyric))
	if prompt == "" {
		writeErr(w, http.StatusBadRequest, "prompt 不能为空", "invalid_request_error")
		return
	}

	start := time.Now()
	key := extractKey(r)
	settings := s.Store.Settings()
	timeout := time.Duration(settings.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}

	var lines []string
	acct, err := s.withAccountRetry(r.Context(), timeout, func(ctx context.Context, acct *doubao.Account) error {
		out, err := s.Up.MusicLyrics(ctx, acct, prompt, req.Genre, req.Mood)
		if err != nil {
			return err
		}
		lines = out
		return nil
	})
	if err != nil {
		s.record(key, acct, "music-lyrics", false, http.StatusBadGateway, start, len(prompt), 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	data := make([]map[string]any, 0, len(lines))
	for i, line := range lines {
		data = append(data, map[string]any{"index": i, "text": line})
	}
	s.record(key, acct, "music-lyrics", false, http.StatusOK, start, len(prompt), len(lines), nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"created": time.Now().Unix(),
		"prompt":  prompt,
		"data":    data,
	})
}

// handleCapabilities 汇总网关能力（模型 / 技能 / 端点），便于客户端自发现。
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	models := registry.List(s.Store.Settings().ModelAliases)
	skills := make([]map[string]any, 0, len(doubao.SkillMetas))
	for _, m := range doubao.SkillMetas {
		skills = append(skills, map[string]any{
			"skill_type":     m.Type,
			"name":           m.Name,
			"aliases":        m.Aliases,
			"default_prompt": m.DefaultPrompt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "capabilities",
		"upstream": map[string]any{
			"app_id":          doubao.AppID,
			"version_code":    doubao.VersionCode,
			"pc_version":      doubao.PCVersion,
			"runtime_version": doubao.RuntimeVersion,
			"chromium":        doubao.ChromiumVersion,
		},
		"models": models,
		"skills": skills,
		"rewards": map[string]any{
			"campaign_group":  doubao.IncentiveCampaignGroupV3,
			"auto_checkin":    s.Store.Settings().AutoCheckin,
			"auto_claim":      s.Store.Settings().AutoClaimTasks,
			"auto_do_tasks":   s.Store.Settings().AutoDoTasks,
			"auto_task_limit": s.Store.Settings().AutoTaskLimit,
		},
		"endpoints": []string{
			"/v1/chat/completions", "/v1/messages", "/v1/models", "/v1/skills",
			"/v1/conversations", "/v1/images/generations", "/v1/videos/generations",
			"/v1/music/lyrics", "/v1/music/audio", "/v1/capabilities",
			"/v1/credits", "/v1/credits/history", "/v1/checkin",
			"/v1/tasks", "/v1/tasks/claim", "/v1/tasks/claim-completed", "/v1/tasks/run",
		},
	})
}

// incentivesGroup 是桌面端「积分任务」默认活动组。
const incentivesGroup = doubao.IncentiveCampaignGroupV3

// withAccount 在指定 account_id 时使用该账号，否则走账号池重试。
func (s *Server) withAccount(ctx context.Context, id string, timeout time.Duration, fn func(context.Context, *doubao.Account) error) (*doubao.Account, error) {
	if strings.TrimSpace(id) == "" {
		return s.withAccountRetry(ctx, timeout, fn)
	}
	a := s.Store.Account(strings.TrimSpace(id))
	if a == nil {
		return nil, fmt.Errorf("账号不存在: %s", id)
	}
	if !a.Enabled {
		return a, fmt.Errorf("账号已停用: %s", a.Name)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := fn(cctx, a)
	if err != nil {
		s.Store.MarkAccountResult(a.ID, false, err.Error())
	} else {
		s.Store.MarkAccountResult(a.ID, true, "")
	}
	return a, err
}

// handleCredits 返回积分余额与可选任务。
//
//	GET /v1/credits?tasks=true
func (s *Server) handleCredits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET", "invalid_request_error")
		return
	}
	needTasks := strings.ToLower(r.URL.Query().Get("tasks")) != "false"
	accountID := r.URL.Query().Get("account_id")
	start := time.Now()
	key := extractKey(r)
	timeout := s.requestTimeout()

	var summary *doubao.CreditSummary
	acct, err := s.withAccount(r.Context(), accountID, timeout, func(ctx context.Context, a *doubao.Account) error {
		out, err := s.Up.CreditSummary(ctx, a, needTasks)
		if err != nil {
			return err
		}
		summary = out
		return nil
	})
	if err != nil {
		s.record(key, acct, "credits", false, http.StatusBadGateway, start, 0, 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
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
	s.record(key, acct, "credits", false, http.StatusOK, start, 0, 1, nil)
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
	start := time.Now()
	key := extractKey(r)
	timeout := s.requestTimeout()

	var history *doubao.CreditHistory
	acct, err := s.withAccount(r.Context(), q.Get("account_id"), timeout, func(ctx context.Context, a *doubao.Account) error {
		out, err := s.Up.CreditHistory(ctx, a, cursor, size, q.Get("type"))
		if err != nil {
			return err
		}
		history = out
		return nil
	})
	if err != nil {
		s.record(key, acct, "credit-history", false, http.StatusBadGateway, start, 0, 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	s.record(key, acct, "credit-history", false, http.StatusOK, start, 0, len(history.Items), nil)
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
	start := time.Now()
	key := extractKey(r)
	timeout := s.requestTimeout()

	var result *doubao.CheckinResult
	acct, err := s.withAccount(r.Context(), body.AccountID, timeout, func(ctx context.Context, a *doubao.Account) error {
		out, err := s.Up.DailyCheckIn(ctx, a, strings.TrimSpace(body.Scene))
		if err != nil {
			return err
		}
		result = out
		return nil
	})
	if err != nil {
		s.record(key, acct, "checkin", false, http.StatusBadGateway, start, 0, 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	if acct != nil {
		date := result.CheckinDate
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		s.Store.MarkCheckin(acct.ID, date, result.RewardAmount)
		_ = s.Store.Save()
	}
	s.record(key, acct, "checkin", false, http.StatusOK, start, 0, 1, nil)
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
	accountID := r.URL.Query().Get("account_id")
	start := time.Now()
	key := extractKey(r)
	timeout := s.requestTimeout()

	var detail *doubao.CampaignDetail
	acct, err := s.withAccount(r.Context(), accountID, timeout, func(ctx context.Context, a *doubao.Account) error {
		out, err := s.Up.CampaignDetail(ctx, a, group)
		if err != nil {
			return err
		}
		detail = out
		return nil
	})
	if err != nil {
		s.record(key, acct, "tasks", false, http.StatusBadGateway, start, 0, 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	s.record(key, acct, "tasks", false, http.StatusOK, start, 0, len(detail.Tasks), nil)
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
	if strings.TrimSpace(body.CampaignItemCode) == "" {
		writeErr(w, http.StatusBadRequest, "campaign_item_code 不能为空", "invalid_request_error")
		return
	}
	start := time.Now()
	key := extractKey(r)
	timeout := s.requestTimeout()

	var raw map[string]any
	acct, err := s.withAccount(r.Context(), body.AccountID, timeout, func(ctx context.Context, a *doubao.Account) error {
		out, err := s.Up.ClaimCampaignReward(ctx, a, strings.TrimSpace(body.TaskID), strings.TrimSpace(body.CampaignGroupCode), strings.TrimSpace(body.CampaignItemCode))
		raw = out
		return err
	})
	if err != nil {
		s.record(key, acct, "task-claim", false, http.StatusBadGateway, start, 0, 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	s.record(key, acct, "task-claim", false, http.StatusOK, start, 0, 1, nil)
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
	start := time.Now()
	key := extractKey(r)
	timeout := s.requestTimeout()

	var results []doubao.ClaimResult
	var detail *doubao.CampaignDetail
	acct, err := s.withAccount(r.Context(), body.AccountID, timeout, func(ctx context.Context, a *doubao.Account) error {
		out, d, err := s.Up.ClaimCompletedCampaignRewards(ctx, a, group)
		results = out
		detail = d
		return err
	})
	if err != nil {
		s.record(key, acct, "task-claim", false, http.StatusBadGateway, start, 0, 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	claimed := 0
	for _, res := range results {
		if res.Status == "claimed" {
			claimed++
		}
	}
	s.record(key, acct, "task-claim", false, http.StatusOK, start, 0, claimed, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"claimed_count": claimed,
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
	start := time.Now()
	key := extractKey(r)
	timeout := s.requestTimeout()
	if timeout < 15*time.Minute {
		timeout = 15 * time.Minute
	}
	var results []doubao.TaskRunResult
	acct, err := s.withAccount(r.Context(), body.AccountID, timeout, func(ctx context.Context, a *doubao.Account) error {
		out, err := s.Up.RunDailyTasks(ctx, a, group, maxTasks, nil)
		results = out
		return err
	})
	cleaned, _ := doubao.CleanupTaskTempFiles()
	if err != nil {
		s.record(key, acct, "task-run", false, http.StatusBadGateway, start, 0, 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	if acct != nil {
		s.Store.MarkTaskRun(acct.ID, time.Now().Format("2006-01-02"), len(results))
		_ = s.Store.Save()
	}
	claimed := 0
	for _, res := range results {
		if res.Status == "claimed" {
			claimed++
		}
	}
	s.record(key, acct, "task-run", false, http.StatusOK, start, 0, claimed, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"claimed_count":      claimed,
		"results":            results,
		"cleaned_temp_files": cleaned,
		"account":            lastCheckinInfo(acct),
	})
}

// StartAutoRewards 启动每日自动签到 / 自动领取已完成任务。
//
// 默认关闭；在控制台设置 auto_checkin / auto_claim_tasks 后生效。
// TriggerAutoRewards 在设置变更后立即执行一次自动签到 / 自动领取。
func (s *Server) TriggerAutoRewards() {
	st := s.Store.Settings()
	if st.AutoCheckin || st.AutoClaimTasks || st.AutoDoTasks {
		// 后台任务不能复用请求上下文（handler 返回即取消），这里用独立上下文。
		go s.runAutoRewards(context.Background())
	}
}

func (s *Server) StartAutoRewards(ctx context.Context) {
	go func() {
		s.runAutoRewards(ctx)
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runAutoRewards(ctx)
			}
		}
	}()
}

func (s *Server) runAutoRewards(ctx context.Context) {
	st := s.Store.Settings()
	if !st.AutoCheckin && !st.AutoClaimTasks {
		return
	}
	today := time.Now().Format("2006-01-02")
	for _, a := range s.Store.Accounts() {
		if !a.Enabled {
			continue
		}
		if st.AutoCheckin && a.LastCheckinDate != today {
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			res, err := s.Up.DailyCheckIn(cctx, a, "")
			cancel()
			if err != nil {
				log.Printf("自动签到失败 %s: %v", a.Name, err)
			} else {
				date := res.CheckinDate
				if date == "" {
					date = today
				}
				s.Store.MarkCheckin(a.ID, date, res.RewardAmount)
				_ = s.Store.Save()
				log.Printf("自动签到成功 %s: %s +%d（连续 %d 天）", a.Name, date, res.RewardAmount, res.ContinuousDays)
			}
		}
		if st.AutoClaimTasks {
			cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
			results, _, err := s.Up.ClaimCompletedCampaignRewards(cctx, a, incentivesGroup)
			cancel()
			claimed := 0
			for _, res := range results {
				if res.Status == "claimed" {
					claimed++
				}
			}
			if err != nil {
				log.Printf("自动领取任务失败 %s: %v", a.Name, err)
			} else if claimed > 0 {
				log.Printf("自动领取任务 %s: %d 个奖励已领取", a.Name, claimed)
			}
		}

		if st.AutoDoTasks {
			count := a.LastTaskCount
			if a.LastTaskDate != today {
				count = 0
			}
			limit := st.AutoTaskLimit
			if limit <= 0 {
				limit = 3
			}
			remaining := limit - count
			if remaining > 0 {
				cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
				results, err := s.Up.RunDailyTasks(cctx, a, incentivesGroup, remaining, nil)
				cancel()
				claimed := 0
				for _, res := range results {
					if res.Status == "claimed" {
						claimed++
					}
				}
				s.Store.MarkTaskRun(a.ID, today, len(results))
				_ = s.Store.Save()
				if cleaned, _ := doubao.CleanupTaskTempFiles(); cleaned > 0 {
					log.Printf("自动任务清理临时文件 %s: %d 个", a.Name, cleaned)
				}
				if err != nil {
					log.Printf("自动执行任务失败 %s: %v", a.Name, err)
				} else {
					log.Printf("自动执行任务 %s: 尝试 %d 个，领取 %d 个", a.Name, len(results), claimed)
				}
			}
		}
	}
}

// requestTimeout 返回上游请求超时。
func (s *Server) requestTimeout() time.Duration {
	timeout := time.Duration(s.Store.Settings().RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	return timeout
}

// lastCheckinInfo 返回账号的签到状态（不含敏感字段）。
func lastCheckinInfo(a *doubao.Account) map[string]any {
	if a == nil {
		return nil
	}
	return map[string]any{
		"id":                a.ID,
		"name":              a.Name,
		"last_checkin_date": a.LastCheckinDate,
		"last_checkin_at":   a.LastCheckinAt,
		"last_reward":       a.LastCheckinReward,
	}
}
