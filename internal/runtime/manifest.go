// Package runtime 负责"让一份源码有可用的解释器/工具链"。
//
// 三级解析顺序（见 D28）：
//
//  1. **系统已装**的解释器，版本满足预设要求 → 直接用，一个字节都不下载；
//  2. **已供给**的托管运行时（数据目录里有标记文件）→ 复用；
//  3. 按下面的固定清单**下载预编译发行版**并校验摘要。
//
// # 为什么清单是代码里的常量
//
// 清单决定了"内核会从网络上取什么字节并把它解压执行"。放在常量里，
// 它就能被 code review、被测试逐条校验、并且离线可复现；放在远程 JSON 里
// 则等于把这条信任链交给一个额外的服务。远程刷新留待后续版本。
//
// # 摘要与算法
//
// 每条都带发行方**自己发布**的摘要，并且逐条与上游核对过（见各条的注释）。
// .NET 只发 SHA-512，其余发 SHA-256，因此 Digest 带算法前缀而不是固定
// SHA-256。清单里**绝不允许**出现"还没核对过"的摘要 —— 一个编造的摘要
// 会让供给 100% 失败，而失败点在用户眼里只是"装不上"。
package runtime

import (
	"strings"
	"time"
)

// Kind 是运行时的类型。
//
// 它定义在这里而不是 presets 包里：运行时是更底层的概念，预设只是
// "某种技术栈需要哪种运行时"。presets 通过类型别名复用它。
type Kind string

const (
	// KindNone 表示不需要外部运行时。
	KindNone   Kind = ""
	KindNode   Kind = "node"
	KindPython Kind = "python"
	KindPHP    Kind = "php"
	KindGo     Kind = "go"
	KindJava   Kind = "java"
	KindDotNet Kind = "dotnet"
	// KindDocker 不由内核供给：Docker Desktop 必须由用户自己安装。
	KindDocker Kind = "docker"
)

// Provisionable 报告内核是否会为该运行时下载发行版。
//
// Docker 不算：Docker Desktop 必须由用户自己安装，内核只检测与引导。
func (k Kind) Provisionable() bool {
	switch k {
	case KindNode, KindPython, KindPHP, KindGo, KindJava, KindDotNet:
		return true
	default:
		return false
	}
}

// Artifact 是一份固定的、带摘要的运行时发行版。
type Artifact struct {
	Kind    Kind
	Version string
	// Platform 是 "GOOS/GOARCH"。
	Platform string
	URL      string
	// Digest 形如 "sha256:<hex>" 或 "sha512:<hex>"。
	Digest string
	// Archive 是下载落盘时使用的文件名。
	Archive string
	// StripRoot 表示解压时剥掉单一顶层目录。
	StripRoot bool
	// Executable 是主可执行文件相对运行时根目录的路径。
	Executable string
	// SizeBytes 是发行方公布的大小，用于在下载前告诉用户要下多少。
	SizeBytes int64
	// License 与 Source 用于 D29 的再分发登记。
	License string
	Source  string
	// Verified 记录摘要的核对来源与日期，便于日后复核。
	Verified string
}

// darwinArm64 是 v0.2.0 唯一供给运行时的平台。
//
// 其它平台不做供给（返回"没有可用发行版"），系统解释器仍然可用 ——
// GUI 只在 macOS 上交付，而内核必须保持三平台可构建（D11）。
const darwinArm64 = "darwin/arm64"

// Catalog 返回内置的发行版清单。
//
// 摘要核对情况逐条写在 Verified 里：
//   - Node / Go / Temurin 三条与既有 Swift 实现使用的一致，本次又各自与
//     上游发布的校验和文件重新比对过；
//   - Python 取自 astral-sh/python-build-standalone 的 release API；
//   - .NET 取自微软官方的 releases.json（该文件只提供 SHA-512）。
func Catalog() []Artifact {
	return []Artifact{
		{
			Kind: KindNode, Version: "22.14.0", Platform: darwinArm64,
			URL:     "https://nodejs.org/dist/v22.14.0/node-v22.14.0-darwin-arm64.tar.gz",
			Digest:  "sha256:e9404633bc02a5162c5c573b1e2490f5fb44648345d64a958b17e325729a5e42",
			Archive: "node-v22.14.0-darwin-arm64.tar.gz",
			// node-v22.14.0-darwin-arm64/ 下才是 bin/node。
			StripRoot: true, Executable: "bin/node",
			License: "MIT", Source: "nodejs.org",
			Verified: "nodejs.org/dist/v22.14.0/SHASUMS256.txt (2026-10-04)",
		},
		{
			Kind: KindGo, Version: "1.27.1", Platform: darwinArm64,
			URL:     "https://go.dev/dl/go1.27.1.darwin-arm64.tar.gz",
			Digest:  "sha256:ee215d57e0ec269c60cc9ceca68e6bda321ba9ee5afe24f4b0988703c2d87d12",
			Archive: "go1.27.1.darwin-arm64.tar.gz",
			// go/ 下才是 bin/go。
			StripRoot: true, Executable: "bin/go",
			SizeBytes: 68100347,
			License:   "BSD-3-Clause", Source: "go.dev",
			Verified: "go.dev/dl/?mode=json (2026-10-04)",
		},
		{
			Kind: KindJava, Version: "21.0.12+101", Platform: darwinArm64,
			URL:     "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.12.1%2B1/OpenJDK21U-jdk_aarch64_mac_hotspot_21.0.12.1_1.tar.gz",
			Digest:  "sha256:3623232f33a9c3baadf304480b2535f9a3cba8a58d42ecbb438ba267315d9998",
			Archive: "OpenJDK21U-jdk_aarch64_mac_hotspot_21.0.12.1_1.tar.gz",
			// jdk-21.0.12+1/Contents/Home/bin/java —— 剥掉顶层之后是
			// Contents/Home/...，这是 macOS 上 JDK 的标准布局。
			StripRoot: true, Executable: "Contents/Home/bin/java",
			License: "GPL-2.0-with-classpath-exception", Source: "Adoptium (Eclipse Temurin)",
			Verified: "api.adoptium.net v3 assets (2026-10-04)",
		},
		{
			Kind: KindDotNet, Version: "8.0.425", Platform: darwinArm64,
			URL:     "https://builds.dotnet.microsoft.com/dotnet/Sdk/8.0.425/dotnet-sdk-8.0.425-osx-arm64.tar.gz",
			Digest:  "sha512:d42156be70b489236479a415536013a89a9fd6eaca57d5716a7017612fcc65bdb459068973d7cffd6fe710f2799af8bee95b2046ba032762d47df176f5c4a7d6",
			Archive: "dotnet-sdk-8.0.425-osx-arm64.tar.gz",
			// 该归档没有单一顶层目录，解压出来就是 dotnet/、sdk/、shared/。
			StripRoot: false, Executable: "dotnet",
			License: "MIT", Source: "Microsoft (.NET SDK)",
			Verified: "builds.dotnet.microsoft.com release-metadata 8.0 (2026-10-04)",
		},
		{
			Kind: KindPython, Version: "3.13.16", Platform: darwinArm64,
			URL:     "https://github.com/astral-sh/python-build-standalone/releases/download/20261003/cpython-3.13.16%2B20261003-aarch64-apple-darwin-install_only.tar.gz",
			Digest:  "sha256:d8975d7df4f08f7b1c7aafcdfacbddcec3d366415f2c1a72b2466b6850815933",
			Archive: "cpython-3.13.16-aarch64-apple-darwin-install_only.tar.gz",
			// python/ 下才是 bin/python3。
			StripRoot: true, Executable: "bin/python3",
			License: "PSF-2.0", Source: "astral-sh/python-build-standalone",
			Verified: "GitHub release API digest (2026-10-04)",
		},
	}
}

// ArtifactFor 查找某个平台上的某个运行时。
func ArtifactFor(kind Kind, platform string) (Artifact, bool) {
	for _, artifact := range Catalog() {
		if artifact.Kind == kind && artifact.Platform == platform {
			return artifact, true
		}
	}
	return Artifact{}, false
}

// Kinds 返回清单里覆盖的全部运行时类型。
func Kinds() []Kind {
	seen := map[Kind]bool{}
	out := make([]Kind, 0, len(seen))
	for _, artifact := range Catalog() {
		if !seen[artifact.Kind] {
			seen[artifact.Kind] = true
			out = append(out, artifact.Kind)
		}
	}
	return out
}

// executableName 是系统解释器在 PATH 上的名字。
func executableName(kind Kind) string {
	switch kind {
	case KindPython:
		// macOS 上 `python` 可能不存在或指向 python2；一律用 python3。
		return "python3"
	case KindDotNet:
		return "dotnet"
	default:
		return string(kind)
	}
}

// systemVersionArgs 是探测系统解释器版本用的参数。
//
// java 的 `-version` 把版本写到 **stderr**，因此调用方要把两路输出合起来看。
func systemVersionArgs(kind Kind) []string {
	switch kind {
	case KindNode:
		return []string{"--version"}
	case KindGo:
		return []string{"version"}
	case KindJava:
		return []string{"-version"}
	case KindDotNet:
		return []string{"--version"}
	default:
		return []string{"--version"}
	}
}

// versionProbeTimeout 限制版本探测。
//
// 一个卡住的解释器（例如等待输入）不该让"准备环境"永远停在原地。
const versionProbeTimeout = 10 * time.Second

// platformOf 把 GOOS/GOARCH 拼成清单里的键。
func platformOf(goos, goarch string) string {
	return strings.TrimSpace(goos) + "/" + strings.TrimSpace(goarch)
}
