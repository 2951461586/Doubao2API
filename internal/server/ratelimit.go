package server

import (
	"strconv"
	"sync"
	"time"
)

// limiter 是一个按 key 计数的固定窗口限流器。
//
// 用标准库自行实现的原因与项目其它部分一致：零第三方依赖。
// 只需要两种行为：
//   - Allow：每窗口最多 N 次（用于 /v1 的按密钥限流）；
//   - Fail / Reset：连续失败锁定（用于管理口令爆破防护）。
type limiter struct {
	mu    sync.Mutex
	m     map[string]*bucket
	now   func() time.Time
	lastS int // 上次清理的秒级时间戳
}

type bucket struct {
	count int
	reset time.Time
}

const maxLimiterKeys = 4096

func newLimiter() *limiter {
	return &limiter{m: make(map[string]*bucket), now: time.Now}
}

// Allow 消耗一个令牌；返回是否放行与需要等待的时长。
func (l *limiter) Allow(key string, limit int, window time.Duration) (bool, time.Duration) {
	if limit <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)

	b := l.m[key]
	if b == nil || now.After(b.reset) {
		b = &bucket{reset: now.Add(window)}
		l.m[key] = b
	}
	if b.count >= limit {
		return false, b.reset.Sub(now)
	}
	b.count++
	return true, 0
}

// Fail 记一次失败；当窗口内失败数达到 max 时返回 blocked=true。
//
// 与 Allow 分开是因为语义不同：失败要「累计到阈值」，成功要清零。
func (l *limiter) Fail(key string, max int, window time.Duration) (blocked bool, retry time.Duration) {
	if max <= 0 {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)

	b := l.m[key]
	if b == nil || now.After(b.reset) {
		b = &bucket{reset: now.Add(window)}
		l.m[key] = b
	}
	b.count++
	if b.count >= max {
		return true, b.reset.Sub(now)
	}
	return false, 0
}

// Exhausted 报告 key 是否已处于锁定状态（不会增加计数）。
func (l *limiter) Exhausted(key string, max int) (bool, time.Duration) {
	if max <= 0 {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.m[key]
	if b == nil || now.After(b.reset) {
		return false, 0
	}
	if b.count >= max {
		return true, b.reset.Sub(now)
	}
	return false, 0
}

// Reset 清空 key 的计数（登录成功后调用）。
func (l *limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}

// pruneLocked 周期性清理过期桶，避免扫描自然增长。
func (l *limiter) pruneLocked(now time.Time) {
	sec := now.Unix()
	if sec == int64(l.lastS) && len(l.m) <= maxLimiterKeys {
		return
	}
	l.lastS = int(sec)
	for k, b := range l.m {
		if now.After(b.reset) {
			delete(l.m, k)
		}
	}
	// 极端情况下（大量不同来源）兜底：清空最旧的一批。
	if len(l.m) > maxLimiterKeys {
		n := 0
		for k := range l.m {
			delete(l.m, k)
			n++
			if n >= maxLimiterKeys/2 {
				break
			}
		}
	}
}

// retryAfterSeconds 把等待时长换算成 Retry-After 头的秒数（至少 1）。
func retryAfterSeconds(d time.Duration) string {
	sec := int(d.Round(time.Second) / time.Second)
	if sec < 1 {
		sec = 1
	}
	return strconv.Itoa(sec)
}
