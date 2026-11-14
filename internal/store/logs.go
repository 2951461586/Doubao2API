package store

import (
	"time"
)

// --- 设置 / 统计 / 日志 ---

// Settings 返回设置快照。
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := s.st.Settings
	cp.ModelAliases = map[string]string{}
	for k, v := range s.st.Settings.ModelAliases {
		cp.ModelAliases[k] = v
	}
	return cp
}

// UpdateSettings 局部更新设置。
func (s *Store) UpdateSettings(fn func(*Settings)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.st.Settings)
}

// Stats 返回统计快照。
func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.st.Stats
}

// Logs 返回最近的日志。
func (s *Store) Logs(limit int) []LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.st.Logs)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]LogEntry, limit)
	copy(out, s.st.Logs[n-limit:])
	return out
}

// Record 记录一次请求结果并更新统计。
//
// 失败率必须可解释：客户端主动断开（canceled）与上游业务错误（business）
// 都不计入 FailedRequests，否则一次长回答被客户端掐掉就会抬高「上游失败率」。
func (s *Store) Record(e LogEntry, outChars int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Stats.TotalRequests++
	switch e.Kind {
	case "canceled":
		s.st.Stats.CanceledRequests++
	case "business":
		s.st.Stats.BusinessErrors++
	case "account", "timeout", "transport":
		s.st.Stats.FailedRequests++
	default:
		if e.Status >= 400 || e.Err != "" {
			s.st.Stats.FailedRequests++
		}
	}
	s.st.Stats.TotalCharsOut += int64(outChars)
	s.st.Logs = append(s.st.Logs, e)
	if keep := s.st.Settings.LogKeep; keep > 0 && len(s.st.Logs) > keep {
		s.st.Logs = s.st.Logs[len(s.st.Logs)-keep:]
	}
	s.dirty.Store(true)
}

// ClearLogs 清空日志。
func (s *Store) ClearLogs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Logs = nil
}

// ClearStats 重置统计。
func (s *Store) ClearStats() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Stats = Stats{StartedAt: time.Now().Unix()}
}
