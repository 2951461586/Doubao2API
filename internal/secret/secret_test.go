package secret

import (
	"strings"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	encoded, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatalf("HashPassword 失败: %v", err)
	}
	if strings.Contains(encoded, "s3cret-pw") {
		t.Fatal("编码结果不得包含明文口令")
	}
	if !strings.HasPrefix(encoded, algoPassword+"$") {
		t.Fatalf("编码前缀不对: %s", encoded)
	}
	if !VerifyPassword(encoded, "s3cret-pw") {
		t.Error("正确口令应校验通过")
	}
	if VerifyPassword(encoded, "s3cret-pW") {
		t.Error("错误口令必须校验失败")
	}
	if VerifyPassword(encoded, "") {
		t.Error("空口令必须校验失败")
	}
}

func TestHashPasswordUniqueSalt(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Error("相同口令两次哈希必须不同（盐随机）")
	}
	if !VerifyPassword(a, "same") || !VerifyPassword(b, "same") {
		t.Error("两个哈希都应能校验通过")
	}
}

func TestVerifyPasswordRejectsGarbage(t *testing.T) {
	for _, in := range []string{
		"", "plain", "pbkdf2-sha256$abc$x$y", "md5$1$aa$bb",
		"pbkdf2-sha256$0$AA$AA", "pbkdf2-sha256$-1$AA$AA",
	} {
		if VerifyPassword(in, "x") {
			t.Errorf("非法编码 %q 不应通过校验", in)
		}
	}
}

func TestNeedRehash(t *testing.T) {
	encoded, _ := HashPassword("pw")
	if NeedRehash(encoded) {
		t.Error("当前参数的哈希不需要重算")
	}
	if !NeedRehash("pbkdf2-sha256$1000$AA$AA") {
		t.Error("低迭代次数的哈希应提示重算")
	}
	if !NeedRehash("garbage") {
		t.Error("非法编码应提示重算")
	}
}

func TestHashKey(t *testing.T) {
	h := HashKey("sk-abcdef")
	if len(h) != 64 {
		t.Fatalf("SHA-256 指纹应为 64 位十六进制，实际 %d", len(h))
	}
	if strings.Contains(h, "sk-abcdef") {
		t.Fatal("指纹不得包含明文密钥")
	}
	if !EqualKey(h, "sk-abcdef") {
		t.Error("相同密钥应判等")
	}
	if EqualKey(h, "sk-abcdeF") {
		t.Error("不同密钥不得判等")
	}
}

func TestPBKDF2KnownVector(t *testing.T) {
	// RFC 6070 的 PBKDF2-HMAC-SHA1 向量不适用；这里用 SHA-256 的公开向量：
	// pbkdf2(sha256, "password", "salt", 1, 32)
	want := "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
	got := pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32)
	if gotHex(got) != want {
		t.Errorf("PBKDF2 向量不匹配: %s", gotHex(got))
	}
}

func gotHex(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexdigits[c>>4], hexdigits[c&0x0f])
	}
	return string(out)
}
