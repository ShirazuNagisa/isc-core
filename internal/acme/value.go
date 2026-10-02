package acme

import (
	"crypto/sha256"
	"encoding/base64"
)

// DNS01Value 计算 DNS-01 挑战记录的值。
//
// RFC 8555 §8.4：值是对 key authorization 做 SHA-256 之后取
// base64url（无填充）编码。
//
//	keyAuth = token + "." + base64url(SHA256(accountKeyThumbprint))
//	value   = base64url(SHA256(keyAuth))
//
// 单独成一个函数是为了让它能被测试直接钉住 —— 这个算法写错的
// 症状是"授权一直失败"，而 ACME 服务器给出的错误信息通常只有
// "challenge failed"，完全看不出是值算错了。
func DNS01Value(keyAuth string) string {
	sum := sha256.Sum256([]byte(keyAuth))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
