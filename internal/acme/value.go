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
// # 参数是**原始的** key authorization，不是已经算好的值
//
// `x/crypto/acme` 的 `Client.DNS01ChallengeRecord` 已经把这个函数
// 做的事做完了 —— 它返回的就是要发布的值。**再调一次这个函数就是对
// 值做二次哈希**，而那样写出来的记录：格式完全正确、长度完全正确、
// 只是 CA 会说"找到了错误的 TXT 记录"。
//
// 这个错误真的发生过，而它极难从症状反推 —— 因为从"摘要"到"摘要的
// 摘要"之间没有任何类型或命名上的提示。因此：
//
//   - 需要**发布的值**时，用 `Client.DNS01ChallengeRecord`，不要再哈希；
//   - 只有手里拿着**原始 keyAuth** 时才用这个函数。
//
// 单独成一个函数是为了让它能被测试直接钉住 —— 这个算法写错的
// 症状是"授权一直失败"，而 ACME 服务器给出的错误信息通常只有
// "challenge failed"，完全看不出是值算错了。
func DNS01Value(keyAuth string) string {
	sum := sha256.Sum256([]byte(keyAuth))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
