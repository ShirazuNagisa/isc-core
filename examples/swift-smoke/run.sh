#!/usr/bin/env bash
# 构建内核库并跑 Swift 冒烟测试。
set -euo pipefail

cd "$(dirname "$0")/../.."   # 仓库根
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

scripts/build-libisc.sh "$work"

# Swift 直接用 cgo 产出的头文件：-import-objc-header 就够了，
# 不需要单独写 module map。
swiftc -import-objc-header "$work/libisc.h" \
  examples/swift-smoke/main.swift \
  -L "$work" -lisc \
  -Xlinker -rpath -Xlinker "$work" \
  -o "$work/swift-smoke"

"$work/swift-smoke"
