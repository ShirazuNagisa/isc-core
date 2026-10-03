# 本机验收（macOS / 这台 Mac）

> v0.1.0 的验收口径是"**在这台 Mac 上功能全部可用、无已知问题**"（见 D24 与
> PLAN §0 的方向表）。这份文档把验收拆成两半：**不需要 root 的**（已自动跑过，
> 结果记在下面）与**需要 root 的**（要你手动跑，命令照抄即可）。

---

## 一、不需要 root 的部分：2026-10-03 实跑，全绿

用当前 `main`（`d317d3a`）构建的二进制，数据目录走**默认路径**
（`~/Library/Application Support/ISC`，即 GUI 将来会用的那个）：

| 检查 | 结果 |
|---|---|
| 构建与版本 | `CGO_ENABLED=0 go build ./cmd/isc` ✓；`isc version` 报 `darwin/arm64`、`go1.27.1`、commit 与构建时刻 ✓ |
| 内核启动 | `isc daemon run` 起得来；`isc status` 报 `running` / `health ok` / `unix_socket` ✓ |
| 数据目录权限 | 数据目录与 `run/` 都是 `drwx------`（0700）✓ |
| 运行期文件 | `run/isc.sock` 与 `run/runtime.json` 都是 `srw-------`/`-rw-------`（0600）✓ |
| **六个平台后端** | 全部 `available=true`：`firewall=pf`、`ip_monitor=polling`、`low_port_binder=darwin-native`、`secret_store=macos-keychain`、`service_manager=launchd`、`transport=unix-socket` ✓ |
| `isc doctor` | IPv6 直连条件齐全：en1 上 3 个全局地址 + 1 个委派前缀（/64）✓；pf 后端已接入 ✓；并如实标注"上游可达性本机无法自测" ✓ |
| 只读命令 | `ip` / `ddns list` / `cert list` / `credential list` / `changes` / `settings` / `expose --help` 全部正常 ✓ |
| 变更计划（不含应用） | `isc expose --port 18080` 生成计划并给出预览；**并提示"该端口没有服务在监听"** ✓ |
| 无 root 时的失败路径 | `isc service install` → "需要 root 权限，请用 sudo 重新运行" ✓；`isc expose --yes` → pf 写 `/etc/pf.conf` 被拒，**并自动回滚**，报错直指原因 ✓ |

复跑方式：

```sh
CGO_ENABLED=0 go build -o /tmp/isc-acc/isc ./cmd/isc
/tmp/isc-acc/isc daemon run &            # 或前台跑
/tmp/isc-acc/isc status --json | python3 -m json.tool | head -30
/tmp/isc-acc/isc doctor
/tmp/isc-acc/isc status                   # 末尾会列出六个后端的可用性
```

---

## 二、需要 root 的部分：2026-10-03 已实跑，16/16 通过

用 `scripts/acceptance-macos-sudo.sh` 在真机上跑完（脚本会自动复原）：

| 检查 | 结果 |
|---|---|
| launchd 服务 | plist 写入 ✓、里面的可执行文件正确 ✓、套接字出现 ✓、`status --json` 报 `running=true` ✓、`health=ok` ✓、`service status` 认为内核可达 ✓、卸载后 plist 已删 ✓ |
| pf 真的放行 | `expose --yes` **确实应用**（状态 `applied`）✓、`/etc/pf.anchors/isc` 写入 ✓、**pf 被启用** ✓、内核里真的有这条规则（`pass in inet proto tcp from any to any port = 18080 flags S/SA keep state`）✓、变更记录有 ✓、撤销成功 ✓、撤销后规则消失 ✓ |

### 这一轮抓到的两个问题

1. **`isc expose --port N --yes --json` 静默什么都没做**（已修）。
   `--json` 分支在生成计划之后直接 `return`，把"确认 + 应用"整段跳过 ——
   于是它打印计划、退出码 0，而系统里没有任何变化。JSON 输出是给脚本用的，
   而脚本判断成败只看退出码，因此这是"看起来成功、实际没做"的那一类。
   修复 + 回归测试见 `internal/cli/expose_json_test.go`（带 `--yes` 必须真的
   调 apply；不带 `--yes` 只输出计划且绝不应用）。

2. **撤销之后 pf 仍是 Enabled**（已知偏差，未修）。
   验收前 pf 是 `Disabled`，撤销规则后规则确实清干净了，但 pf 的**启用状态**
   没被恢复 —— 而变更预览里写的是"撤销时会恢复成添加之前的状态"。
   anchor 挂载点（`/etc/pf.conf` 里那两行 + anchor 文件）是**有意保留**的：
   anchor 文件里明确写了"当前没有任何 ISC 规则"以及如何手工移除。
   要修的话，需要在变更记录里记住"是不是我们启用的 pf"，撤销时按原状恢复。

### 复跑方式

```sh
CGO_ENABLED=0 go build -o /tmp/isc-acc/isc ./cmd/isc
sudo scripts/acceptance-macos-sudo.sh /tmp/isc-acc/isc
```

## 二·原、需要 root 的部分（手动跑的老说明）

三条命令都会**真的改系统**（注册 launchd 服务 / 改 pf 规则），因此没有替你跑。
每条后面都写了怎么撤销。

### 1. 把内核注册成系统服务（launchd）

```sh
sudo /tmp/isc-acc/isc service install       # 数据目录自动落到 /Library/Application Support/ISC
isc      service status                     # 预期两行：内核 / 系统服务
sudo /tmp/isc-acc/isc service start
sleep 2
sudo /tmp/isc-acc/isc status --data-dir "/Library/Application Support/ISC"   # 预期 running
sudo /tmp/isc-acc/isc service stop
sudo /tmp/isc-acc/isc service uninstall     # 撤销：删掉 plist 并卸载
```

要看它是否真的在跑：`sudo launchctl print system/com.isc.core | head -20`。

### 2. 真的放行一个端口（pf）

先起一个临时监听，再放行它，这样"规则开好了但没服务"的坑不会干扰判断：

```sh
python3 -m http.server 18080 &              # 临时服务
sudo /tmp/isc-acc/isc expose --port 18080 --yes
sudo pfctl -a isc -sr                       # 看 isc 的 anchor 里实际写了什么
/tmp/isc-acc/isc changes                    # 变更记录里应当有这一条
# 撤销：
/tmp/isc-acc/isc rollback <变更 ID>          # 或
sudo /tmp/isc-acc/isc expose --port 18080 --revoke
```

### 3. 完整链路（需要真实域名与 DNS 凭据）

`expose` 的完整闭环是"检测地址 → 放行端口 → 写 DNS 记录"，因此要有：

```sh
/tmp/isc-acc/isc credential add <provider>   # 按提示填凭据（密钥进钥匙串）
/tmp/isc-acc/isc zones                       # 确认能列出你的域名
sudo /tmp/isc-acc/isc expose --port 18080 --provider <provider> --yes
```

**外部验证不能省**：`isc doctor` 明确指出"从本机访问自己的公网地址通常走回环"，
所以必须用**手机 4G/5G**打开验证地址确认 —— 这一步是区分"本机没配好"与
"运营商封了"的唯一手段。

---

## 三、验收后的清理

```sh
sudo /tmp/isc-acc/isc service uninstall          # 若装过服务
sudo /tmp/isc-acc/isc rollback <变更 ID>          # 若放过行
pkill -f "isc-acc/isc daemon run"                # 若手动起过内核
```

数据目录（`~/Library/Application Support/ISC`）**不要随手删** —— 里面有主密钥
（macOS 钥匙串里的条目与它绑定，见 D23）。真要清空，先 `isc credential list`
确认没有要留的凭据。
