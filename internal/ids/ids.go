// Package ids 生成随机标识（UUID / 十六进制串 / 数字串）。
//
// 独立成包是为了让持久化层（internal/account、internal/store）不必依赖
// 上游客户端包（internal/doubao）就能生成 ID，保持依赖方向单一。
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// Hex 生成 n 字节的随机十六进制串。
func Hex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}

// Digits 生成 n 位随机十进制数字串（首位非 0）。
func Digits(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("1", n)
	}
	var sb strings.Builder
	sb.WriteByte(byte('1' + b[0]%9))
	for i := 1; i < n; i++ {
		sb.WriteByte(byte('0' + b[i]%10))
	}
	return sb.String()
}

// UUID 生成标准 UUID v4 字符串。
func UUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Hex(16)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
