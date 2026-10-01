# ISC-Core 架构说明

> 本文档描述内核的分层、模块职责、关键接口、数据模型与对外接口面。
> 架构决策的**理由**见 [`DECISIONS.md`](./DECISIONS.md)，此处只描述**是什么**。

---

## 1. 全局视图

```
┌──────────────────────────────────────────────────────────────────────┐
│  下游消费者（均通过本地 API 通信，不链接内核）                          │
│                                                                      │
│   产品 GUI（项目所有者后续自研）   验证控制台 SPA      isc CLI          │
└───────────────────────────┬──────────────────────────────────────────┘
                            │  命名管道 / Unix socket / 回环 HTTP
                            │  Authorization: Bearer <runtime.json 中的 token>
┌───────────────────────────▼──────────────────────────────────────────┐
│                        isc-core（单一 Go 二进制）                      │
│                                                                      │
│  ┌── 接口层 ─────────────────────────────────────────────────────┐   │
│  │  api/       OpenAPI 3.1 → oapi-codegen 生成的 server 接口      │   │
│  │  event/     事件总线（单调序号 + 环形缓冲 + 断线补发）           │   │
│  │  job/       异步任务引擎（进度 / 取消 / 超时 / 持久化）          │   │
│  │  plan/      变更计划框架（Plan → Preview → Apply → Rollback）    │   │
│  └───────────────────────────────────────────────────────────────┘   │
│                                                                      │
│  ┌── 领域层 ─────────────────────────────────────────────────────┐   │
│  │  dns/       自研 Provider 接口 + 记录管理服务                   │   │
│  │             service.go 按能力分发（ZoneLister / RecordLister …）│   │
│  │  ddnsgo/    **移植自 ddns-go** 的 30 家服务商实现（机械变换）    │   │
│  │             + 5 套签名 + IP 缓存 + IP 获取三方式                │   │
│  │  provider/  服务商元信息注册表（字段定义 + 能力位）             │   │
│  │    tier2.go      把移植实现适配成 dns.DynamicUpdater           │   │
│  │    tier1impl.go  接上记录管理并注入动态解析（合成一个对象）      │   │
│  │    tier1/        Tier-1 五家的完整记录 CRUD（为 ISC 新写）      │   │
│  │  ddns/      动态解析任务：实体 + 引擎 + 调度器 + 服务           │   │
│  │  ipmon/     （并入 platform/ipmon.go）IP 与 IPv6 前缀监控       │   │
│  │  credential/ 凭据实体 + 服务（加解密、掩码、引用检查）          │   │
│  │  configio/  配置导入导出 + ddns-go 迁移                        │   │
│  │  audit/     审计日志                                            │   │
│  │  settings/  运行时设置                                          │   │
│  │  reach/     可达性插件（M3）                                     │   │
│  │  proxy/     反向代理（M4）                                       │   │
│  │  acme/      certmagic + DNS-01（M4）                            │   │
│  │  notify/    通知中心（M4）                                       │   │
│  └───────────────────────────────────────────────────────────────┘   │
│                                                                      │
│  ┌── 基础设施层 ─────────────────────────────────────────────────┐   │
│  │  store/     SQLite（modernc.org/sqlite）+ 迁移框架              │   │
│  │  secret/    主密钥 + AES-256-GCM 信封                           │   │
│  │  i18n/      消息目录（zh-CN / en + 移植代码的译文）             │   │
│  │  logx/      slog + 事件总线桥接                                 │   │
│  │  event/     事件总线（单调序号 + 环形缓冲 + 断线补发）           │   │
│  │  job/       异步任务引擎                                        │   │
│  │  paths/     数据目录解析                                        │   │
│  │  runtimeinfo/ runtime.json（客户端发现内核的唯一入口）           │   │
│  └───────────────────────────────────────────────────────────────┘   │
│                                                                      │
│  ┌── 平台适配层（build tag，三平台各一份实现）────────────────────┐   │
│  │  platform/  Firewall（M3）│ ServiceManager（M5）               │   │
│  │             IPMonitor（可移植轮询）│ SecretStore（DPAPI/Keychain）│  │
│  │             Transport（命名管道 / Unix 套接字）│ LowPortBinder   │   │
│  └───────────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────┘
```

### 关于 ddnsgo 包的边界

`internal/ddnsgo/` 是**照搬的上游代码**（8335 行，由 `scripts/port-ddnsgo.ps1`
机械变换生成），`internal/provider/tier2.go` 是**我们写的**适配层。
保持这条边界的原因：

- 上游升级时只需重跑移植脚本，适配层不受影响；
- ISC 的接口演进不用去动那 8000 多行；
- MIT 归属说明可以精确指向一个包，而不是散落各处。

### 关于 Tier-1 与 Tier-2 的能力合成

Tier-1 的五家（Cloudflare / 阿里云 / 腾讯云 / DNSPod / 华为云 / GoDaddy）
在移植代码里**也有**动态解析实现，而记录管理是为 ISC 新写的。两者必须
合成**一个对象**，否则 `Capabilities()` 推导出的能力位只会反映其中一个，
界面上会出现"支持列记录但不支持动态解析"这种与实际不符的组合。

合成通过 `tier1.dynamicDelegate` 完成：Tier-1 实现内嵌一个转发器，
装配层把移植过来的动态解析实现注入进去。

> ⚠️ 注意：这里**不能**用 `struct { dns.Provider; dns.DynamicUpdater }`
> 那种"嵌入接口"的写法 —— 嵌入接口只提升该接口自己的方法，
> 记录增删改查的方法全都传不出来。必须是内嵌**具体结构**。
```

**控制流方向**：接口层 → 领域层 → 基础设施层 / 平台适配层。领域层之间通过接口通信，不直接互相依赖（例如 `acme` 依赖 `dns.Provider` 接口，不依赖具体 provider 包）。

---

## 2. 平台适配层接口

这是全项目唯一允许出现 `runtime.GOOS` 分支的地方。文件命名约定：`xxx_windows.go` / `xxx_linux.go` / `xxx_darwin.go`，未实现平台提供 `xxx_stub.go`（编译可通过，调用返回 `ErrNotImplemented` 并降级为引导模式）。

```go
package platform

// Firewall 防火墙编排。
type Firewall interface {
    // Inspect 读取当前生效的入站规则（只读，用于计划阶段比对）。
    Inspect(ctx context.Context) ([]Rule, error)
    // Plan 计算从当前状态到期望状态的差异，不产生任何副作用。
    Plan(ctx context.Context, desired []Rule) (Change, error)
    // Apply 应用变更；必须幂等，重复 Apply 同一 Change 不产生额外副作用。
    Apply(ctx context.Context, ch Change) error
    // Rollback 回滚变更；必须能恢复到 Apply 之前的状态。
    Rollback(ctx context.Context, ch Change) error
}

// Rule 一条入站放行规则。
type Rule struct {
    Name     string   // 规则标识，用于幂等与回滚
    Protocol Protocol // tcp / udp
    Port     PortRange
    // Source 留空表示任意来源；IPv6 原生场景通常留空。
    Source   string
    Profiles []string // Windows 网络配置文件：domain / private / public
}

// ServiceManager 系统服务安装与生命周期。
type ServiceManager interface {
    Install(ctx context.Context, cfg ServiceConfig) error
    Uninstall(ctx context.Context) error
    Status(ctx context.Context) (ServiceStatus, error)
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}

// IPMonitor 网络地址与 IPv6 前缀监控。
type IPMonitor interface {
    // Snapshot 返回当前全部接口的地址与委派前缀。
    Snapshot(ctx context.Context) ([]InterfaceAddrs, error)
    // Watch 推送地址/前缀变化事件，直到 ctx 取消。
    Watch(ctx context.Context) (<-chan AddrEvent, error)
}

type InterfaceAddrs struct {
    Name        string
    Index       int
    HardwareAddr string
    IPv4        []netip.Addr
    IPv6        []netip.Addr
    // Prefixes 是委派给本接口的 IPv6 前缀（如 240e:xxxx:xxxx:xx00::/64）。
    // 这是本项目的核心概念：ISP 重拨变化的是前缀，不是单个地址。
    Prefixes    []netip.Prefix
    IsUp        bool
    IsLoopback  bool
}

// SecretStore 是小型、平台原生的命名密钥存储。
//
// 用途只有一个但很关键：保存内核的**主密钥**。所有凭据都用主密钥做
// AES-256-GCM 加密后落库，主密钥本身则交给操作系统保护的存储。
type SecretStore interface {
    Put(ctx context.Context, name string, value []byte) error
    Get(ctx context.Context, name string) (value []byte, found bool, err error)
    Delete(ctx context.Context, name string) error
}

// Transport 本地管理通道。
type Transport interface {
    // Listen 返回一个仅供本机访问的 net.Listener。
    // Windows 返回命名管道包装的 listener，类 Unix 返回 Unix socket。
    Listen(ctx context.Context, endpoint string) (net.Listener, error)
    // Endpoint 返回可被客户端连接的地址描述（写入 runtime.json）。
    Endpoint() string
}

// LowPortBinder 低端口绑定能力。
type LowPortBinder interface {
    // CanBindLowPorts 报告当前进程能否直接绑定 <1024 端口。
    CanBindLowPorts() bool
}
```

---

## 3. DNS Provider 抽象

### 3.1 接口

```go
package dns

// Capability 能力位。Provider 声明自己支持哪些操作，
// 上层据此决定 UI 置灰与流程编排。
type Capability uint32

const (
    CapZoneList   Capability = 1 << iota // 列出账号下的区域
    CapRecordList                        // 列出区域内的记录
    CapRecordCreate                      // 新增记录
    CapRecordUpdate                      // 修改记录
    CapRecordDelete                      // 删除记录
    CapRecordTypesAll                    // 支持 A/AAAA 之外的记录类型
    CapTTL                               // 支持自定义 TTL
    CapProxy                             // 支持 CDN 代理开关（如 Cloudflare）
    CapDNSP01                            // 可用于 ACME DNS-01 校验
)

type CapabilitySet map[RecordType]Capability // 按记录类型细分

// Provider 是所有 DNS 服务商实现的统一接口。
type Provider interface {
    // Meta 返回服务商元信息与能力声明。
    Meta() Meta
    // Verify 校验凭据是否有效（不产生副作用）。
    Verify(ctx context.Context, cred Credential) error

    // ZoneLister
    ListZones(ctx context.Context, cred Credential) ([]Zone, error)

    // RecordGetter
    ListRecords(ctx context.Context, cred Credential, zone Zone, filter RecordFilter) ([]Record, error)
    GetRecord(ctx context.Context, cred Credential, zone Zone, id string) (Record, error)

    // RecordAppender
    CreateRecord(ctx context.Context, cred Credential, zone Zone, rec Record) (Record, error)
    // RecordSetter
    UpdateRecord(ctx context.Context, cred Credential, zone Zone, rec Record) (Record, error)
    // RecordDeleter
    DeleteRecord(ctx context.Context, cred Credential, zone Zone, id string) error
}

// DynamicUpdater 是 Tier-2 服务商唯一需要实现的能力：
// 把 A/AAAA 记录更新到指定 IP（照搬 ddns-go 的行为）。
type DynamicUpdater interface {
    UpdateDynamic(ctx context.Context, cred Credential, req DynamicRequest) (DynamicResult, error)
}
```

### 3.2 libdns 双向适配

```
        ┌────────────────────┐
        │  dns.Provider      │  ← 内核内部统一接口
        └─────────┬──────────┘
                  │
        ┌─────────▼──────────┐        ┌──────────────────────┐
        │ libdns_adapter/    │◄──────►│ libdns.Provider      │
        │ (双向胶水 ~200 行) │        │ (社区 40+ 家实现)     │
        └─────────┬──────────┘        └──────────────────────┘
                  │
        ┌─────────▼──────────┐
        │ certmagic          │  ACME DNS-01 消费 libdns 接口
        └────────────────────┘
```

- **正向**（`dns.Provider` → `libdns`）：让 `certmagic` 能直接使用我们手写的 Tier-1 provider 做 DNS-01。
- **反向**（`libdns` → `dns.Provider`）：把社区 provider 包装成内核统一接口，白捐 40+ 家服务商。

---

## 4. 数据模型（SQLite）

> 所有敏感字段以 `*_cipher` 命名，存的是 `SecretStore` 加密后的密文；`key_version` 支持主密钥轮换。

```sql
-- 凭据
credentials(id, provider, label, config_cipher, key_version, created_at, updated_at, last_verified_at, last_verify_ok)

-- DNS 区域缓存
zones(id, credential_id, name, provider_zone_id, status, synced_at)

-- DNS 记录
-- managed_by 区分「DDNS 自动托管」与「用户手动管理」，避免互相覆盖
records(id, zone_id, name, type, content, ttl, proxied, comment,
        provider_record_id, managed_by, synced_at)

-- 动态解析任务
ddns_tasks(id, credential_id, zone_name, domain, record_type,
           ip_source_kind,            -- netInterface | url | cmd
           ip_source_value,           -- 网卡名 / URL 列表 / 命令
           ipv6_selector,             -- 正则或 @N，选择第几个地址
           ttl, enabled, last_run_at, last_result, last_ip)

-- 反代目标服务
services(id, name, domains, listen_port, upstream, tls_mode, enabled, created_at)

-- 证书
certs(id, domains, mode, not_before, not_after, last_renew_at, status, last_error)

-- 防火墙期望规则与已应用状态
firewall_rules(id, platform, name, protocol, port_from, port_to, source, profiles, applied_at)
firewall_state(id, platform, backend, snapshot_cipher, applied_at)   -- 回滚用快照

-- 变更计划
plans(id, kind, title, payload, diff_text, status, created_at, applied_at, rollback_ref, error)

-- 任务
jobs(id, kind, status, progress, payload, result, error, created_at, started_at, finished_at)

-- 事件（环形，可裁剪）
events(seq INTEGER PRIMARY KEY AUTOINCREMENT, ts, type, payload)

-- 审计
audit(id, ts, actor, action, target, result, detail)

-- 通知
notify_channels(id, kind, label, config_cipher, key_version, enabled, min_severity)
notify_state(id, dedup_key, last_sent_at, suppressed_count)

-- 运行时设置（含事件水位、schema 版本等）
settings(key, value)
```

**迁移**　`store/migrations/NNNN_描述.sql`，按序号顺序执行，执行记录写入 `schema_migrations`。迁移文件一旦发布不可修改。

---

## 5. 对外接口面（OpenAPI 3.1，前缀 `/v1`）

> 这是**契约的唯一真理**。实际定义在 `api/openapi.yaml`，此处仅是结构导览。

### 元信息与健康

```
GET    /v1/meta                     版本、平台、能力位、构建信息
GET    /v1/health                   存活与就绪
```

### 事件与任务

```
GET    /v1/events                   WebSocket 事件流（?lastEventId= 断线补发）
GET    /v1/jobs                     任务列表（分页、过滤）
GET    /v1/jobs/{id}                任务详情与进度
POST   /v1/jobs/{id}/cancel         取消任务
```

### 凭据

```
GET    /v1/credentials
POST   /v1/credentials
GET    /v1/credentials/{id}
PATCH  /v1/credentials/{id}
DELETE /v1/credentials/{id}
POST   /v1/credentials/{id}/verify  → 202 job
```

### DNS 区域与记录

```
GET    /v1/credentials/{id}/zones               列区域
GET    /v1/zones/{zoneId}/records               列记录（类型/名称过滤、分页）
POST   /v1/zones/{zoneId}/records               新增记录 → 202 job
GET    /v1/records/{recordId}
PATCH  /v1/records/{recordId}                   修改记录 → 202 job
DELETE /v1/records/{recordId}                   删除记录 → 202 job
```

### IP 与动态解析

```
GET    /v1/ip/current               当前 IPv4 / IPv6 / 前缀 / 网卡清单
GET    /v1/ip/watch                 WebSocket，地址与前缀变化
GET    /v1/ddns-tasks
POST   /v1/ddns-tasks
PATCH  /v1/ddns-tasks/{id}
DELETE /v1/ddns-tasks/{id}
POST   /v1/ddns-tasks/{id}/run      → 202 job（立即执行一次）
```

### 可达性与服务发布

```
GET    /v1/reach/providers          可用可达性插件及其实例
POST   /v1/reach/verify             引导式外部验证 → 202 job（返回一次性验证 URL）
GET    /v1/reach/verify/{id}        验证结果（含手机端回写状态）
GET    /v1/services                 反代目标列表
POST   /v1/services
PATCH  /v1/services/{id}
DELETE /v1/services/{id}
```

### 证书

```
GET    /v1/certs
POST   /v1/certs                    申请证书 → 202 job
POST   /v1/certs/{id}/renew         续期 → 202 job
DELETE /v1/certs/{id}
```

### 变更计划与防火墙

```
GET    /v1/plans                    计划列表
POST   /v1/plans                    生成计划（不产生副作用）
GET    /v1/plans/{id}               计划详情（含人类可读 diff）
POST   /v1/plans/{id}/apply         应用 → 202 job
POST   /v1/plans/{id}/rollback      回滚 → 202 job
GET    /v1/firewall/rules           当前生效规则
```

### 通知

```
GET    /v1/notify/channels
POST   /v1/notify/channels
PATCH  /v1/notify/channels/{id}
DELETE /v1/notify/channels/{id}
POST   /v1/notify/channels/{id}/test → 202 job
GET    /v1/notify/history
```

### 日志与配置

```
GET    /v1/logs                     分页查询
GET    /v1/logs/tail                WebSocket 实时日志
GET    /v1/config/export            YAML 导出（默认不含明文凭据）
POST   /v1/config/import            导入（支持 ddns-go 配置迁移）
POST   /v1/config/import/ddns-go    专用 ddns-go 配置迁移端点
```

### 诊断

```
POST   /v1/doctor                   全链路诊断 → 202 job
GET    /v1/doctor/{id}/report       诊断报告（网络栈 / IPv6 可达 / 前缀委派 /
                                    防火墙 / 端口占用 / DNS 传播 / 证书链）
```

### 约定

- 错误响应统一为 RFC 9457 `application/problem+json`；
- 分页统一使用游标（`cursor` + `limit`），返回 `next_cursor`；
- 所有 `202` 响应返回 `{ "job_id": "..." }`；
- 所有写操作在审计表中留痕。

---

## 6. 事件类型

| 事件 | 触发时机 | 载荷要点 |
|---|---|---|
| `job.progress` | 任务进度变化 | job_id, percent, message |
| `job.finished` | 任务结束 | job_id, status, result, error |
| `ip.changed` | 地址变化 | 接口名, 旧/新地址 |
| `ip.prefix_changed` | **IPv6 前缀变化** | 接口名, 旧/新前缀（本项目核心事件） |
| `dns.record_updated` | 记录更新成功 | 域名, 类型, 旧/新值 |
| `dns.update_failed` | 记录更新失败 | 域名, 类型, 错误 |
| `reachability.changed` | 可达性状态变化 | 服务, 旧/新状态 |
| `cert.renewed` | 证书续期成功 | 域名, 到期时间 |
| `cert.expiring` | 证书即将过期 | 域名, 剩余天数 |
| `firewall.applied` | 防火墙变更已应用 | 计划 id, 规则摘要 |
| `notify.sent` | 通知已发出 | 通道, 严重级别（受静默期影响） |
| `log.appended` | 新日志 | 级别, 消息 |
| `config.changed` | 配置变化 | 变更范围 |

事件全部带 `seq`（单调递增）、`ts`、`type`、`payload`。环形缓冲保留最近 N 条（可配置），水位持久化到 `settings`。

---

## 7. 仓库结构

```
ISC-Core/
├── .gitignore                    上游参考源与构建产物
├── LICENSE                       GPL-3.0
├── THIRD_PARTY_NOTICES.md        ddns-go (MIT) 等第三方声明
├── README.md
├── go.mod                        github.com/ShirazuNagisa/isc-core
├── go.sum
│
├── cmd/
│   └── isc/main.go               CLI 与守护进程统一入口
│
├── internal/
│   ├── api/                      生成的 server 接口 + handler 实现
│   ├── event/                    事件总线
│   ├── job/                      异步任务引擎
│   ├── plan/                     变更计划框架
│   ├── dns/
│   │   ├── provider.go           自研 Provider 接口与类型
│   │   ├── libdns_adapter/       双向适配器
│   │   ├── providers/tier1/      cloudflare, alidns, tencentcloud, huaweicloud, godaddy
│   │   └── providers/tier2/      照搬 ddns-go 的 30 家
│   ├── ipmon/
│   ├── sched/
│   ├── reach/
│   ├── proxy/
│   ├── acme/
│   ├── notify/
│   ├── audit/
│   ├── store/
│   │   └── migrations/
│   ├── secret/
│   ├── i18n/
│   ├── logx/
│   ├── cli/                      cobra 子命令实现
│   └── platform/
│       ├── platform.go           接口定义与通用类型
│       ├── firewall_windows.go / _linux.go / _darwin.go / _stub.go
│       ├── service_*.go
│       ├── ipmon_*.go
│       ├── secret_*.go
│       ├── transport_*.go
│       └── lowport_*.go
│
├── api/
│   └── openapi.yaml              ← 契约唯一真理
│
├── console/                      验证控制台（Vite + React + openapi-typescript）
│   ├── package.json
│   └── src/
│
├── docs/
│   ├── PLAN.md
│   ├── ARCHITECTURE.md
│   ├── DECISIONS.md
│   ├── PROVIDER-MATRIX.md        各服务商能力矩阵
│   └── MIGRATION-from-ddns-go.md
│
├── testdata/
└── .github/workflows/ci.yml
```

> `ddns-go-master/` 位于仓库根目录但**不纳入版本管理**（见 `.gitignore`）。它自带 `go.mod`，因此不会被 `go build ./...` 收集。

---

## 8. 关键运行时约定

### 8.1 启动序列

1. 解析数据目录（见 D20），必要时创建并收紧 ACL；
2. 加载 / 初始化 SQLite，执行迁移；
3. 初始化 `SecretStore`，检查系统密钥库可用性（不可用则告警并回退）；
4. 加载配置与 i18n；
5. 启动事件总线与任务引擎，恢复未完成任务状态；
6. 建立 `Transport`（命名管道 / Unix socket），若失败则回退回环 TCP；
7. 生成 / 读取 token，写 `runtime.json`；
8. 启动调度器、`IPMonitor`、反代、证书续期；
9. 等待信号，优雅关闭（先撤销 `runtime.json`，再停各子系统）。

### 8.2 `runtime.json`

```json
{
  "pid": 12345,
  "version": "0.1.0",
  "endpoint": "npipe://./pipe/isc-core",
  "fallback_endpoint": "tcp://127.0.0.1:52341",
  "token": "<随机 32 字节 base64url>",
  "started_at": "2026-01-01T00:00:00Z"
}
```

**地址格式**（两种通道使用同一套 scheme 词汇）：

| scheme | 形态 | 说明 |
|---|---|---|
| `npipe://` | `npipe://./pipe/isc-core` | Windows 命名管道 |
| `unix://`  | `unix:///var/lib/isc/run/isc.sock` | Unix 域套接字（路径必须绝对） |
| `tcp://`   | `tcp://127.0.0.1:52341` | 回环 TCP；**拒绝任何非回环地址** |

两点必须注意：

1. **两条通道同时监听**，不是主备关系。首选通道（管道 / 套接字）有 ACL 或
   文件权限层防护且不占 TCP 端口；回环 TCP 则是**浏览器唯一能用的入口**
   —— 验证控制台无法连接命名管道。因此关闭回环等于关掉控制台。
2. `endpoint` 中的地址**不是** HTTP URL。浏览器要访问时需要自行拼出
   `http://127.0.0.1:<port>`；CLI 与原生 GUI 直接把它交给本机传输层。

位置：数据目录下的 `run/`。该目录与 `runtime.json` 的访问权限被收紧至
明确的白名单 SID（见 D09）。进程退出时删除该文件。

### 8.3 客户端发现流程

```
读取 runtime.json → 尝试 endpoint（管道/socket）
                 → 失败则尝试 fallback_endpoint（回环 HTTP）
                 → 均失败则报告「内核未运行」
```

CLI、控制台、下游 GUI 复用同一套发现逻辑。

### 8.4 优雅关闭顺序

反向代理停止接受新连接 → 等待在途请求（有超时）→ 停止调度器 → 停止 IPMonitor → 刷新事件水位与任务状态 → 关闭数据库 → 删除 `runtime.json`。

---

## 9. 安全约束清单

| 约束 | 说明 |
|---|---|
| 管理面仅本机 | 命名管道 / Unix socket 优先；回环 HTTP 为回退且强制 token（D09） |
| token 强制 | 即使回环也校验，防止本机其他用户或浏览器 CSRF |
| `runtime.json` ACL | 仅当前用户可读 |
| 凭据加密 | 全部 `*_cipher` 经 `SecretStore`；主密钥不入库 |
| 反代 SSRF 防护 | 上游地址白名单（仅允许回环 / 私有网段）；禁止将反代变为开放代理 |
| 反代请求头清理 | 剥离 `X-Forwarded-*` 伪造、`Connection`、`Upgrade` 非法组合 |
| 禁止 CONNECT 转发 | 不实现正向代理语义 |
| 导出去敏 | `config export` 默认不含明文凭据 |
| 依赖许可证 | 新增依赖必须通过 GPLv3 兼容性检查 |
| **GPL 红线** | 禁止把内核作为 Go 库链接进任何 GUI（D17） |
