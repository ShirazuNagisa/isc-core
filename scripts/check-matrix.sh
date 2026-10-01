# 交叉编译矩阵验证。
#
# 存在意义见 docs/PLAN.md R2 与 docs/DECISIONS.md D11：
# 内核是一套 Go 代码，必须保证**任何时刻**所有目标平台都能编译通过，
# 未实现的平台后端靠 stub 兜底，而不是靠"那个平台先不管"。
#
# 本项目硬约束：禁止 cgo（见 docs/PLAN.md §0），因此这里强制 CGO_ENABLED=0。

set -eu

targets="
windows/amd64
windows/arm64
linux/amd64
linux/arm64
darwin/amd64
darwin/arm64
freebsd/amd64
"

export CGO_ENABLED=0

fail=0
for t in $targets; do
    os=${t%/*}
    arch=${t#*/}
    if GOOS=$os GOARCH=$arch go build ./... 2>/tmp/isc-build-err.$$; then
        printf '  OK   %s/%s\n' "$os" "$arch"
    else
        printf '  FAIL %s/%s\n' "$os" "$arch"
        sed 's/^/       /' /tmp/isc-build-err.$$ >&2
        fail=$((fail + 1))
    fi
    rm -f /tmp/isc-build-err.$$
done

if [ "$fail" -ne 0 ]; then
    printf '\n%d 个目标编译失败\n' "$fail" >&2
    exit 1
fi

printf '\n全部目标编译通过\n'
