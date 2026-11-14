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

// requestTimeout 返回上游请求超时（来自控制台设置，缺省 300s）。
func (s *Server) requestTimeout() time.Duration {
	timeout := time.Duration(s.Store.Settings().RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	return timeout
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

// withAccountRetry 选账号并在失败时换号重试。
//
// 返回本次实际使用的账号（可能为 nil），供调用方记录日志归属。
func (s *Server) withAccountRetry(ctx context.Context, timeout time.Duration, fn func(context.Context, *doubao.Account) error) (*doubao.Account, error) {
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

	var lastErr error
	var used *doubao.Account
	for i := 0; i < maxTries; i++ {
		acct := s.Store.NextAccount()
		if acct == nil {
			break
		}
		used = acct
		cctx, cancel := context.WithTimeout(ctx, timeout)
		err := fn(cctx, acct)
		cancel()

		if err == nil {
			s.Store.MarkAccountResult(acct.ID, true, "")
			return acct, nil
		}
		lastErr = err
		s.Store.MarkAccountResult(acct.ID, false, err.Error())
		// 上下文被取消（客户端断开）时不再重试
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("全部账号均不可用")
	}
	return used, lastErr
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
		s.record(key, acct, c.Label, false, http.StatusBadGateway, start, c.InLen, 0, err)
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return acct, false
	}
	s.record(key, acct, c.Label, false, http.StatusOK, start, c.InLen, outLen, nil)
	return acct, true
}

// record 记录一次请求结果。
//
// acct 是本次实际使用的账号（可为 nil）；stream 标记是否为流式响应。
func (s *Server) record(key string, acct *doubao.Account, model string, stream bool, status int, start time.Time, inLen, outLen int, err error) {
	e := store.LogEntry{
		Time:      time.Now().Unix(),
		Key:       mask(key),
		Model:     model,
		Stream:    stream,
		Status:    status,
		LatencyMS: time.Since(start).Milliseconds(),
		PromptLen: inLen,
		OutputLen: outLen,
	}
	if acct != nil {
		e.Account = acct.ID
	}
	if err != nil {
		e.Err = err.Error()
	}
	s.Store.Record(e, outLen)
}

// statusOf 把上游错误映射为日志用状态码。
func statusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	return http.StatusBadGateway
}

// contextWithTimeout 在请求上下文上叠加超时。
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
