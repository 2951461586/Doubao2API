// Package secret 提供凭据的单向哈希与恒定时间校验。
//
// 为什么单独成包：
//   - 状态文件里不应出现明文管理密码与明文调用密钥；
//   - 哈希算法必须只有一处实现，避免「保存用一套、校验用另一套」；
//   - 项目要求零第三方依赖（CI 有 go.mod 守卫），因此 PBKDF2 在这里用
//     crypto/hmac + crypto/sha256 自行实现，而不是引入 golang.org/x/crypto。
package secret

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// 口令哈希参数。
//
// iterations 取 OWASP 2023 对 PBKDF2-HMAC-SHA256 的建议下限（600k 偏高，
// 本项目是本地网关，取 210k 在 2 vCPU 上约 60ms，兼顾爆破成本与登录体验）。
const (
	algoPassword = "pbkdf2-sha256"
	iterations   = 210_000
	saltLen      = 16
	keyLen       = 32
)

// HashPassword 把明文口令编码为可落盘的字符串。
//
// 格式：pbkdf2-sha256$<iter>$<salt-b64>$<hash-b64>。
// 参数随哈希一起保存，后续调高 iterations 时旧记录仍可校验。
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("口令不能为空")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐失败: %w", err)
	}
	sum := pbkdf2SHA256([]byte(password), salt, iterations, keyLen)
	return fmt.Sprintf("%s$%d$%s$%s", algoPassword, iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword 以恒定时间校验明文口令。
//
// 编码非法或算法未知时返回 false（不区分「格式错」与「口令错」）。
func VerifyPassword(encoded, password string) bool {
	algo, iter, salt, want, ok := parsePassword(encoded)
	if !ok || algo != algoPassword || iter <= 0 || len(salt) == 0 || len(want) == 0 {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NeedRehash 报告已存哈希是否使用了当前默认参数（用于登录时透明升级）。
func NeedRehash(encoded string) bool {
	_, iter, _, _, ok := parsePassword(encoded)
	return !ok || iter < iterations
}

// parsePassword 拆解 HashPassword 的输出。
func parsePassword(encoded string) (algo string, iter int, salt, hash []byte, ok bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 {
		return "", 0, nil, nil, false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, nil, nil, false
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", 0, nil, nil, false
	}
	hash, err = base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return "", 0, nil, nil, false
	}
	return parts[0], n, salt, hash, true
}

// HashKey 返回调用密钥的不可逆指纹（SHA-256，十六进制）。
//
// 密钥是高熵随机串（sk- + 24 字节），不需要加盐或慢哈希；
// 用 SHA-256 既能在每次请求上做低成本查找，又不会把密钥本身写进状态文件。
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", sum[:])
}

// EqualKey 以恒定时间比较两个密钥指纹。
func EqualKey(hash, key string) bool {
	want := HashKey(key)
	return subtle.ConstantTimeCompare([]byte(hash), []byte(want)) == 1
}

// pbkdf2SHA256 是 RFC 8018 PBKDF2 的 HMAC-SHA256 实现。
func pbkdf2SHA256(password, salt []byte, iter, length int) []byte {
	prf := hmac.New(sha256.New, password)
	hLen := prf.Size()
	blocks := (length + hLen - 1) / hLen

	out := make([]byte, 0, blocks*hLen)
	buf := make([]byte, 4)
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		prf.Write(buf)
		u := prf.Sum(nil)

		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:length]
}
