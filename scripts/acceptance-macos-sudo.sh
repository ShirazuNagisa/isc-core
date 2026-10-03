#!/usr/bin/env bash
#
# 本机（macOS）需要 root 的那部分验收 —— 一条命令跑完并汇总。
#
# 它会**真的改动系统**，因此每一步都对应一个明确的检查，跑完自动复原：
#
#   1. 把内核注册成 launchd 系统服务 → 启动 → 确认内核真的在跑 → 停止 → 卸载
#   2. 真的放行一个端口（pf anchor）→ 查 anchor 里的规则 → 撤销
#
# 用法：
#   sudo scripts/acceptance-macos-sudo.sh [二进制路径]
#   sudo scripts/acceptance-macos-sudo.sh --keep        # 跑完不清理（留着服务与规则）
#   sudo scripts/acceptance-macos-sudo.sh --data-dir DIR
#
# 默认二进制：/tmp/isc-acc/isc（可以先用一条不需要 root 的命令构建它：
#   CGO_ENABLED=0 go build -o /tmp/isc-acc/isc ./cmd/isc）
set -uo pipefail

KEEP=0
BIN="/tmp/isc-acc/isc"
DATA_DIR=""
while [ $# -gt 0 ]; do
  case "$1" in
    --keep) KEEP=1 ;;
    --data-dir) DATA_DIR="${2:-}"; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) BIN="$1" ;;
  esac
  shift
done

pass=0; fail=0
ok()   { printf '  ✅ %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  ❌ %s\n' "$1"; fail=$((fail+1)); }
info() { printf '     %s\n' "$1"; }
head_() { printf '\n=== %s ===\n' "$1"; }

if [ "$(id -u)" != "0" ]; then
  echo "需要 root：请用 sudo 运行（它要写 /Library/LaunchDaemons 与 /etc/pf.anchors）。" >&2
  exit 2
fi
if [ ! -x "$BIN" ]; then
  echo "找不到可执行文件：$BIN" >&2
  echo "先构建：CGO_ENABLED=0 go build -o $BIN ./cmd/isc" >&2
  exit 2
fi

PLIST=/Library/LaunchDaemons/com.isc.core.plist
PORT=18080
HTTP_PID=""
PLAN_ID=""
SERVICE_INSTALLED=0

# 无论成功失败都复原：卸载服务、撤销规则、杀掉临时 HTTP 服务。
cleanup() {
  if [ "$KEEP" = "0" ]; then
    head_ "清理"
    if [ -n "$HTTP_PID" ]; then kill "$HTTP_PID" 2>/dev/null && info "已停止临时 HTTP 服务"; fi
    if [ -n "$PLAN_ID" ]; then
      "$BIN" rollback "$PLAN_ID" >/dev/null 2>&1 && info "已撤销变更 $PLAN_ID" || info "撤销未成功（可能已撤销过）"
    fi
    if [ "$SERVICE_INSTALLED" = "1" ]; then
      "$BIN" service uninstall >/dev/null 2>&1 && info "已卸载服务" || info "卸载未成功"
    fi
  else
    head_ "按 --keep 保留现场"
    info "服务与规则都留着：$PLIST / /etc/pf.anchors/isc"
  fi
}
trap cleanup EXIT

echo "使用二进制：$BIN"
"$BIN" version | head -4

# ---------------------------------------------------------------------------
head_ "一、系统服务（launchd）"
# ---------------------------------------------------------------------------
if install_out=$("$BIN" service install 2>&1); then
  SERVICE_INSTALLED=1
else
  bad "service install 失败"; info "$(printf '%s' "$install_out" | tail -3 | tr '\n' ' ')"
  [ -f "$PLIST" ] && SERVICE_INSTALLED=1
fi

if [ -f "$PLIST" ]; then
  ok "plist 已写入 $PLIST"
  if grep -q "<string>$BIN</string>" "$PLIST"; then
    ok "plist 里的可执行文件是我们给的那个"
  else
    bad "plist 里的可执行文件不对"; info "$(grep -A2 ProgramArguments "$PLIST" | head -4 | tr '\n' ' ')"
  fi
else
  bad "plist 没有写出来"
fi

st=$("$BIN" service status --json 2>/dev/null)
info "service status: $(printf '%s' "$st" | tr -d '\n ')"
case "$st" in *'"backend":"launchd"'*) ok "后端是 launchd";; *) bad "后端不是 launchd";; esac

"$BIN" service start >/dev/null 2>&1
for _ in $(seq 1 30); do
  if [ -S "/Library/Application Support/ISC/run/isc.sock" ] || [ -S "${DATA_DIR:-/nonexistent}/run/isc.sock" ]; then break; fi
  sleep 1
done

target="${DATA_DIR:-/Library/Application Support/ISC}"
if [ -S "$target/run/isc.sock" ]; then
  ok "内核套接字出现了（$target/run/isc.sock）"
else
  bad "30 秒内没有出现套接字（内核可能起不来）"
  info "最近的 launchd 输出："
  launchctl print system/com.isc.core 2>&1 | grep -E "state|last exit|path =" | head -5 | sed 's/^/       /'
  info "内核自己的日志（最能说明原因）："
  log show --last 3m --predicate 'process == "isc"' --style compact 2>/dev/null \
    | tail -15 | sed 's/^/       /'
fi

status=$("$BIN" status --data-dir "$target" --json 2>/dev/null)
if printf '%s' "$status" | grep -q '"running":[[:space:]]*true'; then
  ok "status 报 running=true（内核真的在服务方式下跑起来了）"
else
  bad "status 没报 running"; info "实际：$(printf '%s' "$status" | head -c 200)"
fi
if printf '%s' "$status" | grep -q '"status":[[:space:]]*"ok"'; then
  ok "health.status = ok"
else
  bad "health 不是 ok"
fi

# ---------------------------------------------------------------------------
head_ "二、真的放行一个端口（pf）"
# ---------------------------------------------------------------------------
python3 -m http.server "$PORT" >/dev/null 2>&1 &
HTTP_PID=$!
sleep 1
info "临时服务：python3 -m http.server $PORT（pid $HTTP_PID）"

plan=$("$BIN" expose --port "$PORT" --yes --json 2>&1)
if printf '%s' "$plan" | grep -q '"id"'; then
  PLAN_ID=$(printf '%s' "$plan" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
  ok "放行成功（变更 $PLAN_ID）"
else
  bad "放行失败"; info "$(printf '%s' "$plan" | tail -3 | tr '\n' ' ')"
fi

rules=$(pfctl -a isc -sr 2>&1)
if printf '%s' "$rules" | grep -q "$PORT"; then
  ok "pf 的 isc anchor 里确实有 $PORT 的规则"
  info "$(printf '%s' "$rules" | head -3 | tr '\n' ' ')"
else
  bad "anchor 里看不到 $PORT 的规则"; info "$(printf '%s' "$rules" | head -3 | tr '\n' ' ')"
fi

changes=$("$BIN" changes --json 2>/dev/null)
if printf '%s' "$changes" | grep -q "$PORT"; then
  ok "变更记录里有这次放行"
else
  bad "变更记录里没有这次放行"
fi

if [ -n "$PLAN_ID" ]; then
  if "$BIN" rollback "$PLAN_ID" >/dev/null 2>&1; then
    ok "撤销成功"
    PLAN_ID=""
    rules_after=$(pfctl -a isc -sr 2>&1)
    if printf '%s' "$rules_after" | grep -q "$PORT"; then
      bad "撤销之后 anchor 里还有 $PORT 的规则"
    else
      ok "撤销之后 anchor 里已经没有 $PORT 的规则"
    fi
  else
    bad "撤销失败"
  fi
fi

# ---------------------------------------------------------------------------
head_ "三、服务清理"
# ---------------------------------------------------------------------------
"$BIN" service stop >/dev/null 2>&1
if "$BIN" service uninstall >/dev/null 2>&1; then
  SERVICE_INSTALLED=0
  ok "服务已卸载"
else
  bad "服务卸载失败"
fi
if [ ! -f "$PLIST" ]; then ok "plist 已删除"; else bad "plist 还在"; fi

# ---------------------------------------------------------------------------
head_ "汇总"
printf '  通过 %d 项，失败 %d 项\n' "$pass" "$fail"
if [ "$fail" -eq 0 ]; then
  echo "  ✅ 需要 root 的那部分验收全部通过"
else
  echo "  ❌ 有失败项，请把上面的输出发我"
fi
echo
echo "注意：完整链路（检测地址 → 放行 → 写 DNS 记录）需要真实域名与 DNS 凭据，"
echo "      不在这条脚本里；那一步的收尾必须用手机 4G/5G 验证外部可达性。"
exit $(( fail > 0 ))
