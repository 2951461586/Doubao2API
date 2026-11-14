package doubao

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

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
	a.SetCSRF(out.Data.PassportCSRFToken)
	return out.Data.PassportCSRFToken, nil
}

// ensureCSRF 在必要时刷新 token；force=true 时强制刷新。
func (c *Client) ensureCSRF(ctx context.Context, a *Account, force bool) error {
	if !force && !a.NeedsCSRFRefresh() {
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
