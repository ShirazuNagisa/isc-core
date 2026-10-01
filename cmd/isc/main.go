// Command isc 是 ISC 内核的统一入口。
//
// 同一个二进制同时承担两个角色：
//
//   - CLI 客户端：通过本地 API 与守护进程通信（例如 isc status）；
//   - 守护进程：以 isc daemon run 启动，常驻后台提供本地 API。
//
// 这种"单二进制双角色"的设计见 docs/DECISIONS.md D19。
//
// 内核**没有 GUI**：它只提供完整接口（OpenAPI）与一个用于验证功能的
// 浏览器控制台。下游 GUI 由其他项目独立开发，通过 HTTP / WebSocket
// 与内核通信 —— 这条边界同时是许可证边界，见 docs/DECISIONS.md D17。
package main

import (
	"os"

	"github.com/ShirazuNagisa/isc-core/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
