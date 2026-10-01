# ISC

**ISC（接入编排器）** 让一台没有公网 IPv4、只有动态 IPv6 的普通电脑，可以被公网直接访问。

它不是应用商店，也不是面板。它只做一件事：**把"这台机器"接进公网**——跟踪 IPv6 前缀变化、更新动态域名解析、编排防火墙、签发证书、反向代理发布服务。

> **状态：M0（地基）开发中。当前不可用。**

---

## 它解决什么问题

国内家宽普遍没有独立公网 IPv4，只有运营商下发的动态 IPv6。想在家里跑个服务从外面访问，会撞上这些墙：

| 问题 | ISC 的应对 |
|---|---|
| 重拨后 **IPv6 前缀（/64）变了**，不是一个地址 | 跟踪**前缀**变化，一次性更新该前缀下所有 AAAA 记录 |
| 家宽**封禁入站 80/443** | 支持非标端口；证书走 DNS-01，不需要开放 80 |
| 一个端口只能对一个服务 | 内置反向代理，按域名 / SNI 路由，**一个端口发布任意多个服务** |
| 家用路由器 **IPv6 防火墙默认丢弃入站**，UPnP 对 IPv6 无效 | 生成可执行的放行清单，并检测到底哪一环断了 |
| 本机防火墙拦入站，改起来要提权 | 以系统服务身份运行，变更走"计划 → 预览 → 应用 → 回滚" |
| **访问端只有 IPv4**（公司网、部分公共 WiFi） | MVP 暂不支持；可达性做成插件，后续可接入 frp / Cloudflare Tunnel |
| 服务悄悄挂了、证书悄悄过期了 | 多通道通知中心（Webhook / 邮件 / Telegram / 企业微信 / 钉钉 / 飞书 / Bark），带去重与静默期 |

---

## 架构

内核是**无 GUI 的守护进程**，只提供接口。下游 GUI 通过本地 API 连接，不链接内核。

```
   产品 GUI（后续独立开发）      验证控制台 SPA        isc CLI
            └──────────────┬────────────┘              │
                           │  命名管道 / Unix socket / 回环 + Bearer token
                 ┌─────────▼──────────────────────────────┐
                 │   isc-core（单一 Go 二进制，无 GUI）    │
                 │   OpenAPI 3.1 · 事件总线 · 任务引擎     │
                 │   DNS 引擎 · IP/前缀监控 · 可达性插件   │
                 │   反向代理 · ACME · 通知中心            │
                 │   SQLite · 密钥库 · 平台适配层          │
                 └────────────────────────────────────────┘
```

- **接口即契约**：`api/openapi.yaml` 是唯一真理，Go 服务端与前端客户端都由它生成。
- **快同步 + 慢异步**：列表查询同步返回；证书签发这类分钟级操作返回任务 ID 并走事件流推送。
- **仅本机**：管理接口只监听本机，强制 token，绝不对外开放。
- **可回滚**：所有系统级变更（防火墙、服务、端口）先出计划，确认后应用，随时可回滚。

详细设计见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)，决策理由见 [`docs/DECISIONS.md`](docs/DECISIONS.md)，开发计划见 [`docs/PLAN.md`](docs/PLAN.md)。

---

## ⚠️ 许可证红线（下游 GUI 开发者必读）

ISC-Core 以 **GPL-3.0** 发布。

下游 GUI 通过 **HTTP / WebSocket** 与内核通信时，二者是**独立进程**，通常不构成衍生作品，GUI 可以自行选择许可证。

> **但如果你把内核作为 Go 库链接进 GUI，整个 GUI 将继承 GPL-3.0。**

因此本项目的架构强制规定：**内核只能作为独立进程运行，不得被链接。**

---

## 构建

```bash
# 需要 Go 1.25 或更高版本
go build ./cmd/isc

# 交叉编译（内核为纯 Go，无 cgo，可直接交叉编译）
GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/isc
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/isc
```

**本项目禁止引入任何需要 cgo 的依赖。**

---

## 快速上手

> M3 完成后可用。当前为占位说明。

```bash
isc init          # 交互式向导：选网卡 → 填域名 → 填凭据 → 选端口 → 生成变更计划 → 应用
isc status        # 查看内核状态
isc doctor        # 全链路诊断：到底哪一环断了
isc daemon run    # 前台运行守护进程
isc --help        # 全部子命令
```

所有子命令支持 `--json`，便于脚本与下游 GUI 复用。

---

## 支持的 DNS 服务商

| 层级 | 能力 | 服务商 |
|---|---|---|
| **Tier-1** | 完整记录 CRUD（区域列表、记录增删改查、全记录类型、TTL、代理开关）+ 动态解析 + DNS-01 证书 | Cloudflare、阿里云 DNS、腾讯云 / DNSPod、华为云 DNS、GoDaddy |
| **Tier-2** | 仅 A/AAAA 动态解析 | 其余约 30 家（由 ddns-go 移植） |

能力矩阵见 [`docs/PROVIDER-MATRIX.md`](docs/PROVIDER-MATRIX.md)（M2 产出）。

---

## 致谢

DNS 服务商实现大量派生自 [ddns-go](https://github.com/jeessy2/ddns-go)（MIT，Copyright (c) 2020 jeessy）。
详见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。

---

## 许可证

[GPL-3.0](LICENSE)
