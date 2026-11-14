package server

import (
	"context"
	"log"
	"time"

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
