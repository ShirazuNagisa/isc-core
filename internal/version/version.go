// Package version 提供内核版本与构建信息。
//
// 这些变量通过 -ldflags 在构建时注入，例如：
//
//	go build -ldflags "\
//	  -X github.com/ShirazuNagisa/isc-core/internal/version.Version=0.1.0 \
//	  -X github.com/ShirazuNagisa/isc-core/internal/version.Commit=$(git rev-parse --short HEAD) \
//	  -X github.com/ShirazuNagisa/isc-core/internal/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
//	  ./cmd/isc
//
// 未注入时保持开发默认值，便于 go run 直接调试。
package version

import "runtime/debug"

// 构建时注入的变量。
var (
	// Version 是语义化版本号；开发构建为 "dev"。
	Version = "dev"
	// Commit 是构建时的 git commit（短哈希）；未知时为空串。
	Commit = ""
	// BuildTime 是构建时间（RFC 3339 UTC）；未知时为空串。
	BuildTime = ""
)

// APIVersion 是对外接口的版本。
//
// 它与内核版本解耦：内核可以频繁发版，而接口版本只在出现破坏性变更时递增。
//
// # v2（v0.2.0，D27）
//
// 9 个 C 函数**一个都没有变**（D24 之后新增能力走 REST，不走 ABI）。
// 变的是 REST 契约的内容：
//
//   - 新增建站侧：/v1/presets、/v1/sources/inspect、/v1/runtimes*、
//     /v1/apps*、/v1/metrics、/v1/advisories；
//   - 业务服务的生命周期从 GUI 侧的 Supervisor 移进内核（D25）。
//
// 为什么必须递增：v1 的内核**没有** /v1/apps，而 v0.2.0 的界面上每一屏
// 都依赖它。客户端按 v1 放行的话，用户看到的是一片空白与一堆 404，
// 而不是一句"内核与界面版本不匹配"。
const APIVersion = "v2"

// init 在未注入 -ldflags 时，尝试从 Go 构建信息中补齐 Commit。
//
// 这样 `go install ./cmd/isc` 或 `go run ./cmd/isc` 也能得到有用的版本信息，
// 而不需要手工传 ldflags。
func init() {
	if Commit != "" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			Commit = s.Value[:7]
		}
		if s.Key == "vcs.time" && BuildTime == "" {
			BuildTime = s.Value
		}
	}
	// 模块版本（例如通过 go install pkg@v1.2.3 安装时）比 "dev" 更有意义。
	if Version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		Version = info.Main.Version
	}
}
