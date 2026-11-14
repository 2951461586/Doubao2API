package doubao

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"doubao2api/internal/ids"
)

// float64FromBits 避免为单一用途引入 math 包的转换函数别名。
func float64FromBits(b uint64) float64 { return math.Float64frombits(b) }

// readFileAll 读取整个文件。
func readFileAll(path string) ([]byte, error) { return os.ReadFile(path) }

// 随机标识生成集中在 internal/ids，这里保留薄封装沿用协议代码的旧名字。

// RandomHex 生成 n 字节的随机十六进制串（用于 ID）。
func RandomHex(n int) string { return ids.Hex(n) }

// RandomDigits 生成 n 位随机十进制数字串（用于设备指纹占位）。
func RandomDigits(n int) string { return ids.Digits(n) }

// NewUUID 生成标准 UUID v4 字符串。
func NewUUID() string { return ids.UUID() }

// DoubaoWorkUserDataDirs 返回本机 DoubaoWork 的 Chromium User Data 目录候选。
func DoubaoWorkUserDataDirs(explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	var out []string
	for _, base := range []string{os.Getenv("LOCALAPPDATA"), os.Getenv("APPDATA")} {
		if base == "" {
			continue
		}
		out = append(out, filepath.Join(base, "DoubaoWork", "User Data"))
	}
	return out
}

// chromiumProfiles 返回 User Data 下的 profile 目录名。
func chromiumProfiles(userData string) []string {
	names := []string{"Default"}
	entries, err := os.ReadDir(userData)
	if err != nil {
		return names
	}
	re := regexp.MustCompile(`^Profile \d+$`)
	for _, e := range entries {
		if e.IsDir() && re.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names
}

// regexpWebID / regexpDeviceID 用于从 Chromium Local Storage 中
// 尽力提取桌面端写入的指纹值（leveldb 为二进制 / 可能 Snappy 压缩，按子串匹配）。
func regexpWebID() *regexp.Regexp {
	return regexp.MustCompile(`web_id"?\s*:\s*"?(\d{15,20})`)
}

func regexpDeviceID() *regexp.Regexp {
	return regexp.MustCompile(`device_?[iI]d"?\s*:\s*"?(\d{12,20})`)
}

func regexpIID() *regexp.Regexp {
	return regexp.MustCompile(`iid"?\s*:\s*"?(\d{12,20})`)
}

func regexpAppVersion() *regexp.Regexp {
	return regexp.MustCompile(`appVersion"?\s*:\s*"?(\d+\.\d+\.\d+)`)
}

func regexpBuildChannel() *regexp.Regexp {
	return regexp.MustCompile(`buildChannel"?\s*:\s*"?([A-Za-z0-9_-]{2,20})`)
}

// ParseCookieString 解析手工粘贴的 Cookie 串。
//
// 接受 `a=1; b=2` 或每行一条的格式（从浏览器 DevTools 直接复制）。
func ParseCookieString(s string) map[string]string {
	out := map[string]string{}
	s = strings.ReplaceAll(s, "\r\n", ";")
	s = strings.ReplaceAll(s, "\n", ";")
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			continue
		}
		out[name] = value
	}
	return out
}

// AccountFromCookieString 从手工粘贴的 Cookie 串构造账号。
func AccountFromCookieString(cookieStr, name string) (*Account, error) {
	cookies := ParseCookieString(cookieStr)
	if len(cookies) == 0 {
		return nil, fmt.Errorf("未能从输入中解析出任何 Cookie")
	}
	if cookies["sessionid"] == "" && cookies["sid_guard"] == "" {
		return nil, fmt.Errorf("缺少 sessionid / sid_guard，请确认已登录后复制完整 Cookie")
	}
	if name == "" {
		name = "手工录入 " + shortID(cookies["sessionid"])
	}
	a := &Account{
		ID:      RandomHex(8),
		Name:    name,
		Source:  "manual",
		Cookies: cookies,
		Enabled: true,
		Status:  "ok",
	}
	if ms := cookies["msToken"]; ms != "" {
		a.MsToken = ms
	}
	// 设备指纹固定为 verify_<device_id>（见桌面端 getCommonParams 的 partial 参数），
	// 由 Normalize 统一补齐，避免与桌面端导入路径出现两套逻辑。
	a.Normalize()
	return a, nil
}

func shortID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}
