package server

import (
	"sync"
	"time"

	"doubao2api/internal/doubao"
)

// 本文件是账号池的并发闸门与时序控制。
//
// 账号池的选号策略在 store（需要持锁），这里只负责「某个账号同时在跑几个请求」
// 与「失败后等多久再试」。两者都需要在请求级共享，所以放在 Server 上。

// accountSlots 限制单账号的并发上游请求数。
//
// 3 个账号跑 6 条并发流时，上游很容易回 710022004（限流），
// 表现却是「随机某个请求失败」，很难归因。闸门把并发显式地压住。
type accountSlots struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

func newAccountSlots() *accountSlots {
	return &accountSlots{m: make(map[string]chan struct{})}
}

// acquire 获取一个账号的执行槽位；返回释放函数。
//
// 返回 false 表示等待期间上下文被取消（客户端断开 / 总预算耗尽）。
func (a *accountSlots) acquire(ctx interface {
	Done() <-chan struct{}
	Err() error
}, id string, limit int) (func(), bool) {
	if limit <= 0 {
		return func() {}, true
	}
	a.mu.Lock()
	ch := a.m[id]
	if ch == nil || cap(ch) != limit {
		ch = make(chan struct{}, limit)
		a.m[id] = ch
	}
	a.mu.Unlock()

	select {
	case ch <- struct{}{}:
		return func() { <-ch }, true
	case <-ctx.Done():
		return func() {}, false
	}
}

// retryBackoff 返回第 n 次重试前的等待时长（基准 200ms，指数退避，上限 2s）。
//
// 立即重试在限流场景下只会把上游打得更狠；这里加抖动避免多请求同步重试。
func retryBackoff(n int) time.Duration {
	if n < 0 {
		n = 0
	}
	if n > 3 {
		n = 3
	}
	base := 200 * time.Millisecond << n
	if base > 2*time.Second {
		base = 2 * time.Second
	}
	// 抖动 ±25%。
	jitter := time.Duration(len(doubao.RandomHex(2))%50) * base / 100
	return base - base/4 + jitter
}
