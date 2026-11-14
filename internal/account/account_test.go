package account

import (
	"strings"
	"testing"
	"time"
)

func TestCloneIsolatesPersistedFields(t *testing.T) {
	a := &Account{
		ID:      "a1",
		Name:    "原账号",
		Cookies: map[string]string{"sessionid": "s1"},
		Enabled: true,
	}
	a.Normalize()

	cp := a.Clone()
	cp.Name = "改过的"
	cp.Cookies["sessionid"] = "tampered"
	cp.Enabled = false
	cp.Failures = 7

	if a.Name != "原账号" || a.Cookies["sessionid"] != "s1" || !a.Enabled || a.Failures != 0 {
		t.Fatalf("修改副本污染了原账号: %+v", a)
	}
	if cp.ID != a.ID {
		t.Error("副本应保留 ID")
	}
	if cp == a {
		t.Error("Clone 不应返回同一个指针")
	}
}

// TestCloneSharesRuntime 锁死「运行时状态在副本间共享」这一约定。
//
// 否则每次请求拿到新副本都会丢掉 CSRF 缓存，导致每个请求多一次上游往返。
func TestCloneSharesRuntime(t *testing.T) {
	a := &Account{ID: "a1", Cookies: map[string]string{"sessionid": "s1"}}
	cp := a.Clone()

	cp.SetCSRF("token-1")
	if got := a.CSRFToken(); got != "token-1" {
		t.Errorf("CSRF 应在副本间共享，实际 %q", got)
	}
	if a.NeedsCSRFRefresh() {
		t.Error("刚刷新过的 token 不应再要求刷新")
	}

	// 但持久化字段必须互不影响。
	cp.Name = "x"
	if a.Name == "x" {
		t.Error("持久化字段不应共享")
	}
}

func TestCloneNil(t *testing.T) {
	var a *Account
	if got := a.Clone(); got != nil {
		t.Errorf("nil.Clone() = %v, want nil", got)
	}
}

// TestNormalize 验证设备指纹的补齐规则（幂等）。
func TestNormalize(t *testing.T) {
	a := &Account{}
	a.Normalize()
	if a.DeviceID == "" || a.WebID == "" || a.WebTabID == "" {
		t.Fatalf("Normalize 未补齐标识: %+v", a)
	}
	if a.FP != "verify_"+a.DeviceID {
		t.Errorf("FP = %q, want verify_%s", a.FP, a.DeviceID)
	}

	// 覆盖历史随机指纹，且二次调用不改变结果。
	a.FP = "random-legacy-fp"
	before := *a
	a.Normalize()
	if a.FP != "verify_"+a.DeviceID {
		t.Error("Normalize 应把旧指纹修正为 verify_<device_id>")
	}
	if a.DeviceID != before.DeviceID || a.WebTabID != before.WebTabID {
		t.Error("Normalize 必须幂等")
	}

	// PCVersion 缺省时回落到 AppVersion。
	b := &Account{AppVersion: "2.99.0"}
	b.Normalize()
	if b.PCVersion != "2.99.0" {
		t.Errorf("PCVersion = %q, want 2.99.0", b.PCVersion)
	}
	b.PCVersion = "2.36.15"
	b.Normalize()
	if b.PCVersion != "2.36.15" {
		t.Error("已设置的 PCVersion 不应被覆盖")
	}
}

func TestCSRFTokenFallback(t *testing.T) {
	a := &Account{}
	if got := a.CSRFToken(); got != "" {
		t.Errorf("无 token 时应返回空串，实际 %q", got)
	}
	if !a.NeedsCSRFRefresh() {
		t.Error("Cookie 里没有 token 时应要求刷新")
	}

	a.Cookies = map[string]string{"passport_csrf_token": "from-cookie"}
	if got := a.CSRFToken(); got != "from-cookie" {
		t.Errorf("CSRFToken() = %q, want from-cookie", got)
	}
	if a.NeedsCSRFRefresh() {
		t.Error("Cookie 里已有 token 时不必预刷新")
	}

	a.Cookies = map[string]string{"passport_csrf_token_default": "default-cookie"}
	if got := a.CSRFToken(); got != "default-cookie" {
		t.Errorf("应回落到 passport_csrf_token_default，实际 %q", got)
	}

	// 刷新后的 token 优先于 Cookie。
	a.SetCSRF("refreshed")
	if got := a.CSRFToken(); got != "refreshed" {
		t.Errorf("CSRFToken() = %q, want refreshed", got)
	}
	// 空 token 不应覆盖已有缓存。
	a.SetCSRF("")
	if got := a.CSRFToken(); got != "refreshed" {
		t.Error("空 token 不应写入缓存")
	}
}

// TestCSRFCacheExpiry 验证 30 分钟 TTL 生效后回落到 Cookie。
func TestCSRFCacheExpiry(t *testing.T) {
	a := &Account{Cookies: map[string]string{"passport_csrf_token": "cookie-token"}}
	a.SetCSRF("cached-token")

	// 把缓存时间拨到过期之前。
	a.rtOf().mu.Lock()
	a.rtOf().csrfAt = time.Now().Add(-csrfTTL - time.Minute)
	a.rtOf().mu.Unlock()

	if got := a.CSRFToken(); got != "cookie-token" {
		t.Errorf("过期后应回落到 Cookie，实际 %q", got)
	}
	if got := a.cachedCSRF(); got != "" {
		t.Errorf("过期缓存不应返回，实际 %q", got)
	}
}

func TestTabIDStable(t *testing.T) {
	// 有持久化值时直接用它。
	a := &Account{WebTabID: "tab-fixed"}
	if got := a.TabID(); got != "tab-fixed" {
		t.Errorf("TabID() = %q, want tab-fixed", got)
	}

	// 缺失时生成一次并在后续调用中保持稳定，且副本间共享。
	b := &Account{}
	first := b.TabID()
	if first == "" {
		t.Fatal("TabID 不应为空")
	}
	if again := b.TabID(); again != first {
		t.Errorf("TabID 应稳定，%q != %q", again, first)
	}
	if fromClone := b.Clone().TabID(); fromClone != first {
		t.Error("副本应共享运行时 tab id")
	}
}

func TestDerivedValues(t *testing.T) {
	a := &Account{}
	if a.AppIDValue() != AppID {
		t.Errorf("AppIDValue() = %q, want %q", a.AppIDValue(), AppID)
	}
	if a.PCVersionValue() != PCVersion {
		t.Errorf("PCVersionValue() = %q, want %q", a.PCVersionValue(), PCVersion)
	}
	if a.RuntimeVersionValue() != RuntimeVersion {
		t.Errorf("RuntimeVersionValue() = %q, want %q", a.RuntimeVersionValue(), RuntimeVersion)
	}
	if a.ChannelValue() != BuildChannel {
		t.Errorf("ChannelValue() = %q, want %q", a.ChannelValue(), BuildChannel)
	}
	if a.UAValue() != UserAgent {
		t.Errorf("UAValue() = %q, want 默认 UA", a.UAValue())
	}

	a.AppID = "582478"
	a.PCVersion = "9.9.9"
	if a.AppIDValue() != "582478" {
		t.Error("账号级 AppID 应优先")
	}
	if !strings.Contains(a.UAValue(), "SamanthaDoubaoWork/9.9.9") {
		t.Errorf("UAValue() 应使用账号版本: %q", a.UAValue())
	}

	a.DeviceID = "dev-1"
	if a.FPValue() != "verify_dev-1" {
		t.Errorf("FPValue() = %q", a.FPValue())
	}
	a.DeviceID = ""
	a.FP = "explicit-fp"
	if a.FPValue() != "explicit-fp" {
		t.Errorf("无 device_id 时应回落到 FP 字段，实际 %q", a.FPValue())
	}
}

func TestSessionID(t *testing.T) {
	a := &Account{Cookies: map[string]string{"sessionid": "sid-1"}}
	if got := a.SessionID(); got != "sid-1" {
		t.Errorf("SessionID() = %q", got)
	}
	if got := (&Account{}).SessionID(); got != "" {
		t.Errorf("空 Cookie 应返回空串，实际 %q", got)
	}
}
