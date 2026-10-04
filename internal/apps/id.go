package apps

import (
	"crypto/rand"
	"encoding/base64"
)

// newAppID 生成一个短的随机 id。
//
// 随机而不是自增：id 会出现在域名绑定、日志文件名与用户可见的链接里，
// 可枚举的 id 让"猜别人的应用"变成一件容易的事。22 个字符的
// base64url 与 job 包保持一致，便于排障时一眼分辨。
func newAppID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败意味着系统随机源不可用；此时继续用可预测的
		// id 比直接失败更糟，但没有合理的降级路径 —— 让调用方看到 panic
		// 也好过静默产生可枚举的 id。
		panic("apps: cannot read random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
