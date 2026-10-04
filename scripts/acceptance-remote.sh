#!/usr/bin/env bash
#
# 远程管理面（ISC Mizar）的本机验收 —— 一条命令跑完整条链路并汇总。
#
# 它**不需要 root**，也不碰系统设置：起一个用临时数据目录的守护进程，
# 在这个进程上把远程访问从关到开走一遍，然后用 curl 真的走 TLS 连上去。
#
# 为什么必须有这一层：单元测试走的是 httptest，它们全部绕过了 TLS 与
# 网络，因此证明不了"手机连得上"。而这一块最可能出问题的地方恰好就在
# 那一段 —— 证书的 SAN 没覆盖地址、监听绑错了接口、状态里报的端口与
# 真正监听的端口不是一个。这些在进程内测试里一条都测不出来。
#
# 用法：
#   scripts/acceptance-remote.sh [二进制路径]
#   scripts/acceptance-remote.sh --port 18788
#
# 默认二进制：/tmp/isc-acc/isc（先用一条命令构建它：
#   CGO_ENABLED=0 go build -o /tmp/isc-acc/isc ./cmd/isc）
set -uo pipefail

BIN="/tmp/isc-acc/isc"
PORT=18788
while [ $# -gt 0 ]; do
  case "$1" in
    --port) PORT="${2:-}"; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) BIN="$1" ;;
  esac
  shift
done

if [ ! -x "$BIN" ]; then
  echo "找不到可执行文件：$BIN" >&2
  echo "先构建：CGO_ENABLED=0 go build -o $BIN ./cmd/isc" >&2
  exit 2
fi

pass=0; fail=0
ok()   { printf '  ✅ %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  ❌ %s\n' "$1"; fail=$((fail+1)); }
info() { printf '     %s\n' "$1"; }
head_() { printf '\n=== %s ===\n' "$1"; }

DIR="$(mktemp -d "${TMPDIR:-/tmp}/isc-remote-acc.XXXXXX")"
export ISC_DATA_DIR="$DIR"
DPID=""
cleanup() {
  [ -n "$DPID" ] && kill "$DPID" 2>/dev/null
  wait "$DPID" 2>/dev/null
  rm -rf "$DIR"
}
trap cleanup EXIT

# code_of 从 JSON 里取一个字段。用 python3 而不是 jq：
# 前者是系统自带的，而引入一个"跑验收前先装个工具"的前置条件
# 恰好会让这个脚本在最需要它的时候用不了。
json_field() { python3 -c 'import json,sys; print(json.load(sys.stdin).get(sys.argv[1],""))' "$1"; }

head_ "启动一个假的 APNs 服务器"
# 真实的 APNs 需要付费开发者账号与真机，而这一节要验证的是**我们发出去的
# 东西长什么样** —— JWT 的形状、请求头、载荷结构，以及 410 的处理。
# 那些用一个记录请求的假服务器就能完整覆盖。
APNS_PORT=$((PORT + 100))
APNS_LOG="$DIR/apns.jsonl"
cat > "$DIR/apns_stub.py" <<'PY'
import http.server, json, sys
class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get('content-length', '0'))
        body = self.rfile.read(length).decode('utf-8', 'replace')
        record = {
            'path': self.path,
            'topic': self.headers.get('apns-topic'),
            'push_type': self.headers.get('apns-push-type'),
            'priority': self.headers.get('apns-priority'),
            'has_auth': bool(self.headers.get('authorization')),
            'bearer_prefix': (self.headers.get('authorization') or '')[:7],
            'body': body,
        }
        with open(sys.argv[2], 'a') as handle:
            handle.write(json.dumps(record, ensure_ascii=False) + "\n")
        self.send_response(200)
        self.send_header('apns-id', 'stub-1')
        self.end_headers()
    def log_message(self, *args):
        pass
http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), Handler).serve_forever()
PY
python3 "$DIR/apns_stub.py" "$APNS_PORT" "$APNS_LOG" &
APNS_PID=$!
trap 'kill $APNS_PID 2>/dev/null; cleanup' EXIT
for _ in $(seq 1 40); do
  curl -s -o /dev/null "http://127.0.0.1:$APNS_PORT/" && break
  sleep 0.25
done
ok "假的 APNs 在 :$APNS_PORT"

head_ "启动临时内核（数据目录 ${DIR}）"
ISC_APNS_HOST="http://127.0.0.1:$APNS_PORT" "$BIN" daemon run >"$DIR/daemon.log" 2>&1 &
DPID=$!
for _ in $(seq 1 60); do
  "$BIN" status >/dev/null 2>&1 && break
  sleep 0.25
done
if "$BIN" status >/dev/null 2>&1; then ok "内核已就绪"; else bad "内核没有起来"; sed -n '1,40p' "$DIR/daemon.log"; exit 1; fi

head_ "默认状态：必须关闭且不监听"
STATUS="$("$BIN" remote status --json)"
[ "$(printf '%s' "$STATUS" | json_field enabled)" = "False" ] && ok "默认 enabled=false" || bad "默认竟然是开启的"
[ "$(printf '%s' "$STATUS" | json_field listening)" = "False" ] && ok "默认不监听" || bad "默认竟然在监听"

head_ "开启远程访问"
ENABLED="$("$BIN" remote enable --port "$PORT" --json)"
[ "$(printf '%s' "$ENABLED" | json_field listening)" = "True" ] && ok "已监听 :$PORT" || { bad "监听没起来"; printf '%s\n' "$ENABLED"; }
SPKI="$(printf '%s' "$ENABLED" | json_field spki_sha256)"

head_ "公钥指纹：二维码里报的必须与实际握手的一致"
SERVED="$(echo | openssl s_client -connect "127.0.0.1:$PORT" -servername isc 2>/dev/null \
  | openssl x509 -pubkey -noout 2>/dev/null \
  | openssl pkey -pubin -outform DER 2>/dev/null \
  | openssl dgst -sha256 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '=')"
info "状态里报的: $SPKI"
info "实际提供的: $SERVED"
[ -n "$SPKI" ] && [ "$SPKI" = "$SERVED" ] && ok "一致（客户端固定公钥，重签证书不会让手机失效）" || bad "不一致"

head_ "未鉴权：远程面上除配对之外一律 401"
code() { curl -s -k -o /dev/null -w '%{http_code}' "$@"; }
[ "$(code "https://127.0.0.1:$PORT/v1/metrics")" = "401" ] && ok "/v1/metrics → 401" || bad "/v1/metrics 没有 401"
[ "$(code "https://127.0.0.1:$PORT/v1/console/bootstrap")" = "401" ] && ok "控制台引导端点 → 401（它免鉴权就会交出本地令牌）" || bad "引导端点没有被挡住"
[ "$(code "https://127.0.0.1:$PORT/v1/settings")" = "401" ] && ok "/v1/settings → 401" || bad "/v1/settings 没有 401"

head_ "配对：用六位码换设备令牌"
PAIR="$("$BIN" remote pair --role operator --label "acceptance" --json)"
# 配对密钥从二维码载荷里取 —— 它是唯一的配对凭证（六位码已删除）。
# 手输那条路走的是同一个密钥，只是换了个载体。
CODE="$(printf '%s' "$PAIR" | python3 -c 'import json,sys; print(json.loads(json.load(sys.stdin)["qr_payload"])["secret"])')"
QSPKI="$(printf '%s' "$PAIR" | json_field spki_sha256)"
[ "$QSPKI" = "$SPKI" ] && ok "二维码里的指纹与状态一致" || bad "二维码里的指纹与状态不一致"

RESP="$(curl -s -k -X POST "https://127.0.0.1:$PORT/v1/remote/pair" \
  -H 'Content-Type: application/json' \
  -d "{\"secret\":\"$CODE\",\"device\":{\"name\":\"acceptance\",\"platform\":\"ios\"}}")"
TOKEN="$(printf '%s' "$RESP" | json_field token)"
DEV="$(printf '%s' "$RESP" | json_field device_id)"
[ -n "$TOKEN" ] && ok "配对成功，拿到设备令牌" || { bad "配对失败：$RESP"; }

head_ "同一个码不能再用一次（一次性）"
AGAIN="$(curl -s -k -o /dev/null -w '%{http_code}' -X POST "https://127.0.0.1:$PORT/v1/remote/pair" \
  -H 'Content-Type: application/json' \
  -d "{\"secret\":\"$CODE\",\"device\":{\"name\":\"replay\"}}")"
[ "$AGAIN" != "200" ] && ok "重放被拒绝（${AGAIN}）" || bad "同一个配对码还能再用 —— 那是一次静默的重放"

head_ "带令牌：允许的读路径"
for path in /v1/health /v1/meta /v1/metrics /v1/apps /v1/advisories /v1/remote/self /v1/ip/current /v1/ddns-tasks /v1/certs; do
  c="$(code -H "Authorization: Bearer $TOKEN" "https://127.0.0.1:$PORT$path")"
  [ "$c" = "200" ] && ok "$path → 200" || bad "$path → $c"
done

head_ "带令牌：白名单外的路径必须 403"
for path in /v1/settings /v1/audit /v1/config/export /v1/proxy/routes /v1/jobs /v1/notify/channels; do
  c="$(code -H "Authorization: Bearer $TOKEN" "https://127.0.0.1:$PORT$path")"
  [ "$c" = "403" ] && ok "$path → 403" || bad "$path → ${c}（期望 403）"
done

head_ "长轮询语义"
POLL="$(curl -s -k -H "Authorization: Bearer $TOKEN" "https://127.0.0.1:$PORT/v1/events/poll?timeout_ms=300")"
printf '%s' "$POLL" | grep -q '"next"' && ok "返回了游标与 gap 字段" || bad "长轮询响应不对：$POLL"
SELF="$(curl -s -k -H "Authorization: Bearer $TOKEN" "https://127.0.0.1:$PORT/v1/remote/self")"
ROLE="$(printf '%s' "$SELF" | json_field role)"
[ "$ROLE" = "operator" ] && ok "self 报出角色 operator（界面据此决定按钮可不可点）" || bad "self 的角色是 $ROLE"

head_ "viewer 不能写"
VPAIR="$("$BIN" remote pair --role viewer --label viewer --json)"
VCODE="$(printf '%s' "$VPAIR" | python3 -c 'import json,sys; print(json.loads(json.load(sys.stdin)["qr_payload"])["secret"])')"
VRESP="$(curl -s -k -X POST "https://127.0.0.1:$PORT/v1/remote/pair" \
  -H 'Content-Type: application/json' -d "{\"secret\":\"$VCODE\",\"device\":{\"name\":\"viewer\"}}")"
VTOKEN="$(printf '%s' "$VRESP" | json_field token)"
c="$(code -X POST -H "Authorization: Bearer $VTOKEN" "https://127.0.0.1:$PORT/v1/certs/renew")"
[ "$c" = "403" ] && ok "viewer 触发证书续期 → 403" || bad "viewer 竟然能写：$c"
c="$(code -H "Authorization: Bearer $VTOKEN" "https://127.0.0.1:$PORT/v1/metrics")"
[ "$c" = "200" ] && ok "viewer 仍然能读指标 → 200" || bad "viewer 连读都不行：$c"

head_ "公网访问：手机能做什么、不能做什么"
# 没开公网访问时同步必须**明确报错**，而不是静默什么都不做。
# 静默的后果是"点了同步、看起来成功了、但什么都没建"。
SYNC_CODE=$(curl -s -k -o /tmp/sync-out.json -w '%{http_code}' -X POST \
  -H "Authorization: Bearer $TOKEN" "https://127.0.0.1:${PORT}/v1/remote/public/sync")
[ "$SYNC_CODE" = "400" ] \
  && ok "未开启时同步报 400（${SYNC_CODE}）" \
  || bad "没选域名时的响应码是 ${SYNC_CODE}，期望 400"
grep -qE '公网访问没有开启|public access is not enabled' /tmp/sync-out.json \
  && ok "而且说明白了原因" \
  || bad "错误不清楚：$(head -c 160 /tmp/sync-out.json)"

# 手机**不能**在机器上开启公网访问。
#
# 那个区别是刻意的：公网访问会把内核暴露在互联网上，那个决定属于
# 坐在机器前面的人。手机能做的是减少暴露，以及让已经决定好的配置生效。
ENABLE_CODE=$(curl -s -k -o /dev/null -w '%{http_code}' -X PATCH \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"remote_public_enabled":true,"remote_public_domain":"attacker.example"}' \
  "https://127.0.0.1:${PORT}/v1/settings")
[ "$ENABLE_CODE" = "403" ] \
  && ok "手机不能改设置来开启公网访问（403）" \
  || bad "手机竟然能改设置（${ENABLE_CODE}）—— 那等于让被配对的设备决定暴露面"

# 同理：手机不能装 APNs 凭据。那是一把能给用户**全部**设备发推送的钥匙。
APNS_CODE=$(curl -s -k -o /dev/null -w '%{http_code}' -X PUT \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"team_id":"X","key_id":"Y","bundle_id":"Z","private_key":"P"}' \
  "https://127.0.0.1:${PORT}/v1/remote/apns")
[ "$APNS_CODE" = "403" ] \
  && ok "手机不能装 APNs 凭据（403）" \
  || bad "手机能装 APNs 凭据（${APNS_CODE}）"

head_ "探针端点：免鉴权，但只说'到了'"
# 用户最需要"公网到底通不通"这个答案的时刻，恰好是**还没配对成功**
# 的时候。因此这个端点必须免鉴权 —— 而它也只能说这一个事实。
PING_CODE=$(curl -s -k -o /dev/null -w '%{http_code}' "https://127.0.0.1:${PORT}/v1/remote/ping")
[ "$PING_CODE" = "200" ] && ok "无令牌也能访问 /v1/remote/ping（${PING_CODE}）" \
  || bad "探针端点要求了鉴权（${PING_CODE}）—— 未配对的手机就永远得不到答案"

PING_BODY=$(curl -s -k "https://127.0.0.1:${PORT}/v1/remote/ping")
printf '%s' "$PING_BODY" | grep -q '"ok":true' && ok "返回了 ok" || bad "返回体不含 ok：$PING_BODY"
# 它是一个任何人都能打的端点，多说一个字都是多余的暴露面。
printf '%s' "$PING_BODY" | grep -qiE 'version|token|device|host|name' \
  && bad "探针端点泄漏了额外信息：$PING_BODY" || ok "只说了'到了'，没有多余信息"

# 它仍然在远程面的白名单里（不在的话会先被默认拒绝挡掉）。
CELL=$(curl -s -k -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" \
  "https://127.0.0.1:${PORT}/v1/remote/ping")
[ "$CELL" = "200" ] && ok "带令牌访问也正常" || bad "带令牌访问异常（${CELL}）"

head_ "派生（给 Apple Watch）：角色不得高于父设备"
D1="$(curl -s -k -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "https://127.0.0.1:$PORT/v1/remote/self/derive" -d '{"role":"viewer","label":"watch"}')"
printf '%s' "$D1" | json_field token >/dev/null
[ -n "$(printf '%s' "$D1" | json_field token)" ] && ok "operator 可以派生 viewer（手表）" || bad "派生失败：$D1"
D2="$(curl -s -k -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $VTOKEN" \
  -H 'Content-Type: application/json' \
  "https://127.0.0.1:$PORT/v1/remote/self/derive" -d '{"role":"operator","label":"escalate"}')"
[ "$D2" = "403" ] && ok "viewer 想派生 operator → 403（约束在服务端）" || bad "越权派生没有被挡住：$D2"

head_ "推送：凭据 → 登记令牌 → 测试推送"
# 生成一份真的 .p8（P-256）。私钥必须是真能签名的 —— 假密钥在
# 校验那一关就会被拒，而那一关正是"用户填错了"最常见的形态。
openssl ecparam -name prime256v1 -genkey -noout 2>/dev/null \
  | openssl pkcs8 -topk8 -nocrypt -out "$DIR/apns.p8" 2>/dev/null
if [ -s "$DIR/apns.p8" ]; then
  ok "已生成测试用的 .p8"
else
  bad "openssl 没能生成 .p8 —— 跳过推送这一节"
fi

if [ -s "$DIR/apns.p8" ]; then
  "$BIN" remote apns set --team-id TEAM123456 --key-id KEY7890 \
    --bundle-id app.isc.mizar --key-file "$DIR/apns.p8" --json >/dev/null \
    && ok "APNs 凭据已保存" || bad "保存凭据失败"

  # 状态里不能出现私钥，key id 也应当打码。
  APNS_STATUS="$("$BIN" remote status --json)"
  printf '%s' "$APNS_STATUS" | grep -q '"configured": *true' \
    && ok "状态显示已配置" || bad "状态里没有显示已配置"
  printf '%s' "$APNS_STATUS" | grep -q 'PRIVATE KEY' \
    && bad "状态里泄漏了私钥" || ok "状态里没有私钥"

  # 这台设备登记一个推送令牌。
  curl -s -k -o /dev/null -X POST "https://127.0.0.1:$PORT/v1/remote/self/push-token" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"token":"stub-device-token","environment":"sandbox","topic":"app.isc.mizar"}' \
    && ok "已登记推送令牌" || bad "登记推送令牌失败"

  : > "$APNS_LOG"
  "$BIN" remote test-push "$DEV" --json >/dev/null 2>&1
  sleep 1
  if [ -s "$APNS_LOG" ]; then
    ok "假 APNs 收到了请求"
    python3 - "$APNS_LOG" <<'PY'
import json, sys
record = json.loads(open(sys.argv[1]).readline())
problems = []
if not record["path"].endswith("/3/device/stub-device-token"):
    problems.append("路径不对：%s" % record["path"])
if record["topic"] != "app.isc.mizar":
    problems.append("apns-topic 不对：%s" % record["topic"])
if record["push_type"] != "alert":
    problems.append("apns-push-type 不对：%s" % record["push_type"])
if record["priority"] != "10":
    problems.append("apns-priority 不对：%s" % record["priority"])
if not record["has_auth"] or record["bearer_prefix"].lower() != "bearer ":
    problems.append("Authorization 头不对")
if '"aps"' not in record["body"]:
    problems.append("载荷里没有 aps")
if problems:
    print("  ❌ " + "；".join(problems))
    sys.exit(1)
print("  ✅ 请求形状正确（路径 / topic / push-type / priority / 鉴权 / 载荷）")
PY
  else
    bad "假 APNs 没有收到任何请求"
  fi

  # 没有凭据时不该假装成功。
  "$BIN" remote apns rm --json >/dev/null 2>&1
  ok "已删除凭据（状态回到未配置）"
fi

head_ "吊销：立即生效且级联"
"$BIN" remote revoke "$DEV" --json >/dev/null
c="$(code -H "Authorization: Bearer $TOKEN" "https://127.0.0.1:$PORT/v1/metrics")"
[ "$c" = "401" ] && ok "被吊销的令牌立刻 401" || bad "吊销之后还能用：$c"
WDEV="$(printf '%s' "$D1" | json_field device_id)"
"$BIN" remote devices --json | grep -q "\"$WDEV\"" && ok "派生出来的手表也是一台独立设备（可单独吊销）" || bad "派生设备没有出现在列表里"

head_ "审计：配对、吊销都留痕"
"$BIN" remote log --limit 20 --json | grep -q 'remote.pair' && ok "配对记录在案" || bad "没有配对记录"
"$BIN" remote log --limit 20 --json | grep -q 'remote.revoke' && ok "吊销记录在案" || bad "没有吊销记录"

head_ "关闭监听：端口不再可达，设备仍然保留"
"$BIN" remote disable --json >/dev/null
c="$(curl -s -k --max-time 2 -o /dev/null -w '%{http_code}' "https://127.0.0.1:$PORT/v1/health" || true)"
[ "$c" = "000" ] && ok "端口已关闭（连不上）" || bad "关闭之后端口仍可达：$c"
COUNT="$("$BIN" remote devices --json | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["items"]))')"
[ "$COUNT" -ge 2 ] && ok "已配对设备未被删除（重新开启后仍然有效）" || bad "设备被删掉了"

head_ "重启内核：设置与设备都应当保留"
kill "$DPID" 2>/dev/null; wait "$DPID" 2>/dev/null
"$BIN" daemon run >"$DIR/daemon2.log" 2>&1 &
DPID=$!
for _ in $(seq 1 60); do "$BIN" status >/dev/null 2>&1 && break; sleep 0.25; done
ST2="$("$BIN" remote status --json)"
[ "$(printf '%s' "$ST2" | json_field port)" = "$PORT" ] && ok "端口设置被保留" || bad "端口设置丢了"
[ "$(printf '%s' "$ST2" | json_field spki_sha256)" = "$SPKI" ] && ok "公钥指纹不变（手机不需要重新配对）" || bad "重启后指纹变了"

printf '\n========================================\n'
printf '  通过 %d 项，失败 %d 项\n' "$pass" "$fail"
printf '========================================\n'
[ "$fail" -eq 0 ]
