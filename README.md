# ISC — 接入编排器

把一台普通的电脑变成**能从公网访问的服务器**，并顺带管理你的域名解析。

ISC 面向的场景是：家里或办公室有一台常开的机器，你想从外面访问它上面的
服务（文件、媒体库、自建应用），但——

- 宽带没有独立公网 IP（国内家宽的常态，运营商 CGNAT）
- 有公网 IPv6，但**前缀是动态的**，重拨一次就变
- 80 / 443 端口被运营商封着

ISC 内核负责把这些琐碎的事自动化：跟踪 IPv6 前缀变化、把新地址写进 DNS、
按需开防火墙、在内置反向代理上提供 HTTPS。

从 v0.2.0 起它还负责**把一份源码变成上线的站点**：识别技术栈、准备运行时
（优先用本机已装的解释器，缺失时下载固定版本并校验摘要）、装依赖、构建、
守护进程、健康检查、崩溃重启，并把域名、反向代理与证书一起配好。

内核因此独占业务服务的生命周期（[D25](docs/DECISIONS.md)）。图形界面
（ISC Phecda）是它的另一个仓库，通过版本化的 `libisc` C ABI 以库的形式
嵌入内核，自身不含任何功能性逻辑。

---

## 快速上手

### 1. 运行内核

```bash
./isc daemon run
```

它默认只监听**本机**的本地接口（Windows 命名管道 / Unix 域套接字），
不会对外暴露任何端口。

想让它在后台常驻、开机自启：

```bash
sudo ./isc service install      # Linux / macOS
./isc service install           # Windows（需以管理员身份运行终端）
```

### 2. 配一个 DNS 凭据

以 Cloudflare 为例：

```bash
./isc credential add cloudflare --label 我的CF --field token=<API-TOKEN>
```

**最小权限**：给这个 Token 只开 `Zone:DNS:Edit` 权限。不要用全局 API Key ——
内核只需要改 DNS 记录，而一个能改账户全部设置的凭据一旦泄漏，后果
完全不同。

### 3. 建一条动态解析任务

```bash
./isc ddns add \
  --label 家里的IPv6 \
  --credential <凭据ID> \
  --domain home.example.com \
  --type AAAA \
  --source ipv6
```

然后立即跑一次看看：

```bash
./isc ddns run <任务ID>
```

### 4. 确认真的通了

```bash
./isc verify
```

它会引导你做一次**从公网发起的**验证：给一个链接，用手机流量（不要连
WiFi）打开。

这一点很重要——在**本机**上访问 `home.example.com` 成功**不能**证明它在
公网上可达：

- 路由器可能在做 NAT 回环（hairpin），本机访问会走内网
- 解析可能命中了本机 hosts 或本地 DNS 缓存
- 有些系统对「自己的域名」有特殊处理

只有来自**公网**的请求才证明得了。验证页会检查请求的来源地址，并明确
告诉你结果是"证明可达"还是"什么也证明不了"。

### 5. 需要 HTTPS 时

```bash
./isc proxy add home.example.com --to 127.0.0.1:8096 --tls
```

先把 ACME 需要的设置填上：

```bash
./isc settings set \
  --acme-email you@example.com \
  --acme-dns-credential-id <凭据ID>
./isc settings set --proxy-enabled --proxy-port 443 --proxy-tls
```

证书会在几秒内自动签发并生效，到期前会**自动续期**。用
`./isc cert list` 看状态。

---

## 常用命令

| 命令 | 作用 |
|---|---|
| `isc daemon run` | 前台运行内核 |
| `isc doctor` | 体检：环境、权限、网络、依赖 |
| `isc settings` | 查看与修改设置 |
| `isc credential` | 管理 DNS 服务商凭据 |
| `isc ddns` | 管理动态解析任务 |
| `isc zones` / `isc records` | 浏览与编辑 DNS 记录 |
| `isc doctor` | 可达性检查与故障定位 |
| `isc verify` | 引导式外部验证 |
| `isc proxy` | 反向代理路由 |
| `isc cert` | 查看与续期 TLS 证书 |
| `isc notify` | 通知通道与投递记录 |
| `isc service` | 安装为系统服务 |

所有命令都支持 `--json`，便于脚本调用。

### 验证用 Web 控制台

```bash
./isc console
```

它会打印一个本地地址。在浏览器里打开就能看到全部功能——控制台只监听
回环地址，且会校验 `Host` 头（防止 DNS 重绑定）。

---

## 关于权限

| 操作 | 是否需要管理员 |
|---|---|
| 运行内核、`isc` 的绝大多数命令 | 否 |
| 装成系统服务 | Windows / Linux / macOS 都需要 |
| 开防火墙规则 | 是 |
| 绑定 443 等低端口 | Linux 上需要 `CAP_NET_BIND_SERVICE` 或 root |

`isc doctor` 会告诉你在当前机器上哪些功能可用、哪些不可用以及为什么。

---

## 排错

### 外网访问不了

按这个顺序查：

1. **`isc verify` 的结果是什么？**
   如果它说"无法证明可达"，问题在公网侧，不在 DNS。

2. **IPv6 通不通？**
   ```bash
   isc ip
   ```
   运营商的 IPv6 可能是「有地址但不通」。这时任务会一直失败，
   而错误信息通常只有一句超时。

3. **防火墙放行了吗？**
   Windows 上非管理员**无法**创建防火墙规则。`isc doctor` 会标出来。

4. **DNS 真的更新了吗？**
   ```bash
   isc records list <凭据ID> <区域ID>
   ```
   有些服务商有缓存，改动不会立刻生效。

5. **路由器放行了吗？**
   IPv6 下通常不需要端口转发，但需要在路由器防火墙里**放行入站**。
   很多家用路由器默认拦掉全部 IPv6 入站。这一条内核管不了，
   需要你在路由器上操作。

### 证书签不下来

DNS-01 校验失败的常见原因：

- 该域名的**权威 DNS 不是**你所选的服务商（查一下 NS 记录）
- 凭据没有该域名的编辑权限
- 记录还在传播中（稍后重试）

`isc cert renew` 会给出具体原因。

> 首次配置建议先用 Let's Encrypt 的**测试环境**试通：
> `isc settings set --acme-directory https://acme-staging-v02.api.letsencrypt.org/directory`
> 生产环境的失败配额是**每小时 5 次**，调配置很容易把它用光，
> 而用光之后要等一小时。测试环境签的证书浏览器不信任，但流程一样。

### 收不到通知

```bash
isc notify test        # 立刻发一条，看每个通道的结果
isc notify deliveries  # 看最近的投递记录
```

同一个事件在 5 分钟内只会发一条（防止地址抖动刷屏）。静默期过后如果
期间有被抑制的消息，会补发一条汇总。

---

## 数据放在哪

| 平台 | 默认位置 |
|---|---|
| Windows | `%LOCALAPPDATA%\isc` |
| Linux | `/var/lib/isc`（配置在 `/etc/isc`） |
| macOS | `~/Library/Application Support/isc` |

用 `ISC_DATA_DIR` 环境变量或 `--data-dir` 参数覆盖。

**凭据是加密存储的**：主密钥放在系统密钥库里（Windows DPAPI / macOS
钥匙串 / Linux Secret Service），数据库里存的是密文。文件兜底模式也
支持，但保护级别低得多——`isc doctor` 会告诉你当前用的是哪一种。

---

## 许可证

GPLv3。第三方组件的许可证见 `THIRD_PARTY_NOTICES.md`。

内核与图形界面是**分离的两个仓库**：GUI 通过版本化的 `libisc` C ABI 以**库**的形式
嵌入内核（D24），不 import 任何 Go 包，也不使用内核源码。

链接内核库构成衍生作品，因此 **GUI 同样以 GPLv3 兼容许可发布**——这不是例外或双许可，
而是本项目的既定选择（D24/D31）。
