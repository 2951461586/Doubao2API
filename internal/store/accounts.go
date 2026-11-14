package store

import (
	"time"

	"doubao2api/internal/account"
)

// --- 账号 ---

// Accounts 返回账号快照（深拷贝，调用方可以随意修改）。
func (s *Store) Accounts() []*account.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*account.Account, 0, len(s.st.Accounts))
	for _, a := range s.st.Accounts {
		out = append(out, a.Clone())
	}
	return out
}

// Account 按 ID 查找账号（深拷贝；不存在返回 nil）。
func (s *Store) Account(id string) *account.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accountLocked(id).Clone()
}

// accountLocked 返回内部账号指针；调用方必须已持锁且不得泄漏该指针。
func (s *Store) accountLocked(id string) *account.Account {
	for _, a := range s.st.Accounts {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// UpsertAccount 按会话 ID 去重后写入账号；返回是否为新增。
//
// 入参会被拷贝一份存储，调用方后续修改自己那份不会影响状态。
func (s *Store) UpsertAccount(a *account.Account) bool {
	if a == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := a.Clone()
	for i, ex := range s.st.Accounts {
		if ex.SessionID() != "" && ex.SessionID() == cp.SessionID() {
			cp.ID = ex.ID
			// 保留调用方传入的运行期缓存（CSRF 等），其余字段整体替换。
			s.st.Accounts[i] = cp
			s.dirty.Store(true)
			return false
		}
	}
	s.st.Accounts = append(s.st.Accounts, cp)
	s.dirty.Store(true)
	return true
}

// RemoveAccount 删除账号。
func (s *Store) RemoveAccount(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.st.Accounts {
		if a.ID == id {
			s.st.Accounts = append(s.st.Accounts[:i], s.st.Accounts[i+1:]...)
			s.dirty.Store(true)
			return true
		}
	}
	return false
}

// NextAccount 以轮询方式挑一个可用账号（不排除任何账号）。
func (s *Store) NextAccount() *account.Account {
	return s.NextAccountExcluding(nil)
}

// NextAccountExcluding 挑一个未试过的、未在冷却中的可用账号。
//
// 选择规则（按优先级）：
//  1. 已启用、未在 exclude 里、未在冷却中；
//  2. 失败次数最少；
//  3. 最久未被使用（轮询公平性）。
//
// exclude 用于「一次请求内不重复选同一账号」：此前换号重试可能连续选中同一个，
// 导致 3 个账号的池子实际只用了 1 个。
func (s *Store) NextAccountExcluding(exclude map[string]bool) *account.Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.st.Accounts)
	if n == 0 {
		return nil
	}
	now := time.Now().Unix()
	best, bestIdx := s.pickLocked(exclude, now, true)
	if best == nil {
		// 没有任何账号处于「可用且不在冷却中」：退回忽略冷却，
		// 保证单账号 / 全部被限流的场景下仍然能发出请求，而不是直接失败。
		best, bestIdx = s.pickLocked(exclude, now, false)
	}
	if best == nil {
		return nil
	}
	s.rotate = (bestIdx + 1) % n
	// 在「选中」时就更新 LastUsed：并发请求才会分散到不同账号。
	// 只在请求完成后更新的话，同一时刻的 N 个请求会看到相同的 LastUsed，
	// 全部选中同一个账号（此前实测并发 3 路全落在同一账号上）。
	best.LastUsed = now
	s.dirty.Store(true)
	return best.Clone()
}

// pickLocked 在持锁状态下选一个账号；honorCooldown=false 时忽略冷却。
func (s *Store) pickLocked(exclude map[string]bool, now int64, honorCooldown bool) (*account.Account, int) {
	n := len(s.st.Accounts)
	var best *account.Account
	bestIdx := -1
	for i := 0; i < n; i++ {
		idx := (s.rotate + i) % n
		a := s.st.Accounts[idx]
		if !a.Enabled || exclude[a.ID] {
			continue
		}
		if honorCooldown && a.CooldownUntil > now {
			continue
		}
		if best == nil || betterAccount(a, best) {
			best, bestIdx = a, idx
		}
	}
	return best, bestIdx
}

// betterAccount 报告 a 是否比 b 更值得选。
func betterAccount(a, b *account.Account) bool {
	if a.Failures != b.Failures {
		return a.Failures < b.Failures
	}
	return a.LastUsed < b.LastUsed
}

// cooldownFor 返回第 failures 次失败后的冷却时长。
//
// 退避阶梯：30s → 2m → 10m → 30m。上限 30 分钟，避免一个真挂掉的账号
// 在半小时内反复拖慢每个请求。
func cooldownFor(failures int) time.Duration {
	switch {
	case failures <= 1:
		return 30 * time.Second
	case failures == 2:
		return 2 * time.Minute
	case failures == 3:
		return 10 * time.Minute
	default:
		return 30 * time.Minute
	}
}

// MarkAccountResult 记录一次账号调用结果。
func (s *Store) MarkAccountResult(id string, ok bool, note string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.st.Accounts {
		if a.ID != id {
			continue
		}
		now := time.Now()
		a.LastUsed = now.Unix()
		if ok {
			a.Failures = 0
			a.Status = "ok"
			a.LastErr = ""
			a.CooldownUntil = 0
			a.LastSuccess = now.Unix()
			if note != "" {
				a.Note = note
			}
		} else {
			a.Failures++
			a.Status = "error"
			a.LastErr = note
			a.CooldownUntil = now.Add(cooldownFor(a.Failures)).Unix()
		}
		s.dirty.Store(true)
		return
	}
}

// ToggleAccount 切换账号启用状态；返回切换后的值。
func (s *Store) ToggleAccount(id string) (enabled bool, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accountLocked(id)
	if a == nil {
		return false, false
	}
	a.Enabled = !a.Enabled
	s.dirty.Store(true)
	return a.Enabled, true
}

// SetAccountEnabled 设置账号启用状态。
func (s *Store) SetAccountEnabled(id string, enabled bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accountLocked(id)
	if a == nil {
		return false
	}
	a.Enabled = enabled
	s.dirty.Store(true)
	return true
}

// ClearAccountErrors 清空账号的失败计数与错误状态。
func (s *Store) ClearAccountErrors(id string) bool {
	s.mu.Lock()
	a := s.accountLocked(id)
	if a == nil {
		s.mu.Unlock()
		return false
	}
	a.Failures = 0
	a.Status = "ok"
	a.LastErr = ""
	a.CooldownUntil = 0
	s.mu.Unlock()
	s.dirty.Store(true)
	return true
}

// MarkCheckin 记录一次签到结果。
func (s *Store) MarkCheckin(id, date string, reward int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.st.Accounts {
		if a.ID != id {
			continue
		}
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		a.LastCheckinDate = date
		a.LastCheckinAt = time.Now().Unix()
		a.LastCheckinReward = reward
		s.dirty.Store(true)
		return
	}
}

// MarkCheckinPending 登记「上游已受理、奖励确认中」的签到。
//
// 由 /v1/checkin 在收到 pending 结果时调用。这是登记而非重试，因此不累加
// 重试次数；换了一天则重新计数（每日签到按天幂等）。
func (s *Store) MarkCheckinPending(id, date string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accountLocked(id)
	if a == nil {
		return false
	}
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	if a.PendingCheckinDate != date {
		a.PendingCheckinTries = 0
	}
	a.PendingCheckinDate = date
	a.PendingCheckinAt = time.Now().Unix()
	return true
}

// RecordCheckinRetry 记录一次后台重试（尝试次数 +1）。
func (s *Store) RecordCheckinRetry(id, date string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accountLocked(id)
	if a == nil {
		return false
	}
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	if a.PendingCheckinDate != date {
		a.PendingCheckinDate = date
		a.PendingCheckinTries = 0
	}
	a.PendingCheckinTries++
	a.PendingCheckinAt = time.Now().Unix()
	return true
}

// ClearCheckinPending 清除待确认的签到记录。
func (s *Store) ClearCheckinPending(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accountLocked(id)
	if a == nil {
		return false
	}
	if a.PendingCheckinDate == "" && a.PendingCheckinAt == 0 && a.PendingCheckinTries == 0 {
		return false
	}
	a.PendingCheckinDate = ""
	a.PendingCheckinAt = 0
	a.PendingCheckinTries = 0
	return true
}

// PendingCheckins 返回存在待确认签到的账号快照。
//
// 返回的是深拷贝，调用方可以随意读取；是否过期 / 是否放弃由调用方决定。
func (s *Store) PendingCheckins() []*account.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*account.Account, 0, 4)
	for _, a := range s.st.Accounts {
		if a.PendingCheckinDate != "" || a.PendingCheckinAt > 0 {
			out = append(out, a.Clone())
		}
	}
	return out
}

// MarkTaskRun 累加自动任务计数。
func (s *Store) MarkTaskRun(id, date string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.st.Accounts {
		if a.ID != id {
			continue
		}
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		if a.LastTaskDate != date {
			a.LastTaskDate = date
			a.LastTaskCount = 0
		}
		a.LastTaskCount += n
		return
	}
}
