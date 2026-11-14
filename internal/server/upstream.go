package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"doubao2api/internal/doubao"
	"doubao2api/internal/store"
)

// 本文件是「调用上游」的统一入口：选号、超时、日志、错误响应都在这里收口，
// 具体 handler 只表达业务语义，不再各写一遍样板。

// requestTimeout 返回单次上游尝试的超时（来自控制台设置，缺省 300s）。
func (s *Server) requestTimeout() time.Duration {
	timeout := time.Duration(s.Store.Settings().RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	return timeout
}

// overallTimeout 返回一次客户端请求的总预算（含换号重试与退避）。
//
// 没有它时最坏耗时是 RequestTimeout × MaxRetries（默认 300×3=15 分钟），
// 而客户端通常 2–3 分钟就放弃，表现为大量无意义的 `context canceled`。
func (s *Server) overallTimeout() time.Duration {
	d := time.Duration(s.Store.Settings().OverallTimeout) * time.Second
	if d <= 0 {
		d = 600 * time.Second
	}
	if d < s.requestTimeout() {
		d = s.requestTimeout()
	}
	return d
}

// withAccount 在指定 account_id 时使用该账号，否则走账号池重试。
func (s *Server) withAccount(ctx context.Context, id string, timeout time.Duration, fn func(context.Context, *doubao.Account) error) (*doubao.Account, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return s.withAccountRetry(ctx, timeout, fn)
	}
	a := s.Store.Account(id)
	if a == nil {
		return nil, fmt.Errorf("账号不存在: %s", id)
	}
	if !a.Enabled {
		return a, fmt.Errorf("账号已停用: %s", a.Name)
	}

	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, s.overallTimeout())
	defer cancel()

	release, ok := s.slots.acquire(ctx, a.ID, s.Store.Settings().MaxConcurrentPerAccount)
	if !ok {
		// 区分「客户端走了」与「总预算耗尽」：前者 499 不写响应，后者 504。
		if parent.Err() != nil {
			return a, doubao.NewCanceled(ctx.Err())
		}
		return a, fmt.Errorf("等待账号空闲超时: %w", context.DeadlineExceeded)
	}
	defer release()

	cctx, acancel := context.WithTimeout(ctx, timeout)
	err := fn(cctx, a)
	acancel()

	switch {
	case err == nil:
		s.Store.MarkAccountResult(a.ID, true, "")
	case parent.Err() != nil:
		// 客户端先断开：不记账号、不重试。
		return a, doubao.NewCanceled(err)
	case doubao.IsAccountScopedError(err):
		s.Store.MarkAccountResult(a.ID, false, err.Error())
	}
	return a, err
}

// withAccountRetry 选账号并在失败时换号重试。
//
// 约定：
//   - 每次尝试都换一个**未试过**的账号（此前可能连续选中同一个）；
//   - 账号级错误记失败并退避；业务错误立即返回；
//   - 客户端断开立即停止，并标记为 canceled（不算上游失败）；
//   - 总耗时受 overallTimeout 约束。
//
// 返回本次实际使用的账号（可能为 nil），供调用方记录日志归属。
func (s *Server) withAccountRetry(parent context.Context, timeout time.Duration, fn func(context.Context, *doubao.Account) error) (*doubao.Account, error) {
	accts := s.Store.Accounts()
	if len(accts) == 0 {
		return nil, fmt.Errorf("没有可用账号，请先在控制台导入或添加账号")
	}
	maxTries := s.Store.Settings().MaxRetries
	if maxTries <= 0 {
		maxTries = 3
	}
	if maxTries > len(accts) {
		maxTries = len(accts)
	}
	limit := s.Store.Settings().MaxConcurrentPerAccount

	ctx, cancel := context.WithTimeout(parent, s.overallTimeout())
	defer cancel()

	var (
		lastErr error
		used    *doubao.Account
		tried   = make(map[string]bool, maxTries)
	)
	for i := 0; i < maxTries; i++ {
		acct := s.Store.NextAccountExcluding(tried)
		if acct == nil {
			break
		}
		tried[acct.ID] = true
		used = acct

		release, ok := s.slots.acquire(ctx, acct.ID, limit)
		if !ok {
			if parent.Err() != nil {
				return used, doubao.NewCanceled(ctx.Err())
			}
			lastErr = fmt.Errorf("等待账号 %s 空闲超时: %w", acct.Name, context.DeadlineExceeded)
			break
		}
		cctx, acancel := context.WithTimeout(ctx, timeout)
		err := fn(cctx, acct)
		acancel()
		release()

		if err == nil {
			s.Store.MarkAccountResult(acct.ID, true, "")
			return acct, nil
		}
		// 客户端断开优先于一切分类。
		if parent.Err() != nil {
			return used, doubao.NewCanceled(err)
		}
		lastErr = err
		kind := doubao.Classify(err)
		// 只有「账号级」错误才记失败：业务 / 配置类错误换任何账号都一样，
		// 记上去会让整池账号变红、/health 误报 ready: 0。
		if kind == doubao.KindAccount {
			s.Store.MarkAccountResult(acct.ID, false, err.Error())
		}
		if !kind.Retryable() {
			break
		}
		if ctx.Err() != nil {
			// 总预算耗尽：这是超时（504），不是客户端断开（499）。
			lastErr = fmt.Errorf("请求总预算耗尽: %w", context.DeadlineExceeded)
			break
		}
		// 最后一次尝试后不再等待。
		if i+1 >= maxTries {
			break
		}
		if !sleepCtx(ctx, retryBackoff(i)) {
			if parent.Err() != nil {
				lastErr = doubao.NewCanceled(ctx.Err())
			} else {
				lastErr = fmt.Errorf("请求总预算耗尽: %w", context.DeadlineExceeded)
			}
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("全部账号均不可用")
	}
	return used, lastErr
}

// sleepCtx 等待 d，或在 ctx 结束时提前返回 false。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// upstreamCall 描述一次非流式上游调用。
type upstreamCall struct {
	// Label 写入日志的「模型」列（如 credits / skills / conversations）。
	Label string
	// AccountID 非空时只使用指定账号，不轮询。
	AccountID string
	// InLen 是请求侧长度（提示词 / 提示数），用于日志。
	InLen int
	// Timeout 为零时用 requestTimeout()。
	Timeout time.Duration
	// MinTimeout 给慢端点兜一个下限（如视频 / 批量任务）。
	MinTimeout time.Duration
}

// runUpstream 执行一次上游调用，统一处理选号、超时、日志与错误响应。
//
// fn 返回本次响应的业务长度（用于日志 OutputLen）。
// 返回 true 表示成功，调用方继续写自己的业务响应；
// 返回 false 表示错误响应已经写出，调用方应直接 return。
func (s *Server) runUpstream(w http.ResponseWriter, r *http.Request, c upstreamCall,
	fn func(ctx context.Context, acct *doubao.Account) (outLen int, err error)) (*doubao.Account, bool) {

	start := time.Now()
	key := extractKey(r)
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = s.requestTimeout()
	}
	if c.MinTimeout > timeout {
		timeout = c.MinTimeout
	}

	outLen := 0
	acct, err := s.withAccount(r.Context(), c.AccountID, timeout, func(ctx context.Context, acct *doubao.Account) error {
		n, err := fn(ctx, acct)
		outLen = n
		return err
	})
	if err != nil {
		kind := doubao.Classify(err)
		s.record(r, key, acct, c.Label, false, start, c.InLen, 0, err)
		if kind != doubao.KindCanceled {
			writeErrKind(w, kind, err.Error())
		}
		return acct, false
	}
	s.record(r, key, acct, c.Label, false, start, c.InLen, outLen, nil)
	return acct, true
}

// record 记录一次请求结果（状态文件摘要 + 进程日志）。
//
// err 决定分类与状态码；acct 是本次实际使用的账号（可为 nil）。
func (s *Server) record(r *http.Request, key string, acct *doubao.Account, model string, stream bool,
	start time.Time, inLen, outLen int, err error) {

	kind := doubao.Classify(err)
	status := kind.HTTPStatus()
	acctID := ""
	if acct != nil {
		acctID = acct.ID
	}
	e := store.LogEntry{
		Time:      time.Now().Unix(),
		Key:       mask(key),
		Account:   acctID,
		Model:     model,
		Stream:    stream,
		Status:    status,
		LatencyMS: time.Since(start).Milliseconds(),
		PromptLen: inLen,
		OutputLen: outLen,
		Kind:      string(kind),
	}
	if err != nil {
		e.Err = redact(err.Error())
	}
	s.Store.Record(e, outLen)
	writeRequestLog(requestInfoFrom(r), r, model, stream, kind, start, key, acctID, inLen, outLen, err)
}

// contextWithTimeout 在请求上下文上叠加超时。
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
