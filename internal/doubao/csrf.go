package doubao

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CSRF token 的本地缓存时长。
//
// 桌面端在访问 passport 相关 URL 时会主动刷新；实测 token 有效期远长于 10 分钟，
// 这里取 30 分钟，既避免每次请求都多一跳，又能在过期后自动恢复。
const csrfTTL = 30 * time.Minute

// webTabID 返回账号固定 web_tab_id；未初始化时生成并写回。
func (a *Account) webTabID() string {
	a.csrfMu.Lock()
	defer a.csrfMu.Unlock()
	if a.WebTabID == "" {
		a.WebTabID = NewUUID()
	}
	return a.WebTabID
}

// cachedCSRF 返回未过期的缓存 token（空串表示需要刷新）。
func (a *Account) cachedCSRF() string {
	a.csrfMu.Lock()
	defer a.csrfMu.Unlock()
	if a.CSRFTokenValue == "" {
		return ""
	}
	if a.CSRFTokenAt > 0 && time.Since(time.Unix(a.CSRFTokenAt, 0)) > csrfTTL {
		return ""
	}
	return a.CSRFTokenValue
}

// setCSRF 写入刷新得到的 token。
func (a *Account) setCSRF(token string) {
	if token == "" {
		return
	}
	a.csrfMu.Lock()
	a.CSRFTokenValue = token
	a.CSRFTokenAt = time.Now().Unix()
	a.csrfMu.Unlock()
}

// needsCSRFRefresh 报告当前是否需要刷新。
func (a *Account) needsCSRFRefresh() bool {
	a.csrfMu.Lock()
	defer a.csrfMu.Unlock()
	if a.CSRFTokenValue != "" {
		// 已经刷新过：过期就再刷一次。
		return a.CSRFTokenAt == 0 || time.Since(time.Unix(a.CSRFTokenAt, 0)) > csrfTTL
	}
	// 从未刷新过时，Cookie 里的 token 仍可用就不必预刷新。
	return a.Cookies["passport_csrf_token"] == "" && a.Cookies["passport_csrf_token_default"] == ""
}

// RefreshCSRF 调用 /passport/safe/csrf_token/ 刷新 CSRF token 并写入账号缓存。
//
// 与桌面端一致：该接口必须去掉 real_aid（否则返回 1622「非法应用」/error_code=22），
// 其余公参照常携带。返回的 token 通过 x-tt-passport-csrf-token 头使用。
func (c *Client) RefreshCSRF(ctx context.Context, a *Account) (string, error) {
	u := UpstreamBase + "/passport/safe/csrf_token/?" + csrfParams(a).Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	setUpstreamHeaders(req, a)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("刷新 CSRF token 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("刷新 CSRF token HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Message string `json:"message"`
		Data    struct {
			PassportCSRFToken string `json:"passport_csrf_token"`
			ErrorCode         int    `json:"error_code"`
			Description       string `json:"description"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("CSRF 响应解析失败: %s", truncate(string(raw), 200))
	}
	if out.Data.PassportCSRFToken == "" {
		msg := firstNonEmpty(out.Data.Description, out.Message)
		if out.Data.ErrorCode != 0 {
			return "", fmt.Errorf("CSRF 刷新被拒绝 (code=%d): %s", out.Data.ErrorCode, msg)
		}
		return "", fmt.Errorf("CSRF 刷新未返回 token: %s", truncate(string(raw), 200))
	}
	a.setCSRF(out.Data.PassportCSRFToken)
	return out.Data.PassportCSRFToken, nil
}

// ensureCSRF 在必要时刷新 token；force=true 时强制刷新。
func (c *Client) ensureCSRF(ctx context.Context, a *Account, force bool) error {
	if !force && !a.needsCSRFRefresh() {
		return nil
	}
	_, err := c.RefreshCSRF(ctx, a)
	return err
}

// looksLikeAuthError 判断错误信息是否可能由 CSRF / 登录态失效导致。
func looksLikeAuthError(msg string) bool {
	m := strings.ToLower(msg)
	for _, k := range []string{"csrf", "x-tt-passport", "登录", "login", "session", "passport", "unauthorized", "forbidden"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}
