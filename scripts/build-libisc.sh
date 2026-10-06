#!/usr/bin/env bash
#
# 构建**可被其它语言链接的内核库**（c-shared）—— 这就是 ISC-Core 的发布产物。
#
# 为什么必须是 c-shared：GUI 在 macOS 上用 Swift、在 Windows 上用 C#，
# 两者能链接的都只有 C ABI。cgo 会同时产出头文件，Swift 用 bridging header、
# C# 用 DllImport 直接调用。
#
# **安装包不再是发布产物**（2026-10-03 项目主决定）：.deb / .rpm / .pkg / .msi
# 与各平台归档都从发布里去掉，发布只有这个库。
# 打包代码仍留在 scripts/release（有人要自己打包时可以用），只是不进发布。
#
# 用法：
#   scripts/build-libisc.sh [输出目录]      # 默认 dist/；构建后刷新 SHA256SUMS
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

# ISC_BUILD_TAGS 用来出上架版本的内核库。
#
#   ISC_BUILD_TAGS=appstore scripts/build-libisc.sh
#
# appstore 标签会把**取回逻辑整个文件**排除在编译之外（见
# internal/artifacts/download.go）。这不是可选项：App Review 2.5.2 禁止
# 应用下载并执行代码，而内核原本的工作方式正是按需取回 php/python/java/
# dotnet 再执行。出上架包时必须带这个标签，否则那份二进制里仍然有下载器。
#
# 留成环境变量而不是写死：内核库同时供给命令行与图形界面，而只有图形
# 界面的上架版本需要这个约束。
# 空数组的展开要写成 ${arr[@]+"${arr[@]}"}：macOS 自带的是 bash 3.2，
# 而它在 set -u 下对 "${arr[@]}"（空数组）会报 unbound variable —— 症状是
# **不带标签的构建静默失败**，产物还是上一次带标签的那份。踩过一次：
# 重建后 dist/ 里仍是 appstore 版本，而日志被重定向了没看见。
build_tags="${ISC_BUILD_TAGS:-}"
tag_args=()
[ -n "$build_tags" ] && tag_args=(-tags "$build_tags")

CGO_ENABLED=1 go build \
  ${tag_args[@]+"${tag_args[@]}"} \
  -buildmode=c-shared \
  -ldflags "-X github.com/ShirazuNagisa/isc-core/internal/version.Version=$version \
            -X github.com/ShirazuNagisa/isc-core/internal/version.Commit=$commit \
            -X github.com/ShirazuNagisa/isc-core/internal/version.BuildTime=$built \
            $extldflags" \
  -o "$lib" ./cmd/libisc

header="${lib%.*}.h"

echo "✅ $lib"
echo "✅ $header   （Swift 用 bridging header，C# 用 DllImport）"

# 刷新校验和：发布包只有这两个文件，校验和也只覆盖它们。
if command -v shasum >/dev/null 2>&1; then
  (cd "$out" && shasum -a 256 "$(basename "$lib")" "$(basename "$header")" > SHA256SUMS)
  echo "✅ $out/SHA256SUMS"
fi

cat <<EOF

发布产物（仅此三件）：
  $lib
  $header
  $out/SHA256SUMS

GUI 链接方式见 docs/LIBRARY-API.md。
EOF
