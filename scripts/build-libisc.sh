#!/usr/bin/env bash
#
# 构建**可被其它语言链接的内核库**（c-shared）。
#
# 为什么必须是 c-shared：GUI 在 macOS 上用 Swift、在 Windows 上用 C#，
# 两者能链接的都只有 C ABI。cgo 会同时产出头文件，Swift 用 bridging header、
# C# 用 DllImport 直接调用。
#
# 用法：
#   scripts/build-libisc.sh [输出目录]      # 默认 dist/
#
# 注意：**这一步必须开 cgo**（CGO_ENABLED=1），这与内核其余部分坚持的
# CGO_ENABLED=0 并不矛盾 —— 内核本体、CLI 与守护进程仍然零 cgo，
# 只有这个产出库的目标需要。详见 docs/DECISIONS.md D24。
set -euo pipefail

cd "$(dirname "$0")/.."
out="${1:-dist}"
mkdir -p "$out"

case "$(uname -s)" in
  Darwin) lib="$out/libisc.dylib" ;;
  Linux)  lib="$out/libisc.so" ;;
  MINGW*|MSYS*|CYGWIN*)
    # Windows 上需要 MinGW-w64 或 MSVC 提供链接器：
    #   set CGO_ENABLED=1 && go build -buildmode=c-shared -o isc.dll ./cmd/libisc
    lib="$out/isc.dll"
    ;;
  *)
    echo "不支持的构建平台：$(uname -s)" >&2
    exit 1
    ;;
esac

# -ldflags 把版本信息注入，这样 GUI 显示的内核版本与 CLI 一致。
version="${ISC_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
commit="$(git rev-parse --short HEAD 2>/dev/null || echo '')"
built="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# install name 必须显式设成 @rpath/…。
#
# c-shared 默认把库的 install name 写成**裸文件名**（libisc.dylib），于是
# 链接它的程序在运行时只会去找同目录/当前目录，rpath 不生效 —— 现象是
# `dyld: Library not loaded: libisc.dylib`。GUI 打包时库要放进
# `Contents/Frameworks/`，那里正是靠 @rpath 定位的，所以这里就定好。
extldflags=""
if [ "$(uname -s)" = "Darwin" ]; then
  # 注意 -Wl, 前缀：Go 把 -extldflags 的内容当作**一个**参数交给 clang，
  # 少了它 clang 会报 "unknown argument: -install_name,@rpath/..."。
  extldflags="-extldflags=-Wl,-install_name,@rpath/$(basename "$lib")"
fi

CGO_ENABLED=1 go build \
  -buildmode=c-shared \
  -ldflags "-X github.com/ShirazuNagisa/isc-core/internal/version.Version=$version \
            -X github.com/ShirazuNagisa/isc-core/internal/version.Commit=$commit \
            -X github.com/ShirazuNagisa/isc-core/internal/version.BuildTime=$built \
            $extldflags" \
  -o "$lib" ./cmd/libisc

echo "✅ $lib"
echo "✅ ${lib%.*}.h   （Swift 用 bridging header，C# 用 DllImport）"
