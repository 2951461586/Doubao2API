package server

import (
	"context"
	"log"
	"net/http"
	"time"

	"doubao2api/internal/account"
	"doubao2api/internal/doubao"
)

// incentivesGroup 是桌面端「积分任务」默认活动组。
const incentivesGroup = doubao.IncentiveCampaignGroupV3

// dayLayout 是账号内「每日」状态使用的日期格式（本地时区）。
const dayLayout = "2006-01-02"

// todayString 返回本地时区的今日日期（YYYY-MM-DD）。
//
// 签到 / 自动任务的「每天一次」判断都基于它，集中一处避免格式串散落。
func todayString() string { return time.Now().Format(dayLayout) }

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
	today := todayString()
	for _, a := range s.Store.Accounts() {
		if !a.Enabled {
			continue
		}
		// 已登记到重试队列的账号由 retryPendingCheckins 负责，避免重复请求上游。
		if a.PendingCheckinDate == today && st.RetryPendingCheckin {
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
			claimed, pending := 0, 0
			for _, res := range results {
				switch res.Status {
				case "claimed":
					claimed++
				case "pending":
					pending++
				}
			}
			if err != nil {
				log.Printf("自动领取任务失败 %s: %v", a.Name, err)
			} else if claimed > 0 || pending > 0 {
				log.Printf("自动领取任务 %s: %d 个已领取，%d 个确认中", a.Name, claimed, pending)
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

func lastCheckinInfo(a *doubao.Account) map[string]any {
	if a == nil {
		return nil
	}
	out := map[string]any{
		"id":                a.ID,
		"name":              a.Name,
		"last_checkin_date": a.LastCheckinDate,
		"last_checkin_at":   a.LastCheckinAt,
		"last_reward":       a.LastCheckinReward,
	}
	if a.PendingCheckinDate != "" || a.PendingCheckinAt > 0 {
		out["pending_checkin"] = map[string]any{
			"date":     a.PendingCheckinDate,
			"at":       a.PendingCheckinAt,
			"attempts": a.PendingCheckinTries,
		}
	}
	return out
}

// --- 待确认签到的后台重试队列 ---

const (
	// pendingCheckinRetryInterval 是 pending 签到的复查间隔。
	pendingCheckinRetryInterval = time.Hour
	// maxPendingCheckinTries 是同一账号当日最多复查次数（每小时一次，覆盖半天）。
	//
	// 用尽后不再自动重试，但记录会保留在状态文件里，控制台可见，
	// 便于判断「上游到底恢复了没有」，而不是悄悄丢掉。
	maxPendingCheckinTries = 6
)

// pendingAction 是对一条待确认签到的处置决定。
type pendingAction int

const (
	// pendingRetry 表示应当立刻重试一次。
	pendingRetry pendingAction = iota
	// pendingDropStale 表示记录已跨天，直接作废（签到本身按天幂等）。
	pendingDropStale
	// pendingGiveUp 表示今日重试次数用尽，保留记录但不再自动重试。
	pendingGiveUp
)

// pendingCheckinAction 决定一条待确认签到怎么处理（纯函数，便于单测）。
func pendingCheckinAction(a *account.Account, today string, maxTries int) pendingAction {
	if a == nil {
		return pendingDropStale
	}
	if a.PendingCheckinDate != "" && a.PendingCheckinDate != today {
		return pendingDropStale
	}
	if maxTries <= 0 {
		maxTries = maxPendingCheckinTries
	}
	if a.PendingCheckinTries >= maxTries {
		return pendingGiveUp
	}
	return pendingRetry
}

// StartPendingCheckinRetry 启动「待确认签到」的后台重试队列。
//
// 只有开启 retry_pending_checkin 时才会真正发起上游请求：
// 上游把签到奖励做成异步发放（biz_code=3），等它恢复后这里会自动把
// 账号状态补成「已签到」，无需人工再点一次。
func (s *Server) StartPendingCheckinRetry(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(pendingCheckinRetryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.retryPendingCheckins(ctx)
			}
		}
	}()
}

// checkinFn 是一次签到调用（生产环境为 s.Up.DailyCheckIn，测试可注入假实现）。
type checkinFn func(ctx context.Context, a *account.Account, scene string) (*doubao.CheckinResult, error)

// retryPendingCheckins 对处于「确认中」的签到逐个复查一次。
func (s *Server) retryPendingCheckins(ctx context.Context) {
	s.retryPendingCheckinsWith(ctx, s.Up.DailyCheckIn)
}

// retryPendingCheckinsWith 是 retryPendingCheckins 的可测实现。
func (s *Server) retryPendingCheckinsWith(ctx context.Context, do checkinFn) {
	if !s.Store.Settings().RetryPendingCheckin {
		return
	}
	today := todayString()
	for _, a := range s.Store.PendingCheckins() {
		switch pendingCheckinAction(a, today, maxPendingCheckinTries) {
		case pendingDropStale:
			// 昨天的 pending 已无意义（每日签到按天幂等）。
			s.Store.ClearCheckinPending(a.ID)
			_ = s.Store.Save()
			continue
		case pendingGiveUp:
			continue // 保留记录，等人工或次日覆盖
		case pendingRetry:
		}

		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		res, err := do(cctx, a, "")
		cancel()

		switch {
		case err != nil:
			log.Printf("签到复查失败 %s: %v（第 %d 次）", a.Name, err, a.PendingCheckinTries+1)
			s.Store.RecordCheckinRetry(a.ID, today)
		case res.CheckedIn && !res.Pending:
			date := firstNonEmptyStr(res.CheckinDate, today)
			s.Store.MarkCheckin(a.ID, date, res.RewardAmount)
			s.Store.ClearCheckinPending(a.ID)
			log.Printf("签到已确认 %s: %s +%d（连续 %d 天）", a.Name, date, res.RewardAmount, res.ContinuousDays)
		default:
			// 仍在确认中，或上游依旧没有执行签到动作。
			s.Store.RecordCheckinRetry(a.ID, today)
			log.Printf("签到仍在确认中 %s（第 %d 次，upstream pending=%v）", a.Name, a.PendingCheckinTries+1, res.Pending)
		}
		_ = s.Store.Save()
	}
}

// firstNonEmptyStr 返回第一个非空字符串。
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// handleAdminCheckinRetry 立即执行一次「待确认签到」的复查队列。
//
//	POST /admin/api/checkin/retry
//
// 与后台每小时一次的队列是同一套逻辑（仍受 retry_pending_checkin 开关约束），
// 便于在上游恢复后手动催一次，或排障时观察队列状态。
func (s *Server) handleAdminCheckinRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	enabled := s.Store.Settings().RetryPendingCheckin
	s.retryPendingCheckins(r.Context())

	remaining := make([]map[string]any, 0, 4)
	for _, a := range s.Store.PendingCheckins() {
		remaining = append(remaining, map[string]any{
			"id":       a.ID,
			"name":     a.Name,
			"date":     a.PendingCheckinDate,
			"attempts": a.PendingCheckinTries,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"enabled":      enabled,
		"remaining":    remaining,
		"remain_count": len(remaining),
	})
}
