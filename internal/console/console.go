// Package console 内嵌验证控制台的静态资源。
//
// # 为什么是一个单独的包
//
// go:embed 只能引用**本包目录之下**的文件。把资源放在 internal/api/ 下
// 会让那个目录混进 HTML/JS/CSS；放在这里则边界清楚：这个包只做一件事 ——
// 把控制台的前端资源带进二进制。
//
// # 为什么不引入前端构建链
//
// 控制台是纯手写的 HTML + 原生 JS，没有 npm、没有打包器、没有 node_modules。
// 理由：
//
//   - 内核是纯 Go 的（见 docs/DECISIONS.md D01），引入 Node 工具链会让
//     CI 多一套版本管理与供应链审计，而收益只是"写起来舒服一点"；
//   - 全部资源由 go:embed 带进二进制，所以 `isc daemon run` 之后控制台
//     立刻可用，不需要额外分发静态文件；
//   - 它的定位是**验证工具**而不是最终 GUI（最终 GUI 是独立的跨平台应用，
//     见 D02）。验证工具应当能用浏览器直接打开源码看懂。
//
// 代价是没有组件化与类型检查。对这个规模（三个文件、几百行 JS）而言，
// 那笔账是划算的。
package console

import (
	"embed"
	"io/fs"
)

// assets 是控制台的全部静态资源。
//
//go:embed assets
var assets embed.FS

// FS 返回以 assets 目录为根的文件系统。
//
// 去掉 assets/ 这一层前缀：调用方看到的是 index.html / app.js / style.css，
// 而不是 assets/xxx —— 后者会让 URL 里出现一个没有意义的路径段。
func FS() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		// 只可能在 embed 指令与目录结构不一致时发生，属于编译期问题的
		// 运行期表现。panic 比返回一个空文件系统更好定位 ——
		// 后者会让控制台变成一个空白页，而没有任何线索。
		panic("console: 内嵌资源结构异常: " + err.Error())
	}
	return sub
}
