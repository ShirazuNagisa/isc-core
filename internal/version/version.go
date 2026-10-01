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
const APIVersion = "v1"

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
