# Cloudflare 隧道（设计，**尚未实现**）

> 状态：**设计文档，代码未写**。
> 当前仓库里没有任何隧道相关的实现 —— 别把这个文件当成"已经有了"。
> 它记的是已经验证过的机制与已经定位好的集成点，供接下来动手时不必重推。

## 1. 为什么需要它

内核原有的公网暴露有一个前提：**这台机器有一个可被路由到的地址**。直连
模型往 DNS 写 AAAA，客户端直接连回来。这个前提在两处很常见的网络上不
成立：

- 大内网（CGNAT）后的家宽；
- 校园网 / 公司网 —— 入站连接在网关上就被丢掉，本机怎么配都没用。

实测（校园网，10.14.190.170 经 NAT 出口 124.228.218.4）：

| 检查 | 结果 |
|---|---|
| 公网 IPv6 | 无（网络不发 RA） |
| 入站可达性 | 8 个外部节点（澳/匈/印/伊朗/土/英/美×2）**全部超时** |
| 出站可达性 | 正常 |

结论：直连模型在这类网络下**不可能**工作，且不是本机配置能解决的。

## 2. 机制

本机**主动**向 Cloudflare 建一条长连接并保持住，外面来的请求顺着这条
已经存在的连接送进来。不需要公网地址、不需要入站端口、不用碰网关。

已实测（本机，校园网）：

- `region1/2.v2.argotunnel.com` 的 **TCP 7844 与 443 都通**；
- 零账号的临时隧道（`cloudflared tunnel --url`）外部可达；
- 正式隧道 `isc-phecda` 建立后，`php.` / `html.shirazu-nagisa.com`
  从 Google Cloud 与 check-host.net 的 5 个节点（奥/加/西/法/波）均取到页面。

## 3. 关键设计决定：**接在反代前面，不取代它**

```
客户端 → Cloudflare 边缘 ══隧道══> 127.0.0.1:<反代端口> → 各站点
```

隧道配置里只有**一条 catch-all 规则**：

```yaml
ingress:
  - service: http://127.0.0.1:443
```

**已实测：catch-all 规则会保留原样的 Host 头**，因此"哪个域名去哪个站点"
仍然由内核的反向代理按 Host 决定。这带来两个直接好处：

1. 站点到域名的映射只有一份真相（反代路由表），不会与隧道配置漂移；
2. **新增站点只需要建一条 DNS 记录** —— 不改配置、不重启隧道。这正是
   "新站点自动上隧道"之所以便宜的原因。

## 4. 集成点（已定位）

| 位置 | 现状 | 隧道模式下应该做什么 |
|---|---|---|
| `internal/daemon/binder.go` `EnsureDNS` | 建动态解析任务（A/AAAA 跟着地址变） | 改为建一条 **CNAME → `<id>.cfargotunnel.com`（橙云）** |
| `internal/daemon/binder.go` `EnsureRoute` | 建反代规则 | **不变** —— 隧道正需要它 |
| `internal/settings` | `proxy_enabled` / `proxy_port` / `proxy_tls` | 新增 `tunnel_enabled`、`tunnel_binary`（可选） |
| `internal/api` | — | `/v1/tunnel`（状态）、enable/disable |
| Phecda | 设置页 | 隧道开关 + 状态（连接数、缺什么） |

`dns.Service` 已经提供 `CreateRecord` / `UpdateRecord` / `ListRecords`，
`dns.Record` 带 `Proxied` 字段，因此建 CNAME 不需要新的 DNS 能力。

## 5. 已核实的 CLI 面

```
cloudflared tunnel login  --origincert <path>
cloudflared tunnel create <name>
cloudflared tunnel list   --output json
cloudflared tunnel run    --config <path> <name>
```

`--origincert` 与 `TUNNEL_ORIGIN_CERT` 都可用，因此内核可以把授权文件
放在**自己的**数据目录里，完全不碰用户的 `~/.cloudflared`。

## 6. 尚未定的两件事

1. **账号授权怎么来。** 两条路：
   - 交互式：`cloudflared tunnel login` 打开浏览器，用户在页面上选 zone
     并授权。不需要任何 API 权限，但需要用户点一次；
   - API：需要令牌具备 *Account → Cloudflare Tunnel → Edit*（**账号级**，
     与现有的 zone 级 DNS 权限不是一回事）。全自动，但要多一个权限。
2. **cloudflared 由谁供给。** 内核已经有 `internal/artifacts`（下载 +
   摘要校验 + 安全解压），照语言运行时那套加一条固定版本与摘要即可。
   这条要先补上，否则"产品化"仍然要求用户手工装一个二进制。

## 7. 与直连模型的差异（界面必须说清）

- **不再需要 ACME**：TLS 在 Cloudflare 边缘终结，用的是 Cloudflare 的
  通用证书。因此隧道模式下"证书还有几天到期"这类信息不适用；
- 流量经过第三方（Cloudflare）。免费版无 SLA；
- 只承载 HTTP/HTTPS。免费隧道不支持任意 TCP/UDP —— 对建站场景正好，
  但意味着 Mizar 的远程管理面**不能**简单地挂上去（它是自签证书 +
  SPKI 固定的 TLS，而 Cloudflare 会终结 TLS）。这一条要单独设计。

## 8. 当前这台机器上的实际状态

为了让验证结果活过重启，隧道暂时由 launchd 拉起：

```
~/Library/LaunchAgents/app.isc.phecda.tunnel.plist
```

**这是权宜之计。** 内核接管之后应当删掉它 —— 两个监管者同时拉同一个
隧道会让"谁在管它"变得没有答案，而排查问题时那正是第一个要回答的问题。
