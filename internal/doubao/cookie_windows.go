//go:build windows

package doubao

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Chromium 在 Windows 上用 DPAPI 保护一把 AES-256-GCM 主密钥（存于 Local State），
// 再用该密钥加密每个 Cookie 值。Chrome 130+ 起，明文前置 32 字节 sha256(host_key)
// 做域名绑定，解密后需剥离。

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

// dpapiUnprotect 调用 CryptUnprotectData 解密一段 DPAPI 保护的数据。
func dpapiUnprotect(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("dpapi: empty input")
	}
	var inBlob, outBlob dataBlob
	inBlob.cbData = uint32(len(in))
	inBlob.pbData = &in[0]

	r, _, err := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&inBlob)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&outBlob)),
	)
	if r == 0 {
		return nil, fmt.Errorf("dpapi: CryptUnprotectData failed: %w", err)
	}
	defer syscall.LocalFree(syscall.Handle(unsafe.Pointer(outBlob.pbData)))

	out := make([]byte, outBlob.cbData)
	copy(out, unsafe.Slice(outBlob.pbData, outBlob.cbData))
	return out, nil
}

// localStateKey 从 Local State 取出并解密 Chromium 的 AES 主密钥。
func localStateKey(userData string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(userData, "Local State"))
	if err != nil {
		return nil, err
	}
	var ls struct {
		OSCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if err := json.Unmarshal(raw, &ls); err != nil {
		return nil, fmt.Errorf("Local State 解析失败: %w", err)
	}
	if ls.OSCrypt.EncryptedKey == "" {
		return nil, fmt.Errorf("Local State 中缺少 os_crypt.encrypted_key")
	}
	blob, err := base64.StdEncoding.DecodeString(ls.OSCrypt.EncryptedKey)
	if err != nil {
		return nil, err
	}
	if len(blob) < 5 || string(blob[:5]) != "DPAPI" {
		return nil, fmt.Errorf("encrypted_key 前缀不是 DPAPI")
	}
	key, err := dpapiUnprotect(blob[5:])
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("主密钥长度异常: %d", len(key))
	}
	return key, nil
}

// decryptCookie 解密单个 Cookie 值，并剥离域名绑定前缀。
func decryptCookie(key []byte, hostKey string, enc []byte) (string, error) {
	if len(enc) == 0 {
		return "", nil
	}
	prefix := string(enc[:min(3, len(enc))])
	if prefix != "v10" && prefix != "v11" && prefix != "v20" {
		// 未加密（Chromium 在无 key 时可能明文存储）
		return string(enc), nil
	}
	if len(enc) < 15+16 {
		return "", fmt.Errorf("密文长度不足")
	}
	nonce, ct := enc[3:15], enc[15:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("AES-GCM 解密失败: %w", err)
	}
	// 剥离 sha256(host_key) 域名绑定前缀
	if len(pt) >= 32 {
		h := sha256.Sum256([]byte(hostKey))
		if string(pt[:32]) == string(h[:]) {
			pt = pt[32:]
		}
	}
	return string(pt), nil
}

// cookieRow 是从 Cookies 库读出的一行。
type cookieRow struct {
	Host   string
	Name   string
	Value  string
	Path   string
	Secure bool
}

// readProfileCookies 读取单个 profile 的 Cookie 库并解密全部条目。
func readProfileCookies(userData, profile string, key []byte) ([]cookieRow, error) {
	dbPath := filepath.Join(userData, profile, "Network", "Cookies")
	if _, err := os.Stat(dbPath); err != nil {
		// 老版本 Chromium 放在 profile 根目录
		dbPath = filepath.Join(userData, profile, "Cookies")
		if _, err := os.Stat(dbPath); err != nil {
			return nil, err
		}
	}
	// 复制一份，避免与运行中的浏览器争用锁
	tmp, err := os.CreateTemp("", "dbck-*.db")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	if err := copyFile(dbPath, tmpPath); err != nil {
		return nil, err
	}

	db, err := openSQLite(tmpPath)
	if err != nil {
		return nil, err
	}
	root, err := db.tableRoot("cookies")
	if err != nil {
		return nil, err
	}
	rows, err := db.scanTree(root)
	if err != nil {
		return nil, err
	}

	var out []cookieRow
	for _, r := range rows {
		// cookies 表列序：creation_utc, host_key, top_frame_site_key, name,
		// value, encrypted_value, path, ...
		if len(r) < 7 {
			continue
		}
		host := r[1].AsString()
		name := r[3].AsString()
		if host == "" || name == "" {
			continue
		}
		val := r[4].AsString()
		enc := r[5].AsBytes()
		if len(enc) > 0 {
			if dec, derr := decryptCookie(key, host, enc); derr == nil {
				val = dec
			} else {
				continue // 解密失败的单条跳过
			}
		}
		if val == "" {
			continue
		}
		out = append(out, cookieRow{
			Host:   host,
			Name:   name,
			Value:  val,
			Path:   r[6].AsString(),
			Secure: r[8].Type == 1 && r[8].Int != 0,
		})
	}
	return out, nil
}

// ImportResult 是一次桌面端导入的结果。
type ImportResult struct {
	Account *Account
	Profile string
	Dir     string
	Cookies int
}

// ImportFromDesktop 从本机 DoubaoWork 桌面端导入登录态。
//
// explicitDir 为空时自动探测；返回第一个含有效豆包会话的 profile。
func ImportFromDesktop(explicitDir string) (*ImportResult, error) {
	var lastErr error
	for _, userData := range DoubaoWorkUserDataDirs(explicitDir) {
		if _, err := os.Stat(userData); err != nil {
			lastErr = err
			continue
		}
		key, err := localStateKey(userData)
		if err != nil {
			lastErr = err
			continue
		}
		for _, prof := range chromiumProfiles(userData) {
			rows, err := readProfileCookies(userData, prof, key)
			if err != nil || len(rows) == 0 {
				if err != nil {
					lastErr = err
				}
				continue
			}
			acct := accountFromCookies(rows)
			if acct == nil {
				continue
			}
			acct.Source = "auto-import"
			acct.Name = fmt.Sprintf("DoubaoWork/%s", prof)
			// 设备指纹优先从桌面端本地存储读取
			if dev, web := readDeviceIDs(userData, prof); dev != "" || web != "" {
				if dev != "" {
					acct.DeviceID = dev
				}
				if web != "" {
					acct.WebID = web
				}
			}
			return &ImportResult{Account: acct, Profile: prof, Dir: userData, Cookies: len(rows)}, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未找到 DoubaoWork 数据目录")
	}
	return nil, lastErr
}

// accountFromCookies 从 Cookie 列表构造账号；无有效会话时返回 nil。
func accountFromCookies(rows []cookieRow) *Account {
	cookies := map[string]string{}
	for _, r := range rows {
		if !strings.HasSuffix(r.Host, "doubao.com") {
			continue
		}
		// www.doubao.com 的值覆盖 .doubao.com 的同名项（更具体优先）
		if _, ok := cookies[r.Name]; !ok || r.Host == "www.doubao.com" {
			cookies[r.Name] = r.Value
		}
	}
	if cookies["sessionid"] == "" && cookies["sid_guard"] == "" {
		return nil
	}
	acct := &Account{
		ID:      RandomHex(8),
		Cookies: cookies,
		Enabled: true,
		Status:  "ok",
	}
	if ms := findCookie(rows, "msToken"); ms != "" {
		acct.MsToken = ms
	}
	if acct.DeviceID == "" {
		acct.DeviceID = RandomDigits(15)
	}
	if acct.WebID == "" {
		acct.WebID = RandomDigits(19)
	}
	if acct.FP == "" {
		acct.FP = defaultFP()
	}
	return acct
}

func findCookie(rows []cookieRow, name string) string {
	for _, r := range rows {
		if r.Name == name {
			return r.Value
		}
	}
	return ""
}

// readDeviceIDs 从 Chromium Local Storage 中尽力读取设备指纹。
// 读取失败返回空串，调用方回退到随机占位值。
func readDeviceIDs(userData, profile string) (deviceID, webID string) {
	dir := filepath.Join(userData, profile, "Local Storage", "leveldb")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}
	webRe := regexpWebID()
	devRe := regexpDeviceID()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil || len(raw) == 0 {
			continue
		}
		s := string(raw)
		if webID == "" {
			if m := webRe.FindStringSubmatch(s); m != nil {
				webID = m[1]
			}
		}
		if deviceID == "" {
			if m := devRe.FindStringSubmatch(s); m != nil {
				deviceID = m[1]
			}
		}
		if webID != "" && deviceID != "" {
			break
		}
	}
	return deviceID, webID
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = out.ReadFrom(in)
	return err
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
