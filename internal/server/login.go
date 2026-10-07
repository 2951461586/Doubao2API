package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"doubao2api/internal/doubao"
)

// 扫码登录相关常量（对齐桌面端 passport SDK 的 WEB_URL / WEB_SSO）。
//
//	GET  /passport/web/get_qrcode/       ?aid&next      → 二维码 + token
//	POST /passport/web/check_qrconnect/  ?aid&next      → 轮询扫码状态
//
// 注意：豆包账号体系走的是抖音 SSO，扫码需在「抖音 APP」内完成。
const (
	passportBase  = "https://accounts.doubao.com"
	passportAID   = "582478"
	qrNextURL     = "https://www.doubao.com/"
	qrSessionTTL  = 5 * time.Minute
	qrPollTimeout = 20 * time.Second
)

// qrSession 是一次进行中的扫码登录会话。
type qrSession struct {
	Token    string
	ExpireAt int64
	Client   *http.Client
	Jar      http.CookieJar
	Created  time.Time
}

// loginSessions 管理进行中的扫码会话（内存态，不落盘）。
type loginSessions struct {
	mu sync.Mutex
	m  map[string]*qrSession
}

func newLoginSessions() *loginSessions { return &loginSessions{m: map[string]*qrSession{}} }

func (ls *loginSessions) put(id string, s *qrSession) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for k, v := range ls.m {
		if time.Since(v.Created) > qrSessionTTL {
			delete(ls.m, k)
		}
	}
	ls.m[id] = s
}

func (ls *loginSessions) get(id string) *qrSession {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.m[id]
}

func (ls *loginSessions) remove(id string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	delete(ls.m, id)
}

// handleLoginQRCode 开始一次扫码登录，返回二维码与会话 ID。
func (s *Server) handleLoginQRCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "初始化会话失败", "server_error")
		return
	}
	client := &http.Client{Jar: jar, Timeout: qrPollTimeout}

	ctx, cancel := context.WithTimeout(r.Context(), qrPollTimeout)
	defer cancel()

	q, err := fetchQRCode(ctx, client)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "获取二维码失败: "+err.Error(), "upstream_error")
		return
	}
	id := doubao.RandomHex(8)
	s.Login.put(id, &qrSession{
		Token:    q.Data.Token,
		ExpireAt: q.Data.ExpireTime,
		Client:   client,
		Jar:      jar,
		Created:  time.Now(),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": id,
		"qrcode":     q.Data.QRCode, // data:image/png;base64,...
		"expire_at":  q.Data.ExpireTime,
		"hint":       q.Data.Copywriting,
	})
}

// handleLoginQRCodePoll 轮询扫码状态；确认后导入账号。
func (s *Server) handleLoginQRCodePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST", "invalid_request_error")
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败", "invalid_request_error")
		return
	}
	sess := s.Login.get(body.SessionID)
	if sess == nil {
		writeErr(w, http.StatusNotFound, "会话不存在或已过期，请重新获取二维码", "invalid_request_error")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), qrPollTimeout)
	defer cancel()

	poll, err := pollQRConnect(ctx, sess)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "轮询失败: "+err.Error(), "upstream_error")
		return
	}

	status := normalizeQRStatus(poll.Data.Status)
	if status != "confirmed" {
		if status == "expired" {
			s.Login.remove(body.SessionID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": status})
		return
	}

	acct, err := s.finishQRLogin(ctx, sess, poll)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "登录成功但获取凭据失败: "+err.Error(), "upstream_error")
		return
	}
	acct.Source = "qr"
	isNew := s.Store.UpsertAccount(acct)
	_ = s.Store.Save()
	s.Login.remove(body.SessionID)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "confirmed",
		"id":     acct.ID,
		"name":   acct.Name,
		"new":    isNew,
	})
}

// --- 上游调用 ---

type qrCodeResp struct {
	Data struct {
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
		QRCode      string `json:"qrcode"`
		Token       string `json:"token"`
		ExpireTime  int64  `json:"expire_time"`
		Copywriting string `json:"copywriting"`
	} `json:"data"`
	Message string `json:"message"`
}

type qrPollResp struct {
	Data struct {
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
		Status      string `json:"status"`
		RedirectURL string `json:"redirect_url"`
		URL         string `json:"url"`
		Ticket      string `json:"ticket"`
		Extra       string `json:"extra"`
	} `json:"data"`
	Message string `json:"message"`
}

// fetchQRCode 申请二维码。
func fetchQRCode(ctx context.Context, client *http.Client) (*qrCodeResp, error) {
	u := passportBase + "/passport/web/get_qrcode/?aid=" + passportAID +
		"&next=" + url.QueryEscape(qrNextURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", doubao.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out qrCodeResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("响应解析失败 (HTTP %d): %s", resp.StatusCode, clip(string(raw), 200))
	}
	if out.Data.ErrorCode != 0 || out.Data.QRCode == "" {
		return nil, fmt.Errorf("%s (code=%d)", firstNonEmpty(out.Data.Description, out.Message, "上游拒绝"), out.Data.ErrorCode)
	}
	return &out, nil
}

// pollQRConnect 查询扫码状态。
func pollQRConnect(ctx context.Context, sess *qrSession) (*qrPollResp, error) {
	u := passportBase + "/passport/web/check_qrconnect/?aid=" + passportAID +
		"&next=" + url.QueryEscape(qrNextURL)
	form := url.Values{"token": {sess.Token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", doubao.UserAgent)
	resp, err := sess.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out qrPollResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("响应解析失败 (HTTP %d): %s", resp.StatusCode, clip(string(raw), 200))
	}
	return &out, nil
}

// finishQRLogin 在扫码确认后换取会话 Cookie 并构造账号。
//
// 确认响应里通常带一个指向 www.doubao.com 的跳转地址（携带 ticket），
// 跟随它即可拿到 sessionid 等 Cookie；若上游直接下发了 Cookie 则跳过跳转。
func (s *Server) finishQRLogin(ctx context.Context, sess *qrSession, poll *qrPollResp) (*doubao.Account, error) {
	target := firstNonEmpty(poll.Data.RedirectURL, poll.Data.URL)
	if target == "" && poll.Data.Ticket != "" {
		target = "https://www.doubao.com/?ticket=" + url.QueryEscape(poll.Data.Ticket)
	}
	if target != "" {
		if req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil); err == nil {
			req.Header.Set("User-Agent", doubao.UserAgent)
			if resp, err := sess.Client.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
			}
		}
	}

	cookies := sess.Jar.Cookies(mustParseURL("https://www.doubao.com/"))
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("未获取到任何 Cookie（可能需要在抖音 APP 内确认后重试）")
	}
	acct, err := doubao.AccountFromCookieString(strings.Join(parts, "; "), "扫码登录 "+time.Now().Format("01-02 15:04"))
	if err != nil {
		return nil, err
	}
	return acct, nil
}

// --- 工具函数 ---

// normalizeQRStatus 把上游状态归一化为 waiting / scanned / confirmed / expired。
func normalizeQRStatus(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case s == "" || s == "new" || s == "waiting":
		return "waiting"
	case strings.Contains(s, "expire"):
		return "expired"
	case strings.Contains(s, "scan"):
		return "scanned"
	case strings.Contains(s, "confirm"), strings.Contains(s, "success"), strings.Contains(s, "login"):
		return "confirmed"
	}
	return s
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		return &url.URL{Scheme: "https", Host: "www.doubao.com", Path: "/"}
	}
	return u
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
