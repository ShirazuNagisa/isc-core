# ISC-Core 开发计划

> 架构决策依据见 [`DECISIONS.md`](./DECISIONS.md)，模块与接口细节见 [`ARCHITECTURE.md`](./ARCHITECTURE.md)。

- 仓库：`github.com/ShirazuNagisa/isc-core`
- 许可证：GPL-3.0
- 目标：无 GUI 的跨平台 Go 守护进程，让无公网 IPv4 的家用电脑可从公网访问

---

## 0. 当前状态

| 项 | 状态 |
|---|---|
| 工作区 | `D:\Data\2_Areas\Coding\Project\ISC-Core`，git 仓库（`main` 分支） |
| `ddns-go-master/` | 上游参考源，v6，131 文件，MIT，**已加入 `.gitignore`**（`git check-ignore` 已验证） |
| Go 工具链 | ✅ 已安装 `go1.27.1` 到 `%LOCALAPPDATA%\Programs\go`（免管理员），并加入用户 PATH |
| GOMODCACHE 代理 | ✅ `GOPROXY=https://goproxy.cn,direct`、`GOSUMDB=sum.golang.google.cn`、`GOTOOLCHAIN=local` |
| Node / pnpm | Node v24.18 + pnpm 已就绪（控制台用） |
| Rust | 1.97.1 `x86_64-pc-windows-msvc`（下游 GUI 用，本仓库不依赖） |
| MSVC 链接器 | 未安装。**不影响内核**——坚持纯 Go（无 cgo）即可 |

### M0 完成情况

| 项 | 状态 | 证据 |
|---|---|---|
| M0-1 工具链 | ✅ | `go version` → go1.27.1 |
| M0-2 文档 | ✅ | `docs/{PLAN,ARCHITECTURE,DECISIONS}.md` |
| M0-3 仓库骨架 | ✅ | `LICENSE`(GPL-3.0) / `THIRD_PARTY_NOTICES.md` / `README.md` / `go.mod` |
| M0-4 平台适配层 | ✅ | 6 个接口 + 三平台实现；7 个 GOOS/GOARCH 目标全部编译通过 |
| M0-5 OpenAPI 管线 | ✅ | `api/openapi.yaml` → `internal/api/gen`；CI 漂移检查 |
| M0-6 守护进程与传输 | ✅ | 命名管道（SDDL 收紧）+ 回环 TCP + token + `runtime.json`；手动实测 `isc status` 走 `named_pipe` |
| M0-7 事件总线 + 任务引擎 | ✅ | 单元测试 + 端到端测试（进度/结束/取消/gap/日志桥接） |
| M0-8 CLI 骨架 | ✅ | `version` / `status` / `daemon run`，全部支持 `--json` |
| M0-9 CI | ✅ | `.github/workflows/ci.yml`（矩阵编译 + 三平台测试 + race + 契约漂移） |
| M0-10 许可证审计 | ✅ | 9 个编译期依赖，全部与 GPL-3.0 兼容，见 `THIRD_PARTY_NOTICES.md` |
| M0-11 硬验收 | ✅ | 全部测试通过；`isc status` 经命名管道拿到版本；跨平台编译矩阵通过 |

### M1 完成情况

| 项 | 状态 | 证据 |
|---|---|---|
| SQLite schema + 迁移框架 | ✅ | `internal/store/migrations/0001_init.sql`；迁移在独立事务中执行，重复打开幂等 |
| `SecretStore` 三平台 | ✅ | Windows DPAPI + 文件承载；macOS Keychain / Linux Secret Service（经外部命令，无 cgo）；均带回退与显式告警 |
| 主密钥 + AES-256-GCM | ✅ | `internal/secret`；每次加密随机 nonce，逐字节翻转测试证明完整性校验生效 |
| 凭据 CRUD API | ✅ | `/v1/credentials` 全套；敏感字段一律掩码，回传掩码即保留原值 |
| 服务商注册表 | ✅ | `/v1/providers` 输出字段定义与能力位；29 家服务商，Tier-1 六家字段齐备 |
| YAML 导入导出 | ✅ | 默认不含明文；`dry_run` 默认开启 |
| ddns-go 迁移 | ✅ | 按 `DdnsGoSlot` 显式映射槽位；凭据去重；webhook 与解析任务如实报告"暂未迁移" |
| 审计日志 | ✅ | 所有写操作留痕，含失败与请求 ID；内容不含敏感值 |
| 任务落库 | ✅ | 任务引擎**代码一行未改**，仅把 `job.Store` 实现从内存换成 SQLite |
| Cloudflare 凭据校验 | ✅ | `GET /user/tokens/verify`，只读调用 |

### M1 硬验收结果

| 验收标准 | 证据 |
|---|---|
| 能通过接口增删改 DNS 凭据 | `TestCredentialLifecycle`（经命名管道走真实 HTTP） |
| 数据库文件中凭据为密文 | `TestCredentialsAreEncryptedAtRest` —— 直接读 `.db` / `-wal` / `-shm`，断言明文不出现；同时反向验证明文确实能取回（否则断言无意义） |
| 重启后凭据可正常解密 | `TestCredentialSurvivesRestart` —— 同一数据目录启动两次 |
| 能导入真实的 ddns-go 配置 | `TestImportDdnsGoThroughAPI` —— 预览不写入、应用后凭据与密钥正确、未迁移内容有明确警告 |

### M2 完成情况（进行中）

| 项 | 状态 | 证据 |
|---|---|---|
| ddns-go 移植 | ✅ | `internal/ddnsgo/`，8335 行、30 家服务商、5 套签名；由脚本机械变换生成 |
| 移植可用性验证 | ✅ | `TestCallbackDynamicUpdateEndToEnd` —— 假服务商收到真实请求，串起凭据 → 槽位 → 地址注入 → 域名解析 → HTTP → 结果翻译 |
| IPMonitor | ✅ | `internal/platform/ipmon.go`，可移植轮询实现；前缀变化与地址变化分开检测 |
| 动态解析引擎 | ✅ | `internal/ddns/`，按 (任务, 记录类型) 分桶的防抖、任务级缓存失效 |
| 调度器 | ✅ | 定时 + 事件触发 + 1 秒合流；任务级防重入 |
| 任务 API | ✅ | `/v1/ddns-tasks` 全套 + `/v1/ip/current`；凭据删除前的引用检查 |
| CLI | ✅ | `isc ip`、`isc ddns list`、`isc ddns run` |
| 端到端验收 | ✅ | `TestDynamicDNSEndToEnd` —— 经命名管道走真实 HTTP |
| 真机验证 | ✅ | 中国移动家宽实测：识别出 `2409:8a50:6a1:7450::/64` 委派前缀 |
| Tier-1 全量记录 CRUD | ✅ | 六家（Cloudflare / 阿里云 / 腾讯云 / DNSPod / 华为云 / GoDaddy），`internal/provider/tier1/` |
| 能力矩阵文档 | ✅ | `docs/PROVIDER-MATRIX.md` —— 含各家记录模型差异与全部未验证项 |
| 验证控制台 SPA | ✅ | `internal/console/`（原生 JS，无构建链）+ `/console/` + `isc console` |

### M2 硬验收结果

| 验收标准 | 证据 |
|---|---|
| 真机 IPv6 前缀变化 → AAAA 自动更新 | 中国移动家宽实测：识别出 `2409:8a50:6a1:7450::/64`；`TestDynamicDNSEndToEnd` 经真实 HTTP 验证整条链路 |
| 控制台可增删改任意记录类型 | 六家 Tier-1 完整 CRUD + `/v1/credentials/{id}/zones/.../records` 全套端点；控制台已接入 |
| 控制台可被浏览器打开 | 真机实测：根路径 302 → `/console/`（200，7076 字节）；app.js / style.css 正常 |
| 控制台不需要用户手动粘贴令牌 | `/v1/console/bootstrap` 交付，真机验证令牌与 `runtime.json` 一致且可用 |
| 控制台不成为新的攻击面 | 伪造 Host 返回 403 且响应不含令牌；不带令牌访问 `/v1/meta` 仍为 401 |

### M2-d 的实施方式与结果

Tier-1 六家的实现是**并行委托给五个子代理**完成的，每个代理拿到：
接口定义、参考实现（Cloudflare）、参考测试、以及**移植代码里的真实
API 用法**（`internal/ddnsgo/provider_*.go`）作为端点与签名的依据。

结果与预期一致的部分：五家都独立完成了完整 CRUD、零新增依赖、
测试全部通过。

**超出预期的部分更值得记下来** —— 三个子代理主动否决了我的指示：

1. **DNSPod 那家拒绝了我给的 TTL 说法。** 我在任务书里写"TTL 只接受
   特定几个值"，它去查了官方文档后指出那是**区间**（1–604800 + 套餐
   最小值）而非档位表，并按文档实现。它是对的：按档位取整等于静默
   改数据（900→1800）。
2. **华为云那家纠正了我的端点版本。** 我写的是 `/v2/...recordsets`，
   它核对官方文档后确认记录集那一族是 **v2.1**（域名列表仍是 v2），
   并在注释里标明"两个版本并存不是笔误"。
3. **DNSPod 那家指出"没有只读校验端点"这个理由对 DNSPod 不成立** ——
   `Info.Version` 是可用的。它没有借这个理由糊弄过去，而是如实说明
   "当前未接，需要的话可以加"。

三家都主动报告了自己**没有**验证的假设（分页游标形态、权限副作用、
weight 丢失），并且都没有粉饰。这些已全部进 `docs/PROVIDER-MATRIX.md`
的「已知限制与未验证项」。

### M2 真机验证抓到的三个问题

这三个都是**只有跑在真实机器上才会暴露**的，单元测试的构造数据里不会出现：

1. **`/128` 主机路由被当成了委派前缀。** Windows 把 IPv6 主机地址报成 `/128`，
   而隐私扩展地址默认**每小时轮换**。若不区分，内核会每小时检测到一次
   "前缀变化"并触发全量更新 —— 白白消耗服务商配额，用户还会收到莫名通知。
   修法：只把 `/64` 及更粗的前缀视为委派网段（`isDelegatedPrefix`）。
2. **虚拟网卡识别靠英文名称前缀，在中文 Windows 上完全失效。**
   真机上出现了 `蓝牙网络连接`、`本地连接* 1`、`VMware Network Adapter VMnet1`
   （以 `VMware` 开头，不是 `vmnet`）。修法：改用"有没有可用地址"作为
   主判据（`HasUsableAddress`），名称列表退为辅助 —— 语言无关。
3. **`primary_ipv4` 取了第一个 IPv4，而真机上第一个是 `169.254.x`（APIPA）。**
   把自分配地址当成"当前公网地址"展示会直接误导用户。
   修法：只取非链路本地、非私有的地址。



本机没有 MSVC 工具链。为保证三平台交叉编译与 CI 简单可靠，**内核禁止引入任何需要 cgo 的依赖**。这直接决定了：

- SQLite 选 `modernc.org/sqlite`（纯 Go），而不是 `mattn/go-sqlite3`；
- `SecretStore` 走系统 API 调用（`golang.org/x/sys/windows`、`syscall`），而不是链接 libsecret 等 C 库；
- CI 中加一道检查：`CGO_ENABLED=0 go build ./...` 必须通过。

**代价（必须知晓）**：`go test -race` 需要 cgo，因此**开发机上跑不了竞态检测**。
处置方式：CI 在 Linux / macOS 上用 `CGO_ENABLED=1` 单独跑一组 `-race` 测试 ——
只影响测试，不影响发布产物的构建形态。见 `.github/workflows/ci.yml` 的 `race` job。

---

## 1. 里程碑总览

| # | 名称 | 核心产出 | 硬验收 |
|---|---|---|---|
| M0 | 地基 | 工具链、骨架、平台接口 + stub、OpenAPI 管线、daemon + 传输 + token、事件总线 + WS + job、CLI 骨架、CI、许可证审计 | ✅ **已完成**：三平台 `CGO_ENABLED=0 go build ./...` 通过；`isc status` 经命名管道拿到版本；CI 全绿 |
| M1 | 配置与凭据 | SQLite schema + 迁移、`SecretStore` 三平台、配置 CRUD API、YAML 导入导出、审计日志 | ✅ **已完成**：接口增删改凭据；库内为密文（含 WAL）；重启后读回正常；ddns-go 配置导入成功 |
| M2 | 动态解析闭环 | ddns-go 移植（Tier-2 30 家 + 签名 + IP 获取 + ipcache + webhook）、`IPMonitor` 三平台 + 前缀事件、调度器、Tier-1 五家全量 CRUD | 🔶 **进行中**：移植、IPMonitor、调度器、任务 API 已完成；Tier-1 CRUD 待做 |
| M3 | 可达性 | 三平台 `Firewall` 后端 + 计划/预览/应用/回滚、引导式外部验证、端口冲突检测、低端口绑定、`isc doctor` | 新机从零到「手机 4G/5G 打开测试页」全流程走通，且可一键回滚 |
| M4 | 反代 + 自动 HTTPS | certmagic + libdns 适配器、反向代理（域名 / SNI 路由 + 非标端口入口）、证书续期 + 事件 | 家宽单个非标端口 + 两个域名指向两个本地服务，HTTPS 全绿，证书自动续 |
| M5 | 打磨与打包 | 控制台补全、三平台服务安装/自启/崩溃重启、安装包与签名、文档与故障排查手册 | 三平台双击安装即可运行 |

---

## 2. M0 — 地基

> 目标：**没有任何业务功能，但所有管道都通了**。此后每个里程碑都是往这条管道里填肉。

### M0-1 工具链

- 安装 Go（免管理员，装到 `%LOCALAPPDATA%\Programs\go`）并加入用户 PATH；
- 验证 `go version`、`go env GOPROXY`；
- 配置 `GOPROXY`（国内网络建议 `https://goproxy.cn,direct`，需确认实际网络状况）。

**验收**　`go version` 输出 ≥ go1.25；`go env GOPROXY` 非空。

### M0-2 文档

- `docs/PLAN.md`（本文件）、`docs/ARCHITECTURE.md`、`docs/DECISIONS.md`。

**验收**　三份文档存在且与已确认决策一致。

### M0-3 仓库骨架

- `LICENSE`（GPL-3.0 全文）、`THIRD_PARTY_NOTICES.md`（ddns-go MIT 声明）、`README.md`；
- `go.mod`（`module github.com/ShirazuNagisa/isc-core`，`go 1.25`）；
- 目录结构（`cmd/`、`internal/`、`api/`、`console/`、`docs/`）；
- `.gitignore` 补充 Go / Node / 运行时产物；
- 首次提交（不含 `ddns-go-master/`）。

**验收**　`git ls-files` 中不含 `ddns-go-master/` 任何文件；`go mod verify` 通过。

### M0-4 平台适配层

- `internal/platform/platform.go` 定义全部接口与通用类型（见 ARCHITECTURE §2）；
- 六个接口 × 三平台实现文件（此阶段实现为 stub，返回 `ErrNotImplemented`）；
- 通用能力（如 `IsUp` 判断、前缀推导工具）放在平台无关文件中。

**验收**　`GOOS=windows/linux/darwin GOARCH=amd64/arm64 CGO_ENABLED=0 go build ./...` 六种组合全部通过。

### M0-5 OpenAPI 管线

- `api/openapi.yaml` v0：`/v1/meta`、`/v1/health`、`/v1/events`、`/v1/jobs*`；
- 引入 `oapi-codegen`（通过 `tools.go` 或 `go run` 固定版本），生成 server 接口与模型到 `internal/api/gen/`；
- 生成命令写入 `Makefile` / `Taskfile`，并加 CI 漂移检查（重新生成后 `git diff --exit-code`）。

**验收**　`go generate ./...` 后 `git diff --exit-code` 为空；`/v1/health` 通过生成的路由可访问。

### M0-6 守护进程与传输

- `isc daemon run`：启动顺序按 ARCHITECTURE §8.1（此阶段只到第 7 步）；
- `Transport`：Windows 命名管道（含 SDDL 收紧）、类 Unix Unix socket（0600），失败回退回环 TCP；
- token 生成（32 字节随机，base64url）与 `runtime.json` 写入 / 退出删除；
- token 鉴权中间件：所有 `/v1/*`（除 `/v1/health` 可选放开）强制校验。

**验收**　`isc daemon run` 启动后 `runtime.json` 出现且内容正确；无 token 请求返回 401；进程退出后 `runtime.json` 被删除。

### M0-7 事件总线与任务引擎（最小版）

- 事件总线：单调序号、环形缓冲（默认保留 1000 条）、水位持久化；
- `/v1/events` WebSocket：支持 `?lastEventId=` 断线补发；
- 任务引擎：提交、进度、取消、超时、持久化到 `jobs` 表；
- 一个演示任务（如 `noop`，按步进上报进度）用于验证。

**验收**　提交 `noop` 任务后，WebSocket 客户端能收到 `job.progress` 与 `job.finished`；断开重连带 `lastEventId` 能补齐缺失事件。

### M0-8 CLI 骨架

- cobra 接入；子命令：`version`、`status`、`daemon run`；
- 客户端发现逻辑（读 `runtime.json` → 管道/socket → 回退回环）；
- 全局 `--json` 标志。

**验收**　`isc status` 经命名管道连上 daemon 并输出结构化状态；`isc status --json` 输出合法 JSON。

### M0-9 CI

- GitHub Actions：矩阵 `{windows, linux, macos} × {amd64, arm64}`；
- 步骤：`go vet`、`go build`（`CGO_ENABLED=0`）、`go test`、OpenAPI 漂移检查、i18n 硬编码字符串检查（此阶段先占位）。

**验收**　CI 在三平台全绿。

### M0-10 依赖许可证审计

- 列出全部直接与间接依赖及其许可证；
- 确认与 GPL-3.0 兼容（Apache-2.0 / MIT / BSD 均兼容；需警惕 GPL 不兼容条款如某些专利条款）；
- 结果写入 `THIRD_PARTY_NOTICES.md`；
- 固化检查脚本，新增依赖时自动跑。

**验收**　`THIRD_PARTY_NOTICES.md` 覆盖全部依赖；无不兼容许可证。

---

## 3. M1 — 配置与凭据

- **存储**：SQLite（`modernc.org/sqlite`）+ 迁移框架（`store/migrations/NNNN_*.sql`）；
- **SecretStore**：Windows DPAPI、macOS Keychain、Linux Secret Service；均不可用时回退到 0600 主密钥文件并显著告警；
- **凭据 API**：CRUD + `verify` 异步任务；
- **配置导入导出**：YAML 格式，默认不含明文凭据；支持 ddns-go 配置迁移（`POST /v1/config/import/ddns-go`）；
- **审计日志**：所有写操作留痕（时间、actor、动作、目标、结果）。

**验收**
1. 控制台可新增 / 修改 / 删除 DNS 凭据；
2. 直接用 SQLite 工具查看数据库，凭据字段为密文；
3. 重启内核后凭据可正常解密使用；
4. 能导入一份真实的 ddns-go `yaml` 配置并正确生成对应实体。

---

## 4. M2 — 动态解析闭环

> 产品的第一口气。此阶段结束后，ISC 已经能解决「IPv6 前缀变了，AAAA 记录要跟着变」这个核心痛点。

### 移植 ddns-go

| 来源 | 去向 | 处理方式 |
|---|---|---|
| `dns/*.go`（30 家 Tier-2） | `internal/dns/providers/tier2/` | 照搬，仅替换包路径与日志接口 |
| `util/aliyun_signer.go`、`huawei_signer.go`、`tencent_cloud_signer.go`、`baidu_signer.go`、`traffic_route_signer.go` | `internal/dns/signers/` | 照搬，签名逻辑不得改动 |
| `util/ip_cache.go` | `internal/dns/ipcache/` | 照搬，防抖语义保持一致 |
| `util/net.go`、`net_resolver.go`、`http_client_util.go` | `internal/netx/` | 移植，去掉全局状态 |
| `config/config.go` 中 IP 获取逻辑 | `internal/ipmon/source/` | 移植（网卡 / URL / 命令三种方式） |
| `config/webhook.go` | `internal/notify/channels/webhook.go` | 移植并改造为通知中心的一个通道 |

**必须保留**　`THIRD_PARTY_NOTICES.md` 中的 MIT 声明与来源标注；每个移植文件头部加注释说明来源与原始路径。

**必须去掉**　ddns-go 的全局可变状态（`util.ForceCompareGlobal`、`dns.Ipcache` 全局数组、`config` 包级缓存单例）。这些是 ddns-go 单体架构的产物，与 ISC 的接口化设计冲突。

### IPMonitor

- Windows：`GetAdaptersAddresses` + 定期轮询（无原生前缀变化通知）；
- Linux：netlink `RTM_NEWADDR` / `RTM_DELADDR`（真正的实时通知）；
- macOS：`getifaddrs` + 路由 socket 监听；
- **前缀推导**：从接口地址与 RA/DHCPv6-PD 信息推导委派前缀（通常 /64），作为 `ip.prefix_changed` 事件的载荷。

### 调度器

- 定时轮询（默认 5 分钟，可配）；
- 事件触发（`ip.changed` / `ip.prefix_changed` 立刻触发一次）；
- 防抖：沿用 ddns-go 的 `cacheTimes` 语义（N 次未变化才与服务商比对）；
- 失败重试与退避；
- 每次执行结果写入 `ddns_tasks.last_result`。

### Tier-1 全量 CRUD

Cloudflare、阿里云 DNS、腾讯云 / DNSPod、华为云 DNS、GoDaddy，实现 `dns.Provider` 全量能力，并产出 `docs/PROVIDER-MATRIX.md` 记录各家实际支持的能力位。

**验收**
1. 真机上改变 IPv6 前缀（重拨 / 手动切换），AAAA 记录在预期时间内自动更新；
2. 控制台可对 Tier-1 五家执行：列区域、列记录、新增（A/AAAA/CNAME/MX/TXT）、修改、删除；
3. Tier-2 服务商在控制台中仅显示「动态解析」功能，其余置灰。

---

## 5. M3 — 可达性

> 此阶段结束后，ISC 才真正兑现「让电脑可从公网访问」。

- **`Firewall` 三平台后端**：
  - Windows Defender Firewall（`netsh advfirewall` 或 COM API `INetFwPolicy2`，需支持按网络配置文件区分）；
  - Linux nftables 为主，探测并适配 ufw；
  - macOS pf；
- **变更计划框架**：所有变更走 `Plan → Preview → Apply → Rollback`，计划含人类可读 diff；
- **引导式外部验证**：生成一次性验证 URL（本机临时监听 + 随机路径），引导用户用手机 4G/5G 打开，结果回写数据库；
- **端口管理**：端口占用检测、冲突提示、低端口绑定能力探测；
- **`isc doctor`**：一键全检 —— 网络栈 / IPv6 地址与全局可达性 / 前缀委派 / 防火墙规则 / 端口占用 / DNS 传播 / 证书链路，并直接输出「到底哪一环断了」。**必须能区分「本机没通」与「运营商封了端口」。**

**验收**
1. 一台全新机器，从 `isc init` 到「手机 4G/5G 打开测试页」全流程可走通；
2. 整个过程产生的系统变更可在控制台一键回滚，回滚后防火墙恢复原状；
3. 故意封掉端口后，`isc doctor` 能明确指出是运营商侧封禁而非本机问题。

---

## 6. M4 — 反向代理与自动 HTTPS

- **`certmagic` 集成**：通过 `libdns_adapter` 让 certmagic 使用 Tier-1 provider 做 DNS-01；
- **反向代理**：
  - 基于 `net/http/httputil.ReverseProxy`；
  - 域名路由 + SNI 路由（`tls.Config.GetCertificate`）；
  - 单个非标端口承载多个域名的入口；
  - WebSocket 透传；
- **证书生命周期**：首次申请、到期前自动续期、续期事件推送；
- **请求头与安全**：清理由客户端伪造的 `X-Forwarded-*`，追加正确值；禁止开放代理语义。

**验收**
1. 在只开放一个非标端口的前提下，两个不同域名分别路由到两个本地服务；
2. HTTPS 证书由 Let's Encrypt 通过 DNS-01 自动签发，无需开放 80；
3. 证书到期前自动续期并推送 `cert.renewed` 事件。

---

## 7. M5 — 打磨与打包

- 验证控制台功能补全（覆盖全部跨接口流程）；
- 三平台服务安装：Windows 服务（含延迟自启）、systemd unit、launchd plist；
- 崩溃重启与看门狗；
- 安装包与代码签名（Windows 需证书，macOS 需公证）；
- 文档：README、快速上手、故障排查手册、常见路由器 IPv6 放行指引；
- 通知模板完善与多语言文案补齐。

**验收**　三平台下载安装包 → 双击安装 → 开箱可用。

---

## 8. 风险登记

| # | 风险 | 影响 | 处置 |
|---|---|---|---|
| R1 | **Go 工具链缺失** | 阻塞全部开发 | M0-1 第一件事 |
| R2 | 三平台适配层同步做 | 进度风险，易卡在某平台细节 | 先定义全部接口 + stub，保证**任何时刻三平台都可编译**；每个后端配一致性测试套件；未实现后端降级为引导模式 |
| R3 | libdns 国内 provider 偏 beta | 证书签发可靠性 | 已用自研接口隔离关键路径（D15），certmagic 走自家 Tier-1 实现 |
| R4 | GoDaddy 无官方 libdns provider | — | Tier-1 本就要手写，无影响；仅其 `CapDNSP01` 需自行实现 |
| R5 | 少数省份连 IPv6 入站也封 | 产品核心价值失效 | `isc doctor` 必须能检测并明确报告；文档给出判断方法并引导用户转向可达性插件（frp 等，后续里程碑） |
| R6 | 路由器 IPv6 防火墙无法自动化 | 用户卡在最后一步 | 引导 + 检测 + 可执行检查清单；`isc doctor` 输出具体到「去路由器哪个菜单」 |
| R7 | **GPLv3 传染下游 GUI** | 许可证事故 | 架构红线写入 README：GUI 只能走 HTTP/WS，**禁止链接内核** |
| R8 | macOS 无法在本机验证 | 质量盲区 | GitHub Actions `macos-latest` + 需要一台真机 / VM |
| R9 | 纯 Go SQLite 性能 | 高并发下可能不足 | 单机低并发场景足够；如不足可换 `zombiezen.com/go/sqlite`（同为纯 Go） |
| R10 | 自研反代的 SSRF / 开放代理风险 | 安全事故 | 上游地址白名单（仅回环 / 私有网段）、清请求头、禁 CONNECT 转发 |
| R11 | 移植 ddns-go 带入全局可变状态 | 并发缺陷 | 移植时强制剥离包级全局状态，用依赖注入替代；代码审查硬性检查项 |
| R12 | 无 MSVC 工具链 | 交叉编译复杂化 | 硬约束：禁止任何 cgo 依赖；CI 加 `CGO_ENABLED=0` 构建检查 |
| R13 | **令牌对本机其余交互用户可读** | 多用户机器上的本地提权路径 | `runtime.json` 含访问令牌，而"可读"等价于"可控制内核"（内核以 SYSTEM 运行，能改防火墙）。目标场景（家用单用户机器）不构成问题，但必须如实记录。**根治方案**：识别命名管道客户端的会话，只放行控制台会话的用户与管理员（`WTSGetActiveConsoleSessionId` + `WTSQuerySessionInformation`），或改为按用户显式授权。排在 M5 之后。见 `docs/DECISIONS.md` D09 |
| R14 | 开发机无法跑 `go test -race` | 并发缺陷漏检 | 竞态检测需要 cgo，而本机无 C 工具链。处置：CI 在 Linux / macOS 上用 `CGO_ENABLED=1` 单独跑一组 `-race`（见 `.github/workflows/ci.yml`） |

---

## 9. 待决事项

| # | 事项 | 当前倾向 | 状态 |
|---|---|---|---|
| O1 | GitHub 用户名 | `ShirazuNagisa`（唯一标识符，**确定后不再变更**） | ✅ 已定 |
| O2 | 仓库名 | `isc-core`（必须与 module 路径末段一致） | ✅ 已定 |
| O3 | 二进制名 | `isc`（同时是 CLI 与守护进程入口，`isc daemon run`） | 待确认 |
| O4 | 更新策略 | 只检查 + 通知 + 可选一键升级到指定版本，**不做静默自动更新** | 待确认 |
| O5 | 是否保留 ddns-go 的 HTTP 兼容层 | 不保留，只做配置导入 | 待确认 |
| O6 | 单 Go module 还是 `go.work` 多 module | 单 module 在仓库根（`console/` 为非 Go 目录） | 待确认 |
| O7 | Go 版本下限 | ~~`go 1.25`~~ → **`go 1.26`** | ⚠️ 已变更 |
| O8 | GOPROXY 设置 | `https://goproxy.cn,direct` + `GOSUMDB=sum.golang.google.cn` + `GOTOOLCHAIN=local` | ✅ 已验证 |

### 关于 O7 变更的说明

原计划对齐 ddns-go 的 `go 1.25`，但 `golang.org/x/sys v0.48.0` 的
`go.mod` 声明了 `go 1.26.0`，`go mod tidy` 会把本模块的下限一并抬到 1.26。

**决定接受 1.26 上限，而不是为留在 1.25 去降级 `x/sys`**：降级意味着放弃
安全修复与平台 API 更新，只为让一个数字好看一点，不划算。Go 的
`GOTOOLCHAIN=auto`（默认值）会在需要时自动获取对应工具链，因此对使用者
不构成硬性障碍。

注意：本机把 `GOTOOLCHAIN` 显式设为了 `local`（避免在国内网络下
自动下载工具链卡住），这意味着**在其它机器上协作时若 Go 版本低于 1.26，
需要手工升级或改回 `auto`**。
```
