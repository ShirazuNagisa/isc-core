package ddnsgo

import (
	"fmt"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// Errorf 构造一条**已翻译**的错误，签名与 ddns-go 里裸用 fmt.Errorf 的写法一致。
//
// # 为什么需要它
//
// 移植过来的 provider 里原本散着大量 `fmt.Errorf("创建 dnsla 请求失败: %w", err)`
// —— 中文**硬编码**在里面。它们不是 `Log`/`LogStr` 调用，因此既不走译文表，
// 也不被棘轮当作"消息 key"排除；结果是这些错误在英文界面上永远是中文。
//
// 本函数把同一套约定（中文原文即 key）延伸到错误上：
//
//	Errorf("创建 dnsla 请求失败: %w", err)
//
// # 为什么不是 LogStr
//
// `LogStr` 会先 `Sprintf` 再返回字符串 —— 那样 `%w` 就被提前消费掉了，
// 错误链断掉，`errors.Is` / `errors.As` 再也解不开。
// 这里先取**译文格式串**、再交给 `fmt.Errorf` 填参数，因此包裹语义完好。
func Errorf(key string, args ...interface{}) error {
	return fmt.Errorf(i18n.T(key), args...)
}
